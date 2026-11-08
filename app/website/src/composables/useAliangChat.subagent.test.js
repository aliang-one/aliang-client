import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock——本文件测
// ai.subagent 帧消费纯链（归属守卫+状态聚合+M1 详情重拉），不装配 composable
// （useI18n/useNavigation 等需 Vue app 上下文）。getAgentSessionDetail 的 mock
// 让 M1 重拉走真实 refreshSubagentsFromDetail 实现并断言调用与消费。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

import { getAgentSessionDetail } from '../services/agentApi';
import {
  consumeSubagentFrame,
  ingestDetailSubagents,
  refreshSubagentsFromDetail,
  subagentDetailToReducerEntry,
  subagentFrameToReducerMsg,
} from './useAliangChat';

// 构造 WS 蛇形帧（字段名与后端 agentAISubagentPayload 同源）的便捷工厂
const wireFrame = (sessionId, taskId, phase, extra = {}) => ({
  type: 'ai.subagent',
  session_id: sessionId,
  task_id: taskId,
  phase,
  ...extra,
});

describe('subagentFrameToReducerMsg（蛇形→驼峰归一）', () => {
  it('wire 键全量映射为 reducer 驼峰键', () => {
    const msg = subagentFrameToReducerMsg(wireFrame('s1', 't1', 'progress', {
      subagent_type: 'code-reviewer',
      description: '审查改动',
      current_step: '运行测试',
      last_tool: 'Bash',
      tool_uses: 4,
      total_tokens: 1200,
      duration_ms: 3200,
      summary: '全绿',
    }));
    expect(msg).toEqual({
      taskId: 't1',
      phase: 'progress',
      subagentType: 'code-reviewer',
      description: '审查改动',
      currentStep: '运行测试',
      lastTool: 'Bash',
      toolUses: 4,
      totalTokens: 1200,
      durationMs: 3200,
      summary: '全绿',
    });
  });

  it('脏帧/缺键安全：非对象与缺 task_id 都安全（reducer 侧再兜底）', () => {
    expect(subagentFrameToReducerMsg(null)).toEqual(null);
    expect(subagentFrameToReducerMsg({ type: 'ai.subagent', session_id: 's1' }).taskId).toBe('');
  });
});

