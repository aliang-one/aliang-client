// 聊天气泡 markdown 管线(spec §E):markdown-it 渲染 → highlight.js 子集
// 高亮 → DOMPurify 消毒。GLM 中文容错(行首 #/- 后缺空格)在渲染前 normalize,
// 代码围栏内容不受影响。本文件零 Vue 依赖,纯函数,vitest 直测。
import MarkdownIt from 'markdown-it';
import DOMPurify from 'dompurify';
import hljs from 'highlight.js/lib/core';
import javascript from 'highlight.js/lib/languages/javascript';
import typescript from 'highlight.js/lib/languages/typescript';
import go from 'highlight.js/lib/languages/go';
import json from 'highlight.js/lib/languages/json';
import bash from 'highlight.js/lib/languages/bash';
import yaml from 'highlight.js/lib/languages/yaml';
import python from 'highlight.js/lib/languages/python';
import sql from 'highlight.js/lib/languages/sql';
import xml from 'highlight.js/lib/languages/xml';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';

hljs.registerLanguage('javascript', javascript);
hljs.registerLanguage('typescript', typescript);
hljs.registerLanguage('js', javascript);
hljs.registerLanguage('ts', typescript);
hljs.registerLanguage('go', go);
hljs.registerLanguage('json', json);
hljs.registerLanguage('bash', bash);
hljs.registerLanguage('shell', bash);
hljs.registerLanguage('yaml', yaml);
hljs.registerLanguage('python', python);
hljs.registerLanguage('sql', sql);
hljs.registerLanguage('html', xml);
hljs.registerLanguage('xml', xml);
hljs.registerLanguage('css', css);
hljs.registerLanguage('diff', diff);

const md = new MarkdownIt({ html: false, linkify: true, highlight(code, lang) {
  if (lang && hljs.getLanguage(lang)) {
    try { return hljs.highlight(code, { language: lang }).value; } catch (_) { /* fallthrough */ }
  }
  return '';
} });

// 默认链接规则加 target/_rel(消毒白名单同步放行这两个属性)
const defaultRender = md.renderer.rules.link_open || ((tokens, idx, opts, _, self) => self.renderToken(tokens, idx, opts));
md.renderer.rules.link_open = (tokens, idx, opts, env, self) => {
  tokens[idx].attrSet('target', '_blank');
  tokens[idx].attrSet('rel', 'noopener');
  return defaultRender(tokens, idx, opts, env, self);
};

// GLM 中文容错第一批(spec §E):行首 #/-/+/* 后紧跟非空白非换行字符 → 补空格。
// 逐行扫描并跟踪 ``` 围栏开闭,围栏内不动。
export function normalizeCjkMarkdown(src) {
  if (typeof src !== 'string' || !src) return src || '';
  let fenced = false;
  return src.split('\n').map((line) => {
    if (/^\s*(```|~~~)/.test(line)) { fenced = !fenced; return line; }
    if (fenced) return line;
    // lookahead 排除 #*+- 防 ##/**/-- 连写被误插空格,勿简化成 [^\s]
    return line.replace(/^(\s*)(#{1,6}|[-+*])(?=[^\s#*+-])/y, (m, ind, mark) => `${ind}${mark} `);
  }).join('\n');
}

// 括号配平截取 s 中首个完整 JSON 对象(字符串感知,跳过引号内花括号/转义)。
// 截不出(未配平/无花括号)返回 null。
function aliangBalancedJSONObject(s) {
  const start = s.indexOf('{');
  if (start === -1) return null;
  let depth = 0;
  let inStr = false;
  let esc = false;
  for (let i = start; i < s.length; i++) {
    const ch = s[i];
    if (inStr) {
      if (esc) { esc = false; }
      else if (ch === '\\') { esc = true; }
      else if (ch === '"') { inStr = false; }
      continue;
    }
    if (ch === '"') { inStr = true; continue; }
    if (ch === '{') depth++;
    else if (ch === '}') {
      depth--;
      if (depth === 0) return s.slice(start, i + 1);
    }
  }
  return null;
}

// 扫描全部 aliang-options 围栏跨度 [{start,end,body}]。
// 闭合在场时语义与旧正则逐字节一致(opener 后首个 \n``` 收口);闭合缺失
// (模型退化形态:GLM 偶发漏闭合行,2026-11-15 本机 journal 实锤 4/34≈12%)
// 时兜底:取标签后剩余文本按括号配平截出 JSON——卡的合法性本就由 JSON.parse
// + options 非空校验把关,闭合行不是真闸门。标签后没有任何闭合行时不存在
// 后续闭合围栏,扫描随之终止;截不出 JSON(纯垃圾)不产跨度:不产卡、原文保留
// (维持旧行为,半截代码块由 ChatMarkdown enhance 兜底隐藏)。
function scanAliangOptionSpans(src) {
  const spans = [];
  const openRe = /```aliang-options\s*\n/g;
  let m;
  while ((m = openRe.exec(src)) !== null) {
    const start = m.index;
    const bodyStart = start + m[0].length;
    const closeAt = src.indexOf('\n```', bodyStart);
    if (closeAt !== -1) {
      const end = closeAt + '\n```'.length;
      spans.push({ start, end, body: src.slice(bodyStart, closeAt) });
      openRe.lastIndex = end;
      continue;
    }
    const rest = src.slice(bodyStart);
    const json = aliangBalancedJSONObject(rest);
    if (json === null) break;
    spans.push({ start, end: bodyStart + json.length, body: json });
    break;
  }
  return spans;
}

// 提取 ```aliang-options 围栏(供静态卡渲染/防双份对账);坏 JSON 块跳过。
export function extractAliangOptions(src) {
  const out = [];
  if (typeof src !== 'string') return out;
  for (const span of scanAliangOptionSpans(src)) {
    try {
      const parsed = JSON.parse(span.body);
      if (parsed && typeof parsed === 'object' && Array.isArray(parsed.options) && parsed.options.length) {
        out.push(parsed);
      }
    } catch (err) {
      // 不再静默:浏览器端 console 是坏块被丢弃的唯一可查出口(保留跳过行为)
      console.warn('[chat-md] aliang-options block dropped: bad JSON', err);
    }
  }
  return out;
}

// 从原文剔除 aliang-options 围栏(交互卡在场时正文不再重复渲染)。
// 按扫描跨度切除:剔除范围与提取严格同源,退化形态(闭合缺失)同样剔到 JSON
// 末尾,其后的正文保留。
export function stripAliangOptions(src) {
  if (typeof src !== 'string') return '';
  let out = '';
  let last = 0;
  for (const span of scanAliangOptionSpans(src)) {
    out += src.slice(last, span.start);
    last = span.end;
  }
  out += src.slice(last);
  return out.trim();
}

// 渲染+消毒一步到位;任何异常回退为转义纯文本(流式半截内容永不白屏)。
export function renderMarkdownSafe(src) {
  const input = normalizeCjkMarkdown(String(src ?? ''));
  let html;
  try {
    html = md.render(input);
  } catch (_) {
    html = md.utils.escapeHtml(input);
  }
  return DOMPurify.sanitize(html, {
    ADD_ATTR: ['target', 'rel'],
    FORBID_TAGS: ['style', 'iframe', 'form'],
  });
}
