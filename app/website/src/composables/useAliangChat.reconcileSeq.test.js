import { describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 foreignRounds.test / reconcile.test 模式）：只测纯函数。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

import { reconcileLocalSnapshotMessages, chatMessageSeq, orderMessagesBySeq } from './useAliangChat';

// 对账补建的 seq 落位（2026-11-10 聊天记录身份化）。
//
// reconcileLocalSnapshotMessages 会为「锚点之后的外来回合」新建 user 泡与答案
// 泡。此前这两类新泡不带 seq——于是下一次跨端合并又只能退回数组序，等于本轮
// 修复在最重要的场景（另一端新发的提问）上不生效。本组钉死：补建的泡必须带
// 上 journal 的 seq，且第二次对账必须幂等（changed=false），不得反复抖动。

const TS_A = '2026-11-10T03:40:00Z';
const TS_B = '2026-11-10T03:44:34Z';
const FALLBACK = 1700000000000;

const uRow = (mid, text, ts, seq) => ({ ts, role: 'user', message_id: mid, text, ...(seq ? { seq } : {}) });
const aRow = (text, ts, seq) => ({ ts, role: 'assistant', text, ...(seq ? { seq } : {}) });

describe('reconcileLocalSnapshotMessages — seq 落位', () => {
  it('外来回合补建的 user 泡与答案泡都带 seq', () => {
    const local = [{ role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 }];
    const rows = [
      uRow('m1', '本地问', TS_A, 1),
      aRow('本地答', TS_A, 2),
      uRow('m2', '手机问', TS_B, 3),
      aRow('手机答', TS_B, 4),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    expect(res.changed).toBe(true);
    const foreignUser = res.messages.filter(m => m.messageId === 'm2');
    expect(foreignUser).toHaveLength(1);
    expect(chatMessageSeq(foreignUser[0])).toBe(3);
    const foreignAnswer = res.messages.find(m => m.role === 'assistant' && m.content === '手机答');
    expect(foreignAnswer).toBeTruthy();
    expect(chatMessageSeq(foreignAnswer)).toBe(4);
  });

  it('旧 agent（行无 seq）不得凭空写入 seq 键', () => {
    const local = [{ role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 }];
    const rows = [uRow('m1', '本地问', TS_A), aRow('本地答', TS_A), uRow('m2', '手机问', TS_B), aRow('手机答', TS_B)];
    const res = reconcileLocalSnapshotMessages(local, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    for (const m of res.messages) {
      expect(m && m.seq).toBeUndefined();
    }
  });

  it('二次对账幂等：seq 已一致时不再判 changed', () => {
    const local = [{ role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 }];
    const rows = [
      uRow('m1', '本地问', TS_A, 1),
      aRow('本地答', TS_A, 2),
      uRow('m2', '手机问', TS_B, 3),
      aRow('手机答', TS_B, 4),
    ];
    const first = reconcileLocalSnapshotMessages(local, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    expect(first.changed).toBe(true);
    const second = reconcileLocalSnapshotMessages(first.messages, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    // 幂等契约：零改动必须 changed=false（否则调用方每轮重赋值+落盘，渲染抖动）。
    expect(second.changed).toBe(false);
    expect(second.appended).toBe(0);
  });

  it('锚回合已存在的答案泡被复用且内容未变时，seq 不会被反复改写', () => {
    // 锚点取「最后一个能在本地找到 mid 的 user 行」——所以只要本地有该泡，
    // 该回合就成锚，走不到 reuseUser 分支。这里覆盖可达的那条：锚回合答案泡
    // 原样复用（内容一致）时 committed seq 应幂等落定，第二次对账 changed=false。
    const local = [
      { role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '本地答', status: 'done', ts: 1 },
    ];
    const rows = [uRow('m1', '本地问', TS_A, 1), aRow('本地答', TS_A, 2)];
    const first = reconcileLocalSnapshotMessages(local, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    const answer = first.messages.find(m => m.role === 'assistant' && m.content === '本地答');
    expect(chatMessageSeq(answer)).toBe(2);
    const second = reconcileLocalSnapshotMessages(first.messages, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    expect(second.changed).toBe(false);
  });
});

describe('orderMessagesBySeq — 占位部分排序', () => {
  it('双方都有 seq 时按 seq 排', () => {
    const out = orderMessagesBySeq([{ content: 'a', seq: 5 }, { content: 'b', seq: 2 }, { content: 'c', seq: 9 }]);
    expect(out.map(m => m.content)).toEqual(['b', 'a', 'c']);
  });

  it('无 seq 的条目永远原地不动，带 seq 的只在各自槽位内换序', () => {
    // 关键不变量：本地草稿 / 失败发送 / 运行尾占位 / 旧 agent 的行都没有 seq，
    // 它们的位置必须一动不动；只有带 seq 的那几个下标被重新分配。
    const draft = { content: 'draft' };            // 本地草稿，无 seq
    const tail = { content: 'running-tail' };      // 运行尾占位，无 seq
    const list = [{ content: 'a', seq: 5 }, draft, { content: 'c', seq: 2 }, tail];
    const out = orderMessagesBySeq(list);
    expect(out).toHaveLength(4);
    expect(out[1]).toBe(draft); // 位置 1 仍是草稿
    expect(out[3]).toBe(tail);  // 位置 3 仍是尾占位
    // 槽位 0 与 2 之间按 seq 重排：c(2) 到 0，a(5) 到 2。
    expect(out[0].content).toBe('c');
    expect(out[2].content).toBe('a');
  });

  it('全部缺 seq 时保持原数组（零意外重排）', () => {
    const list = [{ content: 'a' }, { content: 'b' }, { content: 'c' }];
    expect(orderMessagesBySeq(list).map(m => m.content)).toEqual(['a', 'b', 'c']);
  });
});

describe('reconcileLocalSnapshotMessages — 乱序快照行按 seq 归位', () => {
  it('快照行数组乱序时，按 seq 重排后再分段（问答不再错位）', () => {
    // 真实成因：快照限幅截尾 + 服务端合并后，行数组序未必等于写侧顺序。
    // 若直接按数组序切回合，助手的行会挂到错误的 user 行下（问答错位）。
    // seq 是跨端可比的先后真值，分段前必须先归位。
    const local = [{ role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 }];
    // 故意打乱：先助手后提问。
    const rows = [
      aRow('手机答', TS_B, 4),
      uRow('m1', '本地问', TS_A, 1),
      aRow('本地答', TS_A, 2),
      uRow('m2', '手机问', TS_B, 3),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, {
      provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK,
    });
    const contents = res.messages.map(m => m.content);
    const askIdx = contents.indexOf('手机问');
    const ansIdx = contents.indexOf('手机答');
    expect(askIdx).toBeGreaterThan(-1);
    expect(ansIdx).toBeGreaterThan(askIdx); // 答案必须在提问之后
    expect(chatMessageSeq(res.messages.find(m => m.messageId === 'm2'))).toBe(3);
    expect(chatMessageSeq(res.messages.find(m => m.content === '手机答'))).toBe(4);
  });
});
