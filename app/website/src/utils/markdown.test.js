// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import { renderMarkdown } from './markdown.js';

describe('renderMarkdown(dashboard 聊天气泡)', () => {
  it('渲染标题/加粗/表格/代码为 HTML', () => {
    const html = renderMarkdown(
      '# 标题\n\n**加粗** 文本\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n`code`',
    );
    expect(html).toContain('<h1>');
    expect(html).toContain('<strong>');
    expect(html).toContain('<table>');
    expect(html).toContain('<code>');
  });

  it('消毒模型输出中的注入面(script/onerror/iframe)', () => {
    const html = renderMarkdown(
      'hello <script>alert(1)</script> <img src=x onerror=alert(1)> <iframe src="evil"></iframe>',
    );
    expect(html).not.toContain('<script');
    expect(html).not.toContain('onerror');
    expect(html).not.toContain('<iframe');
    expect(html).toContain('hello');
  });

  it('链接补 target=_blank + rel=noopener', () => {
    const html = renderMarkdown('[点我](https://example.com)');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener noreferrer"');
  });

  it('空/非字符串输入返回空串', () => {
    expect(renderMarkdown('')).toBe('');
    expect(renderMarkdown(null)).toBe('');
    expect(renderMarkdown(undefined)).toBe('');
  });
});
