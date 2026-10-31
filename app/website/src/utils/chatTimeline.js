// 消息流归并(spec §C):messages(权威流)与 steerItems(瞬态插话)按 ts
// 升序合并为统一时间线,打 type 标供渲染层分流。纯函数,脏输入安全。
export function mergeTimeline(messages, steerItems) {
  const a = Array.isArray(messages) ? messages.map(m => ({ type: 'message', ...m })) : [];
  const b = Array.isArray(steerItems) ? steerItems.map(s => ({ type: 'steer', ...s })) : [];
  return [...a, ...b]
    .map((item, idx) => ({ ...item, _idx: idx }))
    .sort((x, y) => (Number.isFinite(x.ts) ? x.ts : Infinity) - (Number.isFinite(y.ts) ? y.ts : Infinity))
    .map(({ _idx, ...rest }) => rest);
}
