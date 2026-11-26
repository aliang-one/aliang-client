<!-- TransportRow.vue — 面板②传输行(spec §4.2 + 交互契约 §6-1/2/3 + 拍板 2)。
     一行三控件:启停合一主钮 / 重启 38×38 图标钮 / 模式 segmented。
     动作链路:点击 → api 层 + 本地过渡态(localTransition 写入 inject 的
     panelData,StatusHeader 同步渲染过渡 spinner)→ 等 summary 轮询回读
     翻转目标态才撤过渡(§6-1);api 失败立即撤过渡 + warn toast;
     api 成功但轮询迟迟不翻转由 6s 看门狗强撤,控件绝不永久锁死。
     模式 segmented(拍板 2):running/过渡一律禁用,消除 SwitchMode
     「停而不起」陷阱(运行中调用 SwitchMode 会同步停代理且不拉起);
     仅 stopped 可点——乐观选中 + setMode 落盘(POST /api/run/swift),
     成功保留选中、失败回滚,summary 回读后一律以服务端为准。 -->
<script setup>
import { computed, inject, onUnmounted, ref, watch } from 'vue';
import { allControlsDisabled, derivePanelState } from '../state';
import { restartProxy, setMode, startProxy, stopProxy } from '../api';

// 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询。
const { summary, connLost, panelState, localTransition } = inject('panelData');
const toast = inject('panelToast');

// modeLabel — run.mode 线上值→中文(http=常规 / tun=深度,models.RunMode)。
function modeLabel(mode) {
  if (mode === 'tun') return '深度';
  if (mode === 'http') return '常规';
  return '';
}

const busy = computed(() => localTransition.value !== null);
const TRANSITION_LABELS = { starting: '启动中', stopping: '停止中', restarting: '重启中' };
const mainLabel = computed(() => TRANSITION_LABELS[localTransition.value] || '');

// ---- 过渡态确认(§6-1:点按钮 → 过渡 → summary 回读翻转才解除) ----

// remoteKind:不含过渡覆盖的远端七态。确认 watcher 必须盯它而非注入的
// panelState——panelState 带过渡覆盖,过渡不撤它永远停在过渡值,拿不到
// 「远端已翻转」这个信号(自指死锁)。
const remoteKind = computed(() => derivePanelState(summary.value, connLost.value, null));

// confirm — 远端命中目标态:撤过渡 + 成功 toast + 模式选中回正。
function confirm(what) {
  clearConfirmTimer();
  localTransition.value = null;
  adoptServerMode();
  const label = modeLabel(summary.value && summary.value.run ? summary.value.run.mode : '');
  if (what === 'starting') toast.show(label ? `代理已启动（${label}模式）` : '代理已启动');
  else if (what === 'stopping') toast.show('代理已停止');
  else if (what === 'restarting') toast.show('代理已重启');
}

watch(remoteKind, (kind, prevKind) => {
  // 进入 running 稳态(本次启停或外部启动):模式选中以服务端为准回正。
  // running 态 segmented 本就禁用,显示值必须与实际运行模式(状态头副标题
  // 读 summary.run.mode)一致,否则两处打架。
  if (kind === 'running' && prevKind !== 'running') adoptServerMode();
  const t = localTransition.value;
  if (!t) return;
  // start/restart 等远端回 running;stop 等远端回 stopped。restart 中段
  // summary 短暂翻 stopped 不算命中(它的目标态是 running)。
  if ((t === 'starting' || t === 'restarting') && kind === 'running') confirm(t);
  else if (t === 'stopping' && kind === 'stopped') confirm(t);
});

