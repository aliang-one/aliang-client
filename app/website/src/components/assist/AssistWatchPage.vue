<template>
  <!--
    远程协助 P1 Task 10:客户端观看页(spec §9)。固定全屏覆盖层(同 ChatPage
    / TerminalWorkspace 的 z-[900] 惯例),Router 之外由 useNavigation 的
    currentPage==='assist' 切换(App.vue v-if),与 ChatPage 同款。

    只读结构性保证(spec §10):xterm 以 disableStdin 构造,且本页**不订阅
    onData、不持有任何 send 通道**——观看者对目标设备零交互通道。数据仅为
    单向镜像:useAssist 单例的 watch WS 收到 terminal.output/terminal.replay
    字符串分片,逐帧 write 进 xterm;P2 Task 4 起同一 WS 还会收到页面镜像
    帧族 assist.rr.event / assist.rr.snapshot / assist.nav(设备侧纯中继),
    分流进 rrweb Replayer(live 模式)——Replayer 渲染在自带沙箱 iframe 里,
    观众端只渲染快照副本、不派发任何事件,XSS/双执行面不存在。
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

    <!-- 主体:Tab 双视图(P2 Task 4)——[页面] = rrweb Replayer 镜像,
         [终端] = P1 xterm 现状。两 pane 恒挂载 v-show 显隐:xterm 不因切 Tab
         重初始化(保尺寸/保末屏),Replayer iframe 同理不被重建。 -->
    <div class="flex flex-1 flex-col overflow-hidden">
      <!-- Tab 条:两粒小按钮,样式与顶栏按钮族一致 -->
      <div class="flex shrink-0 items-center gap-1 border-b border-slate-800 bg-slate-900 px-2 py-1">
        <button
          type="button"
          :class="tabClass('page')"
          :aria-pressed="activeTab === 'page'"
          data-testid="assist-watch-tab-page"
          @click="activeTab = 'page'"
        >{{ t('assist_watch_tab_page') }}</button>
        <button
          type="button"
          :class="tabClass('terminal')"
          :aria-pressed="activeTab === 'terminal'"
          data-testid="assist-watch-tab-terminal"
          @click="activeTab = 'terminal'"
        >{{ t('assist_watch_tab_terminal') }}</button>
      </div>

      <!-- 终端 pane:xterm 恒挂载(保尺寸/保末屏),等待/无会话以覆盖层盖在上面 -->
      <div v-show="activeTab === 'terminal'" class="relative flex-1 overflow-hidden bg-black" data-testid="assist-watch-pane-terminal">
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

      <!-- 页面 pane:rrweb Replayer 容器(live 模式);无底座(未收到快照)
           时以占位盖住——增量帧没有 FullSnapshot 底座渲染不出任何东西 -->
      <div v-show="activeTab === 'page'" class="relative flex-1 overflow-hidden bg-black" data-testid="assist-watch-pane-page">
        <div ref="pageHostEl" class="absolute inset-0 overflow-auto" data-testid="assist-watch-page-host"></div>
        <div
          v-if="!pageReady"
          class="absolute inset-0 flex flex-col items-center justify-center gap-3 bg-slate-950/95 px-6 text-center"
          data-testid="assist-watch-page-syncing"
        >
          <span class="inline-block h-3 w-3 animate-pulse rounded-full bg-primary" />
          <p class="text-sm text-slate-200">{{ t('assist_watch_page_syncing') }}</p>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
// rrweb Replayer(P2 Task 4):观看页把 admin 采集端中继来的 rrweb 事件流
// 以 live 模式渲染进沙箱 iframe。v2.2.2 的规范入口就是主包 `rrweb`(其
// export map "." → dist/rrweb.js 同时导出 record 与 Replayer),Replayer 的
// 沙箱/鼠标轨迹样式在其 dist/style.css,需一并引入。
import { Replayer } from 'rrweb';
import 'rrweb/dist/style.css';
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
const pageHostEl = ref(null);
const endConfirming = ref(false);
const elapsedSec = ref(0);
// 动作失败(结束/取消)的瞬态提示:composable 的 lastError 只存 i18n KEY,
// refresh 成功会把它清空——故先快照到本地再收敛真源,渲染用本地 KEY。
const actionError = ref('');
let actionErrorTimer = null;

