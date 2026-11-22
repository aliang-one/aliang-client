<template>
  <!--
    网页终端工作台(Task 16 阶段 1 + P2.4 多窗编排重构,计划 2026-11-22-web-terminal-phase23):
    固定全屏覆盖层(同 ChatPage z-[900] 惯例)。门链三段保持阶段 1 原样——
    未开启(开闸卡)→ 未设 PIN(PinGuard setup)→ 已锁(PinGuard unlock);
    已解锁分支自 P2.4 起改为多窗浮窗桌面。分支切换即组件重建,PinGuard 局部态
    (输入/倒计时)天然不复用,无需手动复位。
    桌面编排:注册表(useTerminalWindows)是窗列表唯一权威(建/缩/复/关/z 序);
    几何与本会话挂载态归 useTerminalDesktop(工作台本地,注册表保持纯——P2.1
    评审拍板)。FloatingWindow 壳 v-for 注册表 + activated 门控:只有本会话曾
    open 过的窗才挂 TerminalPane——刷新恢复的窗在注册表里全是 minimized 壳
    (注册表刷新恢复语义),点任务栏重开=全新会话(旧会话随页面进程消失,无法
    attach 回放;诚实语义:壳可恢复、会话不可恢复)。openWindows watch 统一走
    ensureActivated 激活路:新建窗与重开恢复壳同一条道。
    面板保活铁律:minimized 只 v-show(壳 body 藏不销毁),WS+xterm 全程活着,
    切回不闪断(spec §11);关窗才 v-for 摘除,走 TerminalPane 卸载链收 WS。
    z 带拍板(P2.3 评审带陷阱):窗层与任务栏各住独立叠放带——窗层容器 z-20,
    注册表 zIndex 只在容器内竞争(单调递增也永不穿透);任务栏包在 z-30 带里
    恒在窗上,窗与栏零竞争。窗层 bottom-10 给任务栏(h-10)让位;拖拽钳制
    (clampWindowPos)按视口算,KEEP_VISIBLE_Y=40 恰≈栏高,可接受(任务契约注记)。
    单窗 dead:markDead 打 ⚠ 头栏徽标,再按刷新到的保险箱状态 classifyDead
    二分——状态翻转(关闸/PIN 重置/锁定)→ 全局回退 PinGuard+清注册表(阶段 1
    行为);保险箱健康 → 仅窗级死,徽标留给用户手动处理,不牵连其它窗。
    锁箱/急停/外部关闸 → 一并清注册表:锁箱即无窗,防陈旧 minimized 条目
    恢复进锁箱。
    文件浏览窗(P3.6,spec §7.4 单窗):「文件」钮三态循环(开/最小化/恢复),
    不进注册表(工作台本地态,语义见 script 注记),z 恒浮终端之上;锁箱等
    四条清窗路径连带关闭(FS 请求将随令牌失效全 403,不留死窗)。
  -->
  <div class="fixed inset-0 z-[900] flex flex-col bg-slate-100 dark:bg-slate-950" data-testid="terminal-workspace">
    <!-- 头部:标题+管理钮+关闭 -->
    <div class="flex items-center justify-between border-b border-slate-200 px-4 py-2 dark:border-slate-800">
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-slate-400">terminal</span>
        <span class="text-sm font-medium text-slate-900 dark:text-slate-100">{{ t('term_title') }}</span>
      </div>
      <div class="flex items-center gap-2">
        <!-- 新终端随桌面分支走(不只看 unlocked):外部关闸后 token 仍在、unlocked
             仍真,但桌面分支已退场——按钮若留,会把窗建进隐藏桌面(P2.5 关闸路径
             补测发现的陈旧态口子);改密/上锁/急停是保险箱操作,仍按 unlocked 门控。
             「文件」同新终端门控(P3.6):开的是 FS 面同门同令牌,桌面退场即无意义 -->
        <button v-if="unlocked && st && st.enabled" type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-files" @click="toggleBrowser">{{ t('fs_open') }}</button>
        <button v-if="unlocked && st && st.enabled" type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-new" @click="createTerminal">{{ t('win_new') }}</button>
        <button v-if="unlocked" type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-change-pin" @click="showChange = !showChange">{{ t('vault_change_pin') }}</button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-lock" @click="onLock">{{ t('vault_lock_now') }}</button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-killall" @click="onKillall">{{ t('vault_killall') }}</button>
        <button type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-close" @click="showDashboard()">{{ t('term_close') }}</button>
      </div>
    </div>

    <!-- 主体:状态链 未开启→未设PIN→已锁→已解锁多窗桌面 -->
    <div class="min-h-0 flex-1 p-3">
      <div v-if="st && !st.enabled" class="flex h-full items-center justify-center">
        <div class="max-w-md rounded-xl border border-slate-200 bg-white p-6 text-center dark:border-slate-800 dark:bg-slate-900">
          <p class="mb-2 text-sm text-slate-600 dark:text-slate-300">{{ t('vault_disabled_desc') }}</p>
          <button type="button" class="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white" data-testid="terminal-enable" @click="onEnable">{{ t('vault_enable') }}</button>
          <p v-if="enableError" class="mt-2 text-sm text-red-600" data-testid="terminal-enable-error">{{ enableError }}</p>
        </div>
      </div>
      <PinGuard v-else-if="st && st.enabled && !st.pin_set" mode="setup" @setup-done="onPinReady" />
      <PinGuard v-else-if="st && st.enabled && st.pin_set && !unlocked" mode="unlock" @unlocked="onPinReady" />
      <template v-else-if="unlocked">
        <div class="flex h-full min-h-0 flex-col">
          <div v-if="showChange" class="mx-auto mb-3 w-full max-w-sm rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" data-testid="terminal-change-panel">
            <!-- 改 PIN 内联表单:旧PIN/新PIN/提交 -->
            <input v-model="changeOld" type="password" inputmode="numeric" maxlength="4" autocomplete="off" :placeholder="t('vault_enter_pin')" class="mb-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100" data-testid="terminal-change-old" />
            <input v-model="changeNew" type="password" inputmode="numeric" maxlength="4" autocomplete="off" :placeholder="t('vault_new_pin')" class="mb-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100" data-testid="terminal-change-new" />
            <p v-if="changeError" class="mb-2 text-sm text-red-600" data-testid="terminal-change-error">{{ changeError }}</p>
            <button type="button" class="w-full rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white" data-testid="terminal-change-submit" @click="submitChange">{{ t('vault_set_btn') }}</button>
          </div>
          <!-- 多窗桌面:relative 锚定窗层带/任务栏带;改 PIN 面板展开时占据顶部 -->
          <div class="relative min-h-0 flex-1" data-testid="terminal-desktop">
            <!-- 窗层带 z-20:注册表 z 只在本容器内叠放;任务栏可见时 bottom-10 让位 -->
            <div ref="desktopEl" class="absolute inset-x-0 top-0 z-20" :class="hasWindows ? 'bottom-10' : 'bottom-0'" data-testid="terminal-windows-layer">
              <template v-for="w in windows" :key="w.id">
                <!-- activated 门控:本会话从未 open 过的窗(刷新恢复壳)不挂壳——
                     点任务栏重开时经 openWindows watch 激活,壳+面板此时才出现 -->
                <FloatingWindow
                  v-if="desk.activated[w.id]"
                  :title="desk.geometry[w.id] && desk.geometry[w.id].dead ? `⚠ ${w.title}` : w.title"
                  v-bind="desk.effectiveGeom(w.id)"
                  :z-index="w.zIndex"
                  :active="w.id === activeId"
                  :minimized="w.status === 'minimized'"
                  @move="(x, y) => desk.setPos(w.id, x, y)"
                  @minimize="reg.minimizeWindow(w.id)"
                  @maximize-toggle="desk.toggleMaximize(w.id)"
                  @close="desk.closeWindow(w.id)"
                  @focus="reg.focusWindow(w.id)"
                >
                  <!-- 窗体点击聚焦直通:pointerdown 不 .stop——xterm 仍收指针;
                       背景窗点体即抬升(P2.2 评审随身条款) -->
                  <div class="h-full w-full" @pointerdown="reg.focusWindow(w.id)">
                    <TerminalPane :token="token" @exit="statusRefresh" @dead="onPaneDead(w.id)" />
                  </div>
                </FloatingWindow>
              </template>
              <!-- 文件浏览窗(P3.6,spec §7.4 单窗):与终端窗同层同壳语义项——
                   v-if 挂载(browserOpen)即建 store(树/选中随窗生灭),最小化只
                   v-show 壳内藏(FloatingWindow minimized 契约,保活语义同终端);
                   z 拍板=恒浮于终端之上(maxTerminalZ+10,见 browserZ 注记) -->
              <FileBrowserWindow
                v-if="browserOpen"
                v-bind="browserEffectiveGeom"
                :z-index="browserZ"
                :active="browserActive"
                :minimized="browserMinimized"
                @move="(x, y) => setBrowserPos(x, y)"
                @minimize="browserMinimized = true"
                @maximize-toggle="toggleBrowserMaximize"
                @close="browserOpen = false"
                @focus="onBrowserFocus"
              />
            </div>
            <!-- 空桌面:提示+新建入口(任务栏零窗自隐,此处是唯一居中入口);
                 文件窗开着时桌面并非空态,提示退场 -->
            <div v-if="!hasWindows && !browserOpen" class="pointer-events-none absolute inset-0 flex flex-col items-center justify-center gap-2 text-slate-400 dark:text-slate-500" data-testid="terminal-empty">
              <span class="material-symbols-outlined text-3xl">terminal</span>
              <p class="text-sm">{{ t('win_empty_hint') }}</p>
              <button type="button" class="pointer-events-auto rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white" data-testid="terminal-new-empty" @click="createTerminal">{{ t('win_new') }}</button>
            </div>
            <!-- 任务栏带 z-30:包一层建独立叠放带,窗层(z-20)内任意 zIndex 恒在其下;
                 尾 + 钮 emit create(P2.5)接本工作台 createTerminal(同头部新建入口) -->
            <div class="absolute inset-x-0 bottom-0 z-30" data-testid="terminal-taskbar-band">
              <TerminalTaskbar :active-id="activeId" @create="createTerminal" />
            </div>
          </div>
        </div>
      </template>
      <!-- agent 离线/状态拉取失败:st 为 null 时四个分支全不命中,给一句提示
           而非空白体(终审松散端#3)。loading 态显示「处理中」,失败显示通用错。 -->
      <div v-else class="flex h-full items-center justify-center text-sm text-slate-500 dark:text-slate-400" data-testid="terminal-state-pending">
        {{ wt.loading.value ? t('vault_working') : t('vault_error_generic') }}
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, onMounted, ref, watch } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from '../composables/useNavigation';
import { useEscStack } from '../composables/useEscStack';
import { useWebTerminal } from '../composables/useWebTerminal';
import { useTerminalWindows } from '../composables/useTerminalWindows';
import { cascadePos, classifyDead, useTerminalDesktop } from '../composables/useTerminalDesktop';
import PinGuard from './terminal/PinGuard.vue';
import TerminalPane from './terminal/TerminalPane.vue';
import FloatingWindow from './terminal/FloatingWindow.vue';
import TerminalTaskbar from './terminal/TerminalTaskbar.vue';
import FileBrowserWindow from './terminal/FileBrowserWindow.vue';

