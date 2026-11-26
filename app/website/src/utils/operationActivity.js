// 统一操作进度(feat/unified-operation-progress)wire 帧纯函数:parseOperation
// 整帧校验、mergeOperation 按 operation_id 归账、operationPercent 算百分比。
// 全程 snake_case 透传(Go wire 契约原样,不做 camelCase 改写),零 Vue/网络
// 依赖,全部纯函数可单测。
//
// 三条不变量(终态钉死守卫与 src/utils/subagentPanel.js 同型;两处词表各自
// 独立、语义对齐,本表多 waiting/cancelled 两词,勿混用):
//   ① parse 整帧口径:任一必填字段不合法 → 整帧拒收(null),不带病入库;
//   ② merge revision 单调:只认严格更大,重放/乱序/等号一律原引用返回;
//   ③ 终态钉死:终态行(completed/failed/cancelled/interrupted)不得被更新帧
//      翻回活跃(queued/running/waiting),revision 再新也不收——否则终态卡
//      翻回转圈(与 subagentPanel 迟到 progress 不复活条目同一教训)。

// phase 词表:前三为活跃态,后四为终态。词表是硬契约,未知词整帧拒收。
const PHASES = new Set(['queued', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'interrupted']);
const TERMINAL_PHASES = new Set(['completed', 'failed', 'cancelled', 'interrupted']);

// RFC3339(ISO 8601 的 profile,Go time.RFC3339 原生产出):完整日期+T+时分秒,
// 可选小数秒,必须带 Z 或 ±HH:MM 时区。Date.parse 兜底拒正则放行的越界分量
// (13 月等),同时拒绝日期串('2026-11-25')与自然语言串('November 25, 2026')。
const ISO_TIMESTAMP_RE = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/;

const isNonEmptyString = v => typeof v === 'string' && v.length > 0;

// 可选字段语义:undefined/null 一律视作「未提供」,与 JSON null(Go 指针字段
// 空值的常见产出)等价——既不进校验,也不落进输出。
const isProvided = v => v !== undefined && v !== null;

// progress 形状:current 必填有限非负;total 可选(提供则须有限正数);unit
// 可选字符串(契约只对 operation_id/title/source_kind 提 nonempty,unit 不加)。
// progress 一旦提供却不合法 → 整帧拒收:进度是渲染层唯一百分比来源,残缺帧
// 宁缺毋滥,避免「有 total 无 current」这类半帧被下游拼出假进度。
const parseProgress = progress => {
  if (!isProvided(progress)) return undefined;
  if (!progress || typeof progress !== 'object' || Array.isArray(progress)) return null;
  const { current, total, unit } = progress;
  if (!Number.isFinite(current) || current < 0) return null;
  if (isProvided(total) && (!Number.isFinite(total) || total <= 0)) return null;
  if (isProvided(unit) && typeof unit !== 'string') return null;
  const out = { current };
  if (isProvided(total)) out.total = total;
  if (isProvided(unit)) out.unit = unit;
  return out;
};

// wire 帧 → 校验后的规整副本(只保留白名单键,未知键丢弃防脏数据流进渲染层);
// 校验失败返回 null。任何情况下不返回入参引用。
export function parseOperation(raw) {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return null;
  const { operation_id, title, phase, source_kind, revision, updated_at } = raw;
  if (!isNonEmptyString(operation_id) || !isNonEmptyString(title)
    || !isNonEmptyString(source_kind)) return null;
  if (!PHASES.has(phase)) return null;
  if (!Number.isSafeInteger(revision) || revision <= 0) return null;
  if (!isNonEmptyString(updated_at) || !ISO_TIMESTAMP_RE.test(updated_at)
    || !Number.isFinite(Date.parse(updated_at))) return null;
  const progress = parseProgress(raw.progress);
  if (progress === null) return null;
  const out = { operation_id, title, phase, source_kind, revision, updated_at };
  if (progress !== undefined) out.progress = progress;
  for (const key of ['source_device_id', 'source_device_name', 'session_id', 'project_id', 'parent_operation_id', 'kind',
    'stage', 'step', 'message', 'error_code', 'started_at', 'finished_at']) {
    if (typeof raw[key] === 'string') out[key] = raw[key];
  }
  for (const key of ['rate', 'eta_seconds']) {
    if (Number.isFinite(raw[key]) && raw[key] >= 0) out[key] = raw[key];
  }
  if (Array.isArray(raw.transitions)) {
    out.transitions = raw.transitions.slice(-16).filter(item =>
      item && PHASES.has(item.phase) && typeof item.at === 'string' && Number.isFinite(Date.parse(item.at)));
  }
  return out;
}

// 按 operation_id 归账:仅当帧有效且 revision 严格大于存量(或存量缺行)时返回
// 新 Map(原 Map 永不被改);拒收/过期/等号一律原引用返回,不触发无谓响应式。
// 终态钉死优先于 revision:终态行遇到翻活跃的更新帧,revision 再新也不收。
// state 非 Map(null/脏值)视作空账兜底;存量行 revision 脏值视作 0 让位,
// 行本身是垃圾值时整体替换(防旧代码写入的残行永久卡位)。
export function mergeOperation(state, raw) {
  const parsed = parseOperation(raw);
  if (!parsed) return state;
  const base = state instanceof Map ? state : null;
  const prev = base ? base.get(parsed.operation_id) : undefined;
  if (prev) {
    const prevRev = Number.isSafeInteger(prev.revision) ? prev.revision : 0;
    if (parsed.revision <= prevRev) return state;
    if (TERMINAL_PHASES.has(prev.phase) && !TERMINAL_PHASES.has(parsed.phase)) return state;
  }
  const next = new Map(base ?? []);
  next.set(parsed.operation_id, parsed);
  return next;
}

// 百分比:total 未知(缺 progress/缺 total/total 非正有限)返回 null,渲染层
// 据此切「不确定进度」态;否则 (current/total)*100 钳到 0–100——上限吃掉
// current>total 的乱序帧,下限兜住手搓对象的负值(parse 产物不会出现)。
// current 非有限按 0 兜底(total 已知即有确定进度,起点为零)。
export function operationPercent(operation) {
  const progress = operation && typeof operation === 'object' ? operation.progress : null;
  if (!progress || typeof progress !== 'object') return null;
  const { current, total } = progress;
  if (!Number.isFinite(total) || total <= 0) return null;
  const done = Number.isFinite(current) ? current : 0;
  return Math.min(100, Math.max(0, (done / total) * 100));
}

export const isActiveOperation = operation => !TERMINAL_PHASES.has(operation.phase);

export function operationElapsedLabel(operation, now) {
  if (operation.phase === 'queued' || typeof operation.started_at !== 'string') return null;
  const start = Date.parse(operation.started_at);
  const end = isActiveOperation(operation)
    ? now
    : typeof operation.finished_at === 'string' ? Date.parse(operation.finished_at) : NaN;
  if (!Number.isFinite(start) || !Number.isFinite(end)) return null;

  const seconds = Math.max(0, Math.floor((end - start) / 1000));
  const hours = Math.floor(seconds / 3600);
  const minutes = String(Math.floor(seconds / 60) % 60).padStart(2, '0');
  const remainder = String(seconds % 60).padStart(2, '0');
  return hours ? `${hours}:${minutes}:${remainder}` : `${minutes}:${remainder}`;
}