describe('consumeSubagentFrame（handleAIEvent 消费链：归属守卫+聚合+M1）', () => {
  let hooks;

  beforeEach(() => {
    hooks = { isWatching: () => false, refetch: vi.fn() };
  });

  // --- 会话归属守卫（spec M2） ---
  it('帧 session_id ≠ 当前会话 id：状态不变且返回 false（守卫丢弃）', () => {
    const store = {};
    const ok = consumeSubagentFrame(store, wireFrame('other-session', 't1', 'started', { subagent_type: 'r' }), 'mine', hooks);
    expect(ok).toBe(false);
    expect(store).toEqual({});
    expect(hooks.refetch).not.toHaveBeenCalled();
  });

  it('帧 session_id = 当前会话 id：状态更新且返回 true', () => {
    const store = {};
    const ok = consumeSubagentFrame(store, wireFrame('mine', 't1', 'started', { subagent_type: 'reviewer', description: '审查' }), 'mine', hooks);
    expect(ok).toBe(true);
    expect(store.mine).toHaveLength(1);
    expect(store.mine[0]).toMatchObject({ taskId: 't1', name: 'reviewer', description: '审查', phase: 'started' });
  });

  it('当前会话无 aiSessionId（草稿未发送）：一律丢弃', () => {
    const store = {};
    expect(consumeSubagentFrame(store, wireFrame('s1', 't1', 'started'), '', hooks)).toBe(false);
    expect(store).toEqual({});
  });

  it('非 ai.subagent 帧安全拒收（防误路由）', () => {
    const store = {};
    expect(consumeSubagentFrame(store, { type: 'ai.delta', session_id: 's1' }, 's1', hooks)).toBe(false);
    expect(store).toEqual({});
  });

  // --- reducer 集成（经消费链全链） ---
  it('started 建 → progress 更新 → completed 定稿（多帧同 task 全链）', () => {
    const store = {};
    const sid = 'sess-1';
    consumeSubagentFrame(store, wireFrame(sid, 't1', 'started', { subagent_type: 'code-reviewer' }), sid, hooks);
    expect(store[sid]).toHaveLength(1);
    expect(store[sid][0]).toMatchObject({ taskId: 't1', name: 'code-reviewer', phase: 'started' });

    consumeSubagentFrame(store, wireFrame(sid, 't1', 'progress', {
      current_step: '运行测试', last_tool: 'Bash', tool_uses: 3,
    }), sid, hooks);
    expect(store[sid][0]).toMatchObject({ phase: 'progress', currentStep: '运行测试', lastTool: 'Bash', toolUses: 3 });

    consumeSubagentFrame(store, wireFrame(sid, 't1', 'completed', { duration_ms: 4200, summary: '全部通过' }), sid, hooks);
    expect(store[sid][0]).toMatchObject({ phase: 'completed', durationMs: 4200, summary: '全部通过' });
    // 终态后的迟到 progress 不得复活条目（reducer 终态冻结经全链仍生效）
    consumeSubagentFrame(store, wireFrame(sid, 't1', 'progress', { current_step: '幽灵' }), sid, hooks);
    expect(store[sid][0]).toMatchObject({ phase: 'completed', currentStep: '运行测试' });
  });

  it('failed 终态定稿（失败摘要落 summary）', () => {
    const store = {};
    consumeSubagentFrame(store, wireFrame('s1', 't2', 'started', { subagent_type: 'x' }), 's1', hooks);
    consumeSubagentFrame(store, wireFrame('s1', 't2', 'failed', { summary: '上游失联' }), 's1', hooks);
    expect(store.s1[0]).toMatchObject({ phase: 'failed', summary: '上游失联' });
  });

  // --- M1 报告重拉口径 ---
  it('watching=true 时 completed 触发一次重拉（带会话 id）', () => {
    const store = {};
    const watching = { isWatching: () => true, refetch: vi.fn() };
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'started'), 's1', watching);
    expect(watching.refetch).not.toHaveBeenCalled(); // started/progress 不触发
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'progress', { tool_uses: 1 }), 's1', watching);
    expect(watching.refetch).not.toHaveBeenCalled();
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'completed', { summary: 'done' }), 's1', watching);
    expect(watching.refetch).toHaveBeenCalledTimes(1);
    expect(watching.refetch).toHaveBeenCalledWith('s1');
  });

  it('watching=false 时 completed 不触发重拉（面板打开逻辑兜底）', () => {
    const store = {};
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'started'), 's1', hooks);
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'completed'), 's1', hooks);
    expect(hooks.refetch).not.toHaveBeenCalled();
    // 状态仍正常定稿（不触发重拉≠不消费事件）
    expect(store.s1[0].phase).toBe('completed');
  });

  it('failed 终态同样触发重拉；isWatching 按 taskId 精确判定', () => {
    const store = {};
    const watching = { isWatching: (taskId) => taskId === 't2', refetch: vi.fn() };
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'started'), 's1', watching);
    consumeSubagentFrame(store, wireFrame('s1', 't1', 'completed'), 's1', watching);
    consumeSubagentFrame(store, wireFrame('s1', 't2', 'failed'), 's1', watching);
    expect(watching.refetch).toHaveBeenCalledTimes(1);
    expect(watching.refetch).toHaveBeenCalledWith('s1');
  });

  it('跨会话帧（守卫丢弃）绝不触发重拉', () => {
    const store = {};
    const watching = { isWatching: () => true, refetch: vi.fn() };
    consumeSubagentFrame(store, wireFrame('foreign', 't1', 'completed'), 's1', watching);
    expect(watching.refetch).not.toHaveBeenCalled();
    expect(store).toEqual({});
  });
});