const { t } = useI18n();
const { showDashboard } = useNavigation();
const wt = useWebTerminal();
const st = computed(() => wt.status.value);
const { token, unlocked } = wt;
const showChange = ref(false);
const enableError = ref('');
const changeOld = ref('');
const changeNew = ref('');
const changeError = ref('');

// 多窗编排:注册表(窗列表权威)+桌面编排态(几何/挂载标记,工作台本地)
const reg = useTerminalWindows();
const desk = useTerminalDesktop();
const windows = reg.windows;
const hasWindows = computed(() => windows.value.length > 0);
// 活动窗=z 最高 open 窗(openWindows 已按 z 升序,末位即顶);零 open=空串
const activeId = computed(() => {
  const open = reg.openWindows.value;
  return open.length ? open[open.length - 1].id : '';
});

// 激活门:open 窗一旦出现(新建/任务栏重开/回页续开)即挂壳+面板。恢复壳
// (刷新后全 minimized)不经此处,点任务栏转 open 才激活——重开=新会话。
watch(
  reg.openWindows,
  (list) => {
    const count = Math.max(0, windows.value.length - 1);
    list.forEach((w) => desk.ensureActivated(w.id, count));
  },
  { immediate: true }
);

// 泄漏修剪(P2.5):任务栏✕直连注册表 closeWindow(旁路 desk.closeWindow 的
// 本地清理),几何/挂载标记会残留孤儿。watch 注册表窗 id 列表(getter 每次
// 产新数组,增删必触发),变化即 desk.pruneOrphans()——desk.closeWindow 自身
// 已同步清理,那一轮恒为 no-op。
watch(
  () => windows.value.map((w) => w.id),
  () => desk.pruneOrphans()
);

