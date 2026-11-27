// 远程协助 P1 Task 9:客户端会话状态机 + 观看 WS 连接管理。
//
// 单例 composable(与 useNavigation / useAliangChat 同屋规):申请弹窗(Task 9)
// 与观看页(Task 10)共享同一份 state / WS,避免两处各建一条流。
//
// 状态词表(服务端快照为唯一真源,前端只镜像):
//   idle(state=="") → requested → active → ended(带 end_reason)
// 申请被管理员拒绝/超时同样落 ended(reason=pending_expired 等):后端状态机
// 没有独立 rejected 态,拒绝唯一表现就是终态原因。
//
// 错误映射:后端信封 code(见 app/http/common/errors.go)→ 前端 i18n KEY,
// lastError 只存 KEY,弹窗渲染时才本地化。
//   105 → 冷却 / 104 → 忙碌或动作前置不满足 / 201 → 信令离线 / 403 → 开关关闭
import { ref } from 'vue';
import {
  getAssistGates,
  getAssistSession,
  requestAssist,
  cancelAssist,
  endAssist,
} from '../services/agentApi';

// end_reason → 文案 KEY(spec §5 六种终态原因 + 设备侧补充);未知值统一回落。
const END_REASON_KEYS = {
  admin_end: 'assist_end_admin',
  client_end: 'assist_end_client',
  device_offline: 'assist_end_offline',
  admin_timeout: 'assist_end_admin_timeout',
  duration_cap: 'assist_end_duration_cap',
  device_restart: 'assist_end_device_restart',
  pending_expired: 'assist_end_expired',
  terminal_limit: 'assist_end_terminal_limit',
};

export function assistEndReasonKey(reason) {
  return END_REASON_KEYS[reason] || 'assist_end_unknown';
}

const POLL_INTERVAL_MS = 2000;
// 重连退避照 useAliangChat.ensureAISocket 模式:2 次、每次 800ms。
const WS_RECONNECT_DELAYS_MS = [800, 800];
// 连接超时保险丝:CONNECTING 永挂不得让 UI 永久「连接中」。
const WS_CONNECT_TIMEOUT_MS = 10000;
const WS_FRAME_CAP = 2000;

function errorKeyFor(err, action) {
  const code = err && typeof err.code === 'number' ? err.code : undefined;
  const status = err && typeof err.status === 'number' ? err.status : undefined;
  const msg = String((err && err.message) || '').toLowerCase();

  if (code === 105 || status === 429 || msg.includes('cooldown')) {
    return 'assist_error_cooldown';
  }
  // 104/409 的语义随动作而变:申请时=已有活跃会话;cancel=无待审批;
  // end=无可结束会话。
  if (code === 104 || status === 409) {
    if (action === 'cancel') return 'assist_error_not_pending';
    if (action === 'end') return 'assist_error_nothing_to_end';
    return 'assist_error_busy';
  }
  // 只认信令不可用(code 201 / 语义串):真正的内部 500 不应被读成「设备离线」
  // (修订:移除 status===500 的过宽映射,避免误导用户去做网络排查)。
  if (code === 201 || msg.includes('signaling') || msg.includes('unavailable')) {
    return 'assist_error_offline';
  }
  if (status === 403 || code === 102 || msg.includes('disabled')) {
    return 'assist_error_disabled';
  }
  if (status === 400 || msg.includes('invalid request body')) {
    return 'assist_error_bad_request';
  }
  return 'assist_error_generic';
}

