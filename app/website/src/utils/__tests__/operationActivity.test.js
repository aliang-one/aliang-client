import { describe, it, expect } from 'vitest';
import { parseOperation, mergeOperation, operationElapsedLabel, operationPercent } from '../operationActivity';

// 合法 wire 帧工厂:测试逐字段改坏,其余字段保持合法
const frame = extra => ({
  operation_id: 'op-1',
  title: '重构登录链路',
  phase: 'running',
  source_kind: 'terminal',
  revision: 1,
  updated_at: '2026-11-25T10:00:00Z',
  ...extra,
});

const withProgress = extra => frame({
  progress: { current: 25, total: 200, unit: 'steps' },
  ...extra,
});

// 预置账本:逐帧过 parse 入 Map,保证存量行与真实入库形状一致
const seed = (...rows) => {
  const ledger = new Map();
  for (const row of rows) {
    const parsed = parseOperation(row);
    ledger.set(parsed.operation_id, parsed);
  }
  return ledger;
};

describe('parseOperation(整帧校验,白名单规整副本)', () => {
  it('全量合法帧 → 规整副本:白名单键+progress 三键,未知键丢弃,不回传 raw 引用', () => {
    const raw = withProgress({ revision: 7, junk: '脏数据' });
    const out = parseOperation(raw);
    expect(out).not.toBe(raw);
    expect(out).toEqual({
      operation_id: 'op-1',
      title: '重构登录链路',
      phase: 'running',
      source_kind: 'terminal',
      revision: 7,
      updated_at: '2026-11-25T10:00:00Z',
      progress: { current: 25, total: 200, unit: 'steps' },
    });
    expect(Object.keys(out)).toEqual([
      'operation_id', 'title', 'phase', 'source_kind', 'revision', 'updated_at', 'progress',
    ]);
    expect(Object.keys(out.progress)).toEqual(['current', 'total', 'unit']);
    expect(out.junk).toBeUndefined();
  });

  it('缺 progress 帧合法,输出不带 progress 键', () => {
    const out = parseOperation(frame());
    expect('progress' in out).toBe(false);
  });

  it('progress 的 total/unit 为 null 视作未提供(unknown total 语义),输出剔除', () => {
    const out = parseOperation(frame({ progress: { current: 5, total: null, unit: null } }));
    expect(out.progress).toEqual({ current: 5 });
  });

  it('current=0 合法(非负含零)', () => {
    expect(parseOperation(frame({ progress: { current: 0, total: 10 } })).progress)
      .toEqual({ current: 0, total: 10 });
  });

  it('updated_at 接受 RFC3339 变体(Z/时区偏移/小数秒)', () => {
    for (const ts of [
      '2026-11-25T10:00:00Z',
      '2026-11-25T10:00:00+08:00',
      '2026-11-25T10:00:00-05:30',
      '2026-11-25T10:00:00.123456Z',
    ]) {
      expect(parseOperation(frame({ updated_at: ts }))).not.toBeNull();
    }
  });

  it('updated_at 拒非 ISO 时间戳(日期串/自然语言/无时区/越界分量/非字符串)', () => {
    for (const ts of [
      '2026-11-25',
      'November 25, 2026',
      '2026/11/25 10:00',
      '2026-11-25T10:00:00', // 无时区,RFC3339 不认
      '2026-13-01T00:00:00Z', // 13 月,正则放行但 Date.parse 拒
      1732519200000,
      '',
      null,
      undefined,
    ]) {
      expect(parseOperation(frame({ updated_at: ts }))).toBeNull();
    }
  });

  it('revision 只认正 safe integer', () => {
    expect(parseOperation(frame({ revision: 1 })).revision).toBe(1);
    expect(parseOperation(frame({ revision: Number.MAX_SAFE_INTEGER })).revision)
      .toBe(Number.MAX_SAFE_INTEGER);
    for (const rev of [0, -1, 1.5, '2', NaN, Infinity, Number.MAX_SAFE_INTEGER + 1, null]) {
      expect(parseOperation(frame({ revision: rev }))).toBeNull();
    }
  });

  it('phase 词表:七词全收,未知/缺省/大小写不符整帧拒收', () => {
    for (const phase of ['queued', 'running', 'waiting', 'completed', 'failed', 'cancelled', 'interrupted']) {
      expect(parseOperation(frame({ phase })).phase).toBe(phase);
    }
    for (const phase of ['started', 'progress', 'Running', '', undefined, null, 3]) {
      expect(parseOperation(frame({ phase }))).toBeNull();
    }
  });

  it('必填字符串(operation_id/title/source_kind)非空串约束,逐字段逐坏值拒收', () => {
    for (const key of ['operation_id', 'title', 'source_kind']) {
      for (const val of ['', undefined, null, 42]) {
        expect(parseOperation(frame({ [key]: val }))).toBeNull();
      }
    }
  });

  it('非 object 输入(null/标量/数组)整帧拒收', () => {
    for (const raw of [null, undefined, 42, 'x', true, []]) {
      expect(parseOperation(raw)).toBeNull();
    }
  });

  it('progress 提供但残缺 → 整帧拒收(宁缺毋滥,不拼半帧假进度)', () => {
    for (const progress of [
      42,
      'x',
      [],
      {}, // 缺 current
      { current: -1 },
      { current: Infinity },
      { current: NaN },
      { current: '5' },
      { current: 1, total: 0 },
      { current: 1, total: -3 },
      { current: 1, total: Infinity },
      { current: 1, unit: 5 },
    ]) {
      expect(parseOperation(frame({ progress }))).toBeNull();
    }
  });

  it('unit 空串合法(契约只对 id/title/source_kind 提 nonempty)', () => {
    const out = parseOperation(frame({ progress: { current: 1, unit: '' } }));
    expect(out.progress).toEqual({ current: 1, unit: '' });
  });
});

