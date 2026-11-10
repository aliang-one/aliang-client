// 会话级 seq（2026-11-10 聊天记录身份化）前端契约。
//
// 后端 journal 写侧给每行分配会话级单调 seq，读侧原样透传到快照/详情 wire。
// 前端契约三条：
//   ① chatMessageSeq 对 0/负数/缺失/非数字一律返回 0（= 无 seq）；
//   ② orderMessagesBySeq 只对「双方都有 seq」比较，任一侧缺 seq 视为不可比，
//      稳定排序保留原始相对位置（旧 agent 记录序完全不变）；
//   ③ 详情/快照转录行转换后气泡带 seq，且按 seq 排序（跨页/截尾合并正确）。

import { describe, it, expect } from 'vitest';
import {
  chatMessageSeq,
  orderMessagesBySeq,
  deviceDetailTranscriptToMessages,
  deviceSnapshotTranscriptToMessages,
} from './useAliangChat';

describe('chatMessageSeq', () => {
  it('只认正有限数字', () => {
    expect(chatMessageSeq({ seq: 5 })).toBe(5);
    expect(chatMessageSeq({ seq: 0 })).toBe(0);
    expect(chatMessageSeq({ seq: -1 })).toBe(0);
    expect(chatMessageSeq({ seq: '7' })).toBe(0);
    expect(chatMessageSeq({ seq: null })).toBe(0);
    expect(chatMessageSeq({})).toBe(0);
    expect(chatMessageSeq(null)).toBe(0);
  });
});

describe('orderMessagesBySeq', () => {
  it('双方都有 seq 时按 seq 升序', () => {
    const a = { content: 'a', seq: 3 };
    const b = { content: 'b', seq: 1 };
    const c = { content: 'c', seq: 2 };
    expect(orderMessagesBySeq([a, b, c]).map(m => m.content)).toEqual(['b', 'c', 'a']);
  });

  it('任一侧缺 seq 即不可比，保留原始相对位置（旧 agent 兼容）', () => {
    const a = { content: 'a' };          // 无 seq
    const b = { content: 'b', seq: 9 };
    const c = { content: 'c' };          // 无 seq
    expect(orderMessagesBySeq([a, b, c]).map(m => m.content)).toEqual(['a', 'b', 'c']);
  });

  it('全部无 seq 时顺序完全不变（恒等）', () => {
    const list = [{ content: 'x' }, { content: 'y' }, { content: 'z' }];
    expect(orderMessagesBySeq(list).map(m => m.content)).toEqual(['x', 'y', 'z']);
  });

  it('不足两条直接返回', () => {
    expect(orderMessagesBySeq([])).toEqual([]);
    const one = [{ content: 'a' }];
    expect(orderMessagesBySeq(one)).toBe(one);
  });
});

describe('转录行转换携带并遵守 seq', () => {
  it('详情：行按 seq 排序，气泡带 seq', () => {
    const rows = [
      { id: 'u2', role: 'user', content: '第二问', timestamp: '2026-11-10T00:00:03Z', seq: 4 },
      { id: 'a1', role: 'assistant', content: '第一答', timestamp: '2026-11-10T00:00:02Z', seq: 2 },
      { id: 'u1', role: 'user', content: '第一问', timestamp: '2026-11-10T00:00:01Z', seq: 1 },
      { id: 'a2', role: 'assistant', content: '第二答', timestamp: '2026-11-10T00:00:04Z', seq: 5 },
    ];
    const { messages } = deviceDetailTranscriptToMessages(rows, 'agentcore', 0, false);
    expect(messages.map(m => m.content)).toEqual(['第一问', '第一答', '第二问', '第二答']);
    expect(messages.map(m => m.seq)).toEqual([1, 2, 4, 5]);
  });

  it('详情：旧行无 seq 时维持原数组序（向后兼容）', () => {
    const rows = [
      { id: 'u1', role: 'user', content: '旧问', timestamp: '2026-11-09T00:00:01Z' },
      { id: 'a1', role: 'assistant', content: '旧答', timestamp: '2026-11-09T00:00:02Z' },
    ];
    const { messages } = deviceDetailTranscriptToMessages(rows, 'agentcore', 0, false);
    expect(messages.map(m => m.content)).toEqual(['旧问', '旧答']);
    expect(messages.every(m => m.seq === undefined)).toBe(true);
  });

  it('快照：行按 seq 排序，气泡带 seq', () => {
    const rows = [
      { message_id: 'a1', role: 'assistant', text: '第一答', ts: '2026-11-10T00:00:02Z', seq: 2 },
      { message_id: 'u1', role: 'user', text: '第一问', ts: '2026-11-10T00:00:01Z', seq: 1 },
    ];
    const { messages } = deviceSnapshotTranscriptToMessages(rows, 'agentcore', 0, false);
    expect(messages.map(m => m.content)).toEqual(['第一问', '第一答']);
    expect(messages.map(m => m.seq)).toEqual([1, 2]);
  });

  it('系统条同样携带 seq', () => {
    const rows = [
      { id: 'u1', role: 'user', content: '问', timestamp: '2026-11-10T00:00:01Z', seq: 1 },
      { role: 'system', content: '这一轮执行失败', timestamp: '2026-11-10T00:00:02Z', seq: 2 },
    ];
    const { messages } = deviceDetailTranscriptToMessages(rows, 'agentcore', 0, false);
    const note = messages.find(m => m.kind === 'system');
    expect(note).toBeTruthy();
    expect(note.seq).toBe(2);
  });
});
