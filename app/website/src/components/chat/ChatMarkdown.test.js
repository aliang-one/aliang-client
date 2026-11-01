// Task 7:assistant 气泡 markdown 渲染组件。仓内无 @vue/test-utils,
// 沿用 createApp + document.body 挂载断言 innerHTML 的手法(vite.config
// 已配 jsdom)。初始渲染是同步路径(ref 初始化 + onMounted 增强),
// mount() 返回后 innerHTML 即含增强产物,无需等待防抖。
import { describe, it, expect, vi } from 'vitest';
import { createApp, h } from 'vue';
import ChatMarkdown from './ChatMarkdown.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

function mountHtml(content) {
  const el = document.createElement('div');
  createApp({ render: () => h(ChatMarkdown, { content }) }).mount(el);
  return el.innerHTML;
}

// Task 14：静态围栏卡挂载（可带 hiddenOptionIds）。初始渲染同步（正文 ref +
// 静态卡 computed 都不走节流），mount() 后即可断言。
function mountHtmlWith(content, props = {}) {
  const el = document.createElement('div');
  createApp({ render: () => h(ChatMarkdown, { content, ...props }) }).mount(el);
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
  // C1 回归:回调内定时器变量曾遮蔽 i18n 的 t(TDZ ReferenceError),
  // 复制反馈从未生效。jsdom 无 navigator.clipboard,显式置 undefined
  // 强制走 execCommand 兜底路径;文案期望值经 useI18n().t 动态取,
  // 不依赖测试环境 locale。
  it('点击复制按钮:文案变「已复制」并在定时后还原', async () => {
    vi.useFakeTimers();
    try {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true });
      const el = document.createElement('div');
      const app = createApp({ render: () => h(ChatMarkdown, { content: '```go\nx := 1\n```' }) });
      app.mount(el);
      const btn = el.querySelector('.chat-md-copy');
      expect(btn.textContent).toBe(t('chat_md_copy'));
      btn.click();
      await Promise.resolve();
      await Promise.resolve();
      expect(btn.textContent).toBe(t('chat_md_copied'));
      vi.advanceTimersByTime(1500);
      expect(btn.textContent).toBe(t('chat_md_copy'));
      app.unmount();
    } finally {
      vi.useRealTimers();
    }
  });
});

// Task 14:aliang-options 围栏 → 静态只读卡。防双份正确流钉死:正文渲染
// 永远先 strip 围栏(原文块不出现),hiddenOptionIds 只控静态卡显隐。
describe('ChatMarkdown aliang-options 静态卡', () => {
  const FENCE = '正文段\n\n```aliang-options\n' + JSON.stringify({
    option_id: 'op1',
    title: '上线方案',
    options: [
      { id: 'a', label: '滚动升级' },
      { id: 'b', label: '全量切换' },
    ],
  }) + '\n```\n';

  it('围栏渲染静态卡且正文剔除围栏原文', () => {
    const html = mountHtml(FENCE);
    expect(html).toContain('data-testid="chat-option-static"');
    expect(html).toContain('滚动升级');
    expect(html).toContain('全量切换');
    expect(html).toContain('正文段');
    // 围栏原文/代码块不再出现在 v-html 正文(永远 strip,与交互卡是否在场无关)
    expect(html).not.toContain('aliang-options');
  });

  it('hiddenOptionIds 命中 option_id → 静态卡隐藏(交互卡在场防双份)', () => {
    const html = mountHtmlWith(FENCE, { hiddenOptionIds: ['op1'] });
    expect(html).not.toContain('data-testid="chat-option-static"');
    expect(html).toContain('正文段'); // 正文不受隐藏比对影响
  });

  it('无 option_id 的块不参与隐藏比对,照渲染静态卡', () => {
    const noId = '```aliang-options\n' + JSON.stringify({
      options: [{ id: 'a', label: '滚动升级' }],
    }) + '\n```';
    const html = mountHtmlWith(noId, { hiddenOptionIds: ['zzz'] });
    expect(html).toContain('data-testid="chat-option-static"');
  });

  it('流式半截围栏(未闭合)不产静态卡,不白屏', () => {
    const html = mountHtml('```aliang-options\n{"partial"');
    expect(html).not.toContain('data-testid="chat-option-static"');
    // 未闭合围栏由 markdown-it 按代码块渲染后 enhance 隐藏(现状行为保留)
    expect(html).toContain('language-aliang-options');
  });
});