// 看门狗:api 已成功返回但轮询迟迟不翻转(连续失败/服务端卡住)时强撤
// 过渡——否则三控件永久锁死(过渡态优先级压过 connLost 的 error 态,
// 用户会被困在「启动中」出不来)。6s = 3 个轮询周期,confirm/卸载即回收。
const CONFIRM_TIMEOUT_MS = 6000;
let confirmTimer = null;
function clearConfirmTimer() {
  if (confirmTimer !== null) {
    clearTimeout(confirmTimer);
    confirmTimer = null;
  }
}
function armConfirmWatchdog(what) {
  clearConfirmTimer();
  confirmTimer = setTimeout(() => {
    confirmTimer = null;
    if (localTransition.value === what) localTransition.value = null;
  }, CONFIRM_TIMEOUT_MS);
}
onUnmounted(clearConfirmTimer);

// ---- 启停合一主钮(§6-1) ----

async function onMain() {
  const kind = panelState.value;
  // 防抖 + 未登录全禁 + error→启动/running→停止/stopped→启动,全在这一条闸。
  if (allControlsDisabled(kind)) return;
  const what = kind === 'running' ? 'stopping' : 'starting';
  localTransition.value = what;
  try {
    if (what === 'stopping') await stopProxy();
    else await startProxy();
    armConfirmWatchdog(what); // 成功后等回读;失败走 catch 立即撤
  } catch (err) {
    localTransition.value = null;
    toast.show(err && err.message ? err.message : '代理操作失败', 'warn');
  }
}

// ---- 重启(§6-2:仅运行态可用;点击图标自转) ----

async function onRestart() {
  if (panelState.value !== 'running') return;
  localTransition.value = 'restarting';
  try {
    await restartProxy(); // stop→400ms→start 已在 api 层封装
    armConfirmWatchdog('restarting');
  } catch (err) {
    localTransition.value = null;
    toast.show(err && err.message ? err.message : '重启失败', 'warn');
  }
}

// ---- 模式 segmented(拍板 2:仅 stopped 可点) ----

const selectedMode = ref('http');
let lastServerMode = '';
// adoptServerMode — 采纳服务端模式回读值。只在「服务端值确实变了」或
// 「过渡确认(进入新稳态)」时调用,本地点击的乐观选中不被轮询同值弹回。
function adoptServerMode() {
  const run = summary.value && summary.value.run;
  const m = run && typeof run.mode === 'string' ? run.mode : '';
  if (m) {
    lastServerMode = m;
    selectedMode.value = m;
  }
}
watch(
  () => {
    const run = summary.value && summary.value.run;
    return run && typeof run.mode === 'string' ? run.mode : '';
  },
  (m) => {
    if (m && m !== lastServerMode) adoptServerMode();
  },
  { immediate: true }
);

// 模式落盘在途闸:上一次 setMode 未返回前再点直接忽略(防连点双发)。
let modePending = false;

// onMode — 停止态点击:乐观选中 → setMode 落盘(拍板 2「下次启动生效」的
// 服务端真身)→ 成功保留选中,summary 回读以服务端为准;失败回滚 + warn
// toast 带服务端 msg。running/过渡态被 segDisabled + 本闸双重挡住
// (SwitchMode 运行中调用会同步停代理且不拉起——拍板 2 禁用的原因)。
async function onMode(mode) {
  if (panelState.value !== 'stopped') return; // 拍板 2:仅停止态可点
  if (modePending) return;
  if (selectedMode.value === mode) return;
  const previous = selectedMode.value;
  selectedMode.value = mode; // 乐观选中,失败回滚
  modePending = true;
  try {
    await setMode(mode);
    const label = modeLabel(mode);
    toast.show(label ? `已切换为${label}模式（下次启动生效）` : '已切换模式（下次启动生效）');
  } catch (err) {
    selectedMode.value = previous;
    toast.show(err && err.message ? err.message : '模式切换失败', 'warn');
  } finally {
    modePending = false;
  }
}

// ---- 派生给模板 ----

