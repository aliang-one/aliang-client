import { describe, expect, it, vi } from 'vitest';

// agentApi 整体 mock(同 reconcile.test 模式):只测纯函数,不装配 composable。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
  uploadChatAttachment: vi.fn(),
}));

import {
  chatAttachmentUrl,
  reconcileLocalSnapshotMessages,
  ensureAssistantBubble,
  beginRunWindow,
  endRunWindow,
  isConversationRunning,
  findRunningAssistant,
  runStartedNeedsSnapshot,
  runDoneNeedsSnapshot,
} from './useAliangChat';

// 2026-11-09 vm22 事故(计划 docs/superpowers/plans/2026-11-09-web-local-
// foreign-turn-render.md):手机端向网页正在使用的同一 journal 会话发问后,
// 网页本地会话视图 ①快照对账把锚点之后所有 assistant 行拼进最后一个气泡
// (多答连体) ②外来 user 行永不回建(提问隐形) ③活体 delta 续写旧泡或整段
// 丢弃。本组测试钉死「回合分段 + 权威修复 + 外来回合补建」的纯函数内核。

const TS_A = '2026-11-09T03:40:00Z';
const TS_B = '2026-11-09T03:44:34Z';
const TS_C = '2026-11-09T03:45:54Z';
const FALLBACK = 1700000000000;

const row = (ts, o) => ({ ts, ...o });
const uRow = (mid, text, ts, extra = {}) => row(ts, { role: 'user', message_id: mid, text, ...extra });
const aRow = (text, ts) => row(ts, { role: 'assistant', text });

// 本地气泡速造
const uB = (mid, text) => ({ role: 'user', content: text, messageId: mid, provider: 'agentcore', ts: 1 });
const aB = (content, status = 'done') => ({ role: 'assistant', content, status, ts: 1 });
const noteB = (content) => ({ role: 'assistant', kind: 'system', note: 'approval_cancelled', content, status: 'done', ts: 1 });

