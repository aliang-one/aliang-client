// 2026-11-26 症状2修复接线钉死：三族交互卡(审批/方案/tool_ask)按 createdAt
// 内联进时间线,跟随所属回合——已选「已选择」折叠卡贴着对应气泡,不再整体
// 钉在列表底。归并排序本体在 chatTimeline.test.js,本文件只钉组件装配面:
// 卡渲染在时间线内、位置正确、无 createdAt 卡退化列表尾。
import { describe, it, expect, vi } from 'vitest';
import { createApp, h, nextTick, reactive } from 'vue';

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
// ChatApprovalCard 从 composable 模块取审批决策归一纯函数——mock 需保供
// (同 ChatMessageList.imganchor.test 的浅 mock 口径再补两枚)。
vi.mock('../../composables/useAliangChat', () => ({
  isComposingEvent: () => false,
  normalizeApprovalDecision: (d) => String(d || ''),
  normalizeApprovalDecisions: (v) => (Array.isArray(v) ? v.map(x => String(x)) : []),
}));

import ChatMessageList from './ChatMessageList.vue';

const baseProps = {
  conversation: { id: 'c1', provider: 'agentcore' },
  activeId: 'c1',
  steerItems: [],
  approvals: [],
  optionCards: [],
  toolAsks: [],
  hiddenOptionIds: [],
  nowTick: 0,
  setContainer: () => {},
  setNearBottom: () => {},
  scrollToBottom: () => {},
  nearBottom: true,
};

function mountList(props) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(ChatMessageList, { ...baseProps, ...props }),
  });
  app.mount(el);
  return { el, app };
}

function cleanup(app, el) {
  app.unmount();
  el.remove();
}

// 文档序标记序列:卡走 testid;气泡文本走「最深的、trim 后全文等于目标串」
// 的元素(assistant 正文经 ChatMarkdown 渲染成 <p>,user 走纯文本插值 div,
// 两种形态都能以精确全文命中)。compareDocumentPosition 保证与渲染序一致。
function markOf(node) {
  const tid = node.getAttribute('data-testid');
  if (tid === 'chat-option-card' || tid === 'chat-approval-card' || tid === 'chat-steer-item') {
    return tid;
  }
  return null;
}

function sequence(el, texts) {
  const markers = [];
  el.querySelectorAll('*').forEach((n) => {
    const m = markOf(n);
    if (m) markers.push({ node: n, mark: m });
    const txt = (n.textContent || '').trim();
    if (texts.includes(txt) && n.children.length === 0) {
      markers.push({ node: n, mark: `text:${txt}` });
    }
  });
  // querySelectorAll 本身按文档序返回;同节点去重(卡外层 testid 只记一次)。
  const seen = new Set();
  const out = [];
  for (const mk of markers) {
    if (seen.has(mk.mark)) continue;
    seen.add(mk.mark);
    out.push(mk.mark);
  }
  return out;
}

