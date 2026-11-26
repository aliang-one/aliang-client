// 回合 id 归一（症状1修复 2026-11-26）：'assistant_abc.option'→'abc'、
// 'assistant_abc'→'abc'、'abc'→'abc'。option 续跑回合的 message_id 带
// .option 后缀（后端 agent_ai_run_msg.go: pending.MessageID+".option"），
// 前端 ai.error 迟到判定/reconcile 失败条挂回/卡片锚定都需要剥壳后的裸
// 回合 id 才能对上 user 气泡的 messageId。与 useAliangChat isOptionRow 的
// /^assistant_.+\.option$/ 同一词表。
export function chatTurnBareId(mid) {
  return String(mid || '')
    .replace(/^assistant_/, '')
    .replace(/\.option$/, '');
}

// 卡片锚定（症状2升级项 2026-11-26）：按卡自带 messageId（剥壳归一后=发起
// 回合的裸 mid）反查该回合的消息段，把卡定位到「回合末尾 ~ 下一条 user 泡」
// 之间——比纯 createdAt 更稳：重连快照重投/对账补建的回合 ts 与卡片首建
// 时刻错位时，createdAt 会把卡排到 steer/后继回合之后（漂移窗口），锚定法
// 按 messageId 反查恒落在发起回合段内。clamp 三语义：
//   下限=回合段内最大 ts（防卡早于回合答案泡）；
//   上限=下一条 user 泡 ts-1（防重放/重投的迟到卡越过后继回合）；
//   无后继 user 泡=Infinity（卡落会话尾）。
// anchored=false（无 messageId/反查不中——活跃兜底跨会话卡、旧 agent 帧、
// 快照恢复无 id 审批卡）回落调用方的 createdAt 兜底，不丢卡。
export function resolveCardAnchor(card, messages) {
  const bare = chatTurnBareId(card && card.messageId);
  if (!bare) return { ts: 0, anchored: false };
  const msgs = Array.isArray(messages) ? messages : [];
  let uIdx = -1;
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i] && msgs[i].role === 'user' && msgs[i].messageId === bare) { uIdx = i; break; }
  }
  if (uIdx < 0) return { ts: 0, anchored: false };
  let segEnd = Number(msgs[uIdx] && msgs[uIdx].ts);
  if (!Number.isFinite(segEnd)) segEnd = 0;
  for (let i = uIdx + 1; i < msgs.length && msgs[i] && msgs[i].role !== 'user'; i++) {
    const t = Number(msgs[i] && msgs[i].ts);
    if (Number.isFinite(t)) segEnd = Math.max(segEnd, t);
  }
  let nxt = -1;
  for (let i = uIdx + 1; i < msgs.length; i++) {
    if (msgs[i] && msgs[i].role === 'user') { nxt = i; break; }
  }
  const created = Number(card && card.createdAt);
  const createdMs = Number.isFinite(created) ? created : segEnd;
  const max = nxt >= 0
    ? (Number(msgs[nxt].ts) || Infinity) - 1
    : Infinity;
  return { ts: Math.min(Math.max(createdMs, segEnd), max), anchored: true };
}

// 消息流归并(spec §C):messages(权威流)与 steerItems(瞬态插话)按 ts
// 升序合并为统一时间线,打 type 标供渲染层分流。纯函数,脏输入安全。
// 2026-11-26 第三参 cards(审批/方案/tool_ask 交互卡)加入归并:卡片内联进
// 时间线、跟随其所属回合,不再整体钉在列表底部(用户症状:已选择卡孤悬列表
// 底)。定位优先 resolveCardAnchor(messageId 精确锚定+clamp),锚不中回落
// createdAt;两者皆缺落 Infinity(排序兜底=列表尾),保持旧钉底行为不丢卡。
export function mergeTimeline(messages, steerItems, cards = []) {
  // type 放 spread 之后钉死:它是渲染层唯一分流键,不允许输入项同名属性覆盖。
  const a = Array.isArray(messages) ? messages.map(m => ({ ...m, type: 'message' })) : [];
  const b = Array.isArray(steerItems) ? steerItems.map(s => ({ ...s, type: 'steer' })) : [];
  const msgs = a;
  const c = Array.isArray(cards) ? cards.map((k) => {
    const created = Number(k && k.createdAt);
    const fallbackTs = Number.isFinite(created) ? created : k?.ts;
    const anchor = resolveCardAnchor(k, msgs);
    return { ...k, type: 'card', ts: anchor.anchored ? anchor.ts : fallbackTs };
  }) : [];
  return [...a, ...b, ...c]
    .map((item, idx) => ({ ...item, _idx: idx }))
    // _idx tiebreaker:两个无 ts 项比较器本会返回 Infinity-Infinity=NaN(排序
    // 结果不确定),tiebreaker 同时修掉该 NaN 不一致,并钉死同 ts 项按 concat
    // 序排列的稳定序(卡在消息/插话之后——发起卡的请求泡与卡几乎同 ts 时,
    // 卡排泡后,正是「跟随对应气泡」的语义)。
    .sort((x, y) => ((Number.isFinite(x.ts) ? x.ts : Infinity) - (Number.isFinite(y.ts) ? y.ts : Infinity)) || (x._idx - y._idx))
    .map(({ _idx, ...rest }) => rest);
}