const desktopEl = ref(null);
let desktopRO = null;
// 窗层容器量纲 → 最大化满幅面积:RO 跟随(视口/任务栏显隐变化),jsdom 无 RO
// 守卫降级为元素出现时读一次(量纲 0 也不炸,仅最大化窗退化零幅)。
watch(desktopEl, (el) => {
  if (desktopRO) {
    desktopRO.disconnect();
    desktopRO = null;
  }
  if (!el) return;
  desk.syncViewport(el.clientWidth, el.clientHeight);
  if (typeof ResizeObserver !== 'undefined') {
    desktopRO = new ResizeObserver(() => desk.syncViewport(el.clientWidth, el.clientHeight));
    desktopRO.observe(el);
  }
});

const { push: escPush, remove: escRemove } = useEscStack();
let escId = null;

onMounted(() => {
  void wt.refresh();
  // ESC 层:工作台内 Esc 退回仪表盘(spec §7.1;层由组件自管,同 ChatPage 惯例)
  escId = escPush('terminal-workspace', () => showDashboard());
});

onUnmounted(() => {
  if (escId !== null) escRemove(escId);
  if (desktopRO) {
    desktopRO.disconnect();
    desktopRO = null;
  }
});

function createTerminal() {
  // 标题序号=注册表长度+1(任务契约);i18n 模板「终端 {n}」
  const n = windows.value.length + 1;
  desk.createTerminalWindow(t('win_title', { n }));
}

