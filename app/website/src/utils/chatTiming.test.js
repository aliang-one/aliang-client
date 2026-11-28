import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  __resetChatTimingForTests,
  configureChatTimingSender,
  noteChatFirstText,
  noteChatSubsequentText,
  noteChatTextDomCommit,
} from './chatTiming';

// SYNC-02 网页端上屏时序:测「agent 产出 → DOM 可见」,上报 PhoneServer 聚合。
// 与 RN 的 renderTiming 同契约(同一批准入条件、同一种上报帧)。

const sent = [];
const PRODUCED_AT = Date.now() - 120;

beforeEach(() => {
  __resetChatTimingForTests();
  sent.length = 0;
  configureChatTimingSender(payload => sent.push(payload));
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false);
});

afterEach(() => {
  vi.restoreAllMocks();
});

const frame = (over = {}) => ({
  session_id: 's1',
  message_id: 'm1',
  run_id: 'run-1',
  event_seq: 7,
  agent_emitted_at_ms: PRODUCED_AT,
  channel: 'assistant',
  delta: 'hello',
  ...over,
});

describe('首段文字', () => {
  it('收帧 + DOM 提交后上报 first_text,延迟为正', () => {
    const message = { content: '' };
    noteChatFirstText(message, frame(), performance.now());
    noteChatTextDomCommit(message);
    expect(sent).toHaveLength(1);
    expect(sent[0]).toMatchObject({
      type: 'client.timing',
      session_id: 's1',
      run_id: 'run-1',
      message_id: 'm1',
      kind: 'first_text',
    });
    expect(sent[0].latency_ms).toBeGreaterThanOrEqual(100);
  });

  it('同一 run 只测一次', () => {
    const a = { content: '' };
    const b = { content: '' };
    noteChatFirstText(a, frame(), performance.now());
    noteChatTextDomCommit(a);
    noteChatFirstText(b, frame(), performance.now());
    noteChatTextDomCommit(b);
    expect(sent).toHaveLength(1);
  });

  it('未登记过的消息提交:不上报', () => {
    noteChatTextDomCommit({ content: 'x' });
    expect(sent).toHaveLength(0);
  });

  it('标签页隐藏时:不登记也不上报', () => {
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(true);
    const message = { content: '' };
    noteChatFirstText(message, frame(), performance.now());
    noteChatTextDomCommit(message);
    expect(sent).toHaveLength(0);
  });

  it.each([
    ['run_id 非法', { run_id: 'bad id!' }],
    ['event_seq 为 0', { event_seq: 0 }],
    ['缺 agent_emitted_at_ms', { agent_emitted_at_ms: undefined }],
    ['channel 是 user', { channel: 'user' }],
    ['delta 为空', { delta: ' ' }],
  ])('不登记: %s', (_label, over) => {
    const message = { content: '' };
    noteChatFirstText(message, frame(over), performance.now());
    noteChatTextDomCommit(message);
    expect(sent).toHaveLength(0);
  });
});

describe('后续文字', () => {
  it('每个 run 最多采样 5 次', () => {
    for (let i = 0; i < 8; i += 1) noteChatSubsequentText(frame());
    expect(sent).toHaveLength(5);
    expect(sent.every(m => m.kind === 'subsequent_text')).toBe(true);
  });

  it('不同 run 各自计数', () => {
    noteChatSubsequentText(frame({ run_id: 'a' }));
    noteChatSubsequentText(frame({ run_id: 'b' }));
    expect(sent.map(m => m.run_id)).toEqual(['a', 'b']);
  });
});

describe('边界', () => {
  it('未注入 sender 时不抛错', () => {
    configureChatTimingSender(undefined);
    noteChatSubsequentText(frame());
    expect(sent).toHaveLength(0);
  });

  it('sender 抛错被吞掉', () => {
    configureChatTimingSender(() => {
      throw new Error('socket closed');
    });
    expect(() => noteChatSubsequentText(frame())).not.toThrow();
  });

  it('延迟为负或超过 1 小时:丢弃', () => {
    noteChatSubsequentText(frame({ agent_emitted_at_ms: Date.now() + 60_000 }));
    noteChatSubsequentText(frame({ agent_emitted_at_ms: Date.now() - 3_600_001 }));
    expect(sent).toHaveLength(0);
  });
});
