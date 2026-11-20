// 网页聊天审计三批 M6 接线：markdown 图片异步加载撑高内容后的回锚——
// ChatMessageList 消息容器挂 chat-md-img-load 监听，贴底时回底（不拽上翻
// 用户）；非贴底不拽（守卫交还）。
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
vi.mock('../../composables/useAliangChat', () => ({
  isComposingEvent: () => false,
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
};

function mountList(messages, { nearBottom = true, scrollToBottom } = {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(ChatMessageList, {
      ...baseProps,
      activeMessages: messages,
      nearBottom,
      scrollToBottom: scrollToBottom || (() => {}),
    }),
  });
  app.mount(el);
  return { el, app };
}

function cleanup(app, el) {
  app.unmount();
  el.remove();
}

function imgMessage() {
  return [
    { role: 'user', content: 'q', status: 'done', ts: 1, messageId: 'm-u', provider: 'agentcore' },
    { role: 'assistant', content: '![图](/api/agent/ai/attachment?ref=x.png)', status: 'done', ts: 2 },
  ];
}

function findScrollHost(el) {
  // ChatMessageList 模板里挂 @scroll/@chat-md-img-load 的消息滚动容器：
  // 以「同时有 scroll 监听语义」定位——直接找含气泡的最外层 div（组件根）。
  return el.firstElementChild;
}

describe('M6 图片加载回锚接线', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it('贴底时图片 load：scrollToBottom 被调（回锚）', async () => {
    const scrollToBottom = vi.fn();
    const { el, app } = mountList(imgMessage(), { nearBottom: true, scrollToBottom });
    await nextTick();
    const img = el.querySelector('img');
    expect(img).toBeTruthy();
    img.dispatchEvent(new Event('load'));
    await nextTick();
    expect(scrollToBottom).toHaveBeenCalled();
    cleanup(app, el);
  });

  it('非贴底时图片 load：不拽底（上翻阅读不被劫持）', async () => {
    const scrollToBottom = vi.fn();
    const { el, app } = mountList(imgMessage(), { nearBottom: false, scrollToBottom });
    await nextTick();
    const img = el.querySelector('img');
    img.dispatchEvent(new Event('load'));
    await nextTick();
    expect(scrollToBottom).not.toHaveBeenCalled();
    cleanup(app, el);
  });
});
