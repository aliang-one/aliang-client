import { reactive, ref } from 'vue';
import { useTerminalWindows } from './useTerminalWindows';

// 网页终端多窗桌面编排态(P2.4,计划 docs/superpowers/plans/2026-11-22-web-terminal-phase23):
// 窗几何(id→{x,y,width,height,maximized,dead})与本会话面板挂载标记(activated)。
// 刻意不进 useTerminalWindows 注册表——P2.1 评审拍板:注册表保持纯窗口状态机
// (可持久化、可被任务栏等常驻面直接消费);几何/挂载态是工作台本地 UI 态,
// 刷新即弃、随桌面重建。模块级单例(同 useWebTerminal/useTerminalWindows 形态):
// 离开终端页组件卸载后位置仍驻内存,回来 open 窗重挂面板(=新会话,旧 WS 不跨
// 卸载,语义同刷新恢复)。纯函数 cascadePos/classifyDead 独立导出供 vitest 直测
// (先例 floatingWindowGeom)。

// 任务契约缺省窗量纲(P2.4)
export const DEFAULT_WINDOW_WIDTH = 760;
export const DEFAULT_WINDOW_HEIGHT = 480;

const CASCADE_BASE = 40; // 首窗落点
const CASCADE_STEP = 28; // 每级偏移
const CASCADE_WRAP = 5; // 5 级绕回,防长列拖出视口

// 级联落点:count=当前窗数(即新窗 0 起序号)→ {x,y}=40+(count%5)*28(任务契约值)。
// 负数/非数值按 0 防御(数学取模绕回,恒落 0..4 档)。
export function cascadePos(count) {
  const n = Number.isFinite(count) ? Math.trunc(count) : 0;
  const k = ((n % CASCADE_WRAP) + CASCADE_WRAP) % CASCADE_WRAP;
  return { x: CASCADE_BASE + k * CASCADE_STEP, y: CASCADE_BASE + k * CASCADE_STEP };
}

export const DEAD_GLOBAL_RELOCK = 'global-relock';
export const DEAD_WINDOW_ONLY = 'window-only';

// 单窗 dead 分类(P2.4 拍板):TerminalPane 上抛 dead 后刷新保险箱状态,按最新
// 状态二分处置。global-relock=保险箱整面不可用——状态字段语义见
// agent_web_terminal.go Status():关闸 enabled=false / PIN 被重置 pin_set=false /
// 锁定态 locked=true(锁定倒计时)——工作台回退 PinGuard 并清注册表(阶段 1
// 行为);window-only=保险箱健康,只是这一窗的会话/网络死亡——窗留死徽标由
// 用户手动关/开,不牵连其它窗、不踢登录。状态拉取失败(null)≠保险箱已锁,
// 不武断踢出,按 window-only 降级。
export function classifyDead(statusAfterRefresh) {
  if (!statusAfterRefresh || typeof statusAfterRefresh !== 'object') return DEAD_WINDOW_ONLY;
  if (statusAfterRefresh.enabled === false) return DEAD_GLOBAL_RELOCK;
  if (statusAfterRefresh.pin_set === false) return DEAD_GLOBAL_RELOCK;
  if (statusAfterRefresh.locked === true) return DEAD_GLOBAL_RELOCK;
  return DEAD_WINDOW_ONLY;
}

const geometry = reactive({}); // id → {x,y,width,height,maximized,dead}
const activated = reactive({}); // id → true:本会话曾 open 过(挂过面板)
const viewport = ref({ w: 0, h: 0 }); // 窗层容器量纲(最大化窗的满幅面积源)

// 激活=确保挂壳资格:置 activated 标记并补缺省几何(已有几何则原样保留,幂等)。
// 工作台对 openWindows 的 watch 调此处——新建窗与「点任务栏重开恢复壳」同走
// 一条激活路;恢复壳首开=全新会话(旧 WS 不跨刷新,语义见工作台头注)。
function ensureActivated(id, cascadeCount) {
  if (!id || typeof id !== 'string') return null;
  activated[id] = true;
  if (!geometry[id]) {
    const p = cascadePos(cascadeCount);
    geometry[id] = {
      x: p.x,
      y: p.y,
      width: DEFAULT_WINDOW_WIDTH,
      height: DEFAULT_WINDOW_HEIGHT,
      maximized: false,
      dead: false,
    };
  }
  return geometry[id];
}

