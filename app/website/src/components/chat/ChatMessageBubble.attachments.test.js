// 消息图片附件渲染测试（spec 2026-11-05-chat-image-pipeline §3.6）：挂载用
// createApp + jsdom 手法（仓内无 @vue/test-utils）。守两条铁律：①user 气泡
// 纯文本插值不进 markdown 管线（防注入）；②附件走兄弟 DOM 缩略图条（ref →
// 本机端点 URL），纯图消息（content 空）不渲染空文本节点。
import { describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import ChatMessageBubble from './ChatMessageBubble.vue';

const bubbleAttachments = [
  { name: 'photo.png', url: '/api/agent/ai/attachment?ref=s%2Fm-0.png' },
  { name: 'shot.jpg', url: '/api/agent/ai/attachment?ref=s%2Fm-1.jpg' },
];

function mountBubble(msg) {
  const el = document.createElement('div');
  const app = createApp({
    render: () => h(ChatMessageBubble, { msg, nowTick: Date.now(), recallEnabled: false }),
  });
  app.mount(el);
  return el;
}

describe('ChatMessageBubble 图片附件渲染', () => {
  it('user 消息带附件时渲染缩略图条（img 数=附件数，src=本机端点 URL）', () => {
    const el = mountBubble({ role: 'user', content: '看图', attachments: bubbleAttachments });
    const images = el.querySelectorAll('img');
    expect(images.length).toBe(2);
    expect(images[0].getAttribute('src')).toBe(bubbleAttachments[0].url);
    expect(el.textContent).toContain('看图');
  });

  it('纯图消息（content 空）只渲染缩略图条、无空文本节点', () => {
    const el = mountBubble({ role: 'user', content: '', attachments: bubbleAttachments });
    expect(el.querySelectorAll('img').length).toBe(2);
    // user 气泡文本节点不渲染（v-if content 守卫）
    expect(el.querySelectorAll('.bg-primary').length).toBe(0);
  });

  it('铁律：user 文本保持纯文本插值，markdown 语法不渲染为标签', () => {
    const el = mountBubble({ role: 'user', content: '**bold** ![x](/x.png)', attachments: bubbleAttachments });
    expect(el.querySelector('strong')).toBeNull();
    expect(el.textContent).toContain('**bold**');
  });

  it('assistant 消息带附件时同样渲染缩略图条', () => {
    const el = mountBubble({ role: 'assistant', content: '回答', status: 'done', attachments: [bubbleAttachments[0]] });
    expect(el.querySelectorAll('img').length).toBe(1);
    expect(el.textContent).toContain('回答');
  });

  it('无附件消息不渲染缩略图条（零回归）', () => {
    const el = mountBubble({ role: 'user', content: '纯文本' });
    expect(el.querySelectorAll('img').length).toBe(0);
  });
});

// chatAttachmentUrl：ref → 本机端点 URL（encodeURIComponent 防 ref 内特殊字符
// 破坏查询串）。挂 spec §3.6 的 URL 形状契约。
describe('chatAttachmentUrl', () => {
  it('ref 编码进 ref 查询参数', async () => {
    const { chatAttachmentUrl } = await import('../../composables/useAliangChat.js');
    expect(chatAttachmentUrl('s/m-0.jpg')).toBe(
      '/api/agent/ai/attachment?ref=' + encodeURIComponent('s/m-0.jpg')
    );
    expect(chatAttachmentUrl('')).toBe('');
  });
});
