// 症状1渲染族修复（2026-11-26）行为面钉死：
// ①error 泡保持纯文本插值（防注入铁律不破——不产任何 markdown 标签），但
//   aliang-options 围栏经 stripAliangOptionsForPlainText 纯字符串剥离，不再
//   裸漏方案块 JSON；多行错误文案随容器 whitespace-pre-wrap 保留换行
//   （此前与 user 泡/系统条不同口径，换行塌缩成一行）。
// ②eyebrow 时间戳支持 store 真实形态（epoch 毫秒数）：Date.parse(数字)=NaN
//   曾使相对时间恒为空串、hover 无 title——生产环境时间戳从不显示的根因。
import { afterEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import ChatMessageBubble from './ChatMessageBubble.vue';

const mountedApps = [];

function mountBubble(msg, extra = {}) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(ChatMessageBubble, { msg, nowTick: Date.now(), recallEnabled: false, ...extra }),
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
}

const errorBubble = (el) => el.querySelector('.rounded-2xl.border-rose-200');

describe('ChatMessageBubble error 泡纯文本渲染（症状1渲染族）', () => {
  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
  });

  it('error 泡剔除 aliang-options 围栏：正文保留、JSON 与 marker 不裸漏', () => {
    const content = [
      '已排查到一半：定位到模块 X 损坏。',
      '',
      '```aliang-options',
      '{"title":"修复方案","options":[{"id":"a","label":"方案A"},{"id":"b","label":"方案B"}]}',
      '```',
    ].join('\n');
    const el = mountBubble({ role: 'assistant', status: 'error', content, ts: Date.now() });
    const bubble = errorBubble(el);
    expect(bubble).toBeTruthy();
    expect(bubble.textContent).toContain('已排查到一半：定位到模块 X 损坏。');
    expect(bubble.textContent).not.toContain('aliang-options');
    expect(bubble.textContent).not.toContain('方案A');
  });

  it('error 泡流式半截围栏（截不出 JSON）整段截除，不裸漏 marker 与半截 JSON', () => {
    const content = ['进度叙述…', '```aliang-options', '{"title":"t","options":[{"id":"a",'].join('\n');
    const el = mountBubble({ role: 'assistant', status: 'error', content, ts: Date.now() });
    const bubble = errorBubble(el);
    expect(bubble.textContent).toContain('进度叙述…');
    expect(bubble.textContent).not.toContain('aliang-options');
    expect(bubble.textContent).not.toContain('"options"');
  });

  it('防注入铁律回归：error 泡围栏剥离后仍是纯文本插值，markdown 语法不渲染为标签', () => {
    const content = '**加粗** <img src=x onerror=alert(1)> ```aliang-options\n{"title":"t","options":[{"id":"a","label":"方案A"}]}\n```';
    const el = mountBubble({ role: 'assistant', status: 'error', content, ts: Date.now() });
    const bubble = errorBubble(el);
    expect(bubble.querySelector('strong')).toBeNull();
    expect(bubble.querySelector('img')).toBeNull();
    // 剥离只针对围栏结构：非围栏 markdown 语法保持字面文本
    expect(bubble.textContent).toContain('**加粗**');
  });

  it('error 泡多行文案保留换行（容器挂 whitespace-pre-wrap，jsdom 不解析 Tailwind 故查类名）', () => {
    const content = '第一行原因\n第二行原因';
    const el = mountBubble({ role: 'assistant', status: 'error', content, ts: Date.now() });
    const bubble = errorBubble(el);
    expect(bubble.classList.contains('whitespace-pre-wrap')).toBe(true);
    expect(bubble.textContent).toContain('\n');
    // 对照：done 泡容器不带 pre-wrap（其内容由 ChatMarkdown 的 whitespace-normal 根接管）
    const done = mountBubble({ role: 'assistant', status: 'done', content: '正文', ts: Date.now() });
    const doneBubble = done.querySelector('.rounded-2xl.border');
    expect(doneBubble.classList.contains('whitespace-pre-wrap')).toBe(false);
  });

  it('done 泡照常走 markdown 管线（围栏仍被 ChatMarkdown 剥离为静态卡，不经纯文本分支）', () => {
    const content = ['正文说明', '```aliang-options', '{"title":"方案","options":[{"id":"a","label":"方案A"}]}', '```'].join('\n');
    const el = mountBubble({ role: 'assistant', status: 'done', content, ts: Date.now() });
    // done 泡无红壳容器；正文经 ChatMarkdown 渲染为标签（p 元素存在=走了 markdown）
    expect(errorBubble(el)).toBeNull();
    expect(el.querySelector('.chat-md')).toBeTruthy();
    expect(el.textContent).toContain('正文说明');
    expect(el.textContent).not.toContain('aliang-options');
  });

  it('eyebrow 时间戳：epoch 毫秒数 ts 渲染相对时间与 hover title（数字形态回归）', () => {
    const el = mountBubble({ role: 'assistant', status: 'done', content: '回答', ts: Date.now() });
    const spans = el.querySelectorAll('span');
    const timeSpan = Array.from(spans).find((s) => s.textContent === 'chat_justNow');
    expect(timeSpan).toBeTruthy();
    expect(timeSpan.getAttribute('title')).not.toBe('');
  });

  it('eyebrow 时间戳：ISO 字符串 ts 兼容（旧样例形态零回归）', () => {
    const el = mountBubble({ role: 'assistant', status: 'done', content: '回答', ts: new Date(Date.now() - 3 * 60 * 1000).toISOString() });
    expect(Array.from(el.querySelectorAll('span')).some((s) => s.textContent.includes('chat_minutesAgo'))).toBe(true);
  });
});
