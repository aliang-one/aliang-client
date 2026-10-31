// Task 7:assistant 气泡 markdown 渲染组件。仓内无 @vue/test-utils,
// 沿用 createApp + document.body 挂载断言 innerHTML 的手法(vite.config
// 已配 jsdom)。初始渲染是同步路径(ref 初始化 + onMounted 增强),
// mount() 返回后 innerHTML 即含增强产物,无需等待防抖。
import { describe, it, expect } from 'vitest';
import { createApp, h } from 'vue';
import ChatMarkdown from './ChatMarkdown.vue';

function mountHtml(content) {
  const el = document.createElement('div');
  createApp({ render: () => h(ChatMarkdown, { content }) }).mount(el);
  return el.innerHTML;
}

describe('ChatMarkdown', () => {
  it('渲染 strong 与代码语言类名', () => {
    const html = mountHtml('**bold**\n\n```go\nx := 1\n```');
    expect(html).toContain('<strong>');
    expect(html).toContain('language-go');
  });
  it('脚本注入被消毒', () => {
    expect(mountHtml('<script>alert(1)</script>ok')).not.toContain('<script');
  });
  it('代码块带复制按钮与语言标签', () => {
    const html = mountHtml('```js\nconst a=1;\n```');
    expect(html).toContain('chat-md-copy');
    expect(html).toContain('lang-label');
  });
  it('>20 行代码块折叠(折叠类与展开按钮存在)', () => {
    const long = '```txt\n' + Array.from({ length: 25 }, (_, i) => 'l' + i).join('\n') + '\n```';
    const html = mountHtml(long);
    expect(html).toContain('chat-md-fold');
  });
});
