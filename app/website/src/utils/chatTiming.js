import { toRaw } from 'vue';

const validRunId = /^[a-zA-Z0-9_-]{1,160}$/;
const samples = new WeakMap();
let lastMeasuredRun;

export function noteChatFirstText(message, frame, receivedAt) {
  if (!Number.isFinite(receivedAt) || document.hidden ||
      typeof frame.run_id !== 'string' || !validRunId.test(frame.run_id) ||
      frame.run_id === lastMeasuredRun || !Number.isSafeInteger(frame.event_seq) ||
      frame.event_seq <= 0 || !Number.isSafeInteger(frame.agent_emitted_at_ms) ||
      frame.agent_emitted_at_ms <= 0 || typeof frame.delta !== 'string' || !frame.delta.trim() ||
      (frame.channel != null && frame.channel !== 'assistant' && frame.channel !== 'stdout')) return;
  const key = toRaw(message);
  if (samples.get(key)?.runId === frame.run_id) return;
  samples.set(key, { runId: frame.run_id, eventSeq: frame.event_seq, receivedAt });
}

export function noteChatTextDomCommit(message) {
  const key = toRaw(message);
  const sample = samples.get(key);
  if (!sample) return;
  samples.delete(key);
  const elapsed = performance.now() - sample.receivedAt;
  if (document.hidden || elapsed < 0 || elapsed > 30_000 || lastMeasuredRun === sample.runId) return;
  lastMeasuredRun = sample.runId;
  console.log(`[PERF] ai-first-text run=${sample.runId} event_seq=${sample.eventSeq} receive_to_dom_ms=${Math.round(elapsed)}`);
}
