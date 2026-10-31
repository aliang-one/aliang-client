import { describe, it, expect } from 'vitest';
import { renderMarkdownSafe, normalizeCjkMarkdown, extractAliangOptions } from './chatMarkdown';

describe('normalizeCjkMarkdown', () => {
  it('补齐 #/- 后缺空格的标题与列表', () => {
    expect(normalizeCjkMarkdown('#标题')).toBe('# 标题');
    expect(normalizeCjkMarkdown('-项目')).toBe('- 项目');
    expect(normalizeCjkMarkdown('##二级')).toBe('## 二级');
  });
  it('不动代码围栏内的内容', () => {
    const src = '```\n#not-a-heading\n```';
    expect(normalizeCjkMarkdown(src)).toBe(src);
  });
  it('不动正常 markdown', () => {
    expect(normalizeCjkMarkdown('# 已有空格')).toBe('# 已有空格');
  });
});

describe('renderMarkdownSafe', () => {
  it('渲染标题/列表/代码块并保留语言类名', () => {
    const html = renderMarkdownSafe('## 标题\n\n```go\nfmt.Println(1)\n```');
    expect(html).toContain('<h2>');
    expect(html).toContain('language-go');
  });
  it('消毒脚本注入', () => {
    const html = renderMarkdownSafe('<script>alert(1)</script>**bold**');
    expect(html).not.toContain('<script');
    expect(html).toContain('<strong>');
  });
  it('链接带 target=_blank rel=noopener', () => {
    const html = renderMarkdownSafe('[x](https://a.b)');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('rel="noopener"');
  });
  it('未闭合围栏按代码处理不抛错', () => {
    expect(() => renderMarkdownSafe('```js\nconst a = 1;')).not.toThrow();
  });
});

describe('extractAliangOptions', () => {
  it('提取围栏 JSON 并剔除正文', () => {
    const src = '前言\n```aliang-options\n{"option_id":"op1","options":[{"id":"a","label":"A"}]}\n```\n后记';
    const blocks = extractAliangOptions(src);
    expect(blocks).toHaveLength(1);
    expect(blocks[0].option_id).toBe('op1');
  });
  it('坏 JSON 块安全跳过', () => {
    expect(extractAliangOptions('```aliang-options\nnot-json\n```')).toHaveLength(0);
  });
});