// 主钮:过渡=琥珀 busy;running=红⏹停止;其余=绿▶启动(error/logged_out 也显启动)。
const mainClass = computed(() => {
  if (busy.value) return 'busy';
  return panelState.value === 'running' ? 'stop-live' : 'start-idle';
});
// 主钮禁用:过渡防抖 + 未登录全禁(spec §3)。过渡态靠 .busy 的
// pointer-events:none 保持琥珀可见;未登录加 .disabled 灰化。
const mainDisabled = computed(() => allControlsDisabled(panelState.value));
const mainTitle = computed(() => {
  if (busy.value) return '';
  if (panelState.value === 'logged_out') return '未登录，无法启动';
  return panelState.value === 'running' ? '停止代理' : '启动代理';
});
// 重启禁用:仅 running 可用(§6-2);过渡态由行级 .locked 统一压暗,
// 不叠 .off 双重透明。
const restartDisabled = computed(() => !busy.value && panelState.value !== 'running');
const restartBusy = computed(() => busy.value);
// seg 禁用:running/过渡/error/logged_out 全禁,仅 stopped 可点(拍板 2)。
const segDisabled = computed(() => panelState.value !== 'stopped');
const segTitle = computed(() => {
  const kind = panelState.value;
  if (kind === 'running' || kind === 'starting' || kind === 'stopping' || kind === 'restarting') {
    return '停止后可切换';
  }
  if (kind === 'logged_out') return '未登录，暂不可切换';
  if (kind === 'error') return '状态异常，暂不可切换';
  return '';
});
</script>

<template>
  <div class="transport-row" :class="{ locked: busy }">
    <!-- 启停合一(§6-1):stopped 绿▶启动 / running 红⏹停止 / 过渡琥珀 spinner+文案 -->
    <button
      class="t-btn"
      :class="[mainClass, { disabled: panelState === 'logged_out' }]"
      :disabled="mainDisabled"
      :title="mainTitle"
      type="button"
      data-act="main"
      @click="onMain"
    >
      <template v-if="busy">
        <span class="spin"></span><span>{{ mainLabel }}</span>
      </template>
      <template v-else-if="panelState === 'running'">
        <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><rect x="7" y="7" width="10" height="10" rx="1.5" /></svg><span>停止</span>
      </template>
      <template v-else>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="currentColor"><path d="M8 5.5v13l11-6.5z" /></svg><span>启动</span>
      </template>
    </button>

    <!-- 重启 38×38 图标钮(§6-2):仅 running 可用;过渡态图标自转 -->
    <button
      class="t-btn sq"
      :class="{ busy: restartBusy, off: restartDisabled }"
      :disabled="restartDisabled"
      title="重启代理"
      type="button"
      data-act="restart"
      @click="onRestart"
    >
      <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-2.64-6.36L21 8" /><path d="M21 3v5h-5" /></svg>
    </button>

    <!-- 模式 segmented(拍板 2):running/过渡禁用 + title「停止后可切换」 -->
    <div class="seg" :class="{ 'seg-disabled': segDisabled }" :title="segTitle">
      <div class="opt" :class="{ on: selectedMode === 'http' }" data-act="mode" data-v="http" @click="onMode('http')">常规</div>
      <div class="opt" :class="{ on: selectedMode === 'tun' }" data-act="mode" data-v="tun" @click="onMode('tun')">深度</div>
    </div>
  </div>
</template>

<style scoped>
/* 数值全部抄自 mockup 的 .transport-row/.t-btn/.seg/.opt 段原值;深色变体
   按 theme.css 口径用 prefers-color-scheme 媒体查询(mockup body.dark 等价)。 */
.transport-row {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 2px 12px 10px;
}

