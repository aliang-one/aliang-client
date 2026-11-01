// 消息流归并(spec §C):messages(权威流)与 steerItems(瞬态插话)按 ts
// 升序合并为统一时间线,打 type 标供渲染层分流。纯函数,脏输入安全。
export function mergeTimeline(messages, steerItems) {
  // type 放 spread 之后钉死:它是渲染层唯一分流键,不允许输入项同名属性覆盖。
  const a = Array.isArray(messages) ? messages.map(m => ({ ...m, type: 'message' })) : [];
  const b = Array.isArray(steerItems) ? steerItems.map(s => ({ ...s, type: 'steer' })) : [];
  return [...a, ...b]
    .map((item, idx) => ({ ...item, _idx: idx }))
    // _idx tiebreaker:两个无 ts 项比较器本会返回 Infinity-Infinity=NaN(排序
    // 结果不确定),tiebreaker 同时修掉该 NaN 不一致,并钉死同 ts 项按 concat
    // 序排列的稳定序。
    .sort((x, y) => ((Number.isFinite(x.ts) ? x.ts : Infinity) - (Number.isFinite(y.ts) ? y.ts : Infinity)) || (x._idx - y._idx))
    .map(({ _idx, ...rest }) => rest);
}
