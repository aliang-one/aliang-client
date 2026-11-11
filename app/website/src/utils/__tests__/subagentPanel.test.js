import { describe, it, expect } from 'vitest';
import {
  applySubagentEvent,
  hasActiveSubagents,
  subagentPhaseMeta,
  formatSubagentDuration,
  formatSubagentTokens,
} from '../subagentPanel';

// 构造 ai.subagent 事件帧的便捷工厂
const ev = (taskId, phase, extra = {}) => ({ type: 'ai.subagent', taskId, phase, ...extra });

describe('applySubagentEvent', () => {
  it('started 未知 taskId 新增条目并映射字段', () => {
    const out = applySubagentEvent([], ev('t1', 'started', {
      subagentType: 'code-reviewer',
      description: '审查改动',
    }));
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({
      taskId: 't1',
      name: 'code-reviewer',
      description: '审查改动',
      phase: 'started',
    });
  });

  it('started 缺 subagentType 时 name 回落为 taskId', () => {
    const out = applySubagentEvent([], ev('t9', 'started'));
    expect(out[0].name).toBe('t9');
  });

  it('started 已存在时不覆盖已有 name/description(幂等)', () => {
    const seeded = [{ taskId: 't1', name: 'reviewer', description: '原始描述', phase: 'progress', currentStep: '读取文件', lastTool: 'Grep', toolUses: 3, totalTokens: 120, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'started', {
      subagentType: '别的名字',
      description: '别的描述',
    }));
    expect(out[0].name).toBe('reviewer');
    expect(out[0].description).toBe('原始描述');
    // progress 数据也不允许被 started 重置
    expect(out[0].phase).toBe('progress');
    expect(out[0].currentStep).toBe('读取文件');
    expect(out[0].lastTool).toBe('Grep');
    expect(out[0].toolUses).toBe(3);
    // 零数据变化按纪律返回原引用
    expect(out).toBe(seeded);
  });

  it('progress 更新 currentStep/lastTool/toolUses 且 phase 保持 progress', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'progress', {
      currentStep: '运行测试',
      lastTool: 'Bash',
      toolUses: 7,
    }));
    expect(out[0]).toMatchObject({
      phase: 'progress',
      currentStep: '运行测试',
      lastTool: 'Bash',
      toolUses: 7,
    });
  });

  it('progress 的 toolUses 只取大者防乱序回退', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'progress', currentStep: 'a', lastTool: 'Bash', toolUses: 9, totalTokens: null, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'progress', { currentStep: 'b', lastTool: 'Read', toolUses: 4 }));
    expect(out[0].toolUses).toBe(9);
    // 其余字段照常更新
    expect(out[0].currentStep).toBe('b');
    expect(out[0].lastTool).toBe('Read');
  });

  it('progress 未知 taskId 忽略(不新建)', () => {
    const out = applySubagentEvent([], ev('ghost', 'progress', { currentStep: 'x' }));
    expect(out).toHaveLength(0);
  });

  it('completed 定稿 phase 并更新 durationMs/summary', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'progress', currentStep: 'x', lastTool: 'Bash', toolUses: 5, totalTokens: 800, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'completed', { durationMs: 4321, summary: '完成了三处修复' }));
    expect(out[0]).toMatchObject({
      phase: 'completed',
      durationMs: 4321,
      summary: '完成了三处修复',
    });
    // 进度字段保留不重置
    expect(out[0].lastTool).toBe('Bash');
  });

  it('completed 不带 summary/durationMs 时保留旧值', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'progress', currentStep: null, lastTool: null, toolUses: 1, totalTokens: null, durationMs: 111, summary: '旧摘要' }];
    const out = applySubagentEvent(seeded, ev('t1', 'completed'));
    expect(out[0].phase).toBe('completed');
    expect(out[0].durationMs).toBe(111);
    expect(out[0].summary).toBe('旧摘要');
  });

  it('failed 定稿 phase', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'failed'));
    expect(out[0].phase).toBe('failed');
  });

  it('interrupted 定稿 phase', () => {
    const seeded = [{ taskId: 't1', name: 'r', description: '', phase: 'progress', currentStep: 'x', lastTool: null, toolUses: 2, totalTokens: null, durationMs: null, summary: null }];
    const out = applySubagentEvent(seeded, ev('t1', 'interrupted'));
    expect(out[0].phase).toBe('interrupted');
  });

  it('终态后迟到 progress 不复活条目(phase 钉死+原引用)', () => {
    for (const terminal of ['completed', 'failed', 'interrupted']) {
      const seeded = [{ taskId: 't1', name: 'r', description: '', phase: terminal, currentStep: null, lastTool: null, toolUses: 5, totalTokens: 800, durationMs: 4321, summary: '定稿摘要' }];
      const out = applySubagentEvent(seeded, ev('t1', 'progress', { currentStep: '幽灵步骤', toolUses: 99 }));
      // 引用原样:零变化不触发响应式
      expect(out).toBe(seeded);
      // 数据不复活:phase 钉死在终态,进度字段不被迟到帧改写
      expect(out[0]).toMatchObject({
        phase: terminal,
        currentStep: null,
        lastTool: null,
        toolUses: 5,
        durationMs: 4321,
        summary: '定稿摘要',
      });
    }
  });

  it('终态帧对未知 taskId 忽略(不新建)', () => {
    const out = applySubagentEvent([], ev('ghost', 'completed', { summary: '幽灵' }));
    expect(out).toHaveLength(0);
  });

  it('乱序容错:progress 先于 started 被忽略,后续 started 补建', () => {
    let out = applySubagentEvent([], ev('t1', 'progress', { currentStep: '早期步骤', toolUses: 2 }));
    expect(out).toHaveLength(0);
    out = applySubagentEvent(out, ev('t1', 'started', { subagentType: 'explorer', description: '探索代码' }));
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({ taskId: 't1', name: 'explorer', phase: 'started' });
  });

  it('多 task 交替事件互不串线', () => {
    let out = [];
    out = applySubagentEvent(out, ev('a', 'started', { subagentType: 'alpha' }));
    out = applySubagentEvent(out, ev('b', 'started', { subagentType: 'beta' }));
    out = applySubagentEvent(out, ev('a', 'progress', { currentStep: 'A步骤', toolUses: 3 }));
    out = applySubagentEvent(out, ev('b', 'progress', { currentStep: 'B步骤', toolUses: 8 }));
    expect(out.map(s => s.taskId)).toEqual(['a', 'b']);
    const a = out.find(s => s.taskId === 'a');
    const b = out.find(s => s.taskId === 'b');
    expect(a).toMatchObject({ name: 'alpha', currentStep: 'A步骤', toolUses: 3 });
    expect(b).toMatchObject({ name: 'beta', currentStep: 'B步骤', toolUses: 8 });
    out = applySubagentEvent(out, ev('b', 'completed', { summary: 'B 完成' }));
    const aAfter = out.find(s => s.taskId === 'a');
    expect(aAfter.phase).toBe('progress');
    expect(out.find(s => s.taskId === 'b').phase).toBe('completed');
  });

  it('未知 phase 返回原数组引用(不触发响应式)', () => {
    const list = [{ taskId: 't1', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    const out = applySubagentEvent(list, ev('t1', 'teleported', {}));
    expect(out).toBe(list);
  });

  it('不可变更新:原数组与其中的条目均未被改', () => {
    const original = [{ taskId: 't1', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    const snapshot = JSON.parse(JSON.stringify(original));
    const out = applySubagentEvent(original, ev('t1', 'progress', { currentStep: 'x', toolUses: 4 }));
    expect(out).not.toBe(original);
    expect(JSON.parse(JSON.stringify(original))).toEqual(snapshot);
    // 新数组中的条目是新对象,不再共享旧对象引用
    expect(out[0]).not.toBe(original[0]);
  });

  it('脏输入防御:null 状态视同空数组(started 照常建条),缺 taskId 的帧原样返回', () => {
    expect(applySubagentEvent(null, ev('t1', 'started'))).toHaveLength(1);
    expect(applySubagentEvent(null, ev('t1', 'progress'))).toHaveLength(0);
    expect(applySubagentEvent(null, ev('t1', 'completed'))).toHaveLength(0);
    const list = [{ taskId: 't1', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    expect(applySubagentEvent(list, { type: 'ai.subagent', phase: 'started' })).toBe(list);
  });
});

describe('hasActiveSubagents', () => {
  it('存在 started 或 progress 条目返回 true', () => {
    expect(hasActiveSubagents([
      { taskId: 't1', phase: 'completed' },
      { taskId: 't2', phase: 'progress' },
    ])).toBe(true);
    expect(hasActiveSubagents([{ taskId: 't3', phase: 'started' }])).toBe(true);
  });

  it('全部终态或空数组返回 false', () => {
    expect(hasActiveSubagents([
      { taskId: 't1', phase: 'completed' },
      { taskId: 't2', phase: 'failed' },
      { taskId: 't3', phase: 'interrupted' },
    ])).toBe(false);
    expect(hasActiveSubagents([])).toBe(false);
    expect(hasActiveSubagents(null)).toBe(false);
  });
});

describe('subagentPhaseMeta（面板/详情共用 phase 呈现映射，词表单真源）', () => {
  it('活动态映射 running 色调，终态按词各映射（含 interrupted）', () => {
    expect(subagentPhaseMeta('started')).toMatchObject({ tone: 'running', labelKey: 'chat_subagentStatusRunning' });
    expect(subagentPhaseMeta('progress')).toMatchObject({ tone: 'running', labelKey: 'chat_subagentStatusRunning' });
    expect(subagentPhaseMeta('completed')).toMatchObject({ tone: 'done', labelKey: 'chat_subagentStatusCompleted' });
    expect(subagentPhaseMeta('failed')).toMatchObject({ tone: 'failed', labelKey: 'chat_subagentStatusFailed' });
    expect(subagentPhaseMeta('interrupted')).toMatchObject({ tone: 'interrupted', labelKey: 'chat_subagentStatusInterrupted' });
  });

  it('未知/缺省 phase 安全兜底 running（与入口条计数「未知两侧不计」互不影响——本函数只管呈现）', () => {
    expect(subagentPhaseMeta('weird').tone).toBe('running');
    expect(subagentPhaseMeta(undefined).tone).toBe('running');
  });
});

describe('formatSubagentDuration（durationMs→秒呈现）', () => {
  it('正值毫秒转秒显示', () => {
    expect(formatSubagentDuration(4200)).toBe('4.2s');
    expect(formatSubagentDuration(500)).toBe('0.5s');
    expect(formatSubagentDuration(65000)).toBe('1m5s');
  });

  it('非法/非正值返回空串（渲染层不显示该槽位）', () => {
    expect(formatSubagentDuration(null)).toBe('');
    expect(formatSubagentDuration(undefined)).toBe('');
    expect(formatSubagentDuration(-1)).toBe('');
    expect(formatSubagentDuration('abc')).toBe('');
  });
});

describe('formatSubagentTokens', () => {
  it('正值千分位显示；非法/非正值返回空串', () => {
    expect(formatSubagentTokens(1200)).toBe('1,200');
    expect(formatSubagentTokens(0)).toBe('');
    expect(formatSubagentTokens(null)).toBe('');
    expect(formatSubagentTokens('x')).toBe('');
  });
});

describe('queued 排队态(2026-11-10 ③ 排队可见化)', () => {
  it('queued 未知 taskId 新建条目,phase=queued 且带排队文案', () => {
    const out = applySubagentEvent([], ev('tq1', 'queued', {
      subagentType: 'researcher',
      description: '调研',
      currentStep: '排队等待并发槽',
    }));
    expect(out).toHaveLength(1);
    expect(out[0]).toMatchObject({
      taskId: 'tq1',
      name: 'researcher',
      description: '调研',
      phase: 'queued',
      currentStep: '排队等待并发槽',
    });
  });

  it('queued→started(排队放行)翻 started 并清排队文案', () => {
    const seeded = applySubagentEvent([], ev('tq2', 'queued', { currentStep: '排队等待并发槽' }));
    const out = applySubagentEvent(seeded, ev('tq2', 'started', { subagentType: 'r' }));
    expect(out[0].phase).toBe('started');
    expect(out[0].currentStep).toBeNull();
    // name 不被 started 幂等语义覆盖之外误改:queued 新建时 subagentType 缺省
    // 回落 taskId,放行帧带 subagentType 也不覆盖(与既有幂等口径一致)。
    expect(out[0].name).toBe('tq2');
  });

  it('queued 已存在时仅更新文案;空文案原引用返回', () => {
    const seeded = applySubagentEvent([], ev('tq3', 'queued', { currentStep: '排队等待并发槽' }));
    const updated = applySubagentEvent(seeded, ev('tq3', 'queued', { currentStep: '排队中(第 2 位)' }));
    expect(updated[0].currentStep).toBe('排队中(第 2 位)');
    expect(updated).not.toBe(seeded);
    expect(applySubagentEvent(updated, ev('tq3', 'queued'))).toBe(updated);
  });

  it('已 started 条目的迟到 queued 帧忽略;终态条目的 queued 帧忽略(钉死)', () => {
    const running = [{ taskId: 'tq4', name: 'r', description: '', phase: 'started', currentStep: null, lastTool: null, toolUses: 0, totalTokens: null, durationMs: null, summary: null }];
    expect(applySubagentEvent(running, ev('tq4', 'queued'))).toBe(running);
    const done = [{ taskId: 'tq5', name: 'r', description: '', phase: 'completed', currentStep: null, lastTool: null, toolUses: 1, totalTokens: 10, durationMs: 100, summary: 'ok' }];
    expect(applySubagentEvent(done, ev('tq5', 'queued'))).toBe(done);
  });

  it('queued 算活跃(hasActiveSubagents)但呈现不是 running', () => {
    const queued = applySubagentEvent([], ev('tq6', 'queued'));
    expect(hasActiveSubagents(queued)).toBe(true);
    const meta = subagentPhaseMeta('queued');
    expect(meta.tone).toBe('queued');
    expect(meta.labelKey).toBe('chat_subagentStatusQueued');
    expect(meta.tone).not.toBe('running');
  });
});
