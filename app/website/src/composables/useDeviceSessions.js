import { ref } from 'vue';
import { getAgentSessions } from '../services/agentApi';
import { deviceSessionToConversation, normalizeDeviceProvider } from '../utils/chatSettings';

// 设备会话清单（跨端统一阶段 2）：ChatPage 侧栏「后端三源会话」的唯一数据
// 入口。权威在后端（GET /api/agent/sessions），本 composable 不进
// localStorage——每次进页拉取，WS 事件节流刷新。本地草稿仍在 ChatPage 的
// conversations 里，两者在 mergedConversations 合并渲染。

// WS 事件（run.started/done/error、审批族）触发的清单刷新节流窗口。
export const DEVICE_SESSIONS_REFRESH_THROTTLE_MS = 2000;

// 打开设备会话后 5 分钟内重复点击不重拉详情（详情装载幂等，之后的转录更新
// 由 ai.session.snapshot 覆盖）。
export const DEVICE_SESSION_DETAIL_TTL_MS = 5 * 60 * 1000;

// 终端原生会话的 id 前缀约定（claude_<nativeId> / codex_<id>）。带前缀的
// 条目本端没有可直连的 journal，续聊必须走「新建托管会话 + resume」；
// 裸 conversation id（agentcore/托管）则可被 ai.* 帧按 id 直连。
export function isTerminalPrefixedSessionId(id) {
  return typeof id === 'string' && (id.startsWith('claude_') || id.startsWith('codex_'));
}

// resume 目标 id：清单 source_session_id 优先（托管通道回填），缺省从前缀
// id 剥出原生 id（inventory 通道的 claude_/codex_ 条目不带 source_session_id，
// 后端详情解析同样按剥前缀定位原生转录）。非前缀 id 原样返回（不续聊）。
export function deviceResumeSessionId(entry) {
  if (!entry || typeof entry !== 'object') return '';
  const src = typeof entry.source_session_id === 'string' ? entry.source_session_id.trim() : '';
  if (src) return src;
  const id = typeof entry.id === 'string' ? entry.id : '';
  return id.replace(/^(claude|codex)_/, '');
}

export function useDeviceSessions() {
  const deviceSessions = ref([]);
  const available = ref(true);
  let inflight = null;
  let lastRefreshAt = 0;
  let refreshTimer = null;
  // entry.id → 会话壳（deviceSessionToConversation 结果 + UI 字段）。稳定
  // 对象：openDeviceSession 把壳放进会话池后，消息/审批都写在同一对象上，
  // 清单刷新只原位更新清单驱动字段，不换对象（防打开态消息闪断）。
  const shellCache = new Map();

  async function loadDeviceSessions() {
    if (inflight) return inflight; // 单次拉取复用：侧栏清单与设置条「最近项目」共享同一 promise
    inflight = (async () => {
      try {
        const data = await getAgentSessions();
        const list = Array.isArray(data?.sessions) ? data.sessions : [];
        deviceSessions.value = list;
        available.value = true;
        // 200 空列表按「无会话」渲染是已接受的歧义：core 代理失败也回 200 空
        // 列表，区分需另拉 /api/agent/status，v1 不做（阶段 2 计划「已知取舍」）。
      } catch (_) {
        available.value = false; // 网络/代理失败：设备会话不可用，本地草稿照常渲染
      } finally {
        inflight = null;
      }
    })();
    return inflight;
  }

  // 2s 节流：首发即时，窗口内的后续事件合并成一次尾随刷新（不丢最后一次
  // 状态变更——run.done 在 run.started 的节流窗口内到达时仍会刷）。
  function refreshOnEvent() {
    const ts = Date.now();
    if (ts - lastRefreshAt >= DEVICE_SESSIONS_REFRESH_THROTTLE_MS) {
      lastRefreshAt = ts;
      loadDeviceSessions();
      return;
    }
    if (refreshTimer) return;
    refreshTimer = setTimeout(() => {
      refreshTimer = null;
      lastRefreshAt = Date.now();
      loadDeviceSessions();
    }, DEVICE_SESSIONS_REFRESH_THROTTLE_MS - (ts - lastRefreshAt));
  }

  function buildShell(entry) {
    const shell = deviceSessionToConversation(entry);
    if (!shell) return null;
    shell.messages = [];
    shell.approvals = [];
    shell.running = entry.status === 'running'; // 侧栏状态点；快照帧原位更新
    shell.deviceLoadedAt = 0; // 详情装载时刻（openDeviceSession 5 分钟 TTL）
    // 与本地会话的 epoch ms 同一口径，侧栏 today/yesterday/older 分组直接排序
    shell.updatedAt = Date.parse(entry.updated_at || '') || 0;
    // 目录由后端清单权威：设备会话不允许本端改（改了也会被下次清单刷新冲掉）
    shell.dirLocked = true;
    // aiSessionId 绑定策略（阶段 2 计划钉死）：裸 id 直连 journal/ai.* 帧；
    // claude_/codex_ 前缀 → 新建托管会话，resume 字段仅首次 create 消费（Task 4）。
    if (isTerminalPrefixedSessionId(entry.id)) {
      shell.aiSessionId = 'local-' + shell.id;
      shell.resumeSessionId = deviceResumeSessionId(entry);
      shell.resumeProjectPath = entry.project_path || '';
    } else {
      shell.aiSessionId = entry.id;
    }
    return shell;
  }

  function shellForEntry(entry) {
    if (!entry || typeof entry !== 'object' || typeof entry.id !== 'string' || !entry.id) return null;
    const cached = shellCache.get(entry.id);
    if (!cached) {
      const shell = buildShell(entry);
      if (shell) shellCache.set(entry.id, shell);
      return shell;
    }
    // 原位刷新清单驱动字段，保留 messages/approvals/deviceTranscript 等打开态
    cached.title = entry.title || entry.id;
    cached.provider = normalizeDeviceProvider(entry.provider);
    cached.projectPath = entry.project_path || '';
    cached.deviceSession = entry;
    cached.running = entry.status === 'running';
    cached.updatedAt = Date.parse(entry.updated_at || '') || cached.updatedAt || 0;
    // resume 字段仅首次 create：created ack 消费后置 resumeConsumed（ChatPage
    // 侧），清单刷新不得复活——否则每次刷新都会把刚删掉的 resume 加回来。
    if (isTerminalPrefixedSessionId(entry.id) && !cached.resumeConsumed) {
      cached.resumeSessionId = deviceResumeSessionId(entry);
      cached.resumeProjectPath = entry.project_path || '';
    }
    return cached;
  }

  // 会话归属三键查找（阶段 2 计划钉死）：entry.id / source_session_id /
  // managed_conversation_id。供快照/审批族帧把事件挂回设备壳。
  function findDeviceShellBySessionId(sessionId) {
    const sid = typeof sessionId === 'string' ? sessionId : '';
    if (!sid) return null;
    for (const entry of deviceSessions.value) {
      if (!entry || typeof entry !== 'object') continue;
      if (entry.id === sid || entry.source_session_id === sid || entry.managed_conversation_id === sid) {
        return shellForEntry(entry) || null;
      }
    }
    return null;
  }

  return {
    deviceSessions,
    available,
    loadDeviceSessions,
    refreshOnEvent,
    shellForEntry,
    findDeviceShellBySessionId,
  };
}