describe('subagentDetailToReducerEntry（详情 API 蛇形→reducer 驼峰：status→phase）', () => {
  // 详情 API（AgentVibeSubagent,models/agent.go）契约：蛇形键 + status 词表
  // started|completed|failed + tools[]（tool/summary/is_error/ts）。评审钉死：
  // 详情条目原样进 store 会与 WS 帧（phase 键）混存两种形状，入口条/面板
  // 计数全 0——入库前必须归一为 reducer 条目形状。
  it('全量字段映射：task_id/status/total_tokens/duration_ms→驼峰+phase，result/tools 透传保留', () => {
    const entry = subagentDetailToReducerEntry({
      task_id: 't1',
      name: 'code-reviewer',
      description: '审查改动',
      status: 'completed',
      summary: '全部通过',
      result: '## 结论\n**全绿**',
      total_tokens: 1200,
      duration_ms: 4200,
      started_at: '2026-11-07T01:00:00Z',
      ended_at: '2026-11-07T01:00:04Z',
      tools: [
        { tool: 'Bash', summary: 'npm test', is_error: false, ts: '2026-11-07T01:00:01Z' },
        { tool: 'Edit', summary: '改文件失败', is_error: true, ts: '2026-11-07T01:00:02Z' },
      ],
    });
    expect(entry).toEqual({
      taskId: 't1',
      name: 'code-reviewer',
      description: '审查改动',
      phase: 'completed',
      currentStep: null,
      lastTool: null,
      toolUses: 0,
      totalTokens: 1200,
      durationMs: 4200,
      summary: '全部通过',
      result: '## 结论\n**全绿**',
      startedAt: '2026-11-07T01:00:00Z',
      endedAt: '2026-11-07T01:00:04Z',
      tools: [
        { tool: 'Bash', summary: 'npm test', isError: false, ts: '2026-11-07T01:00:01Z' },
        { tool: 'Edit', summary: '改文件失败', isError: true, ts: '2026-11-07T01:00:02Z' },
      ],
    });
  });

  it('缺 finish 行的 started 与空 status 兜底为 phase=started；缺 name 回落 taskId', () => {
    expect(subagentDetailToReducerEntry({ task_id: 't2', status: 'started' }).phase).toBe('started');
    expect(subagentDetailToReducerEntry({ task_id: 't3' }).phase).toBe('started');
    const noname = subagentDetailToReducerEntry({ task_id: 't4', status: 'completed' });
    expect(noname.name).toBe('t4');
  });

  it('tools 缺省→空数组；is_error 非真值一律归 false；坏行过滤', () => {
    expect(subagentDetailToReducerEntry({ task_id: 't1' }).tools).toEqual([]);
    expect(
      subagentDetailToReducerEntry({
        task_id: 't1',
        tools: [null, { tool: 'Bash' }, { tool: 'Read', is_error: 1 }],
      }).tools,
    ).toEqual([
      { tool: 'Bash', summary: '', isError: false, ts: '' },
      { tool: 'Read', summary: '', isError: false, ts: '' },
    ]);
  });

  it('脏输入防御：null/缺 task_id 返回 null（refresh 侧过滤丢弃）', () => {
    expect(subagentDetailToReducerEntry(null)).toBeNull();
    expect(subagentDetailToReducerEntry({ name: 'no-id' })).toBeNull();
    expect(subagentDetailToReducerEntry('str')).toBeNull();
  });
});

