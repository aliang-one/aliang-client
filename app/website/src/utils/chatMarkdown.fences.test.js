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

  it('列表项 4 空格/Tab 缩进围栏内的代码受保护（评审 blocker：markdown-it 实测渲染为真实代码块）', () => {
    const src = [
      '- 配置项：',
      '    ```c',
      '#include <stdio.h>',
      '*args = 1;',
      '    ```',
      '- 正文待办',
    ].join('\n');
    const out = normalizeCjkMarkdown(src);
    expect(out).toContain('#include <stdio.h>');
    expect(out).toContain('*args = 1;');
    expect(out.split('\n').at(-1)).toBe('- 正文待办');
  });

  it('Tab 缩进围栏同口径', () => {
    const src = ['- 项：', '\t```bash', '\t#!/bin/sh', '\t```'].join('\n');
    const out = normalizeCjkMarkdown(src);
    expect(out).toContain('#!/bin/sh');
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

describe('M4 评审修订：缩进不对称收敛 + strip JSON 闸门', () => {
  const sampleJson = '{"title":"示例","options":[{"id":"a","label":"甲"}]}';

  it('≥4 空格纯缩进示例不进 span：不产卡、原文逐字节保留', () => {
    const src = [
      '- 用法示例：',
      '    ```aliang-options',
      '    ' + sampleJson,
      '    ```',
      '正文',
    ].join('\n');
    expect(extractAliangOptions(src)).toEqual([]);
    const stripped = stripAliangOptions(src);
    expect(stripped).toContain('```aliang-options');
    expect(stripped).toContain('正文');
  });

  it('散文前缀黏行形态（GLM 实锤）照常出卡（回归钉：删黏行分支后 strict 须承接）', () => {
    const src = '排查完成，修复路径你来定：```aliang-options\n' + sampleJson;
    const cards = extractAliangOptions(src);
    expect(cards).toHaveLength(1);
    expect(cards[0].title).toBe('示例');
  });

  it('退化形态+后续带 info 围栏行（无 prose）：body 停在 JSON 末尾照常出卡', () => {
    const src = [
      '```aliang-options',
      sampleJson,
      '```python',
      'print(1)',
      '```',
    ].join('\n');
    const cards = extractAliangOptions(src);
    expect(cards).toHaveLength(1);
    expect(cards[0].title).toBe('示例');
  });

  it('退化形态 JSON 与围栏行之间夹 prose：不产卡且 strip 不吞正文（JSON 闸门）', () => {
    const src = [
      '```aliang-options',
      sampleJson,
      '……（模型省略闭合行）',
      '```python',
      'print(1)',
      '```',
    ].join('\n');
    expect(extractAliangOptions(src)).toEqual([]);
    const stripped = stripAliangOptions(src);
    expect(stripped).toContain('```python');
    expect(stripped).toContain('模型省略闭合行');
  });

  it('strip JSON 闸门：坏 body（JSON 后跟 prose）不参与剔除、正文保留', () => {
    const src = [
      '```aliang-options',
      '{"options":[{"id":"a","label":"A"}]}',
      '这行 prose 让 body 非纯 JSON',
      '```',
      '正文保留',
    ].join('\n');
    expect(extractAliangOptions(src)).toEqual([]); // extract 既有口径：丢坏块
    const stripped = stripAliangOptions(src);
    expect(stripped).toContain('正文保留');
    expect(stripped).toContain('这行 prose');
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

describe('aliang-options 四反引号围栏（2026-11-28 线上漂移）', () => {
  // 模型偶尔把围栏写成 4 反引号。RN 与服务端的整段正则只认 3 个,会把第 4 个
  // 反引号留在原处,JSON 转成审批标签后整段被 markdown 当成代码块外晒。
  // 网页端是按行扫描:indexOf 从第 2 个反引号起匹配,开栏判定仍成立;闭合行
  // 的 `{3,} 也接受 4 个。本组锁死这条路径不被回退。
  const optionJson = JSON.stringify({
    v: 2,
    title: 'mermaid 支持的实现方案选型',
    summary: '请选择实施方向。',
    options: [
      { id: 'inplace-auto', label: '方案A：自动渲染', description: '约1-2天' },
      { id: 'spec-first', label: '先出设计文档', description: '评审后再实施' },
    ],
    allow_custom: true,
    multi: false,
  });

  it('extractAliangOptions 能从 4 反引号围栏里取出卡', () => {
    const src = '评估结论如下。\n\n````aliang-options\n' + optionJson + '\n````';
    const out = extractAliangOptions(src);
    expect(out).toHaveLength(1);
    expect(out[0].title).toBe('mermaid 支持的实现方案选型');
    expect(out[0].options).toHaveLength(2);
  });

  it('stripAliangOptions 剥掉整段,不把 JSON 留在正文', () => {
    const src = '前文。\n\n````aliang-options\n' + optionJson + '\n````\n\n后文。';
    const out = stripAliangOptions(src);
    expect(out).not.toContain('"options"');
    expect(out).not.toContain('inplace-auto');
    expect(out).toContain('前文。');
    expect(out).toContain('后文。');
    // 不留孤立反引号:start 必须回退到本行连续反引号的起点,否则第 1 个
    // 反引号会挂在卡前(4 反引号漂移的显示瑕疵)。
    expect(out).not.toContain('`');
  });

  it('3 反引号围栏仍然正常（回归护栏）', () => {
    const src = '```aliang-options\n' + optionJson + '\n```';
    expect(extractAliangOptions(src)).toHaveLength(1);
  });
});
