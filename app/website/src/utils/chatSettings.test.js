import { describe, expect, it } from 'vitest';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  deviceSessionToConversation,
  isAliangCodeSession,
  isDirectoryLocked,
  normalizeDeviceProvider,
  recentProjectsFromSessions,
  deviceTranscriptForCreate,
  sessionCreatePayload,
  sessionSourceBadge,
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

  it('未设置 resume 字段时载荷不含对应键（e2e 断言 create 帧字段集，不得多出空键）', () => {
    const payload = sessionCreatePayload({ aiSessionId: 's1', projectPath: '/w' });
    expect(Object.keys(payload)).not.toContain('resume_session_id');
    expect(payload.project_path).toBe('/w');
  });

  it('resumeSessionId 映射 resume_session_id，resumeProjectPath 覆盖 project_path', () => {
    const payload = sessionCreatePayload({ aiSessionId: 's3', resumeSessionId: 'abc-123', resumeProjectPath: '/src' });
    expect(payload.resume_session_id).toBe('abc-123');
    expect(payload.project_path).toBe('/src');
    // 只有 resumeSessionId 时路径不被动。
    expect(sessionCreatePayload({ aiSessionId: 's4', projectPath: '/keep', resumeSessionId: 'x' }).project_path).toBe('/keep');
  });

  it('resume 字段为空串/未定义等价于未设置', () => {
    const payload = sessionCreatePayload({ aiSessionId: 's5', resumeSessionId: '', resumeProjectPath: '' });
    expect(Object.keys(payload)).not.toContain('resume_session_id');
    expect(payload.project_path).toBe('');
  });
});

describe('deviceTranscriptForCreate（agentcore 上下文喂入）', () => {
  const transcript = [
    { role: 'user', content: '早' },
    { role: 'assistant', content: '好' },
  ];
  const agentcoreShell = {
    deviceSession: { id: 'conv-1' },
    provider: 'agentcore',
    aiSessionId: 'conv-1',
    deviceTranscript: transcript,
  };

  it('agentcore 裸 id 设备壳且未 ack 消费 → 返回转录原样', () => {
    expect(deviceTranscriptForCreate(agentcoreShell)).toEqual(transcript);
  });

  it('claude_ 前缀壳（resume 链）/本地草稿/provider 非 agentcore → null', () => {
    expect(deviceTranscriptForCreate({ ...agentcoreShell, aiSessionId: 'local-dev-claude_x', deviceSession: { id: 'claude_x' } })).toBeNull();
    expect(deviceTranscriptForCreate({ provider: 'agentcore', deviceTranscript: transcript })).toBeNull();
    expect(deviceTranscriptForCreate({ ...agentcoreShell, provider: 'claudecode' })).toBeNull();
  });

  it('deviceTranscriptSent=true（created ack 已消费）或空/缺失转录 → null', () => {
    expect(deviceTranscriptForCreate({ ...agentcoreShell, deviceTranscriptSent: true })).toBeNull();
    expect(deviceTranscriptForCreate({ ...agentcoreShell, deviceTranscript: [] })).toBeNull();
    expect(deviceTranscriptForCreate({ ...agentcoreShell, deviceTranscript: undefined })).toBeNull();
  });

  it('脏输入安全返回 null', () => {
    expect(deviceTranscriptForCreate(null)).toBeNull();
    expect(deviceTranscriptForCreate('x')).toBeNull();
    expect(deviceTranscriptForCreate(undefined)).toBeNull();
  });
});

describe('sessionCreatePayload transcript 键', () => {
  const transcript = [{ role: 'user', content: '早' }];

  it('agentcore 设备壳带转录 → create 载荷携带 transcript（remoteAgentAIHistory 行形态）', () => {
    const payload = sessionCreatePayload({
      aiSessionId: 'conv-1',
      deviceSession: { id: 'conv-1' },
      provider: 'agentcore',
      deviceTranscript: transcript,
    });
    expect(payload.transcript).toEqual(transcript);
  });

  it('已 ack 消费/本地草稿 → 载荷不含 transcript 键（e2e 字段集断言不得多出空键）', () => {
    const consumed = sessionCreatePayload({
      aiSessionId: 'conv-1',
      deviceSession: { id: 'conv-1' },
      provider: 'agentcore',
      deviceTranscript: transcript,
      deviceTranscriptSent: true,
    });
    expect(Object.keys(consumed)).not.toContain('transcript');
    const draft = sessionCreatePayload({ aiSessionId: 's6', deviceTranscript: transcript });
    expect(Object.keys(draft)).not.toContain('transcript');
  });
});

