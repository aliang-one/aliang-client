// 子 agent 状态面板 reducer(spec:ai.subagent 事件聚合)。纯函数,不可变更新,
// 返回新数组引用驱动 Vue 响应式;未知 phase 返回原数组引用避免无谓重渲染。
// 会话归属(sessionId 过滤)由调用方保证,本函数只按 taskId 聚合。

// 终态 phase:定稿后条目冻结——迟到的 progress 帧不得复活条目(否则入口条
// 翻回 active 卡转圈),与 Go 读侧 3e18f3a 的终态守卫对称。
const ACTIVE_PHASES = new Set(['started', 'progress']);
const TERMINAL_PHASES = new Set(['completed', 'failed', 'interrupted']);

// 条目形状兜底:输入侧允许字段缺失,输出侧始终补齐整组键,渲染层免判空。
const blankEntry = taskId => ({
  taskId,
  name: taskId,
  description: null,
  phase: 'started',
  currentStep: null,
  lastTool: null,
  toolUses: 0,
  totalTokens: null,
  durationMs: null,
  summary: null,
});

// 数值字段取大者:progress 帧乱序到达时 toolUses 只进不退,防计数回退。
const maxOr = (current, incoming) => {
  const cur = Number(current);
  const nxt = Number(incoming);
  if (!Number.isFinite(nxt)) return Number.isFinite(cur) ? cur : null;
  if (!Number.isFinite(cur)) return nxt;
  return Math.max(cur, nxt);
};

// 若有值(含 0)则取新值,否则保留旧值:completed 的 durationMs/summary 只在有
// 载荷时覆盖,空帧不清空已定稿数据。
const pickDefined = (current, incoming) => (incoming !== undefined && incoming !== null && incoming !== '' ? incoming : current);

export function applySubagentEvent(subagents, msg) {
  const list = Array.isArray(subagents) ? subagents : [];
  if (!msg || typeof msg !== 'object' || typeof msg.taskId !== 'string' || !msg.taskId) {
    return list;
  }
  const phase = msg.phase;
  const idx = list.findIndex(s => s && s.taskId === msg.taskId);

  // started:无则新建(浅拷贝补齐整组键),有则幂等返回既有数据不动。
  if (phase === 'started') {
    if (idx >= 0) {
      // 幂等:已有条目的 name/description/进度数据一律不覆盖,零数据变化
      // 按纪律返回原数组引用,不触发无谓响应式。
      return list;
    }
    return [...list, {
      ...blankEntry(msg.taskId),
      name: (typeof msg.subagentType === 'string' && msg.subagentType) ? msg.subagentType : msg.taskId,
      description: msg.description ?? null,
    }];
  }

  // progress:未知 taskId 忽略(乱序容错——progress 早于 started 到达时直接丢帧,
  // 后续 started 帧会补建);已知则更新三个进度字段。
  if (phase === 'progress') {
    if (idx < 0) return list;
    // 终态钉死:completed/failed/interrupted 之后的迟到 progress 帧原样返回,
    // 条目不得复活(否则 hasActiveSubagents 翻回 true 卡转圈)。
    if (TERMINAL_PHASES.has(list[idx].phase)) return list;
    return list.map((s, i) => (i === idx ? {
      ...s,
      currentStep: msg.currentStep ?? s.currentStep,
      lastTool: msg.lastTool ?? s.lastTool,
      toolUses: maxOr(s.toolUses, msg.toolUses),
      phase: 'progress',
    } : s));
  }

  // 终态:未知 taskId 忽略;已知则 phase 定稿,durationMs/summary 有载荷才覆盖。
  if (TERMINAL_PHASES.has(phase)) {
    if (idx < 0) return list;
    return list.map((s, i) => (i === idx ? {
      ...s,
      phase,
      durationMs: pickDefined(s.durationMs, msg.durationMs),
      summary: pickDefined(s.summary, msg.summary),
    } : s));
  }

  // 未知 phase:原数组引用原样返回,不触发响应式。
  return list;
}

// 便捷谓词:存在进行中的子 agent(started/progress)返回 true。
export function hasActiveSubagents(subagents) {
  if (!Array.isArray(subagents)) return false;
  return subagents.some(s => s && ACTIVE_PHASES.has(s.phase));
}

// ---- 呈现层辅助(Task 8 面板/详情共用) ----

// phase → 呈现元数据(i18n 键/图标/色调)。词表真源仍是上方 ACTIVE/TERMINAL
// 集合,这里只补「词 → 展示」映射,面板与详情不各自维护第二份词表。未知
// phase 兜底 running 呈现(reducer 未知帧原样保底的容错口径;入口条计数侧
// 「未知两侧不计」不受影响——本函数只管展示)。
const PHASE_META = {
  started: { labelKey: 'chat_subagentStatusRunning', icon: 'progress_activity', tone: 'running' },
  progress: { labelKey: 'chat_subagentStatusRunning', icon: 'progress_activity', tone: 'running' },
  completed: { labelKey: 'chat_subagentStatusCompleted', icon: 'check_circle', tone: 'done' },
  failed: { labelKey: 'chat_subagentStatusFailed', icon: 'cancel', tone: 'failed' },
  interrupted: { labelKey: 'chat_subagentStatusInterrupted', icon: 'pause_circle', tone: 'interrupted' },
};

export function subagentPhaseMeta(phase) {
  return PHASE_META[phase] || PHASE_META.started;
}

// durationMs → 秒呈现:<60s 显示一位小数秒,≥60s 折算分秒。非法/非正值返回
// 空串(渲染层以空串判据隐藏该槽位,不显示「0.0s」假数据)。
export function formatSubagentDuration(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n <= 0) return '';
  const s = n / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  return `${Math.floor(s / 60)}m${Math.round(s % 60)}s`;
}

// token 数千分位呈现;非法/非正值返回空串(同上,隐藏槽位)。
export function formatSubagentTokens(tokens) {
  const n = Number(tokens);
  if (!Number.isFinite(n) || n <= 0) return '';
  return n.toLocaleString('en-US');
}
