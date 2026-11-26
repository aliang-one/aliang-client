// 症状1渲染族修复（2026-11-26）：错误气泡纯文本路径的 aliang-options 围栏
// 处置。done/running 泡的围栏在 markdown 管线里被 stripAliangOptions 剔除、
// 半截块由 ChatMarkdown enhance 隐藏；error 泡不走 markdown（防注入铁律），
// 此前整段裸漏原始 JSON（用户截图实锤：红泡正文末尾挂着方案块原文）。
// stripAliangOptionsForPlainText = 与 strip 同源剔合法块 + 半截围栏截尾兜底。
import { describe, it, expect } from 'vitest';
import {
  extractAliangOptions,
  stripAliangOptions,
  stripAliangOptionsForPlainText,
} from './chatMarkdown';

const goodJson = '{"title":"方案","options":[{"id":"a","label":"方案A"}]}';

describe('stripAliangOptionsForPlainText（error 泡纯文本剥离）', () => {
  it('合法闭合围栏整段剔除，前后正文保留', () => {
    const src = ['排查进度正常。', '', '```aliang-options', goodJson, '```', '后续说明'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).toContain('排查进度正常。');
    expect(out).toContain('后续说明');
    expect(out).not.toContain('aliang-options');
    expect(out).not.toContain('方案A');
  });

  it('流式半截围栏（无闭合、截不出 JSON）：从 marker 起截到文末', () => {
    const src = ['进度叙述…', '```aliang-options', '{"title":"方案","options":[{"id":"a",'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).toContain('进度叙述…');
    expect(out).not.toContain('aliang-options');
    expect(out).not.toContain('"options"');
  });

  it('散文黏行开栏保留散文前缀，只截 marker 起的流式尸体', () => {
    const src = '排查完成：```aliang-options\n{"title":"t","options":[{"id":"a"';
    expect(stripAliangOptionsForPlainText(src)).toBe('排查完成：');
  });

  it('闭合缺失但括号配平成功（退化形态）：剔到 JSON 末，其后正文保留', () => {
    const src = ['```aliang-options', goodJson, '收尾说明'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).not.toContain('aliang-options');
    expect(out).toContain('收尾说明');
  });

  it('≥4 缩进示例不吞（openOk=false 不触发半截截尾）', () => {
    const src = ['- 用法示例：', '    ```aliang-options', '    ' + goodJson, '    ```', '结尾'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).toContain('```aliang-options');
    expect(out).toContain('结尾');
  });

  it('嵌套示例块（四反引号围栏内）不吞', () => {
    const src = ['````md', '```aliang-options', goodJson, '```', '````', '结尾'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).toContain('```aliang-options');
    expect(out).toContain('结尾');
  });

  it('坏 body（闭合在场 JSON 后跟 prose）保留原文，与 done 泡 strip 口径一致', () => {
    const src = ['```aliang-options', goodJson, '这行 prose 让 body 非纯 JSON', '```', '正文保留'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).toContain('这行 prose');
    expect(out).toContain('正文保留');
  });

  it('合法块在前、半截围栏在后：两段都处理，中间正文保留', () => {
    const src = ['```aliang-options', goodJson, '```', '中间说明', '```aliang-options', '{"title"'].join('\n');
    const out = stripAliangOptionsForPlainText(src);
    expect(out).not.toContain('aliang-options');
    expect(out).toContain('中间说明');
  });

  it('无围栏正文原样保留（trim 语义与 strip 一致）', () => {
    expect(stripAliangOptionsForPlainText('  出错了：tool timeout  ')).toBe('出错了：tool timeout');
    expect(stripAliangOptionsForPlainText('')).toBe('');
  });

  it('非字符串输入安全（与 strip 同守卫）', () => {
    expect(stripAliangOptionsForPlainText(null)).toBe('');
    expect(stripAliangOptionsForPlainText(undefined)).toBe('');
    expect(stripAliangOptionsForPlainText(42)).toBe('');
  });

  it('回归：stripAliangOptions/extractAliangOptions 语义不受扫描器签名调整影响', () => {
    const src = ['```aliang-options', goodJson, '```', '正文'].join('\n');
    expect(extractAliangOptions(src)).toHaveLength(1);
    const stripped = stripAliangOptions(src);
    expect(stripped).not.toContain('aliang-options');
    expect(stripped).toContain('正文');
  });
});