describe('mergeOperation(revision 单调 + 终态钉死,原 Map 永不被改)', () => {
  it('空账(null/undefined/脏值)视作空,首帧入新 Map,key=operation_id', () => {
    for (const state of [null, undefined, 'junk', 42]) {
      const out = mergeOperation(state, frame());
      expect(out).toBeInstanceOf(Map);
      expect(out.size).toBe(1);
      expect(out.get('op-1')).toMatchObject({ operation_id: 'op-1', phase: 'running', revision: 1 });
    }
  });

  it('空 Map 收首帧返回新 Map 实例,原 Map 不动', () => {
    const state = new Map();
    const out = mergeOperation(state, frame());
    expect(out).not.toBe(state);
    expect(state.size).toBe(0);
    expect(out.size).toBe(1);
  });

  it('严格新 revision 更新:返回新 Map 旧行替换,原 Map 原样,入库行是 parse 副本', () => {
    const state = seed(frame({ revision: 1, phase: 'running' }));
    const raw2 = frame({ revision: 3, phase: 'waiting', progress: { current: 2, total: 9 }, junk: 'x' });
    const out = mergeOperation(state, raw2);
    expect(out).not.toBe(state);
    expect(out.get('op-1')).toMatchObject({
      revision: 3,
      phase: 'waiting',
      progress: { current: 2, total: 9 },
    });
    expect(out.get('op-1').junk).toBeUndefined();
    expect(out.get('op-1')).not.toBe(raw2);
    // 原 Map 原样
    expect(state.get('op-1')).toMatchObject({ revision: 1, phase: 'running' });
  });

  it('stale revision(更小)原引用返回,行不动', () => {
    const state = seed(frame({ revision: 3, phase: 'running' }));
    const out = mergeOperation(state, frame({ revision: 2, phase: 'completed' }));
    expect(out).toBe(state);
    expect(out.get('op-1')).toMatchObject({ revision: 3, phase: 'running' });
  });

  it('等 revision 重放不更新(只认严格更大),原引用返回', () => {
    const state = seed(frame({ revision: 2 }));
    const out = mergeOperation(state, frame({ revision: 2, phase: 'queued' }));
    expect(out).toBe(state);
    expect(out.get('op-1').phase).toBe('running');
  });

  it('终态钉死:终态行遇翻活跃帧,revision 再新也不收(原引用+数据不复活)', () => {
    for (const terminal of ['completed', 'failed', 'cancelled', 'interrupted']) {
      for (const active of ['queued', 'running', 'waiting']) {
        const state = seed(frame({
          revision: 5,
          phase: terminal,
          progress: { current: 9, total: 10 },
        }));
        const out = mergeOperation(state, frame({
          revision: 9,
          phase: active,
          progress: { current: 1, total: 10 },
        }));
        expect(out).toBe(state);
        expect(out.get('op-1')).toMatchObject({
          phase: terminal,
          revision: 5,
          progress: { current: 9, total: 10 },
        });
      }
    }
  });

  it('终态→终态新 revision 允许更新(契约只禁翻活跃)', () => {
    const state = seed(frame({ revision: 5, phase: 'failed' }));
    const out = mergeOperation(state, frame({ revision: 6, phase: 'cancelled' }));
    expect(out).not.toBe(state);
    expect(out.get('op-1')).toMatchObject({ phase: 'cancelled', revision: 6 });
  });

  it('终态行收同 phase 新 revision(摘要补发)允许更新', () => {
    const state = seed(frame({ revision: 5, phase: 'completed' }));
    const out = mergeOperation(state, frame({ revision: 6, phase: 'completed' }));
    expect(out).not.toBe(state);
    expect(out.get('op-1').revision).toBe(6);
  });

  it('首见终态帧直接入库(钉死守卫只对存量行)', () => {
    const out = mergeOperation(new Map(), frame({ revision: 1, phase: 'completed' }));
    expect(out.get('op-1')).toMatchObject({ phase: 'completed', revision: 1 });
  });

  it('非法帧(parseOperation 为 null)原引用返回', () => {
    const state = seed(frame());
    expect(mergeOperation(state, frame({ revision: 0 }))).toBe(state);
    expect(mergeOperation(state, null)).toBe(state);
    expect(mergeOperation(state, 'x')).toBe(state);
  });

  it('多行账本互不串线:只换目标行,他行引用原样保留', () => {
    const rowB = parseOperation(frame({ operation_id: 'op-2', revision: 2, phase: 'completed' }));
    const state = new Map([['op-2', rowB]]);
    const out = mergeOperation(state, frame({ operation_id: 'op-1', revision: 1 }));
    expect(out).not.toBe(state);
    expect(out.size).toBe(2);
    expect(out.get('op-2')).toBe(rowB);
    expect(out.get('op-1').operation_id).toBe('op-1');
  });

  it('存量行脏数据防御:revision 非法视作 0 让位,垃圾行整体替换', () => {
    const state = new Map([['op-1', { phase: 'running' }]]);
    const out = mergeOperation(state, frame({ revision: 1 }));
    expect(out).not.toBe(state);
    expect(out.get('op-1')).toMatchObject({ revision: 1, phase: 'running' });
    const junkState = new Map([['op-1', '垃圾行']]);
    const out2 = mergeOperation(junkState, frame({ revision: 1 }));
    expect(out2.get('op-1')).not.toBe('垃圾行');
    expect(out2.get('op-1').operation_id).toBe('op-1');
  });
});

