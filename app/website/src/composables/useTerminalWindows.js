import { computed, ref } from 'vue';

// 网页终端多窗注册表(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md §7.3;
// 计划 docs/superpowers/plans/2026-11-22-web-terminal-phase23.md P2.1)。
// 纯前端窗口状态机:注册/最小化/恢复/关闭/z 序/持久化。持久化键
// aliang-web-terminal-windows——刷新恢复全部为 minimized(AliangBoard 同语义:
// 活 WS 无法跨刷新,恢复即挂起壳,重开由用户点任务栏)。
// 模块级单例(同 useWebTerminal 形态),非 Pinia:状态纯前端、无跨端同步面。
// FloatingWindow 壳/任务栏/工作台编排是后续任务(P2.2-P2.4),本文件只管注册表。

export const WINDOWS_STORAGE_KEY = 'aliang-web-terminal-windows';

// z 序上限:当前最大 z 越过此值即把 open 窗重排回紧凑序列(AliangBoard
// zScale renumber 语义)——防长会话单调递增穿透浮窗带、溢出 Number 精度。
const Z_CAP = 10000;

const windows = ref([]); // [{id, title, status:'open'|'minimized', zIndex, createdAt}]

// localStorage 全接触 try/catch(隐私模式/配额满不炸,先例 chatSettings.safeStorage)。
function safeStorage() {
  try {
    return typeof localStorage !== 'undefined' ? localStorage : null;
  } catch {
    return null;
  }
}

function persist() {
  const s = safeStorage();
  if (!s) return;
  try {
    s.setItem(WINDOWS_STORAGE_KEY, JSON.stringify(windows.value));
  } catch {
    /* 持久化是锦上添花:失败降级为本次会话内有效 */
  }
}

// 持久化行 → 恢复条目:status/zIndex/geom 原样保留(2026-11-23 会话保活改造
// +窗口排布持久化)——sessionId=窗 id 后,刷新恢复的 open 窗经 openWindows
// immediate watch 自动挂面板并 attach 同 id 会话,滚动缓冲/活 shell 跨刷新
// 存活,窗直接回桌面;几何随条目恢复,排布原样(此前几何只在 desktop 内存,
// 刷新即弃=每次恢复都落缺省级联位)。缺 id 的脏行丢弃;status 非法值回落
// minimized;geom 脏值回落 null(ensureActivated 落缺省级联位);createdAt
// 缺失/非法补当前时间。
function parseWindowGeom(raw) {
  if (!raw || typeof raw !== 'object') return null;
  const g = {
    x: Number.isFinite(raw.x) ? raw.x : null,
    y: Number.isFinite(raw.y) ? raw.y : null,
    width: Number.isFinite(raw.width) && raw.width > 0 ? raw.width : null,
    height: Number.isFinite(raw.height) && raw.height > 0 ? raw.height : null,
    maximized: raw.maximized === true,
  };
  // 四数值齐才算有效排布,否则整组弃用(半截数据恢复出畸形窗不如缺省位)
  if (g.x === null || g.y === null || g.width === null || g.height === null) return null;
  return g;
}

function parsePersistedRows() {
  const s = safeStorage();
  if (!s) return [];
  let rows;
  try {
    rows = JSON.parse(s.getItem(WINDOWS_STORAGE_KEY) || '[]');
  } catch {
    return [];
  }
  if (!Array.isArray(rows)) return [];
  return rows
    .filter((r) => r && typeof r === 'object' && typeof r.id === 'string' && r.id)
    .map((r) => ({
      id: r.id,
      title: typeof r.title === 'string' ? r.title : '',
      status: r.status === 'open' ? 'open' : 'minimized',
      zIndex: Number.isFinite(r.zIndex) && r.zIndex > 0 ? r.zIndex : 0,
      createdAt: Number.isFinite(r.createdAt) ? r.createdAt : Date.now(),
      geom: parseWindowGeom(r.geom),
    }));
}

// 启动恢复:整表替换(冷启动时表为空,等价于补入)。供模块级调用,也可显式重放。
export function initPersistence() {
  loadPersisted();
}

// z 序重排:open 窗按现序(低→高)压回 1..n;minimized 不占窗口带、不参与。
function renumberOpenWindows() {
  const open = windows.value
    .filter((w) => w.status === 'open')
    .sort((a, b) => (a.zIndex || 0) - (b.zIndex || 0));
  open.forEach((w, i) => {
    w.zIndex = i + 1;
  });
  return open.length;
}

