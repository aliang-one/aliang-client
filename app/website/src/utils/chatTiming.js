import { toRaw } from 'vue';

/**
 * 客户端上屏时序(SYNC-02):测「agent 产出 → 网页可见」的端到端延迟,上报
 * PhoneServer 聚合。验收口径:首段/后续文字的 P95 ≤ 500ms。
 *
 * 两个时刻:
 *  - 产出:ai.delta 帧上的 agent_emitted_at_ms(墙钟,epoch ms)。
 *  - 可见:Vue 把这段文字插进 DOM 之后的时刻。网页有真实的 DOM 插入点
 *    (ChatMarkdown 的 onMounted/updated),比 RN 的 rAF 更贴近「用户看见」。
 *
 * 时钟口径:两端都是墙钟,可直接相减。跨设备时钟有偏差,但这是唯一的端到端
 * 口径,与 SYNC-01 服务端已用的 agent_emitted_at_ms 同源。
 *
 * 采样策略:每个 run 只测首段一次;后续文字每 run 最多 5 次;只测可见标签页
 * (document.hidden 时的上屏时刻没有意义)。
 *
 * 上报 best-effort:发不出去、服务端拒收都不影响任何用户可见行为。
 */

const validRunId = /^[a-zA-Z0-9_-]{1,160}$/;
const samples = new WeakMap();
let lastMeasuredRun;
let sender;
const SUBSEQUENT_PER_RUN = 5;
const subsequentCount = new Map();
let measuredRuns = 0;

/** 注入上行通道(utils 不反向依赖 transport)。 */
export function configureChatTimingSender(next) {
  sender = next;
}

export function noteChatFirstText(message, frame, receivedAt) {
  if (!Number.isFinite(receivedAt) || document.hidden ||
      typeof frame.run_id !== 'string' || !validRunId.test(frame.run_id) ||
      frame.run_id === lastMeasuredRun || !Number.isSafeInteger(frame.event_seq) ||
      frame.event_seq <= 0 || !Number.isSafeInteger(frame.agent_emitted_at_ms) ||
      frame.agent_emitted_at_ms <= 0 || typeof frame.delta !== 'string' || !frame.delta.trim() ||
      (frame.channel != null && frame.channel !== 'assistant' && frame.channel !== 'stdout')) return;
  const key = toRaw(message);
  if (samples.get(key)?.runId === frame.run_id) return;
  samples.set(key, {
    runId: frame.run_id,
    eventSeq: frame.event_seq,
    receivedAt,
    sessionId: frame.session_id,
    messageId: frame.message_id,
    emittedAtMs: frame.agent_emitted_at_ms,
  });
}

export function noteChatTextDomCommit(message) {
  const key = toRaw(message);
  const sample = samples.get(key);
  if (!sample) return;
  samples.delete(key);
  const elapsed = performance.now() - sample.receivedAt;
  if (document.hidden || elapsed < 0 || elapsed > 30_000 || lastMeasuredRun === sample.runId) return;
  lastMeasuredRun = sample.runId;
  // 保留原有的本地取证行(与既有 [PERF] 口径一致),额外上报服务端。
  console.log(`[PERF] ai-first-text run=${sample.runId} event_seq=${sample.eventSeq} receive_to_dom_ms=${Math.round(elapsed)}`);
  report('first_text', sample);
}

/** 后续文字:每 run 最多采样 5 次,避免长回答刷爆上报。 */
export function noteChatSubsequentText(frame) {
  if (document.hidden ||
      typeof frame.run_id !== 'string' || !validRunId.test(frame.run_id) ||
      !Number.isSafeInteger(frame.agent_emitted_at_ms) || frame.agent_emitted_at_ms <= 0 ||
      typeof frame.delta !== 'string' || !frame.delta.trim() ||
      (frame.channel != null && frame.channel !== 'assistant' && frame.channel !== 'stdout')) return;
  const count = subsequentCount.get(frame.run_id) ?? 0;
  if (count >= SUBSEQUENT_PER_RUN) return;
  subsequentCount.set(frame.run_id, count + 1);
  report('subsequent_text', {
    sessionId: frame.session_id,
    messageId: frame.message_id,
    runId: frame.run_id,
    emittedAtMs: frame.agent_emitted_at_ms,
  });
}

function report(kind, sample) {
  if (!sender) return;
  const latency = Date.now() - sample.emittedAtMs;
  if (latency < 0 || latency > 3_600_000) return;
  try {
    sender({
      type: 'client.timing',
      session_id: sample.sessionId,
      run_id: sample.runId,
      message_id: sample.messageId,
      kind,
      latency_ms: Math.round(latency),
    });
  } catch {
    /* best-effort */
  }
}

export function __resetChatTimingForTests() {
  lastMeasuredRun = undefined;
  sender = undefined;
  subsequentCount.clear();
}