.t-btn {
  height: 38px;
  min-width: 56px;
  padding: 0 14px;
  border-radius: 11px;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 7px;
  font-size: 12.5px;
  font-weight: 700;
  background: rgba(0, 0, 0, 0.05);
  cursor: pointer;
  transition: all 0.18s;
  flex-shrink: 0;
  border: none;
  font-family: inherit;
  color: inherit;
}
@media (prefers-color-scheme: dark) {
  .t-btn {
    background: rgba(255, 255, 255, 0.07);
  }
}
.t-btn:active {
  transform: scale(0.96);
}
/* 停止态主钮:--brand-dim 底绿▶(浅深两色文字色不同,mockup 同款) */
.t-btn.start-idle {
  background: var(--brand-dim);
  color: #15803d;
}
@media (prefers-color-scheme: dark) {
  .t-btn.start-idle {
    color: #4ade80;
  }
}
/* 运行态主钮:红 13% 底 + 红描边 inset */
.t-btn.stop-live {
  background: rgba(229, 72, 77, 0.13);
  color: #d93840;
  box-shadow: inset 0 0 0 1px rgba(229, 72, 77, 0.25);
}
/* 过渡态主钮:琥珀底 + spinner;pointer-events none 挡住点击(mockup 同款) */
.t-btn.busy {
  background: rgba(230, 162, 60, 0.12);
  color: #b97a1e;
  pointer-events: none;
}
@media (prefers-color-scheme: dark) {
  .t-btn.busy {
    color: #f0b357;
  }
}
/* 未登录禁用灰化(spec §3 三键全禁;.busy 优先级在其后声明不受影响) */
.t-btn.disabled {
  opacity: 0.45;
  pointer-events: none;
  color: var(--text-muted);
}
/* 重启方形钮(§6-2):38×38;停止态 0.35 透明禁用;过渡态图标自转 */
.t-btn.sq {
  min-width: 38px;
  width: 38px;
  padding: 0;
}
.t-btn.sq.off {
  opacity: 0.35;
  pointer-events: none;
}
.t-btn.sq.busy svg {
  animation: spin 0.8s linear infinite;
}

/* 过渡期行级锁(§6-1):三控件全禁 + 压暗 */
.transport-row.locked .t-btn,
.transport-row.locked .seg {
  opacity: 0.45;
  pointer-events: none;
}

/* 模式 segmented(mockup .seg/.opt 原值):flex 撑满剩余宽度 */
.seg {
  flex: 1;
  display: flex;
  min-width: 0;
  border-radius: 10px;
  padding: 3px;
  background: rgba(0, 0, 0, 0.05);
  border: 1px solid rgba(0, 0, 0, 0.05);
  transition: opacity 0.2s;
}
@media (prefers-color-scheme: dark) {
  .seg {
    background: rgba(255, 255, 255, 0.06);
    border-color: rgba(255, 255, 255, 0.05);
  }
}
.seg .opt {
  flex: 1;
  text-align: center;
  font-size: 11.5px;
  font-weight: 600;
  padding: 5px 0;
  border-radius: 8px;
  color: #7d8f85;
  cursor: pointer;
  transition: all 0.15s;
  white-space: nowrap;
}
.seg .opt.on {
  background: #fff;
  color: #17251d;
  box-shadow: 0 1px 4px rgba(0, 0, 0, 0.12);
}
@media (prefers-color-scheme: dark) {
  .seg .opt {
    color: #7e9186;
  }
  .seg .opt.on {
    background: rgba(255, 255, 255, 0.14);
    color: #eef5f0;
  }
}
/* 拍板 2:非停止态禁用(0.45 + pointer-events none),title 由模板给 */
.seg.seg-disabled {
  opacity: 0.45;
  pointer-events: none;
}
.seg.seg-disabled .opt {
  cursor: default;
}

/* 主钮内过渡 spinner(mockup busy 款:border-top-color 取 currentColor) */
.spin {
  width: 13px;
  height: 13px;
  border-radius: 50%;
  border: 2px solid rgba(0, 0, 0, 0.12);
  border-top-color: currentColor;
  animation: spin 0.8s linear infinite;
  flex-shrink: 0;
}
@media (prefers-color-scheme: dark) {
  .spin {
    border-color: rgba(255, 255, 255, 0.15);
  }
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