// 取下一个窗口 z:全部窗口 zIndex 最大值 + 1;最大值越过 Z_CAP 时先重排再取
// n+1(重排本身不改窗口相对叠放,只回收号码空间)。
function nextZ() {
  const max = windows.value.reduce((m, w) => Math.max(m, w.zIndex || 0), 0);
  if (max > Z_CAP) return renumberOpenWindows() + 1;
  return max + 1;
}

function findById(id) {
  return windows.value.find((w) => w.id === id);
}

function createWindow(title) {
  const entry = {
    id: 'win-' + Date.now().toString(36) + Math.random().toString(36).slice(2, 6),
    title: typeof title === 'string' ? title : '',
    status: 'open',
    zIndex: nextZ(),
    createdAt: Date.now(),
  };
  windows.value.push(entry);
  persist();
  // 返回表内响应式代理(裸对象不穿代理,外部持引用改字段不触依赖追踪)。
  return windows.value[windows.value.length - 1];
}

function minimizeWindow(id) {
  const w = findById(id);
  if (w) w.status = 'minimized';
  persist();
}

// 恢复=置顶:status 回 open 且 z 取 max+1(AliangBoard restoreTerminal 同款)。
function restoreWindow(id) {
  const w = findById(id);
  if (w) {
    w.status = 'open';
    w.zIndex = nextZ();
  }
  persist();
}

function closeWindow(id) {
  const i = windows.value.findIndex((w) => w.id === id);
  if (i !== -1) windows.value.splice(i, 1);
  persist();
}

// focusWindow 语义(拍板):open 窗=仅置顶不改状态;minimized 窗=恢复语义
// (状态回 open 并置顶)。消费方是任务栏点击(P2.3)——「点了必须有反应」,
// 两种状态各走 restore/置顶一条路,不留第三种静默分支。
function focusWindow(id) {
  const w = findById(id);
  if (!w) return;
  if (w.status === 'minimized') {
    restoreWindow(id);
    return;
  }
  w.zIndex = nextZ();
  persist();
}

function setWindowTitle(id, title) {
  const w = findById(id);
  if (w && typeof title === 'string') w.title = title;
  persist();
}

// 窗口几何落条目并持久化(desktop 的 setPos/toggleMaximize/ensureActivated
// 缺省落位三处调用)——geom 随注册表同键同频落盘,刷新后排布原样恢复。
// 全数值有效才落盘;任一非法(NaN/undefined/非正宽高)整次丢弃并保持条目
// 原值——拖拽中间态可能带 NaN,不许污染持久化。
function saveWindowGeom(id, geom) {
  const w = findById(id);
  if (!w || !geom || typeof geom !== 'object') return;
  if (![geom.x, geom.y, geom.width, geom.height].every(Number.isFinite)) return;
  if (geom.width <= 0 || geom.height <= 0) return;
  w.geom = {
    x: geom.x,
    y: geom.y,
    width: geom.width,
    height: geom.height,
    maximized: geom.maximized === true,
  };
  persist();
}

function getWindowGeom(id) {
  const w = findById(id);
  return (w && w.geom) || null;
}

const openWindows = computed(() =>
  windows.value.filter((w) => w.status === 'open').slice().sort((a, b) => a.zIndex - b.zIndex)
);
const minimizedWindows = computed(() => windows.value.filter((w) => w.status === 'minimized'));

// 模块加载即恢复(先例:chatIdentityCache 模块级加载);loadPersisted 内部已
// 全包裹,此层 try/catch 属双保险,失败不阻断模块加载。
try {
  initPersistence();
} catch {
  /* 启动恢复失败:空表起步 */
}

export function resetForTests() {
  windows.value = [];
  const s = safeStorage();
  if (!s) return;
  try {
    s.removeItem(WINDOWS_STORAGE_KEY);
  } catch {
    /* 存储不可用:清内存态即可 */
  }
}

function loadPersisted() {
  windows.value = parsePersistedRows();
}

export function useTerminalWindows() {
  return {
    windows,
    openWindows,
    minimizedWindows,
    createWindow,
    minimizeWindow,
    restoreWindow,
    closeWindow,
    focusWindow,
    setWindowTitle,
    saveWindowGeom,
    getWindowGeom,
    nextZ,
    loadPersisted,
  };
}