// —— 文件浏览窗(P3.6,spec §7.4 单窗)——
// 拍板(工作项 P3.6「DECIDE pragmatically」):不进 useTerminalWindows 注册表
// (注册表是终端窗状态机+localStorage 持久化,文件窗无会话无持久化诉求),
// 工作台本地三态:browserOpen(挂载)/browserMinimized(壳内 v-show 藏,保活
// 语义同终端窗)/browserGeom({x,y,maximized};关窗保留,重开复用原位)。状态
// 工作台本地=离开终端页即弃——文件树缓存/选中本就随 FileBrowserWindow 挂载
// 生灭(per-window store),持久化几何只会复活一个空树,不划算。
const browserOpen = ref(false);
const browserMinimized = ref(false);
const browserActive = ref(false);
const browserGeom = ref(null); // {x,y,maximized}|null(未开过)

// 终端侧最高 z(注册表全窗含最小化;z 序单调,最大值即层内顶)
const maxTerminalZ = computed(() => reg.windows.value.reduce((m, w) => Math.max(m, w.zIndex || 0), 0));
// z 拍板(工作项给出并拍板记录):单文件窗在窗层带内恒浮于全部终端之上
// (maxTerminalZ+10)——终端置顶/聚焦不越过它;文件窗碍事时拖开或最小化。
// focus 只影响高亮归属(browserActive),不改层序——简单且行为可预期。
const browserZ = computed(() => maxTerminalZ.value + 10);

// 展示几何:maximized=窗层满幅(复用 desk.viewport,同一 RO 同一容器量纲);
// 否则原位 860×540(FileBrowserWindow 契约量纲,壳内 props 化只为最大化)
const browserEffectiveGeom = computed(() => {
  const g = browserGeom.value;
  if (!g) return { x: 80, y: 80, width: 860, height: 540 };
  if (g.maximized) return { x: 0, y: 0, width: desk.viewport.value.w, height: desk.viewport.value.h };
  return { x: g.x, y: g.y, width: 860, height: 540 };
});

// 文件钮三态循环(单钮即全控,无任务栏 pill):关→开(缺省几何按当前终端数
// 级联错位,与终端窗同款 cascade);开且可见→最小化;最小化→恢复+高亮。
function toggleBrowser() {
  if (!browserOpen.value) {
    if (!browserGeom.value) browserGeom.value = { ...cascadePos(windows.value.length), maximized: false };
    browserOpen.value = true;
    browserMinimized.value = false;
    browserActive.value = true;
    return;
  }
  if (browserMinimized.value) {
    browserMinimized.value = false;
    browserActive.value = true;
    return;
  }
  browserMinimized.value = true;
  browserActive.value = false;
}

