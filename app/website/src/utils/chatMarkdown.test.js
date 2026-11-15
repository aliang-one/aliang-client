import { describe, it, expect } from 'vitest';
import { renderMarkdownSafe, normalizeCjkMarkdown, extractAliangOptions, stripAliangOptions } from './chatMarkdown';

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

describe('stripAliangOptions', () => {
  it('带围栏剔除:围栏消失、正文保留、trim', () => {
    const src = '前言\n```aliang-options\n{"options":[{"id":"a","label":"A"}]}\n```\n后记';
    // 围栏整块移除,前后文各保留一个换行(段间空行);trim 只去首尾空白
    expect(stripAliangOptions(src)).toBe('前言\n\n后记');
  });
  it('恒等性钉死:无围栏时仅 trim,无其他副作用', () => {
    expect(stripAliangOptions('hello ')).toBe('hello');
  });
});

// 2026-11-15 本机 journal 实锤(GLM 偶发退化,4/34≈12%):黏行开头 + 闭合围栏
// 缺失,JSON 完整写到 } 就收尾(一次被字面 "..." 顶替闭合行)。原实现三道闸
// (后端提取/前端提取/前端剔除)全要求精确闭合 → 卡不出 + 裸 JSON 露在气泡里。
describe('退化形态容错:闭合围栏缺失', () => {
  it('JSON 到文本末尾仍提取出卡(黏行开头形态)', () => {
    const src = '排查完成，修复路径你来定：```aliang-options\n'
      + '{"v":2,"title":"选择修复方案","options":[{"id":"a","label":"方案A"}],"allow_custom":true,"multi":false}';
    const blocks = extractAliangOptions(src);
    expect(blocks).toHaveLength(1);
    expect(blocks[0].title).toBe('选择修复方案');
    expect(blocks[0].options[0].id).toBe('a');
  });
  it('JSON 后跟字面省略号:按括号配平截出 JSON', () => {
    const src = '你来定：```aliang-options\n{"options":[{"id":"a","label":"A"}]}...';
    expect(extractAliangOptions(src)).toHaveLength(1);
  });
  it('剔除只吞围栏+JSON,其后正文保留', () => {
    const src = '前言\n```aliang-options\n{"options":[{"id":"a","label":"A"}]}\n好的，等你选择';
    const stripped = stripAliangOptions(src);
    expect(stripped).not.toContain('aliang-options');
    expect(stripped).toContain('好的，等你选择');
  });
  it('闭合缺失且整体不可解析:维持旧行为,不产卡不吞文', () => {
    const src = '你来定：```aliang-options\nnot-json-at-all';
    expect(extractAliangOptions(src)).toHaveLength(0);
    expect(stripAliangOptions(src)).toContain('not-json-at-all');
  });
  it('闭合在场时行为逐字节不变(回归钉死)', () => {
    const closed = '前言\n```aliang-options\n{"options":[{"id":"a","label":"A"}]}\n```\n后记';
    expect(extractAliangOptions(closed)).toHaveLength(1);
    expect(stripAliangOptions(closed)).toBe('前言\n\n后记');
  });
});