// —— 页面镜像 Tab(P2 Task 4)——
// 默认 [终端]:P1 已验证的主视图;[页面] 是 admin 开始镜像操作后才有的画面,
// 用户自行点击切换。activeTab 只管显隐(v-show),不影响两 pane 的帧消费。
const activeTab = ref('terminal');
// pageReady = Replayer 已有可渲染底座(快照已注入)。false 时页面 pane 显示
// 「等待画面同步」占位:进入页面/收到 nav 重置/新会话,都回到 false。
const pageReady = ref(false);

let term = null;
let fit = null;
let resizeObserver = null;
let tickTimer = null;
let startedAt = 0;
let disposed = false;

// —— rrweb Replayer 状态 ——
// replayer 恒以 live 惯例构造:new Replayer([], {liveMode:true}) → startLive()
// → 事件经 addEvent 注入。底座(Meta + FullSnapshot)由快照引导:
//   · assist.rr.snapshot → 重建 Replayer,注入快照(数组或单事件都收);
//   · assist.rr.event 带 FullSnapshot(type 2)→ 同样引导建 Replayer(spec
//     §帧族表:rr.event 事件增量「含 FullSnapshot 标记」,首连流量走这条);
//   · 其余 rr.event 增量 → replayer 存在才 addEvent,无底座静默丢弃。
let replayer = null;
// 底座前奏缓冲:Meta 事件(type 4)先行到达且尚无 Replayer 时暂存——Replayer
// 的 iframe 尺寸由 Meta 定(handleResize 才把 iframe 从 display:none 放出来),
// 缺 Meta 时以合成 Meta 兜底。
let pendingPageMeta = null;
// rrweb 事件分型常量(EventType):Meta=4,FullSnapshot=2,增量=3。
const RR_TYPE_FULL_SNAPSHOT = 2;
const RR_TYPE_META = 4;
// 合成 Meta 的兜底视口:admin 采集端的真实视口未知(快照帧只带 FullSnapshot
// 不带 Meta 时才走到这),取常见桌面尺寸;镜像 DOM 会按此宽度回流,不是像素
// 拷贝,宽度差异只影响排版不影响内容。若 pane 处于隐藏 Tab(clientWidth=0),
// 取容器尺寸会得到 0×0 的隐形 iframe,故必须用固定值。
const SYNTH_VIEWPORT = { width: 1280, height: 800 };
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

// Tab 按钮样式:选中态实底,未选中淡字(顶栏按钮族同款灰阶)。
function tabClass(which) {
  return [
    'rounded border px-3 py-1 text-xs font-medium transition-colors',
    activeTab.value === which
      ? 'border-slate-600 bg-slate-700 text-slate-100'
      : 'border-transparent text-slate-400 hover:bg-slate-800 hover:text-slate-200',
  ].join(' ');
}

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

// —— 页面镜像:rrweb Replayer ——
// 载荷防御:rrweb 事件以 JSON 字符串坐 data 字段,坏 JSON / 非 object / 缺
// 数值 type 一律跳过(记一条 warn),绝不向上抛——残帧连片到达时不能把整条
// 帧流(含终端腿)拖死。
function parseRrPayload(data) {
  let ev = data;
  if (typeof data === 'string') {
    try {
      ev = JSON.parse(data);
    } catch (_) {
      console.warn('[assist-watch] rrweb 帧载荷非合法 JSON,已跳过');
      return null;
    }
  }
  if (!ev || typeof ev !== 'object' || typeof ev.type !== 'number') return null;
  return ev;
}

function synthMetaEvent() {
  return {
    type: RR_TYPE_META,
    timestamp: Date.now(),
    data: { href: window.location.href, width: SYNTH_VIEWPORT.width, height: SYNTH_VIEWPORT.height },
  };
}

function destroyReplayer() {
  if (replayer) {
    try {
      replayer.destroy();
    } catch (_) { /* 已残的 Replayer 销毁失败不影响页面 */ }
    replayer = null;
  }
  pageReady.value = false;
}

// 页面镜像整体重置:销毁 Replayer + 丢掉底座前奏缓冲。assist.nav(整页跳转,
// admin 侧录制随文档生命周期中断、重建后重发全量快照)与新会话走这里。
function resetPageMirror() {
  destroyReplayer();
  pendingPageMeta = null;
}

