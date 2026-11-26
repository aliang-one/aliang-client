// 会话诊断 WS 消费纯链测试（spec 2026-11-26-chat-info-drawer）：progress 帧
// 归一/键存/空心跳拒收/详情回填，与 ai.subagent 纯链测试同惯用法——不装配
// composable，直接驱动模块级导出。
import { describe, it, expect, vi } from 'vitest';

const { consumeProgressFrame, diagnosticsFrameToState, ingestDetailDiagnostics } = await import('./useAliangChat');

// 宿主 diag.payload() 的键形（agent_ai_agentcore.go:1801 起，snake_case）。
const HOST_PAYLOAD = {
  turns: 4,
  context_tokens: 51234,
  context_window: 200000,
  dup_streak: 1,
  err_streak: 0,
  trunc_streak: 2,
  elapsed_ms: 61320,
  stop_reason: 'end_turn',
};

describe('diagnosticsFrameToState', () => {
  it('蛇形帧归一为驼峰快照（含文件/Git 计数）', () => {
    const out = diagnosticsFrameToState({
      type: 'ai.run.progress',
      session_id: 's1',
      files_touched_count: 3,
      git_changed_count: 1,
      diagnostics: HOST_PAYLOAD,
    });
    expect(out).toEqual({
      turns: 4,
      contextTokens: 51234,
      contextWindow: 200000,
      dupStreak: 1,
      errStreak: 0,
      truncStreak: 2,
      elapsedMs: 61320,
      stopReason: 'end_turn',
      terminal: false,
      reason: null,
      status: null,
      error: null,
      filesTouchedCount: 3,
      gitChangedCount: 1,
    });
  });

  it('diagnostics 缺席（尚无回合空心跳）返回 null', () => {
    expect(diagnosticsFrameToState({ type: 'ai.run.progress', session_id: 's1', diagnostics: null })).toBeNull();
    expect(diagnosticsFrameToState({ type: 'ai.run.progress', session_id: 's1' })).toBeNull();
  });

  it('终态快照字段（terminal/reason/status/error）如实透传', () => {
    const out = diagnosticsFrameToState({
      type: 'ai.run.progress',
      session_id: 's1',
      diagnostics: { ...HOST_PAYLOAD, terminal: true, status: 'error', reason: 'err_streak_overflow', error: '上游连续失败' },
    });
    expect(out.terminal).toBe(true);
    expect(out.status).toBe('error');
    expect(out.reason).toBe('err_streak_overflow');
    expect(out.error).toBe('上游连续失败');
  });

  it('非数值字段归 null、非法输入整体拒收', () => {
    const out = diagnosticsFrameToState({
      type: 'ai.run.progress',
      session_id: 's1',
      diagnostics: { turns: 'x', context_tokens: 5 },
    });
    expect(out.turns).toBe(0);
    expect(out.contextTokens).toBe(5);
    expect(out.contextWindow).toBeNull();
    expect(diagnosticsFrameToState(null)).toBeNull();
    expect(diagnosticsFrameToState('frame')).toBeNull();
  });
});

describe('consumeProgressFrame', () => {
  it('按 session_id 键整条覆盖快照', () => {
    const store = {};
    expect(consumeProgressFrame(store, { type: 'ai.run.progress', session_id: 's1', diagnostics: { ...HOST_PAYLOAD, turns: 1 } })).toBe(true);
    expect(consumeProgressFrame(store, { type: 'ai.run.progress', session_id: 's1', diagnostics: HOST_PAYLOAD })).toBe(true);
    expect(store.s1.turns).toBe(4);
    // 他会话帧入他键——展示层只读当前会话键，无污染面
    consumeProgressFrame(store, { type: 'ai.run.progress', session_id: 's2', diagnostics: { turns: 9 } });
    expect(store.s2.turns).toBe(9);
    expect(store.s1.turns).toBe(4);
  });

  it('非 progress 帧与空心跳安全拒收（防误路由）', () => {
    const store = {};
    expect(consumeProgressFrame(store, { type: 'ai.subagent', session_id: 's1' })).toBe(false);
    expect(consumeProgressFrame(store, { type: 'ai.run.progress', session_id: 's1', diagnostics: null })).toBe(false);
    expect(consumeProgressFrame(store, { type: 'ai.run.progress', diagnostics: HOST_PAYLOAD })).toBe(false);
    expect(consumeProgressFrame(store, null)).toBe(false);
    expect(Object.keys(store)).toEqual([]);
  });
});

describe('ingestDetailDiagnostics', () => {
  it('journal end 终态诊断经详情回填（含 status/error）', () => {
    const store = {};
    expect(ingestDetailDiagnostics(store, 's1', {
      diagnostics: { ...HOST_PAYLOAD, terminal: true, status: 'error', error: '上游连续失败' },
      files_touched_count: 5,
    })).toBe(true);
    expect(store.s1.status).toBe('error');
    expect(store.s1.error).toBe('上游连续失败');
    expect(store.s1.filesTouchedCount).toBe(5);
  });

  it('详情无 diagnostics 键（旧会话）不写存储', () => {
    const store = {};
    expect(ingestDetailDiagnostics(store, 's1', { subagents: [] })).toBe(false);
    expect(ingestDetailDiagnostics(store, 's1', null)).toBe(false);
    expect(Object.keys(store)).toEqual([]);
  });
});

// 静默重拉（真实实现路径）：getAgentSessionDetail 经 vi.mock 注入，成功回填
// + 失败静默 + in-flight 去重，与 refreshSubagentsFromDetail 同款断言口径。
describe('refreshDiagnosticsFromDetail', () => {
  it('拉取成功回填终态快照；失败静默不抛', async () => {
    vi.resetModules();
    const detail = { diagnostics: { ...HOST_PAYLOAD, terminal: true, status: 'success' } };
    vi.doMock('../services/agentApi', () => ({
      getAgentIntegrations: vi.fn(),
      getAgentSessionDetail: vi.fn().mockResolvedValueOnce(detail).mockRejectedValueOnce(new Error('network')),
    }));
    const mod = await import('./useAliangChat');
    const store = {};
    await mod.refreshDiagnosticsFromDetail(store, 's1');
    expect(store.s1.status).toBe('success');
    await mod.refreshDiagnosticsFromDetail(store, 's1'); // 失败静默
    expect(store.s1.status).toBe('success'); // 保留上一次快照
  });

  it('同会话 in-flight 去重：并发只发一次请求', async () => {
    vi.resetModules();
    let resolveDetail;
    vi.doMock('../services/agentApi', () => ({
      getAgentIntegrations: vi.fn(),
      getAgentSessionDetail: vi.fn().mockReturnValue(new Promise((r) => { resolveDetail = r; })),
    }));
    const mod = await import('./useAliangChat');
    const store = {};
    const p1 = mod.refreshDiagnosticsFromDetail(store, 's1');
    const p2 = mod.refreshDiagnosticsFromDetail(store, 's1');
    resolveDetail({ diagnostics: HOST_PAYLOAD });
    await Promise.all([p1, p2]);
    const { getAgentSessionDetail } = await import('../services/agentApi');
    expect(getAgentSessionDetail).toHaveBeenCalledTimes(1);
  });
});