describe('交互卡内联进时间线(症状2)', () => {
  it('已选方案卡按 createdAt 落在发起泡之后、后续消息之前', () => {
    const messages = [
      { role: 'user', content: '构建策略怎么选', ts: 100, messageId: 'm1', provider: 'agentcore' },
      { role: 'assistant', content: '上游依赖死锁,给你方案', ts: 200 },
      { role: 'user', content: '继续', ts: 400, messageId: 'm2', provider: 'agentcore' },
      { role: 'assistant', content: '收到,开始执行', ts: 500 },
    ];
    const optionCards = [{
      key: 'k1', optionId: 'opt1', status: 'answered', selected: ['a'],
      options: [{ id: 'a', label: '最小绕过' }], createdAt: 200, updatedAt: 300,
    }];
    const { el, app } = mountList({ activeMessages: messages, optionCards });
    const seq = sequence(el, ['构建策略怎么选', '上游依赖死锁,给你方案', '继续', '收到,开始执行']);
    const ansIdx = seq.indexOf('text:上游依赖死锁,给你方案');
    const cardIdx = seq.indexOf('chat-option-card');
    const contIdx = seq.indexOf('text:继续');
    expect(ansIdx).toBeGreaterThanOrEqual(0);
    expect(cardIdx).toBeGreaterThan(ansIdx);
    expect(contIdx).toBeGreaterThan(cardIdx);
    expect(seq.indexOf('text:收到,开始执行')).toBe(seq.length - 1);
    cleanup(app, el);
  });

  it('待答卡同样内联,落发起泡之后', () => {
    const messages = [
      { role: 'user', content: 'q', ts: 100, messageId: 'm1', provider: 'agentcore' },
      { role: 'assistant', content: 'ans', ts: 200 },
    ];
    const optionCards = [{
      key: 'k2', optionId: 'opt2', status: 'pending',
      options: [{ id: 'a', label: 'A' }], createdAt: 200, updatedAt: 200,
    }];
    const { el, app } = mountList({ activeMessages: messages, optionCards });
    const seq = sequence(el, ['ans']);
    expect(seq.indexOf('chat-option-card')).toBeGreaterThan(seq.indexOf('text:ans'));
    cleanup(app, el);
  });

  it('审批卡与 tool_ask 卡同轨内联', () => {
    const messages = [
      { role: 'user', content: 'q', ts: 100, messageId: 'm1', provider: 'agentcore' },
      { role: 'assistant', content: 'ans', ts: 200 },
      { role: 'assistant', content: 'ans2', ts: 500 },
    ];
    const { el, app } = mountList({
      activeMessages: messages,
      approvals: [{ key: 'ap1', id: 'ap1', status: 'pending', title: 't', createdAt: 210 }],
      toolAsks: [{ key: 'ta1', interactionId: 'i1', status: 'pending', options: [{ id: 'x', label: 'X' }], createdAt: 210 }],
    });
    const seq = sequence(el, ['ans', 'ans2']);
    expect(seq.indexOf('chat-approval-card')).toBeGreaterThan(0);
    expect(seq.indexOf('chat-option-card')).toBeGreaterThan(0);
    // 两卡都在首条消息之后(内联),而非仅在列表尾才能看到
    expect(seq.indexOf('chat-approval-card')).toBeLessThan(seq.length);
    cleanup(app, el);
  });

  it('无 createdAt 的存量卡退化为列表尾(不丢卡)', () => {
    const messages = [
      { role: 'user', content: 'q', ts: 100, messageId: 'm1', provider: 'agentcore' },
    ];
    const { el, app } = mountList({
      activeMessages: messages,
      optionCards: [{ key: 'k9', optionId: 'o9', status: 'pending', options: [{ id: 'a', label: 'A' }] }],
    });
    const seq = sequence(el, ['q']);
    expect(seq[seq.length - 1]).toBe('chat-option-card');
    cleanup(app, el);
  });

  it('卡对象引用传递:原对象突变直接驱动终态渲染(防spread副本回归)', async () => {
    // respondOption/respondApproval 靠对象引用突变 pending→sending→answered
    // 驱动终态渲染;时间线卡条目若经 spread 变成副本,全部状态突变会落在副本
    // 上(作答终态永不显示、防重复提交失效)。此用例钉死引用链完整。
    const card = reactive({
      key: 'kr', optionId: 'or', status: 'pending',
      options: [{ id: 'a', label: 'A' }], createdAt: 200,
    });
    const messages = [
      { role: 'user', content: 'q', ts: 100, messageId: 'm1', provider: 'agentcore' },
      { role: 'assistant', content: 'ans', ts: 200 },
    ];
    const { el, app } = mountList({ activeMessages: messages, optionCards: [card] });
    expect(el.textContent).toContain('chat_optionSubmit'); // pending 形态
    card.status = 'answered';
    card.selected = ['a'];
    await nextTick();
    expect(el.textContent).toContain('chat_optionSelected'); // 突变可见=引用链完整
    expect(el.textContent).not.toContain('chat_optionSubmit');
    cleanup(app, el);
  });
});
