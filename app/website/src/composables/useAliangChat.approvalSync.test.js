import { describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 optionResolved.test 模式：只测本文件的桌面审批卡
// 跨端同步纯函数，不碰 composable 装配/网络）。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import {
  chatRecoverySubscribeSessionIds,
  reconcileSnapshotApprovals,
  sweepApprovalKey,
  wireChatSocketRecovery,
} from './useAliangChat';

// 2026-11-09 桌面审批卡滞留家族（诊断定案见 mem approval-card-cross-end-stale）：
// ③ 快照对账单向（running 态只 upsert 不清卡）→ reconcileSnapshotApprovals；
// 归属漂移（request/resolved 可落不同会话对象）→ sweepApprovalKey；
// ⑤ 重连预算耗尽后死寂 → wireChatSocketRecovery（visibilitychange/online 两条
// 确定性恢复时机）。

function makeConv(id, approvals = []) {
  return { id, aiSessionId: `local-${id}`, approvals };
}

function makeCard(convId, approvalId, status = 'pending') {
  return {
    key: `local-${convId}:${approvalId}`,
    id: approvalId,
    sessionId: `local-${convId}`,
    status,
    updatedAt: 1,
  };
}

describe('reconcileSnapshotApprovals（快照权威清卡）', () => {
  it('字段在场+空集：running 态也权威收走残留卡（不封键位）', () => {
    const handled = new Set();
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    const changed = reconcileSnapshotApprovals([conv], [], 'local-c1');
    expect(changed).toBe(true);
    expect(conv.approvals).toHaveLength(0);
    // 不封键是刻意的：快照与 request 帧两条写路径不保序，陈旧快照可能晚到；
    // 封键会把竞态窗口里仍 pending 的真卡永久锁死（审批卡隐形、回合挂死）。
    // 不封键最坏退化为迟到重复帧复活陈卡，下一张快照/终态帧自愈。
    expect(handled.has('local-c1:ap-1')).toBe(false);
  });

  it('字段在场+列表含该卡：保留（后续 upsert 走既有链）', () => {
    const handled = new Set();
    const conv = makeConv('c1', [makeCard('c1', 'ap-1'), makeCard('c1', 'ap-2')]);
    const changed = reconcileSnapshotApprovals(
      [conv],
      [{ session_id: 'local-c1', approval_id: 'ap-1' }],
      'local-c1',
    );
    expect(changed).toBe(true);
    expect(conv.approvals.map(a => a.id)).toEqual(['ap-1']);
    expect(handled.has('local-c1:ap-2')).toBe(false);
  });

  it('字段缺席（旧 agent）：不越权，卡原样保留', () => {
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    expect(reconcileSnapshotApprovals([conv], undefined, 'local-c1')).toBe(false);
    expect(conv.approvals).toHaveLength(1);
    expect(reconcileSnapshotApprovals([conv], null, 'local-c1')).toBe(false);
    expect(conv.approvals).toHaveLength(1);
  });

  it('行畸形（非对象/无 id）：不可配对的卡按权威语义收走', () => {
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    reconcileSnapshotApprovals([conv], ['garbage', {}], 'local-c1');
    expect(conv.approvals).toHaveLength(0);
  });

  it('清单非法或缺会话 id：安全返回', () => {
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    expect(reconcileSnapshotApprovals(null, [], 'local-c1')).toBe(false);
    expect(reconcileSnapshotApprovals([conv], [], '')).toBe(false);
    expect(conv.approvals).toHaveLength(1);
  });

  it('归属漂移（快照会话的卡漂到别的会话对象上）：同样权威收走', () => {
    // ③活跃兜底把本会话的卡挂在了 home 上，快照却归属 shell——两处都该清。
    const shell = makeConv('c1', []);
    const parked = makeCard('c1', 'ap-9'); // sessionId === 快照会话
    const home = makeConv('c2', [parked]);
    expect(reconcileSnapshotApprovals([shell, home], [], 'local-c1')).toBe(true);
    expect(home.approvals).toHaveLength(0);
  });
});

describe('sweepApprovalKey（resolved/cancelled 全局扫卡）', () => {
  it('跨会话对象同键一律收走（归属漂移下 request/resolved 落不同 conv）', () => {
    const handled = new Set();
    const home = makeConv('c1', [makeCard('c1', 'ap-1')]);
    const drifted = makeConv('c2', [makeCard('c2', 'ap-1')]);
    // 归属漂移形态：卡实际挂在 drifted（request 落点），resolved 路由到 home。
    const swept = sweepApprovalKey([home, drifted], 'local-c2', 'ap-1', handled);
    expect(swept).toBe(true);
    expect(drifted.approvals).toHaveLength(0);
    expect(home.approvals).toHaveLength(1);
    expect(handled.has('local-c2:ap-1')).toBe(true);
  });

  it('键不在任何会话：no-op 不封键位（迟到帧不误伤未来同键卡）', () => {
    const handled = new Set();
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    expect(sweepApprovalKey([conv], 'local-c1', 'ap-other', handled)).toBe(false);
    expect(conv.approvals).toHaveLength(1);
    expect(handled.has('local-c1:ap-other')).toBe(false);
  });

  it('approvalId 缺失/会话表为空：防御性 no-op', () => {
    const handled = new Set();
    expect(sweepApprovalKey([], 's', 'a', handled)).toBe(false);
    expect(sweepApprovalKey([makeConv('c1')], 's', '', handled)).toBe(false);
    expect(sweepApprovalKey(null, 's', 'a', handled)).toBe(false);
  });
});

