// 网页聊天审计三批（2026-11-21，TODOS「审计二批遗留」Markdown 族 M2/M3/M4/M1-util）
// 行为面锁定。每条缺陷先红后绿：
//   M2 围栏跟踪与 CommonMark 不一致（任意 ```/~~~ 行一律翻转）：嵌套/更长
//      围栏/异字符交叉全部错位 → 代码内容被误插空格（复制按钮复制到坏代码）。
//      修复=CommonMark 规则跟踪器：开栏 0-3 空格+≥3 同字符（` 栏 info 不得
//      含 `），闭栏同字符同长度其后仅空白。
//   M3 行首负数/强调被 GLM 容错误插空格：『-5°C』变列表项、『*注意*x』强调
//      被拆坏。修复=-/+ 后跟数字不插；* 行内还有第二个 * 不作列表。
//   M4 scanAliangOptionSpans 不识别嵌套围栏上下文：示例/文档代码块里的
//      ```aliang-options 被误当真实选项卡剥离。修复=同款跟踪器，围栏内的
//      options 开栏视为内容。
//   M1-util 大内容自适应渲染间隔：chatMdRenderInterval(len) 分档。
import { describe, it, expect } from 'vitest';
import {
  normalizeCjkMarkdown,
  extractAliangOptions,
  stripAliangOptions,
  chatMdRenderInterval,
} from './chatMarkdown';

describe('M2 围栏跟踪（CommonMark 规则）', () => {
  it('四反引号围栏内的 ``` 行不再翻转：代码内容不被误插空格', () => {
    const src = [
      '````txt',
      '-待办一',
      '```go',
      '-待办二',
      '````',
      '-正文待办',
    ].join('\n');
    const out = normalizeCjkMarkdown(src);
    // 围栏内两行原样（旧实现第一个 ``` 行错误闭栏，后面的 - 行被插空格）
    expect(out).toContain('-待办一');
    expect(out).toContain('-待办二');
    expect(out).toContain('```go');
    // 正文行照常容错
    expect(out.split('\n').at(-1)).toBe('- 正文待办');
  });

  it('波浪线围栏内的反引号围栏是内容：不交叉闭栏', () => {
    const src = [
      '~~~txt',
      '```js',
      '-围栏内',
      '~~~',
      '-正文',
    ].join('\n');
    const out = normalizeCjkMarkdown(src);
    expect(out).toContain('-围栏内');
    expect(out.split('\n').at(-1)).toBe('- 正文');
  });

  it('4 空格缩进的 ``` 不是围栏（CommonMark ≤3 空格）', () => {
    const src = [
      '段落',
      '    ```',
      '-缩进后的正文仍按正文容错',
    ].join('\n');
    const out = normalizeCjkMarkdown(src);
    expect(out).toContain('- 缩进后的正文仍按正文容错');
  });

  it('常规围栏开闭不受影响（回归）', () => {
    const src = '```go\n-围栏内\n```\n-正文外';
    const out = normalizeCjkMarkdown(src);
    expect(out).toContain('-围栏内');
    expect(out.split('\n').at(-1)).toBe('- 正文外');
  });
});

describe('M3 行首负数/强调不误插空格', () => {
  it('『-5°C』保持原样（负数不是列表）', () => {
    expect(normalizeCjkMarkdown('-5°C 的天气')).toBe('-5°C 的天气');
  });

  it('『+5』同理', () => {
    expect(normalizeCjkMarkdown('+5 分')).toBe('+5 分');
  });

  it('『-待办』照常补空格（CJK 列表容错保留）', () => {
    expect(normalizeCjkMarkdown('-待办事项')).toBe('- 待办事项');
  });

  it('『*注意*xxx』保持强调（行内有第二个 * 不作列表）', () => {
    expect(normalizeCjkMarkdown('*注意*这是强调')).toBe('*注意*这是强调');
  });

  it('『*无闭合项』照常按列表补空格', () => {
    expect(normalizeCjkMarkdown('*无闭合项')).toBe('* 无闭合项');
  });

  it('『#标题』照常补空格（回归）', () => {
    expect(normalizeCjkMarkdown('#标题')).toBe('# 标题');
  });
});

describe('M4 aliang-options 嵌套围栏不误吞', () => {
  const sampleJson = '{"title":"示例","options":[{"id":"a","label":"甲"}]}';

  it('四反引号示例块里的 ```aliang-options 不产卡、原文保留', () => {
    const src = [
      '````md',
      '```aliang-options',
      sampleJson,
      '```',
      '````',
      '正文',
    ].join('\n');
    expect(extractAliangOptions(src)).toEqual([]);
    expect(stripAliangOptions(src)).toContain('```aliang-options');
  });

  it('波浪线示例块同口径', () => {
    const src = [
      '~~~md',
      '```aliang-options',
      sampleJson,
      '```',
      '~~~',
      '正文',
    ].join('\n');
    expect(extractAliangOptions(src)).toEqual([]);
    expect(stripAliangOptions(src)).toContain('```aliang-options');
  });

  it('真实顶层 aliang-options 照常产卡+剔除（回归）', () => {
    const src = ['```aliang-options', sampleJson, '```', '正文'].join('\n');
    const cards = extractAliangOptions(src);
    expect(cards).toHaveLength(1);
    expect(cards[0].title).toBe('示例');
    const stripped = stripAliangOptions(src);
    expect(stripped).not.toContain('aliang-options');
    expect(stripped).toContain('正文');
  });
});

describe('M1-util 大内容自适应渲染间隔', () => {
  it('小内容 80ms（既有基线）', () => {
    expect(chatMdRenderInterval(1000)).toBe(80);
    expect(chatMdRenderInterval(39999)).toBe(80);
  });
  it('40KB 档 300ms', () => {
    expect(chatMdRenderInterval(40000)).toBe(300);
    expect(chatMdRenderInterval(119999)).toBe(300);
  });
  it('120KB 档 800ms', () => {
    expect(chatMdRenderInterval(120000)).toBe(800);
    expect(chatMdRenderInterval(300000)).toBe(800);
  });
});
