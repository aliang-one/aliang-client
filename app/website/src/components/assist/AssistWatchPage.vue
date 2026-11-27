<template>
  <!--
    远程协助 P1 Task 10:客户端观看页(spec §9)。固定全屏覆盖层(同 ChatPage
    / TerminalWorkspace 的 z-[900] 惯例),Router 之外由 useNavigation 的
    currentPage==='assist' 切换(App.vue v-if),与 ChatPage 同款。

    只读结构性保证(spec §10):xterm 以 disableStdin 构造,且本页**不订阅
    onData、不持有任何 send 通道**——观看者对目标设备零交互通道。数据仅为
    单向镜像:useAssist 单例的 watch WS 收到 terminal.output/terminal.replay
    字符串分片,逐帧 write 进 xterm。
  -->
  <div class="fixed inset-0 z-[900] flex flex-col bg-slate-950 text-slate-100" data-testid="assist-watch-page">
    <!-- 顶栏:返回 + 状态指示 + 计时 + 结束(双段确认) -->
    <header class="flex items-center justify-between gap-3 border-b border-slate-800 bg-slate-900 px-3 py-2">
      <div class="flex min-w-0 items-center gap-2">
        <button
          type="button"
          class="flex h-8 w-8 items-center justify-center rounded text-slate-400 hover:bg-slate-800 hover:text-slate-100"
          :aria-label="t('assist_watch_back')"
          data-testid="assist-watch-back"
          @click="goBack"
        >
          <span class="material-symbols-outlined text-lg">arrow_back</span>
        </button>
        <span
          v-if="isLive"
          class="inline-block h-2 w-2 shrink-0 animate-pulse rounded-full bg-emerald-400"
          data-testid="assist-watch-live-dot"
        />
        <span class="truncate text-sm font-medium" data-testid="assist-watch-status">{{ statusText }}</span>
        <span v-if="connHint" class="shrink-0 text-xs text-amber-300" data-testid="assist-watch-conn">{{ connHint }}</span>
      </div>

      <div class="flex shrink-0 items-center gap-3">
        <span
          v-if="elapsedText"
          class="tabular-nums text-xs text-slate-400"
          data-testid="assist-watch-timer"
        >{{ elapsedText }}</span>

        <template v-if="isLive || state === 'requested'">
          <template v-if="!endConfirming">
            <button
              v-if="isLive"
              type="button"
              class="rounded border border-rose-500/50 px-3 py-1 text-xs font-medium text-rose-300 hover:bg-rose-500/10"
              data-testid="assist-watch-end"
              @click="endConfirming = true"
            >
              {{ t('assist_watch_end') }}
            </button>
            <button
              v-else
              type="button"
              class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-300 hover:bg-slate-800"
              data-testid="assist-watch-cancel"
              @click="cancelRequest"
            >
              {{ t('assist_cancel') }}
            </button>
          </template>
          <template v-else>
            <span class="text-xs text-rose-300">{{ t('assist_watch_end_confirm') }}</span>
            <button
              type="button"
              class="rounded bg-rose-600 px-3 py-1 text-xs font-bold text-white hover:bg-rose-500"
              data-testid="assist-watch-end-confirm"
              @click="confirmEnd"
            >
              {{ t('assist_watch_confirm') }}
            </button>
            <button
              type="button"
              class="rounded border border-slate-600 px-3 py-1 text-xs font-medium text-slate-300 hover:bg-slate-800"
              data-testid="assist-watch-end-cancel"
              @click="endConfirming = false"
            >
              {{ t('assist_watch_cancel_confirm') }}
            </button>
          </template>
        </template>
      </div>
    </header>

    <!-- 动作失败瞬态提示(结束/取消失败的映射原因,5s 自动消退) -->
    <div
      v-if="actionError"
      class="border-b border-rose-500/30 bg-rose-500/10 px-4 py-1.5 text-xs text-rose-300"
      data-testid="assist-watch-action-error"
    >{{ t(actionError) }}</div>

    <!-- 终态横幅:原因文案(复用 Task 9 的 END_REASON_KEYS 映射) -->
    <div
      v-if="state === 'ended'"
      class="border-b border-amber-500/30 bg-amber-500/10 px-4 py-2 text-sm text-amber-200"
      data-testid="assist-watch-ended"
    >
      {{ t(assistEndReasonKey(endReason)) }}
    </div>

    <!-- 主体:xterm 恒挂载(保尺寸/保末屏),等待/无会话以覆盖层盖在上面 -->
    <div class="relative flex-1 overflow-hidden bg-black">
      <div ref="hostEl" class="absolute inset-0" data-testid="assist-watch-term-host"></div>

      <!-- 待审批覆盖层:管理员尚未开始,等待模式下 WS 已连但无数据 -->
      <div
        v-if="state === 'requested'"
        class="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-slate-950/95 px-6 text-center"
        data-testid="assist-watch-waiting"
      >
        <span class="inline-block h-3 w-3 animate-pulse rounded-full bg-primary" />
        <p class="text-sm text-slate-200">{{ t('assist_watch_waiting') }}</p>
        <p v-if="message" class="max-w-md text-xs text-slate-400">{{ message }}</p>
      </div>

      <!-- 无进行中的协助(页面以无会话状态被打开):空白 + 返回 -->
      <div
        v-if="state === 'idle'"
        class="absolute inset-0 flex flex-col items-center justify-center gap-4 bg-slate-950 px-6 text-center"
        data-testid="assist-watch-none"
      >
        <p class="text-sm text-slate-300">{{ t('assist_watch_none') }}</p>
        <button
          type="button"
          class="rounded bg-primary px-4 py-2 text-sm font-bold text-white hover:bg-primary/90"
          data-testid="assist-watch-none-back"
          @click="goBack"
        >
          {{ t('assist_watch_back') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { useI18n } from '../../i18n';
import { useNavigation } from '../../composables/useNavigation';
import { useEscLayer } from '../../composables/useEscStack';
import { useAssist, assistEndReasonKey } from '../../composables/useAssist';

const { t } = useI18n();
const { showDashboard } = useNavigation();
const {
  state,
  endReason,
  message,
  lastError,
  watchStatus,
  frames,
  watchWs,
  refresh,
  cancel,
  end,
  startPolling,
  stopPolling,
} = useAssist();

const hostEl = ref(null);
const endConfirming = ref(false);
const elapsedSec = ref(0);
// 动作失败(结束/取消)的瞬态提示:composable 的 lastError 只存 i18n KEY,
// refresh 成功会把它清空——故先快照到本地再收敛真源,渲染用本地 KEY。
const actionError = ref('');
let actionErrorTimer = null;

let term = null;
let fit = null;
let resizeObserver = null;
let tickTimer = null;
let startedAt = 0;
let disposed = false;
// 帧游标:上一帧已写对象(对象身份锚)。用「最后一帧身份」当 watch 触发源,
// 而非靠 length 增量——frames 是上限 2000 的环形数组,封顶后 push+splice 同步
// 发生,净长度不变,Vue 的 length watcher 会因新旧值相等而不触发,导致丢帧。
// 身份锚对 append、封顶裁剪、connect 清空三种变化都成立,且每拍只写新增。
let lastWrittenFrame = null;
// 重连清屏闩:服务端每次新连接都重发整段 ring 回放,旧内容必须先清掉再写新批,
// 否则旧屏 + 新回放会叠加。watchStatus 从「曾 open」再次回到 open 即判定为重连。
let wasOpen = false;
let pendingClear = false;

const isLive = computed(() => state.value === 'active');

// 顶栏只放短状态标签:详细文案由主体覆盖层/终态横幅承担,避免逐字重复。
// idle 不挂标签(主体已说明「无进行中的协助」)。
const statusText = computed(() => {
  if (state.value === 'requested') return t('assist_watch_status_waiting');
  if (state.value === 'active') return t('assist_watch_manager_acting');
  if (state.value === 'ended') return t('assist_watch_status_ended');
  return '';
});

const connHint = computed(() => {
  if (state.value !== 'active') return '';
  if (watchStatus.value === 'connecting') return t('assist_watch_connecting');
  if (watchStatus.value === 'closed') return t('assist_watch_disconnected');
  return '';
});

function formatElapsed(sec) {
  const total = Math.max(0, Math.floor(sec));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`;
}

const elapsedText = computed(() => {
  if (!isLive.value && state.value !== 'ended') return '';
  if (elapsedSec.value <= 0) return '';
  return t('assist_watch_elapsed', { time: formatElapsed(elapsedSec.value) });
});

// —— xterm:只读镜像 ——
// 有意不复用 TerminalPane.vue:后者与 useTerminalSocket(双向 WS + onData 上行)
// 硬耦合、无只读 prop;本页是需要 disableStdin + 零 send 的纯推送面,直接实例化
// 是刻意分歧,不是遗漏(渲染器/FitAddon/RO 仍沿用其封装手法)。
function initTerminal() {
  term = new Terminal({
    disableStdin: true, // 结构性只读:键盘输入永不进 PTY
    cursorBlink: false,
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, monospace',
    theme: { background: '#000000' },
    scrollback: 5000,
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  if (hostEl.value) {
    term.open(hostEl.value);
    try { fit.fit(); } catch (_) { /* 零量纲(隐藏挂载)忽略,RO 稍后自纠 */ }
  }
  if (typeof ResizeObserver !== 'undefined' && hostEl.value) {
    resizeObserver = new ResizeObserver(() => {
      try { fit.fit(); } catch (_) { /* 未量纲忽略 */ }
    });
    resizeObserver.observe(hostEl.value);
  }
}

function writeFrame(frame) {
  if (!frame || typeof frame.type !== 'string') return;
  if (frame.type !== 'terminal.output' && frame.type !== 'terminal.replay') return;
  if (typeof frame.data === 'string' && frame.data.length) term.write(frame.data);
}

// 帧游标推进:只写自重连/挂载以来的新增帧。锚点法见 lastWrittenFrame 注释。
function processFrames() {
  if (!term) return;
  const arr = frames.value;
  if (pendingClear) {
    // 清屏但**保留锚点**:旧帧已写过,清屏后只写锚点之后的新回放批,避免重放。
    pendingClear = false;
    term.clear();
  }
  let start = 0;
  if (lastWrittenFrame) {
    const idx = arr.indexOf(lastWrittenFrame);
    // 命中:从锚点后一帧起写。未命中(极端长会话把锚点挤出 2000 环):保守
    // 从头写当前窗口,宁可重复不可丢帧。
    start = idx >= 0 ? idx + 1 : 0;
  }
  for (let i = start; i < arr.length; i += 1) writeFrame(arr[i]);
  lastWrittenFrame = arr.length ? arr[arr.length - 1] : null;
}

function fitOnce() {
  try { fit.fit(); } catch (_) { /* 忽略 */ }
}

// —— 计时器:仅 active 期间走动,终态冻结显示 ——
function syncTimer() {
  if (isLive.value) {
    if (!startedAt) startedAt = Date.now();
    elapsedSec.value = Math.floor((Date.now() - startedAt) / 1000);
    if (!tickTimer && typeof setInterval === 'function') {
      tickTimer = setInterval(() => {
        elapsedSec.value = Math.floor((Date.now() - startedAt) / 1000);
      }, 1000);
    }
  } else if (tickTimer) {
    clearInterval(tickTimer);
    tickTimer = null;
  }
}

function ensureConnected() {
  if (disposed) return;
  if (watchStatus.value === 'open' || watchStatus.value === 'connecting') return;
  watchWs.connect();
}

// 动作失败提示:快照 KEY 后 5s 自动消退(house 内联闻例,不引全局 toast)。
function showActionError(key) {
  actionError.value = key || 'assist_error_generic';
  if (actionErrorTimer) clearTimeout(actionErrorTimer);
  actionErrorTimer = setTimeout(() => {
    actionErrorTimer = null;
    actionError.value = '';
  }, 5000);
}

onMounted(async () => {
  initTerminal();
  // 以 idle 进入(刷新/直接进页):向服务端对齐一次真源,可能有进行中的会话。
  if (state.value === 'idle') {
    try { await refresh(); } catch (_) { /* 失败保持 idle 屏 */ }
  }
  // await 期间组件可能已被卸载(用户立刻返回):卸载链已断连,不得再建连。
  if (disposed) return;
  if (state.value === 'requested') startPolling();
  if (state.value === 'requested' || state.value === 'active') ensureConnected();
  syncTimer();
  processFrames(); // 挂载前已到达的滞留帧(如从弹窗切页前缓冲的回放)
});

watch(state, (next) => {
  if (next === 'requested') {
    startPolling();
    ensureConnected();
  } else {
    stopPolling();
  }
  if (next === 'active') {
    // 审批通过 → 必保有一条活连接:wait 模式零流量,服务端 90s 读窗会静默
    // 关闭等待 WS,重连预算仅在「生产性帧」归零(Task 9 设计),长等待
    // (>~4.5min,小于 5min pending 过期窗)后预算已耗尽、watchStatus=closed。
    // 此处 ensureConnected 在 closed 时重新 connect()(清帧+重置预算),恢复
    // 审批后实时帧;open/connecting 则 no-op。否则长等待获批 = 永久空白终端。
    ensureConnected();
    syncTimer();
    nextTick(fitOnce);
  }
  if (next === 'ended') {
    endConfirming.value = false;
    syncTimer(); // 停表,冻结在 ended 时的秒数
  }
});

// 重连判定:曾 open 且再次 open = 新连接(服务端重发整段 ring 回放)。
watch(watchStatus, (next) => {
  if (next !== 'open') return;
  if (wasOpen) pendingClear = true;
  wasOpen = true;
});

// 帧触发源:最后一帧身份变化(append / 封顶裁剪 / connect 清空都会触发)。
watch(
  () => frames.value[frames.value.length - 1],
  () => { processFrames(); },
);

onUnmounted(() => {
  disposed = true;
  if (tickTimer) { clearInterval(tickTimer); tickTimer = null; }
  if (actionErrorTimer) { clearTimeout(actionErrorTimer); actionErrorTimer = null; }
  if (resizeObserver) { resizeObserver.disconnect(); resizeObserver = null; }
  if (term) { term.dispose(); term = null; }
  stopPolling();
  // 只断 WS,不 resetTransient:弹窗的瞬态复位是它自己的职责(Task 9 注记)。
  watchWs.disconnect();
});

useEscLayer('assist-watch', goBack, computed(() => true));

function goBack() {
  watchWs.disconnect();
  stopPolling();
  showDashboard();
}

async function cancelRequest() {
  // 取消申请 → 服务端落 ended,WS 随终帧/关闭收敛,页面显示终态原因。
  const ok = await cancel();
  if (!ok) {
    // 失败(如 409 无待审批):展示映射原因,并拉一次真源收敛(死 WS 场景)。
    showActionError(lastError.value);
    await refresh();
  }
}

async function confirmEnd() {
  endConfirming.value = false;
  const ok = await end();
  if (!ok) {
    // 409「无可结束」等:提示一句 + 一次 refresh 从服务端真源收敛(可能其实
    // 已 ended / idle,或死 WS 尚未收到终帧)——不再静默。
    showActionError(lastError.value);
    await refresh();
  }
}
</script>
