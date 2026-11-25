import { afterEach, describe, expect, it, vi } from 'vitest';

// 钉死 user 气泡复制链路:此前 copyMessage 用可选链裸探 navigator.clipboard,
// http 局域网直连(56431 无 TLS)下 clipboard 不存在,复制静默无效(与
// ChatMarkdown 已有 execCommand 兜底不同口径)。现统一走 utils/clipboard 的
// copyText 兜底;本文件防回退。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import ChatMessageBubble from './ChatMessageBubble.vue';

const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function stubClipboard(value) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true });
}

function stubExecCommand(impl) {
  Object.defineProperty(document, 'execCommand', { value: impl, configurable: true });
}

function mountBubble(msg) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(ChatMessageBubble, { msg }) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
}

const copyBtn = (el) => el.querySelector('button[aria-label="chat_copyMessage"]');

describe('ChatMessageBubble 复制链路', () => {
  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
    try { delete navigator.clipboard; } catch (_) { /* 已不存在 */ }
    try { delete document.execCommand; } catch (_) { /* 已不存在 */ }
  });

  it('回归:http 场景(navigator.clipboard=undefined)点复制走 execCommand 兜底,不再静默无效', async () => {
    stubClipboard(undefined);
    const execCommand = vi.fn(() => true);
    stubExecCommand(execCommand);
    const el = mountBubble({ role: 'user', content: 'bubble text', ts: '2026-11-16T00:00:00Z' });
    const btn = copyBtn(el);
    expect(btn).toBeTruthy();
    btn.click();
    await flush();
    expect(execCommand).toHaveBeenCalledWith('copy');
  });

  it('安全上下文:clipboard.writeText 存在时走 API', async () => {
    const writeText = vi.fn().mockResolvedValue();
    stubClipboard({ writeText });
    const el = mountBubble({ role: 'user', content: 'bubble text', ts: '2026-11-16T00:00:00Z' });
    copyBtn(el).click();
    await flush();
    expect(writeText).toHaveBeenCalledWith('bubble text');
  });

  it('空文本不动作(不写剪贴板不触发兜底)', async () => {
    stubClipboard(undefined);
    const execCommand = vi.fn(() => true);
    stubExecCommand(execCommand);
    const el = mountBubble({ role: 'user', content: '', ts: '2026-11-16T00:00:00Z' });
    copyBtn(el).click();
    await flush();
    expect(execCommand).not.toHaveBeenCalled();
  });

  it('直接显示回合失败原因，并保持上游文本为纯文本', () => {
    const content = '404 model_not_found\n<example>不支持该模型</example>';
    const el = mountBubble({ role: 'assistant', kind: 'system', note: 'turn_failed', content });
    const note = el.querySelector('[data-testid="chat-turn-failed-note"]');
    expect(note.getAttribute('role')).toBe('status');
    expect(note.textContent).toBe(content);
    expect(note.querySelector('example')).toBeNull();
  });
});
