import { describe, expect, it } from 'vitest';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  isDirectoryLocked,
  recentProjectsFromSessions,
  sessionCreatePayload,
} from './chatSettings';

describe('EFFORT_OPTIONS', () => {
  it('与内核 NormalizeEffortLevel 白名单同源', () => {
    expect(EFFORT_OPTIONS.map(o => o.value)).toEqual(['', 'low', 'medium', 'high', 'xhigh', 'max']);
  });
});

describe('AGENTCORE_MODEL_OPTIONS', () => {
  it('含默认/常用档/[1m] 变体/自定义哨兵，且不含重复值', () => {
    const values = AGENTCORE_MODEL_OPTIONS.map(o => o.value);
    expect(values[0]).toBe('');
    expect(values).toContain('glm-5.3');
    expect(values).toContain('glm-5.3[1m]');
    expect(values).toContain('glm-5.3-flash[1m]');
    expect(values[values.length - 1]).toBe(MODEL_CUSTOM_VALUE);
    expect(new Set(values).size).toBe(values.length);
  });
});

describe('recentProjectsFromSessions', () => {
  it('按输入顺序去重并截断 limit（输入已是 updatedAt 倒序）', () => {
    const sessions = [
      { project_path: '/a' }, { project_path: '/b' }, { project_path: '/a' }, { project_path: '/c' },
    ];
    expect(recentProjectsFromSessions(sessions)).toEqual(['/a', '/b', '/c']);
    expect(recentProjectsFromSessions(sessions, { limit: 2 })).toEqual(['/a', '/b']);
  });

  it('过滤空/非字符串路径，容错非数组输入', () => {
    expect(recentProjectsFromSessions([{ project_path: '  ' }, {}, null, { project_path: '/ok' }])).toEqual(['/ok']);
    expect(recentProjectsFromSessions(null)).toEqual([]);
    expect(recentProjectsFromSessions('x')).toEqual([]);
    expect(recentProjectsFromSessions([{ project_path: ' /p ' }])).toEqual(['/p']);
  });

  it('limit<=0 直接返回空数组', () => {
    expect(recentProjectsFromSessions([{ project_path: '/a' }], { limit: 0 })).toEqual([]);
    expect(recentProjectsFromSessions([{ project_path: '/a' }], { limit: -1 })).toEqual([]);
  });
});

describe('isDirectoryLocked', () => {
  it('服务端确认建会话（dirLocked，由 ai.session.created 回显置位）后锁定', () => {
    expect(isDirectoryLocked({ dirLocked: true })).toBe(true);
  });

  it('未确认不锁定：新会话、create 失败的会话、legacy 会话（无该字段）', () => {
    expect(isDirectoryLocked({ dirLocked: false })).toBe(false);
    expect(isDirectoryLocked({})).toBe(false);
    // legacy：本功能上线前的会话只有 aiSessionCreated/messages，不锁定（可修）
    expect(isDirectoryLocked({ aiSessionCreated: true, messages: [{ role: 'user', content: 'hi' }] })).toBe(false);
    expect(isDirectoryLocked(null)).toBe(false);
  });
});

describe('sessionCreatePayload', () => {
  it('空值归空串，provider 缺省 claudecode，字段齐全', () => {
    expect(sessionCreatePayload({ aiSessionId: 's1' })).toEqual({
      type: 'ai.session.create',
      session_id: 's1',
      project_path: '',
      provider: 'claudecode',
      mode: 'vibe',
      model: '',
      effort: '',
    });
  });

  it('透传非空值与自定义模型/effort', () => {
    const conv = { aiSessionId: 's2', projectPath: '/w', provider: 'agentcore', model: 'my-model', effort: 'xhigh' };
    expect(sessionCreatePayload(conv)).toMatchObject({ project_path: '/w', provider: 'agentcore', model: 'my-model', effort: 'xhigh' });
  });
});
