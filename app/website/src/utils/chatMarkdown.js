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

// 围栏跟踪（2026-11-21 审计三批 M2/M4 共用；评审 blocker 修订）：开栏=行首
// 任意缩进 + ≥3 个同字符 ` 或 ~（反引号栏 info 不得再含 `）；闭栏=同字符、
// 长度≥开栏、其后仅空白。开栏放宽是刻意的：列表项内 4 空格/Tab 缩进的代码
// 围栏是 markdown-it 实测渲染的真实代码块（GLM 高频输出），0-3 空格收紧会
// 让这些块失去保护、内容被误插空格（对旧实现回归）。M2 的核心缺陷（嵌套更
// 长围栏错位翻转）由闭栏强规则解决——嵌套短围栏带 info/长度不足不再闭栏。
// 返回新状态：null=栏外；{marker}=栏内。
function nextFenceState(state, line) {
  const m = /^\s*(`{3,}|~{3,})(.*)$/.exec(line);
  if (!state) {
    if (!m) return state;
    const marker = m[1];
    const info = m[2] || '';
    if (marker[0] === '`' && info.includes('`')) return state;
    return { marker };
  }
  if (m && m[1][0] === state.marker[0] && m[1].length >= state.marker.length && !/\S/.test(m[2] || '')) {
    return null;
  }
  return state;
}

// GLM 中文容错第一批(spec §E):行首 #/-/+/* 后紧跟非空白非换行字符 → 补空格。
// 逐行扫描并按 CommonMark 规则跟踪围栏(M2),围栏内不动。
export function normalizeCjkMarkdown(src) {
  if (typeof src !== 'string' || !src) return src || '';
  let fenced = null;
  return src.split('\n').map((line) => {
    const prev = fenced;
    fenced = nextFenceState(fenced, line);
    if (prev || fenced) return line; // 开栏/闭栏行与栏内内容一律不动
    // lookahead 排除 #*+- 防 ##/**/-- 连写被误插空格,勿简化成 [^\s]。
    // M3(2026-11-21 审计):『-5°C』负数不是列表(-/+ 后跟数字不插);
    // 『*注意*x』行内还有第二个 * 是强调对,不作列表拆坏。
    return line.replace(/^(\s*)(#{1,6}|[-+*])(?=([^\s#*+-]))/y, (m, ind, mark, next) => {
      if ((mark === '-' || mark === '+') && /\d/.test(next)) return m;
      if (mark === '*' && line.slice(ind.length + 1).includes('*')) return m;
      return `${ind}${mark} `;
    });
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
// 返回 { spans, halfStart }：spans 同旧语义；halfStart = 流式半截围栏的
// 开栏 marker 偏移（扫描器在「独占行开栏但截不出配平 JSON」处 break 的位置，
// -1=无）。stripAliangOptionsForPlainText 用它做纯文本侧截尾兜底。
function scanAliangOptionSpans(src) {
  const spans = [];
  let halfStart = -1;
  if (typeof src !== 'string') return { spans, halfStart };
  const lines = src.split('\n');
  let fence = null; // 普通围栏态（与 normalizeCjkMarkdown 同一跟踪器）
  let offset = 0;   // 当前行在 src 中的起始偏移
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const openIdx = fence === null ? line.indexOf('```aliang-options') : -1;
    // 开栏统一 ≤3 空格缩进口径（评审 should：开栏无视缩进/闭栏 ≤3 不对称，
    // ≥4 缩进示例被误判真实开栏后其缩进闭栏永找不到——吞示例+腰斩正文）。
    const openLinePrefix = openIdx !== -1 ? line.slice(0, openIdx) : '';
    // 前缀规则双口径（评审 should + 黏行实锤）：纯空格缩进须 ≤3（≥4 缩进的
    // 用法示例不进 span——其缩进闭栏永找不到，吞示例+腰斩正文）；散文前缀
    // （GLM 黏行实锤「排查完成：```aliang-options」）放行。
    const openOk = openIdx !== -1
      && /^```aliang-options\s*$/.test(line.slice(openIdx))
      && (/^ {0,3}$/.test(openLinePrefix) || /\S/.test(openLinePrefix));
    if (openOk) {
      // 独占行开栏（可带 ≤3 空格前缀散文——marker 后须纯空白）。行制扫描：
      // 嵌套保护（2026-11-21 审计三批 M4：示例/文档代码块里的 options 语法
      // 是内容不是开栏，误剥会吞示例并凭空多出静态卡）。
      // start 回退到本行连续反引号的起点:4 反引号漂移时 indexOf 从第 2 个
      // 反引号起匹配,不回退会把第 1 个反引号留在正文里(孤立的 ` 挂在卡前)。
      let fenceStart = openIdx;
      while (fenceStart > 0 && line[fenceStart - 1] === '`') fenceStart -= 1;
      const start = offset + fenceStart;
      const bodyStart = offset + line.length + 1;
      let j = i + 1;
      let closedAt = -1;
      while (j < lines.length) {
        // 闭栏探测与旧 indexOf('\n```') 同口径：接受带 info 的围栏行（评审
        // should：退化形态后续 ```python 块可提前收口，body 停在 JSON 末尾）。
        if (/^ {0,3}(`{3,})/.test(lines[j])) { closedAt = j; break; }
        j++;
      }
      if (closedAt !== -1) {
        const bodyLines = lines.slice(i + 1, closedAt);
        const body = bodyLines.join('\n');
        const closeLineStart = bodyStart + (bodyLines.length ? body.length + 1 : 0);
        const end = closeLineStart + lines[closedAt].length;
        spans.push({ start, end, body });
        offset = end + 1;
        i = closedAt + 1;
        continue;
      }
      // 退化形态（独占行但闭合缺失）：括号配平兜底，其后不再扫（旧终止语义）。
      // 截不出 JSON = 流式半截围栏：记下 marker 偏移供纯文本截尾（2026-11-26
      // 症状1），markdown 管线侧仍由 ChatMarkdown enhance 隐藏半截代码块。
      const rest = lines.slice(i + 1).join('\n');
      const json = aliangBalancedJSONObject(rest);
      if (json === null) {
        halfStart = start;
        break;
      }
      spans.push({ start, end: bodyStart + json.length, body: json });
      break;
    }
    if (openIdx !== -1) {
      // marker 后还有别的文字：不是 options 开栏（旧正则同口径），当普通行走
      // 围栏门。
      fence = nextFenceState(fence, line);
      offset += line.length + 1;
      i++;
      continue;
    }
    // 普通围栏开闭：栏内的 ```aliang-options 行是内容，不是真实开栏（M4）。
    fence = nextFenceState(fence, line);
    offset += line.length + 1;
    i++;
  }
  return { spans, halfStart };
}

// 提取 ```aliang-options 围栏(供静态卡渲染/防双份对账);坏 JSON 块跳过。
export function extractAliangOptions(src) {
  const out = [];
  if (typeof src !== 'string') return out;
  for (const span of scanAliangOptionSpans(src).spans) {
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
  for (const span of scanAliangOptionSpans(src).spans) {
    // body JSON 闸门（评审 should：退化形态闭栏探测放宽后，提前收口的坏
    // body——JSON 后跟 prose/别的围栏——截出的 span 不参与剔除，杜绝吞正文；
    // 与 extract 的丢弃口径对齐）。JSON.parse 每气泡仅 AliangOptions 块数量级,
    // 成本可忽略。
    let bodyOk = false;
    try {
      JSON.parse(span.body);
      bodyOk = true;
    } catch (_) { /* 坏 body：不剔除 */ }
    if (!bodyOk) continue;
    out += src.slice(last, span.start);
    last = span.end;
  }
  out += src.slice(last);
  return out.trim();
}

// 错误气泡纯文本路径的围栏处置（2026-11-26 症状1：红泡正文裸漏 aliang-options
// 原始 JSON，用户截图实锤）。防注入铁律要求 error 泡不走 markdown 管线
// （纯文本插值），剥离只能做成纯字符串过滤——本函数与 stripAliangOptions
// 同源剔块（同一扫描器、同一合法 JSON 闸门、同一「坏 body 保留原文」口径），
// 另补 done/running 泡在 markdown 管线里由 ChatMarkdown enhance 兜底隐藏、
// 而纯文本侧无人兜底的形态：流式半截围栏（开栏后截不出配平 JSON，扫描器在
// 此 break）从 marker 起截到文末——半截 JSON 是流式尸体不是内容，保留即
// 复现用户投诉的泄漏形态。≥4 缩进/嵌套示例开栏 openOk=false 不触发 break，
// 文档示例不受影响。返回值仍走 Vue 转义插值，绝不进 markdown/HTML 管线。
export function stripAliangOptionsForPlainText(src) {
  if (typeof src !== 'string') return '';
  const { spans, halfStart } = scanAliangOptionSpans(src);
  let out = '';
  let last = 0;
  for (const span of spans) {
    let bodyOk = false;
    try {
      JSON.parse(span.body);
      bodyOk = true;
    } catch (_) { /* 坏 body：不剔除（与 done 泡「原文保留」口径一致） */ }
    if (!bodyOk) continue;
    out += src.slice(last, span.start);
    last = span.end;
  }
  out += src.slice(last, halfStart >= 0 ? halfStart : undefined);
  return out.trim();
}

// 大内容自适应渲染间隔（2026-11-21 审计三批 M1）：全量 markdown+hljs+
// DOMPurify 渲染成本随长度线性增长（300KB 实测 346-442ms/次），固定 80ms
// 间隔=流式期间主线程持续冻结。分档放宽；组件的 trailing 定时器保证最终
// 内容总在间隔内渲染。导出供组件与测试单源。
export function chatMdRenderInterval(contentLength) {
  if (contentLength >= 120000) return 800;
  if (contentLength >= 40000) return 300;
  return 80;
}

// 图片 src 白名单（2026-11-21 审计拍板「限同源」）：同源（相对路径/同 host
// 绝对 URL）+ data:image 放行；其余（远程图）降级为可点击链接文本。旧实现
// 放行任意远程 src——提示注入可让仪表盘零交互自动向第三方发追踪信标、URL
// 拼会话敏感内容构成外传通道（IP/UA 泄露面）。降级而非删除：合法图示保留
// 入口（点开才出网）。本机附件图走同源端点，不受影响。
function isAllowedImageSrc(src) {
  const t = String(src || '').trim();
  if (!t) return false;
  // 协议相对（//evil.com/pixel）是跨源请求（评审 blocker：两字符绕穿让信标
  // 通道原样复活），显式拒绝后再放行同源根相对路径。
  if (t.startsWith('//')) return false;
  if (t.startsWith('/') || t.startsWith('#')) return true;
  if (/^data:image\//i.test(t)) return true;
  try {
    if (typeof window === 'undefined' || !window.location) return false;
    return new URL(t, window.location.href).origin === window.location.origin;
  } catch (_) {
    return false;
  }
}

// 远程 <img> → <a>（alt 文本兜底）。二次 DOM 解析仅在消毒产物含 <img> 时
// 发生（indexOf 廉价短路，纯文本/无图气泡零开销）；入参已是消毒后 HTML，
// DOMParser 往返不引入未消毒内容。
function demoteRemoteImages(html) {
  if (!html.includes('<img')) return html;
  let doc;
  try {
    doc = new DOMParser().parseFromString(html, 'text/html');
  } catch (_) {
    return html;
  }
  let changed = false;
  doc.querySelectorAll('img').forEach((img) => {
    const src = img.getAttribute('src') || '';
    if (isAllowedImageSrc(src)) return;
    const a = doc.createElement('a');
    try {
      a.setAttribute('href', src);
      a.setAttribute('target', '_blank');
      a.setAttribute('rel', 'noopener');
    } catch (_) { /* 危险 src：DOMPurify 已剥，链接退化为纯文本 */ }
    a.textContent = img.getAttribute('alt') || src || '[图片]';
    img.replaceWith(a);
    changed = true;
  });
  return changed ? doc.body.innerHTML : html;
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
  const safe = DOMPurify.sanitize(html, {
    ADD_ATTR: ['target', 'rel'],
    FORBID_TAGS: ['style', 'iframe', 'form'],
  });
  return demoteRemoteImages(safe);
}
