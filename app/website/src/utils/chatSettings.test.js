import { beforeEach, describe, expect, it } from 'vitest';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  LAST_USED_EFFORT_KEY,
  LAST_USED_MODEL_KEY,
  MODEL_CUSTOM_VALUE,
  deviceSessionToConversation,
  isAliangCodeSession,
  isDirectoryLocked,
  normalizeDeviceProvider,
  readLastUsedEffort,
  readLastUsedModel,
  recentProjectsFromSessions,
  rememberLastUsedEffort,
  rememberLastUsedModel,
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

  it('输出预算只在显式设置时出现；未设置时整个键不出现（协议漂移守卫）', () => {
    const bare = sessionCreatePayload({ aiSessionId: 's1' });
    expect('max_tokens' in bare).toBe(false);
    expect('context_window' in bare).toBe(false);

    const set = sessionCreatePayload({ aiSessionId: 's1', maxTokens: 32000, contextWindow: 200000 });
    expect(set.max_tokens).toBe(32000);
    expect(set.context_window).toBe(200000);

    // 0 / 负数 / 非数字一律视为「未设置」，不下发
    const zero = sessionCreatePayload({ aiSessionId: 's1', maxTokens: 0, contextWindow: -1 });
    expect('max_tokens' in zero).toBe(false);
    expect('context_window' in zero).toBe(false);
    const junk = sessionCreatePayload({ aiSessionId: 's1', maxTokens: 'abc', contextWindow: null });
    expect('max_tokens' in junk).toBe(false);
    expect('context_window' in junk).toBe(false);

    // 小数被截断成整数（后端是 int 字段）
    const frac = sessionCreatePayload({ aiSessionId: 's1', maxTokens: 32000.9 });
    expect(frac.max_tokens).toBe(32000);
  });

  it('会话级 MCP 开关：数组才下发，空数组=显式全部启用，缺键=不动', () => {
    // 缺键：不出现
    const bare = sessionCreatePayload({ aiSessionId: 's1' });
    expect('disabled_mcp_servers' in bare).toBe(false);

    // 显式空数组：出现且为空（语义是「全部启用」，与缺键不同）
    const allOn = sessionCreatePayload({ aiSessionId: 's1', disabledMcpServers: [] });
    expect(allOn.disabled_mcp_servers).toEqual([]);

    // 正常列表：trim + 去空
    const some = sessionCreatePayload({ aiSessionId: 's1', disabledMcpServers: [' a ', '', 'b'] });
    expect(some.disabled_mcp_servers).toEqual(['a', 'b']);

    // 非数组（脏数据）不下发
    const junk = sessionCreatePayload({ aiSessionId: 's1', disabledMcpServers: 'a,b' });
    expect('disabled_mcp_servers' in junk).toBe(false);
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
      model: '',
      effort: '',
      deviceSession: entry,
      createdAt: 'c1',
      updatedAt: 'u1',
    });
  });

  it('回填 model/effort：续聊时设置条显示真实值，而不是假「默认」', () => {
    // 回归：曾漏映射 model/effort，导致设备会话续聊恒显示「默认」且下一条
    // 消息重发 create 带空值、把服务端记住的模型抹掉 → 内核「未指定模型」拒跑。
    const conv = deviceSessionToConversation({
      id: 's11', provider: 'agentcore', model: 'glm-5.3', effort: 'high',
    });
    expect(conv.model).toBe('glm-5.3');
    expect(conv.effort).toBe('high');
  });

  it('title 缺省回落 id，时间/路径缺省空串，provider 归一', () => {
    const conv = deviceSessionToConversation({ id: 's10', provider: 'claude' });
    expect(conv.id).toBe('dev-s10');
    expect(conv.title).toBe('s10');
    expect(conv.provider).toBe('claudecode');
    expect(conv.projectPath).toBe('');
    expect(conv.model).toBe('');
    expect(conv.effort).toBe('');
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

// last-used 记忆（用户拍板 2026-10-31：默认模型留空，新会话沿用上一次用过的）。
// 用内存版 localStorage 桩，避免依赖 vitest 的 jsdom 环境。
describe('last-used model/effort 记忆', () => {
  beforeEach(() => {
    const store = new Map();
    globalThis.localStorage = {
      getItem: (k) => (store.has(k) ? store.get(k) : null),
      setItem: (k, v) => store.set(k, String(v)),
      removeItem: (k) => store.delete(k),
      clear: () => store.clear(),
    };
  });

  it('写入后可读回；空值不覆盖已记录的模型', () => {
    rememberLastUsedModel('glm-5.3');
    expect(readLastUsedModel()).toBe('glm-5.3');
    rememberLastUsedModel('');
    rememberLastUsedModel('   ');
    expect(readLastUsedModel()).toBe('glm-5.3');
  });

  it('读回时 trim；无记录返回空串', () => {
    expect(readLastUsedModel()).toBe('');
    localStorage.setItem(LAST_USED_MODEL_KEY, '  glm-5.2  ');
    expect(readLastUsedModel()).toBe('glm-5.2');
  });

  it('effort 空值表示「清除记忆」（与模型不同：空 effort 是合法语义）', () => {
    rememberLastUsedEffort('high');
    expect(readLastUsedEffort()).toBe('high');
    rememberLastUsedEffort('');
    expect(readLastUsedEffort()).toBe('');
    expect(localStorage.getItem(LAST_USED_EFFORT_KEY)).toBe(null);
  });
});
