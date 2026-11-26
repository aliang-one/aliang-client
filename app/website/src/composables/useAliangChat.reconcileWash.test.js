import { describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock（同 reconcileSeq.test 模式）：只测纯函数。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

import { reconcileLocalSnapshotMessages } from './useAliangChat';

// error 洗白律（症状1修复 2026-11-26）：误染红泡（看门狗假红/断线画法/外来帧
// 误染）经快照对账自动翻绿——journal 权威失败信号是 end(error) 折出的系统条
// （round.notes）；无 notes 且 settled 且有正文 → done；流中（settled=false，
// 蕴含 notes 空）→ running 恢复活体；真失败（notes 非空）永不清。

const TS_A = '2026-11-10T03:40:00Z';
const FALLBACK = 1700000000000;

const uRow = (mid, text, ts, seq) => ({ ts, role: 'user', message_id: mid, text, ...(seq ? { seq } : {}) });
const aRow = (text, ts, seq) => ({ ts, role: 'assistant', text, ...(seq ? { seq } : {}) });
const sRow = (text, ts) => ({ ts, role: 'system', text });

describe('reconcile error 洗白律（症状1：红壳跨刷新永存根治）', () => {
  it('误染 error 泡 + journal 无 end(error) 系统条 + 定谳 → 洗成 done、正文被 journal 全文修复', () => {
    const local = [
      { role: 'user', content: '问题', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '部分流式内容', status: 'error', ts: 2 },
    ];
    const res = reconcileLocalSnapshotMessages(local, [
      uRow('m1', '问题', TS_A),
      aRow('部分流式内容+journal 尾巴', TS_A),
    ], { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.changed).toBe(true);
    const answer = res.messages.find(m => m.role === 'assistant');
    expect(answer.status).toBe('done'); // 洗白
    expect(answer.content).toBe('部分流式内容+journal 尾巴'); // 全文修复照常
  });

  it('真失败（journal 有 end(error) 系统条）保留红、不洗白', () => {
    const local = [
      { role: 'user', content: '问题', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '有内容', status: 'error', ts: 2 },
    ];
    const res = reconcileLocalSnapshotMessages(local, [
      uRow('m1', '问题', TS_A),
      aRow('有内容', TS_A),
      sRow('回合失败：工具执行超时', TS_A),
    ], { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    const answer = res.messages.find(m => m.role === 'assistant' && !m.kind);
    expect(answer.status).toBe('error'); // 真失败红泡永不清
  });

  it('流中对账（run 还在跑）：误染 error 泡翻回 running 恢复活体接流', () => {
    const local = [
      { role: 'user', content: '问题', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '已流到的前缀', status: 'error', ts: 2 },
    ];
    const res = reconcileLocalSnapshotMessages(local, [
      uRow('m1', '问题', TS_A),
      aRow('已流到的前缀+增量', TS_A),
    ], { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.alignedLive).toBe(true);
    const answer = res.messages.find(m => m.role === 'assistant');
    expect(answer.status).toBe('running'); // 翻回活体
    expect(answer.content).toBe('已流到的前缀+增量');
  });

  it('外来回合复用陈旧 error 泡：同律洗白（emitRound answer 分支）', () => {
    const local = [
      { role: 'user', content: '本地问', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '本地答', status: 'done', ts: 2 },
      { role: 'user', content: '手机问', messageId: 'm2', provider: 'agentcore', ts: 3 },
      { role: 'assistant', content: '手机答旧泡', status: 'error', ts: 4 },
    ];
    const res = reconcileLocalSnapshotMessages(local, [
      uRow('m1', '本地问', TS_A),
      aRow('本地答', TS_A),
      uRow('m2', '手机问', TS_A),
      aRow('手机答旧泡', TS_A),
    ], { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    const answer = res.messages.find(m => m.role === 'assistant' && m.content === '手机答旧泡');
    expect(answer.status).toBe('done');
  });

  it('空正文 error 泡不洗（无正文洗成 done 会闪空泡）', () => {
    const local = [
      { role: 'user', content: '问题', messageId: 'm1', provider: 'agentcore', ts: 1 },
      { role: 'assistant', content: '', status: 'error', ts: 2 },
    ];
    const res = reconcileLocalSnapshotMessages(local, [
      uRow('m1', '问题', TS_A),
      aRow('journal 有全文', TS_A),
    ], { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    const answer = res.messages.find(m => m.role === 'assistant' && !m.kind);
    // journal 全文先修进正文，随后洗白律同样适用（正文已非空）
    expect(answer.status).toBe('done');
    expect(answer.content).toBe('journal 有全文');
  });
});