// live 惯例建 Replayer(rrweb 文档口径):空事件数组初始化 → startLive(未来
// baseline) → 之后一律 addEvent 注入。root 指进页面 pane 容器,wrapper/iframe
// 都长在那。
// 两个非默认项(rrweb 2.2.2 本机实测,P2 冒烟定案):
// ①useVirtualDom:false——默认 true 会把增量事件分流进永不挂载的虚拟树
//   (Meta/FullSnapshot 走 rebuild 例外能渲染,增量画面恒不更新);
// ②startLive(未来 baseline)——中继事件的时间戳来自 admin 采集时钟,若
//   ≤ startLive 时刻会走 timer 排队按捕获时距延迟应用(慢数分钟),未来
//   baseline 让一切事件走 isSync 同步分支,实时落地。
function ensureReplayer() {
  if (replayer || disposed || !pageHostEl.value) return;
  try {
    replayer = new Replayer([], {
      liveMode: true,
      useVirtualDom: false,
      root: pageHostEl.value,
    });
    replayer.startLive(Date.now() + 3600_000);
    pageReady.value = true;
  } catch (e) {
    replayer = null;
    pageReady.value = false;
    console.warn('[assist-watch] rrweb Replayer 创建失败', e);
  }
}

function addRrEvent(ev) {
  if (!replayer) return false;
  try {
    replayer.addEvent(ev);
    return true;
  } catch (e) {
    console.warn('[assist-watch] rrweb 事件注入失败,已跳过', e);
    return false;
  }
}

// 底座引导:按序注入 lead(可为空)+ 底座事件。底座集里缺 Meta 就补(缓冲的
// 真 Meta 优先,合成兜底)——Meta 决定 iframe 尺寸,缺了画面恒不可见。
function pageBoot(events) {
  const list = Array.isArray(events) ? events.filter(Boolean) : [];
  if (!list.length) return;
  const hasMeta = list.some((e) => e.type === RR_TYPE_META);
  const withMeta = hasMeta ? list : [pendingPageMeta || synthMetaEvent(), ...list];
  if (!withMeta.some((e) => e.type === RR_TYPE_FULL_SNAPSHOT)) return; // 无底座渲染不出
  resetPageMirror(); // 重建语义:旧 Replayer 销毁,从新底座长出新画面
  ensureReplayer();
  if (!replayer) return;
  for (const ev of withMeta) addRrEvent(ev);
}

function handleRrEvent(frame) {
  const ev = parseRrPayload(frame.data);
  if (!ev) return;
  if (ev.type === RR_TYPE_META && !replayer) {
    pendingPageMeta = ev; // 底座前奏,等 FullSnapshot 一同注入
    return;
  }
  if (ev.type === RR_TYPE_FULL_SNAPSHOT) {
    pageBoot([ev]); // spec 帧族表:FullSnapshot 可经 rr.event 走(首连流量)
    return;
  }
  if (replayer) addRrEvent(ev); // 无底座的增量静默丢弃(等快照重建)
}

// 快照载荷:单事件对象或事件数组均可,线上形态是 JSON 字符串——先整体
// 解出再分型(直接把字符串喂 parseRrPayload 会把数组整体当残帧丢掉)。
function handleRrSnapshot(frame) {
  let payload = frame.data;
  if (typeof payload === 'string') {
    try {
      payload = JSON.parse(payload);
    } catch (_) {
      console.warn('[assist-watch] rrweb 快照载荷非合法 JSON,已跳过');
      return;
    }
  }
  let list = null;
  if (Array.isArray(payload)) {
    list = payload.map((d) => parseRrPayload(d)).filter(Boolean);
  } else if (payload && typeof payload === 'object') {
    const single = parseRrPayload(payload);
    list = single ? [single] : null;
  }
  if (list && list.length) pageBoot(list);
}

// 帧分派:一条 WS 上两族帧(终端 + 页面镜像),按 type 分流。信号族
// (terminal.exit / assist.request 等)与未知类型页面侧无事可做,直接过。
function writeFrame(frame) {
  if (!frame || typeof frame.type !== 'string') return;
  switch (frame.type) {
    case 'terminal.output':
    case 'terminal.replay':
      if (typeof frame.data === 'string' && frame.data.length) term.write(frame.data);
      return;
    case 'assist.rr.event':
      handleRrEvent(frame);
      return;
    case 'assist.rr.snapshot':
      handleRrSnapshot(frame);
      return;
    case 'assist.nav':
      resetPageMirror();
      return;
    default:
      return;
  }
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
    // 新会话开档:上一会话的页面镜像是死会话的残影(admin 侧录制随旧会话
    // 已断),重置回占位,等新会话的快照重建。终态(ended)不清——末屏保留
    // 与终端腿同款口径。
    resetPageMirror();
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
  destroyReplayer(); // 含沙箱 iframe 的整个 wrapper 随 destroy 移除
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