function setBrowserPos(x, y) {
  const g = browserGeom.value;
  if (!g) return;
  if (Number.isFinite(x)) g.x = x;
  if (Number.isFinite(y)) g.y = y;
}

function toggleBrowserMaximize() {
  const g = browserGeom.value;
  if (g) g.maximized = !g.maximized;
}

// 文件窗聚焦:最小化态先恢复(壳头栏点击即复活,语义同终端 focusWindow 的
// restore 分支);高亮归属文件窗。
function onBrowserFocus() {
  if (browserMinimized.value) browserMinimized.value = false;
  browserActive.value = true;
}

// 高亮互斥:任一终端成为活动窗(新建/聚焦/任务栏恢复,activeId 必变)即撤
// 文件窗高亮;文件窗置顶 z 不受影响(见 browserZ 拍板)。
watch(activeId, (id) => {
  if (id) browserActive.value = false;
});

// 关文件窗(锁箱/急停/全局重锁/外部关闸统一调用):同「锁箱即无窗」语义——
// 不留文件窗开进一个令牌已失效的桌面(FS 请求将全 403)。几何保留(复用原位)。
function closeBrowser() {
  browserOpen.value = false;
  browserMinimized.value = false;
  browserActive.value = false;
}

async function onEnable() {
  enableError.value = '';
  const res = await wt.setEnabled(true);
  if (!res || !res.ok) enableError.value = t('vault_error_generic');
}

async function onPinReady() {
  await wt.refresh();
}

// 锁箱:服务端清令牌(wt.lock 内 token 置空 → 门链回落密码盘)+本地清窗——
// 锁箱即无窗,防陈旧 minimized 条目解锁后恢复进一个已换了令牌的桌面。
async function onLock() {
  await wt.lock();
  desk.closeAllWindows();
  closeBrowser();
}

// 单窗 dead:先窗级标记(头栏 ⚠),再按刷新到的保险箱状态分类处置——
// global-relock:清令牌回落 PinGuard 并清注册表(阶段 1 行为,改 PIN 面板一并
// 收起防陈旧输入复现);window-only:窗留死徽标,用户手动关/开,不牵连其它窗。
async function onPaneDead(id) {
  desk.markDead(id);
  const stNow = await wt.refresh();
  if (classifyDead(stNow) === 'global-relock') {
    wt.invalidateToken();
    showChange.value = false;
    changeOld.value = '';
    changeNew.value = '';
    changeError.value = '';
    desk.closeAllWindows();
    closeBrowser();
  }
}

async function submitChange() {
  changeError.value = '';
  if (!/^\d{4}$/.test(changeNew.value)) { changeError.value = t('vault_bad_form'); return; }
  const res = await wt.changePin(changeOld.value, changeNew.value);
  if (res && res.ok) {
    showChange.value = false;
    changeOld.value = '';
    changeNew.value = '';
  } else if (res && res.reason === 'bad_old_pin') {
    changeError.value = t('vault_wrong_pin');
  } else if (res && res.reason === 'bad_pin_form') {
    changeError.value = t('vault_bad_form');
  } else if (res && res.reason === 'bad_token') {
    wt.invalidateToken();
    changeError.value = t('vault_error_generic');
  } else {
    changeError.value = t('vault_error_generic');
  }
}

// 急停:服务端杀全部会话 + 清令牌 + 清窗(同锁箱语义:终端面整场退到密码盘)
async function onKillall() {
  if (!window.confirm(t('vault_killall_confirm'))) return;
  await wt.killall();
  wt.invalidateToken();
  desk.closeAllWindows();
  closeBrowser();
  await wt.refresh();
}

// 外部关闸(设置页等路径把 enabled 置 false,刷新后此处可观测)→ 同锁箱清窗:
// 防陈旧条目在别处重新开闸后恢复进一个会话已被杀光的桌面
watch(
  () => st.value?.enabled === false,
  (off) => {
    if (off) {
      desk.closeAllWindows();
      closeBrowser();
    }
  }
);

async function statusRefresh() {
  await wt.refresh();
}
</script>