// 新建窗:注册表登记(标题由调用方组,含 i18n)+立即激活(新建即 open 即挂面板)。
function createTerminalWindow(title) {
  const reg = useTerminalWindows();
  const entry = reg.createWindow(title);
  // 级联 count=新窗序号(创建后窗数-1 == 创建前窗数,任务契约「count=当前窗数」)
  ensureActivated(entry.id, reg.windows.value.length - 1);
  return entry;
}

// 拖拽回写(FloatingWindow @move 已是 clamp 后绝对坐标);非数值防御忽略
function setPos(id, x, y) {
  const g = geometry[id];
  if (!g) return;
  if (Number.isFinite(x)) g.x = x;
  if (Number.isFinite(y)) g.y = y;
}

// 最大化切换:只翻标记,x/y/w/h 原值保留(还原时回原位),满幅尺寸由展示层取
function toggleMaximize(id) {
  const g = geometry[id];
  if (g) g.maximized = !g.maximized;
}

// 窗级死亡标记(浮窗头栏 ⚠ 徽标的数据源;全局回退判定归 classifyDead)
function markDead(id) {
  const g = geometry[id];
  if (g) g.dead = true;
}

// 关窗:注册表摘除(v-for 卸载壳 → TerminalPane 卸载链收 WS)+本地态清理
function closeWindow(id) {
  useTerminalWindows().closeWindow(id);
  delete geometry[id];
  delete activated[id];
}

// 清全部:锁箱/急停/外部关闸调用(锁箱即无窗,防陈旧 minimized 条目恢复进锁箱)。
// 快照注册表全量 id 再逐个关——含从未激活的恢复壳,且边遍历边删会跳项。
function closeAllWindows() {
  const ids = useTerminalWindows().windows.value.map((w) => w.id);
  for (const id of ids) closeWindow(id);
}

// 孤儿修剪(P2.5):注册表条目被旁路摘除(任务栏✕直连注册表 closeWindow,不经
// desk.closeWindow 的本地清理)后,geometry/activated 残留孤儿条目。以注册表现存
// id 集为准裁本地两表;供工作台 watch 注册表窗列表调用——desk.closeWindow 自身
// 已同步清理,那一轮对它恒为 no-op。纯内存操作,不回写注册表。
function pruneOrphans() {
  const live = new Set(useTerminalWindows().windows.value.map((w) => w.id));
  for (const id of Object.keys(geometry)) {
    if (!live.has(id)) delete geometry[id];
  }
  for (const id of Object.keys(activated)) {
    if (!live.has(id)) delete activated[id];
  }
}

// 展示几何:maximized=窗层满幅(容器量纲,忽略存量 x/y/w/h);否则存量原值
function effectiveGeom(id) {
  const g = geometry[id];
  if (!g) return { x: 0, y: 0, width: DEFAULT_WINDOW_WIDTH, height: DEFAULT_WINDOW_HEIGHT };
  if (g.maximized) return { x: 0, y: 0, width: viewport.value.w, height: viewport.value.h };
  return { x: g.x, y: g.y, width: g.width, height: g.height };
}

// 窗层容器量纲上报(工作台 RO/挂载回调);非数值防御归零
function syncViewport(w, h) {
  viewport.value = {
    w: Number.isFinite(w) ? w : 0,
    h: Number.isFinite(h) ? h : 0,
  };
}

export function resetForTests() {
  for (const k of Object.keys(geometry)) delete geometry[k];
  for (const k of Object.keys(activated)) delete activated[k];
  viewport.value = { w: 0, h: 0 };
}

export function useTerminalDesktop() {
  return {
    geometry,
    activated,
    viewport,
    ensureActivated,
    createTerminalWindow,
    setPos,
    toggleMaximize,
    markDead,
    closeWindow,
    closeAllWindows,
    pruneOrphans,
    effectiveGeom,
    syncViewport,
  };
}