describe('wireChatSocketRecovery（断线自愈第二生命线）', () => {
  function makeFakeTarget() {
    const listeners = new Map();
    return {
      addEventListener(type, fn) {
        if (!listeners.has(type)) listeners.set(type, new Set());
        listeners.get(type).add(fn);
      },
      removeEventListener(type, fn) {
        listeners.get(type)?.delete(fn);
      },
      dispatch(type) {
        listeners.get(type)?.forEach(fn => fn({ type }));
      },
    };
  }

  it('回到标签页（visible）与网络恢复（online）：各触发一次恢复', () => {
    const doc = makeFakeTarget();
    const win = makeFakeTarget();
    doc.visibilityState = 'visible';
    const recover = vi.fn();
    wireChatSocketRecovery(doc, win, recover);
    doc.dispatch('visibilitychange');
    win.dispatch('online');
    expect(recover).toHaveBeenCalledTimes(2);
  });

  it('离开标签页（hidden）：不触发恢复', () => {
    const doc = makeFakeTarget();
    const win = makeFakeTarget();
    doc.visibilityState = 'hidden';
    const recover = vi.fn();
    wireChatSocketRecovery(doc, win, recover);
    doc.dispatch('visibilitychange');
    expect(recover).not.toHaveBeenCalled();
  });

  it('拆卸后不再触发（composable 卸载防泄漏）', () => {
    const doc = makeFakeTarget();
    const win = makeFakeTarget();
    doc.visibilityState = 'visible';
    const recover = vi.fn();
    const dispose = wireChatSocketRecovery(doc, win, recover);
    dispose();
    doc.dispatch('visibilitychange');
    win.dispatch('online');
    expect(recover).not.toHaveBeenCalled();
  });
});

describe('reconcileSnapshotApprovals 跨会话越权守卫（评审 P1 修复）', () => {
  it('快照只裁决自身会话的卡：外来会话卡（②/③兜底合法挂靠）不得被收走', () => {
    const handled = new Set();
    // 外来卡：云端会话经③活跃兜底落在本地会话对象上，键为外来 sessionId。
    const foreign = makeCard('c1', 'ap-1');
    foreign.sessionId = 'ai_FOREIGN';
    foreign.key = 'ai_FOREIGN:ap-1';
    const own = makeCard('c1', 'ap-2');
    const conv = makeConv('c1', [foreign, own]);
    // 快照描述 local-c1 会话,空集只裁决 local-c1 的卡。
    const changed = reconcileSnapshotApprovals([conv], [], 'local-c1');
    expect(changed).toBe(true);
    expect(conv.approvals.map(a => a.id)).toEqual(['ap-1']);
    expect(handled.has('ai_FOREIGN:ap-1')).toBe(false);
    expect(handled.has('local-c1:ap-2')).toBe(false);
  });

  it('快照会话 id 缺省（空串）：整体 no-op 不误伤', () => {
    const conv = makeConv('c1', [makeCard('c1', 'ap-1')]);
    expect(reconcileSnapshotApprovals([conv], [], undefined)).toBe(false);
    expect(conv.approvals).toHaveLength(1);
  });

  it('自身会话在挂卡仍在快照列表：保留', () => {
    const conv = makeConv('c1', [makeCard('c1', 'ap-1'), makeCard('c1', 'ap-2')]);
    reconcileSnapshotApprovals(
      [conv],
      [{ session_id: 'local-c1', approval_id: 'ap-1' }],
      'local-c1',
    );
    expect(conv.approvals.map(a => a.id)).toEqual(['ap-1']);
  });
});

describe('chatRecoverySubscribeSessionIds（恢复路径设备壳补订，评审 P1 修复）', () => {
  // 谓词契约镜像真实 deviceSessionSubscribable 的形状（首查 conv?.deviceSession）：
  // 评审 verify 阶段 node 仿真抓过一版「谓词收到裸清单条目 → 恒 false → 零壳
  // 被订」的惰性接线，此处用同形状桩防回归。
  const deviceSessionSubscribableStub = (conv) => !!conv?.deviceSession
    && conv.deviceSession.id !== 'sess-raw'; // 不可订阅形态（agentcore 裸 id 才可订阅）

  it('返回全部可订阅设备壳的会话对象（订阅 id 与子agent刷新各取所需）', () => {
    const convs = [
      { id: 'a', aiSessionId: 'ja', deviceSession: { id: 'sess-open' } },
      { id: 'b', deviceSession: { id: 'sess-raw' } },
      { id: 'c', aiSessionId: 'local-c' },
    ];
    const targets = chatRecoverySubscribeSessionIds(convs, deviceSessionSubscribableStub);
    expect(targets.map(c => c.deviceSession.id)).toEqual(['sess-open']);
    expect(targets[0].aiSessionId).toBe('ja'); // 刷新参数要用 aiSessionId 优先（镜像 onclose）
  });

  it('空清单/无壳：返回空数组', () => {
    expect(chatRecoverySubscribeSessionIds([], deviceSessionSubscribableStub)).toEqual([]);
    expect(chatRecoverySubscribeSessionIds([{ id: 'c', aiSessionId: 'local-c' }], deviceSessionSubscribableStub)).toEqual([]);
  });

  it('真实谓词形状防惰性回归：无 deviceSession 键的会话不得产出目标', () => {
    // 谓词收到的是会话对象（含 .deviceSession 键）而非裸条目——若实现错把裸
    // 条目喂给谓词，.deviceSession 恒 undefined，此用例全部漏订即炸。
    const convs = [
      { deviceSession: { id: 'x1' } },
      { deviceSession: { id: 'x2' } },
    ];
    expect(chatRecoverySubscribeSessionIds(convs, deviceSessionSubscribableStub).map(c => c.deviceSession.id)).toEqual(['x1', 'x2']);
  });
});