describe('reconcileLocalSnapshotMessages(回合分段对账:连体修复 + 外来补建)', () => {
  it('T1 核心事故:连体气泡修复为锚回合文本,外来回合逐个补建提问+回答', () => {
    // vm22 实况形态:锚回合气泡被旧对账拼进外来回合文本
    const local = [
      uB('m1', '你好'),
      aB('答1'),
      uB('m2', '调研'),
      aB('答2\n\n外来A文本\n\n外来B文本'), // 连体垃圾
    ];
    const rows = [
      uRow('m1', '你好', TS_A),
      aRow('答1', TS_A),
      uRow('m2', '调研', TS_A),
      aRow('答2', TS_A),
      uRow('msg_p3', '几点', TS_B),
      aRow('外来A文本', TS_B),
      uRow('msg_p4', '断网', TS_C),
      aRow('外来B文本', TS_C),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.changed).toBe(true);
    expect(res.messages).toHaveLength(8);
    const [l1, l2, l3, l4, l5, l6, l7, l8] = res.messages;
    expect(l1).toBe(local[0]); // 锚点前区域不动(同引用)
    expect(l2).toBe(local[1]);
    expect(l3).toBe(local[2]);
    // 连体修复:锚回合气泡收敛为 journal 文本
    expect(l4.content).toBe('答2');
    expect(l4.status).toBe('done');
    // 外来回合补建:user 气泡带 journal 同源 messageId + provider
    expect(l5).toMatchObject({ role: 'user', content: '几点', messageId: 'msg_p3', provider: 'agentcore' });
    expect(l6).toMatchObject({ role: 'assistant', content: '外来A文本', status: 'done' });
    expect(l7).toMatchObject({ role: 'user', content: '断网', messageId: 'msg_p4', provider: 'agentcore' });
    expect(l8).toMatchObject({ role: 'assistant', content: '外来B文本', status: 'done' });
  });

  it('T2 流中锚回合:delta 半截文本不被收缩,内容按 journal 增长式对齐', () => {
    const local = [uB('m1', '问'), aB('', 'running')];
    const rows = [uRow('m1', '问', TS_A), aRow('答1turn1', TS_A)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.messages[1].status).toBe('running');
    expect(res.messages[1].content).toBe('答1turn1');
    expect(res.alignedLive).toBe(true);
    expect(res.changed).toBe(true);
  });

  it('T2b 流中分叉(delta 领先 turn 行):内容保守不动,状态保持 running', () => {
    const partial = '我正在排查,先看第一组日志';
    const local = [uB('m1', '问'), aB(partial, 'running')];
    const rows = [uRow('m1', '问', TS_A), aRow('答1turn1(半截外的完成回合)', TS_A)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.messages[1].status).toBe('running');
    expect(res.messages[1].content).toBe(partial);
    expect(res.changed).toBe(false);
  });

  it('T3 滞留 running 泡 + 回合已定谳:关账并修复连体内容', () => {
    const local = [uB('m1', '问'), aB('答1\n\n外来垃圾', 'running')];
    const rows = [uRow('m1', '问', TS_A), aRow('答1', TS_A)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.messages[1].status).toBe('done');
    expect(res.messages[1].content).toBe('答1');
    expect(res.closedRunning).toBe(1);
  });

  it('T4 活体竞态:外来回合 delta 先落泡、快照后到 → user 气泡插到活泡前,答案不重复', () => {
    const live = aB('外来答案流…', 'running');
    const local = [uB('m1', '问'), aB('答1'), live];
    const rows = [uRow('m1', '问', TS_A), aRow('答1', TS_A), uRow('msg_p3', '几点', TS_B)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.messages).toHaveLength(4);
    expect(res.messages[2]).toMatchObject({ role: 'user', messageId: 'msg_p3' });
    expect(res.messages[3]).toBe(live); // 活泡原对象保留,delta 续流不断
    expect(res.messages[3].status).toBe('running');
    // 不重复补建答案泡(活泡即本回合答案)
    const answers = res.messages.filter(m => m.role === 'assistant');
    expect(answers).toHaveLength(2);
  });

  it('T5 .option 作答行跳过 user 泡(翻卡即作答),回合回答照常补建', () => {
    const local = [uB('m1', '问'), aB('答1')];
    const rows = [
      uRow('m1', '问', TS_A),
      aRow('答1', TS_A),
      uRow('assistant_m1.option', '用户已在上述方案中做出选择', TS_B),
      aRow('答2', TS_B),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    const mids = res.messages.filter(m => m.role === 'user').map(m => m.messageId);
    expect(mids).toEqual(['m1']); // 不新建 .option 泡
    expect(res.messages[res.messages.length - 1]).toMatchObject({ role: 'assistant', content: '答2', status: 'done' });
  });

  it('T6 外来提问附件透传(ref → 网页取图 URL)', () => {
    const local = [uB('m1', '问'), aB('答1')];
    const rows = [
      uRow('m1', '问', TS_A),
      aRow('答1', TS_A),
      uRow('msg_p3', '几点', TS_B, {
        attachments: [{ name: 'amw7guvi91hc90.jpg', mime: 'image/jpeg', bytes: 552348, ref: 'local-x\\msg_p3-0.jpg', origin: 'phone' }],
      }),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    const backfilled = res.messages.find(m => m.messageId === 'msg_p3');
    expect(backfilled.attachments).toEqual([
      { name: 'amw7guvi91hc90.jpg', url: chatAttachmentUrl('local-x\\msg_p3-0.jpg') },
    ]);
  });

  it('T7 本地独有物(失败发送/系统条)保留,journal 尾部补建在其后', () => {
    const sysErr = noteB('审批已取消');
    const local = [uB('m1', '问'), aB('答1'), uB('m9', '没发出去'), sysErr];
    const rows = [uRow('m1', '问', TS_A), aRow('答1', TS_A), uRow('msg_p3', '几点', TS_B), aRow('外来A', TS_B)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.messages.map(m => m.messageId || m.content)).toEqual([
      'm1', '答1', 'm9', '审批已取消', 'msg_p3', '外来A',
    ]);
  });

  it('T8 无锚(本地 user 气泡全不在 rows 中):整体 no-op', () => {
    const local = [uB('mX', '别的'), aB('别的答')];
    const rows = [uRow('msg_p1', '外来问', TS_A), aRow('外来答', TS_A)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.changed).toBe(false);
    expect(res.messages).toBe(local);
  });

  it('T9 锚回合无 assistant 行(turn 未落)且本地无泡:不误建;装饰按序保留', () => {
    const local = [uB('m1', '问'), aB('答1'), noteB('审批已取消'), uB('m2', '问2'), aB('半截', 'running')];
    const rows = [uRow('m1', '问', TS_A), aRow('答1', TS_A), uRow('msg_p3', '几点', TS_B), aRow('外来A', TS_B)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    // 锚=m2(最后一个命中),其回合 journal 无文本 → 不误建;滞留 running 关账
    expect(res.messages[4]).toMatchObject({ status: 'done', content: '半截' });
    expect(res.messages[5]).toMatchObject({ role: 'user', messageId: 'msg_p3' });
    expect(res.messages[6]).toMatchObject({ role: 'assistant', content: '外来A' });
    expect(res.messages[2]).toEqual(noteB('审批已取消'));
  });

  it('T10 帽截断孤儿头行(首条 user 行被截走)跳过,不误判回合', () => {
    const local = [uB('m1', '问'), aB('答1', 'running')];
    const rows = [aRow('孤儿回合残留', TS_A), uRow('m1', '问', TS_A), aRow('答1', TS_A)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.messages[1].content).toBe('答1');
    expect(res.messages[1].status).toBe('running');
  });

  it('T11 幂等:已补建形态二次对账零改动', () => {
    const local = [
      uB('m1', '问'), aB('答1'),
      uB('msg_p3', '几点'), aB('外来A'),
      uB('msg_p4', '断网'), aB('外来B'),
    ];
    const rows = [
      uRow('m1', '问', TS_A), aRow('答1', TS_A),
      uRow('msg_p3', '几点', TS_B), aRow('外来A', TS_B),
      uRow('msg_p4', '断网', TS_C), aRow('外来B', TS_C),
    ];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: false, fallbackTs: FALLBACK });
    expect(res.changed).toBe(false);
    expect(res.messages).toHaveLength(6);
  });

  it('T12 末回合 running 快照:补建的答案泡为 running,先于它的回合照常 done', () => {
    const local = [uB('m1', '问'), aB('答1')];
    const rows = [uRow('m1', '问', TS_A), aRow('答1', TS_A), uRow('msg_p3', '几点', TS_B), aRow('外来A开头', TS_B)];
    const res = reconcileLocalSnapshotMessages(local, rows, { provider: 'agentcore', snapshotRunning: true, fallbackTs: FALLBACK });
    expect(res.messages[2]).toMatchObject({ role: 'user', messageId: 'msg_p3' });
    expect(res.messages[3]).toMatchObject({ role: 'assistant', content: '外来A开头', status: 'running' });
    // running 快照要建运行态泡:调用方据此置 recoveredRun(终态回取)
    expect(res.createdRunning).toBe(true);
  });

  it('T13 空 rows / 空本地:安全 no-op', () => {
    const local = [uB('m1', '问')];
    expect(reconcileLocalSnapshotMessages(local, [], { fallbackTs: FALLBACK }).changed).toBe(false);
    expect(reconcileLocalSnapshotMessages([], [uRow('m1', '问', TS_A)], { fallbackTs: FALLBACK }).changed).toBe(false);
  });
});

describe('ensureAssistantBubble(run 窗口感知)', () => {
  it('窗口外:末泡 done → 迟到 delta 丢弃(守卫保留)', () => {
    const conv = { messages: [aB('答', 'done')] };
    expect(ensureAssistantBubble(conv, 1)).toBe(null);
    expect(conv.messages).toHaveLength(1);
  });

  it('窗口内(runActive):无 running 泡必产新——外来回合 delta 不再整段丢弃', () => {
    const conv = { messages: [aB('答', 'done')], runActive: true };
    const fresh = ensureAssistantBubble(conv, 42);
    expect(fresh).toMatchObject({ role: 'assistant', content: '', status: 'running', ts: 42 });
    expect(conv.messages).toHaveLength(2);
  });

  it('已有 running 泡:原对象返回,不另产泡', () => {
    const running = aB('流', 'running');
    const conv = { messages: [running] };
    expect(ensureAssistantBubble(conv, 1)).toBe(running);
    expect(conv.messages).toHaveLength(1);
  });
});

describe('beginRunWindow / endRunWindow(串行回合权威收敛)', () => {
  it('新回合 started:滞留旧 running 泡关账(终态帧丢失根治口)+ 产新泡', () => {
    const stale = aB('旧答案', 'running');
    const conv = { messages: [stale] };
    const fresh = beginRunWindow(conv, 7);
    expect(stale.status).toBe('done');
    expect(conv.runActive).toBe(true);
    expect(conv.running).toBe(true);
    expect(fresh).toMatchObject({ status: 'running', ts: 7 });
    expect(conv.messages).toEqual([stale, fresh]);
  });

  it('本端在途占位泡(ownPending):复用同一对象,绝不关账——占位可能已含先到的 delta', () => {
    const placeholder = aB('', 'running');
    const conv = { messages: [uB('m1', '问'), placeholder] };
    const ensured = beginRunWindow(conv, 7, { ownPending: true });
    expect(ensured).toBe(placeholder);
    expect(placeholder.status).toBe('running');
    expect(conv.messages).toHaveLength(2);
  });

  it('endRunWindow:清窗 + 关残留 running,返回 hadRunning', () => {
    const conv = { messages: [aB('x', 'running')], runActive: true, running: true };
    expect(endRunWindow(conv)).toBe(true);
    expect(conv.runActive).toBe(false);
    expect(conv.running).toBe(false);
    expect(conv.messages[0].status).toBe('done');
    expect(endRunWindow({ messages: [aB('y')] })).toBe(false);
  });
});

describe('isConversationRunning(三源并集)', () => {
  it('快照态/runActive/气泡 三源任一即 running', () => {
    expect(isConversationRunning(null)).toBe(false);
    expect(isConversationRunning({})).toBe(false);
    expect(isConversationRunning({ running: true })).toBe(true);
    expect(isConversationRunning({ runActive: true })).toBe(true);
    expect(isConversationRunning({ messages: [aB('x', 'running')] })).toBe(true);
    expect(isConversationRunning({ messages: [aB('x', 'done')] })).toBe(false);
  });
});

describe('快照拉取判定(外来回合补建的订阅口)', () => {
  it('runStartedNeedsSnapshot:本端会话且非设备壳才补订阅', () => {
    expect(runStartedNeedsSnapshot({ aiSessionId: 'local-a' }, true)).toBe(true);
    expect(runStartedNeedsSnapshot({ aiSessionId: 'local-a', deviceSession: {} }, true)).toBe(false);
    expect(runStartedNeedsSnapshot({ aiSessionId: 'local-a' }, false)).toBe(false);
    expect(runStartedNeedsSnapshot({}, true)).toBe(false);
  });

  it('runDoneNeedsSnapshot:recoveredRun 或本回合曾有 running 泡才定谳复订阅', () => {
    expect(runDoneNeedsSnapshot({ aiSessionId: 'local-a', recoveredRun: true }, false)).toBe(true);
    expect(runDoneNeedsSnapshot({ aiSessionId: 'local-a' }, true)).toBe(true);
    expect(runDoneNeedsSnapshot({ aiSessionId: 'local-a' }, false)).toBe(false);
    expect(runDoneNeedsSnapshot({ aiSessionId: 'local-a', deviceSession: {} }, true)).toBe(false);
  });
});

describe('findRunningAssistant(倒序首个 running)', () => {
  it('从尾部倒查,跳过 done', () => {
    const running = aB('流', 'running');
    expect(findRunningAssistant({ messages: [aB('d'), running] })).toBe(running);
    expect(findRunningAssistant({ messages: [aB('d')] })).toBe(null);
    expect(findRunningAssistant({ messages: [] })).toBe(null);
  });
});