describe('refreshSubagentsFromDetail（M1 静默重拉：只消费 subagents[]，入库前归一 status→phase）', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('详情带 subagents[]：整体替换该会话面板状态，转录不落壳，条目已归一为 reducer 形状', async () => {
    getAgentSessionDetail.mockResolvedValue({
      transcript: [{ role: 'user', content: '全量重拉的转录行——绝不能进壳' }],
      subagents: [{
        task_id: 't1',
        name: 'reviewer',
        status: 'completed',
        summary: '全部通过',
        result: '全绿报告',
        total_tokens: 900,
        duration_ms: 4200,
        tools: [{ tool: 'Bash', summary: 'npm test', is_error: false }],
      }],
    });
    const store = { s1: [{ taskId: 't1', name: 'reviewer', phase: 'progress', currentStep: '旧步骤' }] };
    await refreshSubagentsFromDetail(store, 's1');
    expect(getAgentSessionDetail).toHaveBeenCalledWith('s1');
    // phase 键 + 驼峰键：与 WS 聚合条目同形状，入口条/面板计数不因详情来源而归 0
    expect(store.s1).toEqual([{
      taskId: 't1',
      name: 'reviewer',
      description: null,
      phase: 'completed',
      currentStep: null,
      lastTool: null,
      toolUses: 0,
      totalTokens: 900,
      durationMs: 4200,
      summary: '全部通过',
      result: '全绿报告',
      startedAt: null,
      endedAt: null,
      tools: [{ tool: 'Bash', summary: 'npm test', isError: false, ts: '' }],
    }]);
  });

  it('详情带空 subagents[]：合法清空（无子agent会话），坏条目（缺 task_id）被过滤', async () => {
    getAgentSessionDetail.mockResolvedValue({
      subagents: [{ name: 'bad-no-task-id' }, { task_id: 'ok', status: 'failed', summary: '上游失联' }],
    });
    const store = {};
    await refreshSubagentsFromDetail(store, 's1');
    expect(store.s1).toHaveLength(1);
    expect(store.s1[0]).toMatchObject({ taskId: 'ok', phase: 'failed' });
  });

  it('详情未带 subagents 键（后端降级/旧版）：保留 WS 聚合态不覆写', async () => {
    getAgentSessionDetail.mockResolvedValue({ transcript: [] });
    const existing = [{ taskId: 't1', phase: 'progress', currentStep: 'ws 态' }];
    const store = { s1: existing };
    await refreshSubagentsFromDetail(store, 's1');
    expect(store.s1).toBe(existing);
  });

  it('详情拉取失败：静默（不抛错）保留既有状态', async () => {
    getAgentSessionDetail.mockRejectedValue(new Error('boom'));
    const existing = [{ taskId: 't1', phase: 'completed' }];
    const store = { s1: existing };
    await expect(refreshSubagentsFromDetail(store, 's1')).resolves.toBeUndefined();
    expect(store.s1).toEqual(existing);
  });

  it('空 sessionId 拒绝；同会话 in-flight 去重（并发 completed 只拉一次）', async () => {
    let release;
    getAgentSessionDetail.mockImplementation(() => new Promise((resolve) => { release = resolve; }));
    const store = {};
    const first = refreshSubagentsFromDetail(store, 's1');
    const second = refreshSubagentsFromDetail(store, 's1'); // in-flight 窗口内第二发
    release({ subagents: [] });
    await first;
    await second;
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(1);
    await expect(refreshSubagentsFromDetail(store, '')).resolves.toBeUndefined();
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(1);
  });
});

describe('ingestDetailSubagents（历史装配内核：openDeviceSession 与重拉共用的纯装配）', () => {
  it('只消费 subagents[] 整体替换面板状态；转录键绝不落进 store', () => {
    const store = {};
    ingestDetailSubagents(store, 's1', {
      transcript: [{ role: 'user', content: '详情转录行——绝不进面板状态' }],
      subagents: [{ task_id: 't1', name: 'reviewer', status: 'completed' }],
    });
    expect(Object.keys(store)).toEqual(['s1']);
    expect(store.s1).toHaveLength(1);
    expect(store.s1[0]).toMatchObject({ taskId: 't1', phase: 'completed' });
  });

  it('空 sessionId 拒绝；详情缺 subagents 键保留既有聚合态', () => {
    const existing = [{ taskId: 't1', phase: 'progress' }];
    const store = { s1: existing };
    ingestDetailSubagents(store, '', { subagents: [] });
    ingestDetailSubagents(store, 's1', { transcript: [] });
    expect(store.s1).toBe(existing);
  });
});
