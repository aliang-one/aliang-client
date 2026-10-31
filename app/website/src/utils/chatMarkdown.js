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

// matchAll 克隆正则/replace 自归零 lastIndex,勿改用共享 lastIndex 的 exec() 循环
const aliangOptionsRe = /```aliang-options\s*\n([\s\S]*?)\n```/g;

// 提取 ```aliang-options 围栏(供静态卡渲染/防双份对账);坏 JSON 块跳过。
export function extractAliangOptions(src) {
  const out = [];
  if (typeof src !== 'string') return out;
  for (const m of src.matchAll(aliangOptionsRe)) {
    try {
      const parsed = JSON.parse(m[1]);
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
export function stripAliangOptions(src) {
  if (typeof src !== 'string') return '';
  return src.replace(aliangOptionsRe, '').trim();
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
