import { describe, it, expect } from 'vitest';
import { sliceMessagesBefore, canRecall, recallCreatePayload } from './chatRecall';

describe('sliceMessagesBefore', () => {
  it('返回目标消息之前的切片(不含目标)', () => {
    const msgs = [
      { role: 'user', content: '1', messageId: 'm1' },
      { role: 'assistant', content: '2', messageId: 'm2' },
      { role: 'user', content: '3', messageId: 'm3' },
    ];
    expect(sliceMessagesBefore(msgs, 'm3').map(m => m.messageId)).toEqual(['m1', 'm2']);
  });
  it('目标是首条 → 空数组(撤回到空)', () => {
    expect(sliceMessagesBefore([{ role: 'user', messageId: 'm1' }], 'm1')).toEqual([]);
  });
  it('目标不存在 → null(调用方禁用入口)', () => {
    expect(sliceMessagesBefore([{ role: 'user', messageId: 'm1' }], 'nope')).toBeNull();
  });
});

describe('canRecall', () => {
  it('agentcore + 有 messageId + 有 role=user 才可撤回', () => {
    expect(canRecall({ provider: 'agentcore', role: 'user', messageId: 'x' })).toBe(true);
    expect(canRecall({ provider: 'claudecode', role: 'user', messageId: 'x' })).toBe(false);
    expect(canRecall({ provider: 'agentcore', role: 'user' })).toBe(false);
    expect(canRecall({ provider: 'agentcore', role: 'assistant', messageId: 'x' })).toBe(false);
    expect(canRecall(null)).toBe(false);
  });
  it('messageId 空串/非字符串 → 不可撤回', () => {
    expect(canRecall({ provider: 'agentcore', role: 'user', messageId: '' })).toBe(false);
    expect(canRecall({ provider: 'agentcore', role: 'user', messageId: 123 })).toBe(false);
  });
});

describe('recallCreatePayload', () => {
  it('基于常规载荷但绝不携带 transcript,且带 truncate_before_message_id', () => {
    const conv = {
      aiSessionId: 'sid', projectPath: '/p', provider: 'agentcore',
      model: 'glm-5.3', effort: 'high', resumeSessionId: 'old',
      deviceSession: { id: 'sid' }, deviceTranscript: [{ role: 'user', content: 'x' }],
    };
    const p = recallCreatePayload(conv, 'm9');
    expect(p.truncate_before_message_id).toBe('m9');
    expect(p.transcript).toBeUndefined();          // 撤回路径绝不喂 transcript
    expect(p.resume_session_id).toBeUndefined();   // resume 仅首次,撤回不带(带上 resumeSessionId 才是真删)
    expect(p.project_path).toBe('/p');             // resumeProjectPath 不在撤回路径污染 project_path
    expect(p.effort).toBe('high');
    expect(p.session_id).toBe('sid');
  });
});