describe('operationElapsedLabel', () => {
  const started_at = '2026-11-25T10:00:00Z';
  const now = Date.parse('2026-11-25T11:02:03Z');

  it('shows hours for long-running work', () => {
    expect(operationElapsedLabel(frame({ started_at, phase: 'waiting' }), now)).toBe('1:02:03');
  });

  it('omits elapsed time when work is queued or timestamps are unavailable', () => {
    expect(operationElapsedLabel(frame({ started_at, phase: 'queued' }), now)).toBeNull();
    expect(operationElapsedLabel(frame({ started_at: '' }), now)).toBeNull();
    expect(operationElapsedLabel(frame({ started_at, phase: 'completed' }), now)).toBeNull();
  });
});

describe('operationPercent(未知 total → null,余者钳 0–100)', () => {
  it('已知 total:按 (current/total)*100 返回', () => {
    expect(operationPercent({ progress: { current: 25, total: 200 } })).toBe(12.5);
    expect(operationPercent({ progress: { current: 0, total: 100 } })).toBe(0);
    expect(operationPercent({ progress: { current: 1, total: 3 } })).toBeCloseTo(33.3333, 3);
  });

  it('钳位:current>total 封顶 100,current 为负兜底 0(防御手搓对象)', () => {
    expect(operationPercent({ progress: { current: 300, total: 200 } })).toBe(100);
    expect(operationPercent({ progress: { current: -5, total: 100 } })).toBe(0);
  });

  it('未知 total(缺 operation/缺 progress/缺 total/total 非正有限)→ null', () => {
    expect(operationPercent(null)).toBeNull();
    expect(operationPercent(undefined)).toBeNull();
    expect(operationPercent({})).toBeNull();
    expect(operationPercent({ progress: { current: 5 } })).toBeNull();
    expect(operationPercent({ progress: { current: 5, total: null } })).toBeNull();
    expect(operationPercent({ progress: { current: 5, total: 0 } })).toBeNull();
    expect(operationPercent({ progress: { current: 5, total: Infinity } })).toBeNull();
    expect(operationPercent({ progress: { current: 5, total: '50' } })).toBeNull();
  });

  it('current 缺失按 0 兜底(total 已知即有确定进度)', () => {
    expect(operationPercent({ progress: { total: 50 } })).toBe(0);
  });
});

describe('端到端:parse → merge → percent 一条龙', () => {
  it('排队→进行→完成全生命周期;终态后迟到的活跃帧不翻车', () => {
    let ledger = mergeOperation(null, frame({ revision: 1, phase: 'queued' }));
    ledger = mergeOperation(ledger, frame({
      revision: 2,
      phase: 'running',
      progress: { current: 4, total: 10, unit: 'steps' },
    }));
    expect(operationPercent(ledger.get('op-1'))).toBe(40);
    ledger = mergeOperation(ledger, frame({
      revision: 3,
      phase: 'completed',
      progress: { current: 10, total: 10, unit: 'steps' },
    }));
    expect(operationPercent(ledger.get('op-1'))).toBe(100);
    // 迟到的活跃帧(revision 再新也一样)不复活终态行
    ledger = mergeOperation(ledger, frame({
      revision: 9,
      phase: 'running',
      progress: { current: 2, total: 10, unit: 'steps' },
    }));
    expect(ledger.get('op-1')).toMatchObject({ phase: 'completed', revision: 3 });
    expect(operationPercent(ledger.get('op-1'))).toBe(100);
  });

  it('无 progress 的操作全链合法,percent 全程 null(渲染层切不确定态)', () => {
    let ledger = mergeOperation(null, frame({ revision: 1, phase: 'waiting' }));
    expect(operationPercent(ledger.get('op-1'))).toBeNull();
    ledger = mergeOperation(ledger, frame({ revision: 2, phase: 'completed' }));
    expect(operationPercent(ledger.get('op-1'))).toBeNull();
  });
});