function createAssistState() {
  const state = ref('idle');
  const endReason = ref('');
  const sessionId = ref('');
  const message = ref('');
  const lastError = ref(''); // i18n KEY
  const busy = ref(false);
  const gatesEnabled = ref(false);
  const gatesLoaded = ref(false);
  const watchStatus = ref('idle'); // idle | connecting | open | closed
  const frames = ref([]);

  let pollTimer = null;
  let ws = null;
  let wsConnectTimer = null;
  let wsReconnectTimer = null;
  let wsRetries = 0;
  let wsWanted = false;

  function applySnapshot(snap) {
    const s = snap && typeof snap === 'object' ? snap : {};
    const prevSessionId = sessionId.value;
    // 稳定形状契约:idle = 零值快照(state 空串),键恒在。"" → idle。
    state.value = typeof s.state === 'string' && s.state ? s.state : 'idle';
    endReason.value = typeof s.end_reason === 'string' ? s.end_reason : '';
    sessionId.value = typeof s.session_id === 'string' ? s.session_id : '';
    message.value = typeof s.message === 'string' ? s.message : '';
    // 会话切换(session A → B)必须清空残留帧:本 composable 为 Task 10 观看
    // 页持有 frames,上一会话的 terminal.* 绝不能混进下一会话。
    if (sessionId.value !== prevSessionId) frames.value = [];
    // 离开 requested(approved/ended)就停轮询:approval 由轮询推进,其它
    // 态已无需 2s 探测。
    if (state.value !== 'requested') stopPolling();
  }

  async function checkGates() {
    try {
      const data = await getAssistGates();
      gatesEnabled.value = !!(data && data.enabled);
    } catch (_) {
      // 取不到开关 = 保守隐藏入口(不误放行)。
      gatesEnabled.value = false;
    } finally {
      gatesLoaded.value = true;
    }
    return gatesEnabled.value;
  }

  async function refresh() {
    try {
      const snap = await getAssistSession();
      applySnapshot(snap);
      lastError.value = '';
      return state.value;
    } catch (err) {
      lastError.value = errorKeyFor(err, 'refresh');
      return state.value;
    }
  }

  async function request(msg) {
    busy.value = true;
    lastError.value = '';
    try {
      const snap = await requestAssist(typeof msg === 'string' ? msg : '');
      applySnapshot(snap);
      return true;
    } catch (err) {
      lastError.value = errorKeyFor(err, 'request');
      return false;
    } finally {
      busy.value = false;
    }
  }

  async function cancel() {
    busy.value = true;
    lastError.value = '';
    try {
      const snap = await cancelAssist();
      applySnapshot(snap);
      return true;
    } catch (err) {
      lastError.value = errorKeyFor(err, 'cancel');
      return false;
    } finally {
      busy.value = false;
    }
  }

  async function end() {
    busy.value = true;
    lastError.value = '';
    try {
      const snap = await endAssist();
      applySnapshot(snap);
      return true;
    } catch (err) {
      lastError.value = errorKeyFor(err, 'end');
      return false;
    } finally {
      busy.value = false;
    }
  }

  function startPolling(intervalMs = POLL_INTERVAL_MS) {
    stopPolling();
    if (typeof setInterval !== 'function') return;
    pollTimer = setInterval(() => {
      refresh();
    }, intervalMs);
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function assistStreamUrl() {
    // 已知限制(P2 预裁决):WS 刻意不走 assistPrefix——观看页宿主直连本机
    // agent(根挂载),不经镜像;镜像 iframe 内的 WS 视为降级,勿当 bug 追。
    const proto = typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss' : 'ws';
    const host = typeof window !== 'undefined' ? window.location.host : '';
    const sid = sessionId.value ? `?session_id=${encodeURIComponent(sessionId.value)}` : '';
    return `${proto}://${host}/api/agent/assist/stream${sid}`;
  }

  function clearConnectTimer() {
    if (wsConnectTimer) {
      clearTimeout(wsConnectTimer);
      wsConnectTimer = null;
    }
  }

  function clearReconnectTimer() {
    if (wsReconnectTimer) {
      clearTimeout(wsReconnectTimer);
      wsReconnectTimer = null;
    }
  }

  function handleFrame(ev) {
    let parsed = null;
    try {
      parsed = JSON.parse(ev && ev.data);
    } catch (_) {
      return;
    }
    if (!parsed || typeof parsed !== 'object') return;
    frames.value.push(parsed);
    if (frames.value.length > WS_FRAME_CAP) {
      frames.value.splice(0, frames.value.length - WS_FRAME_CAP);
    }
    // 归零重连预算只认「生产性入站帧」——与 useAliangChat 同款(其 onopen 注释
    // 记录了 2026-11-04 e2e 实证):服务端接受 Upgrade 后立刻 close(鉴权/会话
    // 漂移)时每轮都会触发 open,若 open 即归零预算,重连封顶永远到不了,退化成
    // 无限 800ms 重连风暴。terminal.* / assist.* 帧才证明通道真正可用。
    if (
      typeof parsed.type === 'string' &&
      (parsed.type.startsWith('terminal.') || parsed.type.startsWith('assist.'))
    ) {
      wsRetries = 0;
    }
    // assist.end 是生命周期唯一终态信号;随后服务端以 1000 关闭,不再重连。
    if (parsed.type === 'assist.end') {
      state.value = 'ended';
      if (typeof parsed.reason === 'string') endReason.value = parsed.reason;
      // 终态若恰在申请态轮询窗口内到达,必须停掉 interval(否则 2s 轮询残留)。
      stopPolling();
    }
  }

  function scheduleReconnect() {
    if (!wsWanted) return;
    if (wsRetries >= WS_RECONNECT_DELAYS_MS.length) {
      watchStatus.value = 'closed';
      return;
    }
    const delay = WS_RECONNECT_DELAYS_MS[wsRetries];
    wsRetries += 1;
    watchStatus.value = 'connecting';
    // 句柄留存:disconnect/reset 时可立即取消挂起的 800ms 重连。
    clearReconnectTimer();
    wsReconnectTimer = setTimeout(() => {
      wsReconnectTimer = null;
      if (wsWanted) openSocket();
    }, delay);
  }

  function openSocket() {
    if (ws && (ws.readyState === 0 || ws.readyState === 1)) return; // CONNECTING/OPEN
    let sock;
    try {
      sock = new WebSocket(assistStreamUrl());
    } catch (_) {
      scheduleReconnect();
      return;
    }
    ws = sock;
    watchStatus.value = 'connecting';
    clearConnectTimer();
    wsConnectTimer = setTimeout(() => {
      if (sock.readyState === 0) {
        try { sock.close(); } catch (_) { /* 已死即跳过 */ }
      }
    }, WS_CONNECT_TIMEOUT_MS);

    sock.onopen = () => {
      if (wsConnectTimer) clearConnectTimer();
      // 刻意不在此归零 wsRetries:见 handleFrame 的「生产性帧才归零」注释。
      if (ws === sock) watchStatus.value = 'open';
    };
    sock.onmessage = (ev) => handleFrame(ev);
    sock.onerror = () => { /* onclose 会接管重连 */ };
    sock.onclose = (ev) => {
      if (wsConnectTimer) clearConnectTimer();
      if (ws === sock) ws = null;
      if (!wsWanted) {
        watchStatus.value = 'closed';
        return;
      }
      const code = ev && typeof ev.code === 'number' ? ev.code : 0;
      // assist.end + close(1000) 是唯一生命周期终态:不再重连。
      if (code === 1000 || state.value === 'ended') {
        watchStatus.value = 'closed';
        wsWanted = false;
        return;
      }
      scheduleReconnect();
    };
  }

  function connect() {
    wsWanted = true;
    wsRetries = 0;
    // 新建观看连接 = 全新视野:清空上一会话/上一轮残留帧。
    frames.value = [];
    clearReconnectTimer(); // 取消可能挂起的重连,避免双开
    openSocket();
  }

  function disconnect() {
    wsWanted = false;
    wsRetries = 0;
    clearConnectTimer();
    clearReconnectTimer();
    if (ws) {
      const sock = ws;
      ws = null;
      try { sock.close(); } catch (_) { /* ignore */ }
    }
    watchStatus.value = 'closed';
  }

  // 轻量瞬态复位:保留 gates(入口显隐依赖它),清空会话/帧/连接/轮询。
  // 弹窗每次打开调用,避免读到上一次会话的 active/ended 瞬态(修订:持久
  // 单例下 immediate watcher 会用陈旧状态同步闪关/显示旧原因)。
  function resetTransient() {
    stopPolling();
    disconnect();
    state.value = 'idle';
    endReason.value = '';
    sessionId.value = '';
    message.value = '';
    lastError.value = '';
    busy.value = false;
    watchStatus.value = 'idle';
    frames.value = [];
  }

  function reset() {
    resetTransient();
    gatesEnabled.value = false;
    gatesLoaded.value = false;
  }

  return {
    state,
    endReason,
    sessionId,
    message,
    lastError,
    busy,
    gatesEnabled,
    gatesLoaded,
    watchStatus,
    frames,
    actions: { checkGates, refresh, request, cancel, end },
    watchWs: { connect, disconnect, status: watchStatus, url: assistStreamUrl },
    polling: { start: startPolling, stop: stopPolling },
    // 便捷扁平别名(弹窗直接解构)。
    checkGates,
    refresh,
    request,
    cancel,
    end,
    startPolling,
    stopPolling,
    resetTransient,
    reset,
  };
}

let singleton = null;

export function useAssist() {
  if (!singleton) singleton = createAssistState();
  return singleton;
}

// 仅供测试隔离:丢弃单例(同时清理定时器/socket)。命名对齐 house 约定
// (useAgentStatus/useTerminalDesktop/useTerminalWindows 的 resetForTests)。
export function resetForTests() {
  if (singleton) {
    singleton.reset();
    singleton = null;
  }
}
