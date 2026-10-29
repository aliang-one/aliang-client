import { describe, expect, it } from 'vitest';
import {
  AGENTCORE_MODEL_OPTIONS,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  agentSessionDrawerEntries,
  attachSessionPayload,
  findConversationForEvent,
  findRunningAssistant,
  isDirectoryLocked,
  planWatchSwitch,
  recentProjectsFromSessions,
  restorableAttachedSessionIds,
  sessionCreatePayload,
  shouldLoadTranscript,
  transcriptToMessages,
  unwatchSessionPayload,
  watchSessionPayload,
  watchTargetOf,
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

// —— attach 语义（Go 契约：六属主字段服务端静默忽略，前端不发）——
describe('attachSessionPayload', () => {
  it('attach 载荷 = 仅 type + session_id：绝不携带六属主字段', () => {
    const payload = attachSessionPayload('ai_abc123');
    expect(payload).toEqual({ type: 'ai.session.create', session_id: 'ai_abc123' });
    expect(Object.keys(payload).sort()).toEqual(['session_id', 'type']);
    for (const forbidden of ['provider', 'model', 'effort', 'resume_session_id', 'binding_version', 'initial_context', 'project_path', 'mode']) {
      expect(payload).not.toHaveProperty(forbidden);
    }
  });

  it('空 id 归空串（服务端按未知 id 新建，回归现行行为）', () => {
    expect(attachSessionPayload('')).toEqual({ type: 'ai.session.create', session_id: '' });
    expect(attachSessionPayload(undefined)).toEqual({ type: 'ai.session.create', session_id: '' });
  });
});

describe('watchSessionPayload / unwatchSessionPayload', () => {
  it('op 与 session_id 正确，无多余字段（Required=type,session_id）', () => {
    expect(watchSessionPayload('ai_1')).toEqual({ type: 'ai.session.watch', session_id: 'ai_1' });
    expect(unwatchSessionPayload('ai_1')).toEqual({ type: 'ai.session.unwatch', session_id: 'ai_1' });
  });
});

describe('watchTargetOf（B1：本地会话绝不自 watch）', () => {
  it('attached 会话返回 session id；本地会话返回 null', () => {
    expect(watchTargetOf({ attached: true, aiSessionId: 'codex_1' })).toBe('codex_1');
    expect(watchTargetOf({ attached: false, aiSessionId: 'local-x' })).toBeNull();
    expect(watchTargetOf({ attached: true })).toBeNull();
    expect(watchTargetOf(null)).toBeNull();
  });
});

describe('planWatchSwitch（watch 生命周期）', () => {
  it('attach 后首次 watch：无旧目标 → 仅 watch', () => {
    expect(planWatchSwitch(null, 'ai_1')).toEqual([{ op: 'watch', sessionId: 'ai_1' }]);
  });

  it('切会话：先 unwatch 旧目标，再 watch 新目标', () => {
    expect(planWatchSwitch('ai_1', 'ai_2')).toEqual([
      { op: 'unwatch', sessionId: 'ai_1' },
      { op: 'watch', sessionId: 'ai_2' },
    ]);
  });

  it('切到本地会话（next=null）：仅 unwatch；相同目标幂等无操作', () => {
    expect(planWatchSwitch('ai_1', null)).toEqual([{ op: 'unwatch', sessionId: 'ai_1' }]);
    expect(planWatchSwitch('ai_1', 'ai_1')).toEqual([]);
    expect(planWatchSwitch(null, null)).toEqual([]);
  });
});

describe('findConversationForEvent（帧按 session_id 归位）', () => {
  const local = { id: 'c1', aiSessionId: 'local-x', attached: false };
  const attached = { id: 'c2', aiSessionId: 'ai_run', attached: true };

  it('session_id 精确命中（matchedById 纪律），fallback 不参与', () => {
    expect(findConversationForEvent([local, attached], 'ai_run', local)).toBe(attached);
    expect(findConversationForEvent([local, attached], 'local-x', attached)).toBe(local);
  });

  it('本地活跃会话承接未知帧（既有 fallback 语义保留）', () => {
    expect(findConversationForEvent([local], 'ai_unknown', local)).toBe(local);
    expect(findConversationForEvent([], null, local)).toBe(local);
  });

  it('attached 活跃会话绝不走 fallback：他页会话/未知 session 的帧不污染直播流', () => {
    expect(findConversationForEvent([attached], 'ai_other', attached)).toBeNull();
    expect(findConversationForEvent([], 'ai_other', attached)).toBeNull();
    expect(findConversationForEvent([], null, attached)).toBeNull();
  });
});

describe('agentSessionDrawerEntries（抽屉列表：合并/标记/排序/客户端分页）', () => {
  const sessions = [
    { id: 'ai_a', title: '手机会话', provider: 'agentcore', updated_at: '2026-10-29T10:00:00Z' },
    { id: 'ai_b', title: '', provider: 'claude', updated_at: '2026-10-29T12:00:00Z', tool: 'claude' },
    { id: 'ai_c', title: '旧会话', provider: 'codex' }, // 无 updated_at → 排最后
  ];

  it('按 updated_at 倒序，缺时间戳排最后；字段清洗', () => {
    const { items, total, hasMore } = agentSessionDrawerEntries(sessions, {});
    expect(items.map(e => e.id)).toEqual(['ai_b', 'ai_a', 'ai_c']);
    expect(items[0]).toMatchObject({ id: 'ai_b', title: '', provider: 'claude', running: false, bound: false });
    expect(total).toBe(3);
    expect(hasMore).toBe(false);
  });

  it('runtime 快照合并出「运行中」徽章；本页绑定集合标记「已接入」', () => {
    const { items } = agentSessionDrawerEntries(sessions, {
      runningIds: ['ai_a'],
      localSessionIds: ['ai_b'],
    });
    expect(items.find(e => e.id === 'ai_a').running).toBe(true);
    expect(items.find(e => e.id === 'ai_b').bound).toBe(true);
    expect(items.find(e => e.id === 'ai_c').running).toBe(false);
  });

  it('客户端分页：limit/offset 切页，hasMore 正确', () => {
    const page1 = agentSessionDrawerEntries(sessions, { limit: 2, offset: 0 });
    expect(page1.items.map(e => e.id)).toEqual(['ai_b', 'ai_a']);
    expect(page1.hasMore).toBe(true);
    const page2 = agentSessionDrawerEntries(sessions, { limit: 2, offset: 2 });
    expect(page2.items.map(e => e.id)).toEqual(['ai_c']);
    expect(page2.hasMore).toBe(false);
  });

  it('超过默认 20 条时按传入 limit 翻页（「显示更多」把 limit 提到 visibleCount）', () => {
    const many = Array.from({ length: 25 }, (_, i) => ({
      id: `s_${i}`,
      updated_at: `2026-10-29T00:${String(i).padStart(2, '0')}:00Z`,
    }));
    const page1 = agentSessionDrawerEntries(many, { limit: 20 });
    expect(page1.items).toHaveLength(20);
    expect(page1.total).toBe(25);
    expect(page1.hasMore).toBe(true);
    const page2 = agentSessionDrawerEntries(many, { limit: 40 });
    expect(page2.items).toHaveLength(25);
    expect(page2.hasMore).toBe(false);
  });

  it('脏输入安全：非数组/脏行/无 id 行', () => {
    expect(agentSessionDrawerEntries(null, {})).toEqual({ items: [], total: 0, hasMore: false });
    expect(agentSessionDrawerEntries([null, {}, { id: '' }, 'x'], {}).total).toBe(0);
  });
});

describe('shouldLoadTranscript（F1：运行中不载转录）', () => {
  const idleAttached = { attached: true, aiSessionId: 'codex_1', messages: [] };

  it('空闲的 attached 会话可加载', () => {
    expect(shouldLoadTranscript(idleAttached)).toBe(true);
  });

  it('运行中不加载：直播气泡会被整体替换冲掉', () => {
    expect(shouldLoadTranscript({
      ...idleAttached,
      messages: [{ role: 'assistant', content: '部分输出', status: 'running' }],
    })).toBe(false);
  });

  it('非 attached / 无 id / 已在加载 / 已加载过 → false', () => {
    expect(shouldLoadTranscript({ attached: false, aiSessionId: 'local-x', messages: [] })).toBe(false);
    expect(shouldLoadTranscript({ attached: true, messages: [] })).toBe(false);
    expect(shouldLoadTranscript({ ...idleAttached, transcriptLoading: true })).toBe(false);
    expect(shouldLoadTranscript({ ...idleAttached, transcriptLoaded: true })).toBe(false);
    expect(shouldLoadTranscript(null)).toBe(false);
  });

  it('findRunningAssistant 取最后一条 running 助手气泡，脏输入安全', () => {
    const conv = {
      messages: [
        { role: 'assistant', content: '旧', status: 'done' },
        { role: 'assistant', content: '', status: 'running' },
      ],
    };
    expect(findRunningAssistant(conv)?.status).toBe('running');
    expect(findRunningAssistant({ messages: [{ role: 'user', content: 'hi' }] })).toBeNull();
    expect(findRunningAssistant(null)).toBeNull();
  });
});

describe('transcriptToMessages（转录渲染历史）', () => {
  it('映射 user/assistant 气泡，status=done，时间戳解析', () => {
    const rows = [
      { role: 'user', content: '你好', timestamp: '2026-10-29T10:00:00Z' },
      { role: 'assistant', content: '回复', timestamp: '2026-10-29T10:00:05Z' },
    ];
    const msgs = transcriptToMessages(rows);
    expect(msgs).toHaveLength(2);
    expect(msgs[0]).toMatchObject({ role: 'user', content: '你好', status: 'done' });
    expect(msgs[0].ts).toBe(Date.parse('2026-10-29T10:00:00Z'));
    expect(msgs[1].role).toBe('assistant');
  });

  it('空 content 行丢弃，未知 role 按 assistant，脏输入安全', () => {
    expect(transcriptToMessages([
      { role: 'user', content: '   ' },
      { role: 'tool', content: 'x' },
      null,
      { role: 'user' },
    ]).map(m => m.role)).toEqual(['assistant']);
    expect(transcriptToMessages(null)).toEqual([]);
    expect(transcriptToMessages('x')).toEqual([]);
  });
});

describe('restorableAttachedSessionIds（刷新恢复候选）', () => {
  it('仅排除 local- 伪 id：真实设备会话 id（codex_*/claude_*/自造串）都参与恢复', () => {
    expect(restorableAttachedSessionIds([
      { attached: true, aiSessionId: 'codex_abc' },
      { attached: true, aiSessionId: 'claude_xyz' },
      { attached: true, aiSessionId: 'session-20261029' }, // agentcore 内部自造 id 无固定前缀
      { attached: true, aiSessionId: 'local-c1' },         // 本地伪 id 不恢复
      { attached: true, aiSessionId: '' },                 // 空 id 不恢复
      { attached: false, aiSessionId: 'codex_2' },         // 非 attached 不恢复
      { aiSessionId: 'codex_3' },
      null,
    ])).toEqual(['codex_abc', 'claude_xyz', 'session-20261029']);
  });

  it('脏输入安全', () => {
    expect(restorableAttachedSessionIds(null)).toEqual([]);
    expect(restorableAttachedSessionIds('x')).toEqual([]);
  });
});
