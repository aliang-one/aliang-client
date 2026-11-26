// 会话信息抽屉的纯呈现工具(spec 2026-11-26-chat-info-drawer-design):无进展
// 守卫阈值与百分比数学。数据归一化(帧/详情 snake_case → camelCase)收在
// useAliangChat(与 subagentFrameToReducerMsg 同址同惯用法),本文件只管展示侧
// 数学,保持零依赖可测。

// 无进展守卫阈值(与手机端 DiagnosticsPanel.tsx 同源:dup 3 / err 5 / trunc 2
// ——内核守卫触发点即这三个上限)。键=诊断快照的 camelCase 字段名。
export const GUARD_MAXES = { dupStreak: 3, errStreak: 5, truncStreak: 2 };

// streak/max → 0-100 整数百分比,封顶 100(连击越过阈值后仍以「超载」呈现,
// 不回绕);非数值输入或 max 非正返回 null——渲染层以 null 判据隐藏该条,
// 不画空表。streak 为 0/负值时返回 0(合法的「未连击」)。
export function guardPct(streak, max) {
  // null/undefined/空串显式判缺(Number(null)===0 会把「无数据」静默洗成 0%,
  // 与窗口缺失同缺陷)——先于数值化拦截。
  if (streak === null || streak === undefined || streak === '') return null;
  if (max === null || max === undefined || max === '') return null;
  const s = Number(streak);
  const m = Number(max);
  if (!Number.isFinite(s) || !Number.isFinite(m) || m <= 0) return null;
  if (s <= 0) return 0;
  return Math.min(100, Math.round((s / m) * 100));
}

// 上下文用量百分比与守卫同一套数学(used/window,封顶 100);窗口缺失时
// 返回 null,面板显示「暂无用量数据」而非 0% 假表。
export const contextPct = guardPct;