describe('sessionSourceBadge', () => {
  it('running → active 且优先于 origin/id 前缀', () => {
    expect(sessionSourceBadge({ status: 'running', origin: 'phone', id: 'claude_x' })).toBe('active');
  });

  it('origin=phone / dashboard 各自映射', () => {
    expect(sessionSourceBadge({ status: 'closed', origin: 'phone', id: 'conv-1' })).toBe('phone');
    expect(sessionSourceBadge({ status: 'closed', origin: 'dashboard', id: 'conv-2' })).toBe('dashboard');
  });

  it('id 前缀 claude_/codex_ → terminal，其余前缀不命中', () => {
    expect(sessionSourceBadge({ status: 'closed', id: 'claude_abc' })).toBe('terminal');
    expect(sessionSourceBadge({ status: 'closed', id: 'codex_def' })).toBe('terminal');
    expect(sessionSourceBadge({ status: 'closed', id: 'conv-123' })).toBe('');
    expect(sessionSourceBadge({ status: 'closed', id: 'CLAUDE_abc' })).toBe('');
    expect(sessionSourceBadge({ status: 'closed', id: 123 })).toBe('');
  });

  it('脏输入安全返回空串', () => {
    expect(sessionSourceBadge(null)).toBe('');
    expect(sessionSourceBadge(undefined)).toBe('');
    expect(sessionSourceBadge('x')).toBe('');
    expect(sessionSourceBadge(42)).toBe('');
    expect(sessionSourceBadge({})).toBe('');
  });
});

describe('normalizeDeviceProvider', () => {
  it('归一映射全覆盖：claude/claudecode→claudecode，codex→codex，agentcore→agentcore，其余→claudecode', () => {
    expect(normalizeDeviceProvider('claude')).toBe('claudecode');
    expect(normalizeDeviceProvider('claudecode')).toBe('claudecode');
    expect(normalizeDeviceProvider('codex')).toBe('codex');
    expect(normalizeDeviceProvider('agentcore')).toBe('agentcore');
    expect(normalizeDeviceProvider('')).toBe('claudecode');
    expect(normalizeDeviceProvider('unknown')).toBe('claudecode');
    expect(normalizeDeviceProvider(undefined)).toBe('claudecode');
    expect(normalizeDeviceProvider('  Codex ')).toBe('codex');
  });
});

describe('deviceSessionToConversation', () => {
  it('设备会话条目映射为统一会话对象，原始条目挂 deviceSession', () => {
    const entry = { id: 's9', title: '手机会话', provider: 'codex', project_path: '/w', created_at: 'c1', updated_at: 'u1' };
    expect(deviceSessionToConversation(entry)).toEqual({
      id: 'dev-s9',
      title: '手机会话',
      provider: 'codex',
      projectPath: '/w',
      deviceSession: entry,
      createdAt: 'c1',
      updatedAt: 'u1',
    });
  });

  it('title 缺省回落 id，时间/路径缺省空串，provider 归一', () => {
    const conv = deviceSessionToConversation({ id: 's10', provider: 'claude' });
    expect(conv.id).toBe('dev-s10');
    expect(conv.title).toBe('s10');
    expect(conv.provider).toBe('claudecode');
    expect(conv.projectPath).toBe('');
    expect(conv.createdAt).toBe('');
    expect(conv.updatedAt).toBe('');
  });

  it('脏输入安全返回 null', () => {
    expect(deviceSessionToConversation(null)).toBe(null);
    expect(deviceSessionToConversation(undefined)).toBe(null);
    expect(deviceSessionToConversation('x')).toBe(null);
    expect(deviceSessionToConversation(42)).toBe(null);
  });
});

// 「这条会话属不属于 AliangCode」的唯一判据（聊天窗侧栏与设置页共用）。
describe('isAliangCodeSession', () => {
  it('agentcore 及其别名拼写判真', () => {
    expect(isAliangCodeSession({ provider: 'agentcore' })).toBe(true);
    expect(isAliangCodeSession({ provider: ' AgentCore ' })).toBe(true); // trim+lower
    // 服务端历史通道可能下发连字符/下划线拼写——漏认会让 AliangCode 会话
    // 被误过滤掉（本次一并修正 normalizeDeviceProvider）。
    expect(isAliangCodeSession({ provider: 'agent-core' })).toBe(true);
    expect(isAliangCodeSession({ provider: 'agent_core' })).toBe(true);
  });

  it('claude/codex/未知/脏输入一律判否', () => {
    expect(isAliangCodeSession({ provider: 'claude' })).toBe(false);
    expect(isAliangCodeSession({ provider: 'claudecode' })).toBe(false);
    expect(isAliangCodeSession({ provider: 'codex' })).toBe(false);
    expect(isAliangCodeSession({ provider: 'openai' })).toBe(false);
    expect(isAliangCodeSession({ provider: '' })).toBe(false);
    expect(isAliangCodeSession({})).toBe(false);
    expect(isAliangCodeSession(null)).toBe(false);
    expect(isAliangCodeSession('agentcore')).toBe(false);
  });
});
