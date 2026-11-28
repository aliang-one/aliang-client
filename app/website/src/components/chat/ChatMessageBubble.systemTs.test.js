// 系统条时间戳（2026-11-28 三审 F5）：kind==='system' 的弱化胶囊此前只渲染
// content——时间 eyebrow 行只在普通气泡分支，approval_decided/run_notice 等
// 系统条全部无时间（数据侧 deviceSystemNoteBubble 的 ts 全链已在且跨恢复
// 存活，纯展示缺口）。修复=胶囊内补 muted 时间小字（相对时间+title 绝对
// 时间），ts 缺失/非有限数不渲染，同分支全族生效。
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import ChatMessageBubble from './ChatMessageBubble.vue';

const mountedApps = [];

function mountBubble(msg) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(ChatMessageBubble, { msg }) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
}

describe('ChatMessageBubble 系统条时间戳', () => {
  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
  });

  it('系统条渲染相对时间且 title 带绝对时间', () => {
    const el = mountBubble({
      role: 'assistant', kind: 'system', note: 'approval_decided',
      content: '已选择：允许', ts: Date.now() - 120000,
    });
    const pill = el.querySelector('[data-testid="chat-approval-cancelled-note"]');
    expect(pill).toBeTruthy();
    expect(pill.textContent).toContain('已选择：允许');
    expect(pill.textContent).toContain('chat_minutesAgo');
    const timed = pill.querySelector('[title]');
    expect(timed).toBeTruthy();
    expect(timed.getAttribute('title')).not.toBe('');
  });

  it('turn_failed 失败条同族生效', () => {
    const el = mountBubble({
      role: 'assistant', kind: 'system', note: 'turn_failed',
      content: '上游 403', ts: Date.now() - 30000,
    });
    const pill = el.querySelector('[data-testid="chat-turn-failed-note"]');
    expect(pill).toBeTruthy();
    expect(pill.textContent).toContain('chat_justNow');
  });

  it('ts 缺失时不渲染时间元素（不出现空 title/NaN）', () => {
    const el = mountBubble({
      role: 'assistant', kind: 'system', note: 'run_notice',
      content: '运行通知',
    });
    const pill = el.querySelector('[data-testid="chat-run-notice"]');
    expect(pill).toBeTruthy();
    expect(pill.querySelector('[title]')).toBeNull();
    expect(pill.textContent).not.toContain('NaN');
  });
});
