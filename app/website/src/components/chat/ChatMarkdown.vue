<template>
  <!--
    whitespace-normal:气泡容器带 whitespace-pre-wrap 且会被继承,markdown
    HTML 源里的块级换行会被 pre-wrap 渲染成多余空行,这里显式还原。
    交互(复制/展开)全部走根节点事件委托,v-html 内容不绑任何事件。
    模板分两段(Task 14):①v-html 只承载正文(渲染前永远 stripAliangOptions,
    围栏原文块不再出现——与交互卡是否在场无关);②extractAliangOptions 提取
    的每个结构化块渲染静态只读卡(命中 hiddenOptionIds 且带 option_id 的隐藏,
    防与交互卡双份;无 option_id 的块不参与比对,照渲染)。
  -->
  <div class="whitespace-normal text-sm leading-relaxed break-words">
    <div
      ref="rootRef"
      class="chat-md"
      v-html="html"
      @click="onClick"
    ></div>
    <div
      v-for="(block, bi) in visibleOptionBlocks"
      :key="bi"
      data-testid="chat-option-static"
      class="mt-2 rounded-xl border border-primary/30 bg-primary/5 px-3 py-2 text-xs dark:border-primary/40 dark:bg-primary/10"
    >
      <div class="flex items-center gap-1.5 font-semibold text-primary">
        <span class="material-symbols-outlined text-sm leading-none">checklist</span>
        <span>{{ blockTitle(block) }}</span>
      </div>
      <ul class="mt-1 space-y-0.5 text-slate-600 dark:text-slate-300">
        <li
          v-for="opt in blockOptions(block)"
          :key="opt.id"
          class="break-words"
        >
          • {{ opt.label }}
          <span
            v-if="opt.description"
            class="mt-0.5 block text-[11px] text-slate-400 dark:text-slate-500"
          >{{ opt.description }}</span>
        </li>
      </ul>
      <p class="mt-1 text-slate-400 dark:text-slate-500">{{ t('chat_optionStaticHint') }}</p>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, watch, onMounted, onUnmounted } from 'vue';
import { renderMarkdownSafe, extractAliangOptions, stripAliangOptions } from '../../utils/chatMarkdown';
import { useI18n } from '../../i18n';

const { t } = useI18n();

// 流式容忍(spec §E):连续 token 流的 delta 间隔常 <80ms,纯 trailing
// 防抖会把气泡冻结到流暂停,故改为 leading+trailing 节流——距上次渲染
// ≥阈值立即渲染(leading),否则排单个 trailing 定时器在剩余时间后渲染
// 最新值,流式期间最多每阈值渲染一次。测试/E2E 经
// globalThis.__CHAT_MD_DEBOUNCE_MS__ 注入正值可覆盖(注入口名保留),
// 0 会被 || 回退到默认(与 __CHAT_IDLE_TIMEOUT_MS__ 手法一致);测试需要
// 同步渲染路径时走初始 ref 渲染。未闭合围栏由 markdown-it 天然按代码块
// 处理,半截内容不闪错。
const CHAT_MD_RENDER_INTERVAL_MS = Number(globalThis.__CHAT_MD_DEBOUNCE_MS__) || 80;
const FOLD_LINE_THRESHOLD = 20;
// 复制成功反馈在按钮上的停留时长,超时还原为「复制」,避免长期占位误导。
const FEEDBACK_RESET_MS = 1500;

const props = defineProps({
  content: { type: String, default: '' },
  // 该会话在挂交互卡（ai.option.request upsert 的 optionCards）的 option_id
  // 集合：命中（且块自带 option_id）的静态卡隐藏——防交互卡/静态卡双份。
  // 只控静态卡显隐，正文 strip 围栏无条件执行（防双份正确流）。
  hiddenOptionIds: { type: Array, default: () => [] },
});

// 渲染短路:content 未变直接复用上次 HTML——撤回重发/尾部重复等场景不
// 重跑 markdown 也不触碰 v-html,代码块交互态(复制反馈/展开)不被无谓
// 抹掉(renderMarkdownSafe 确定性渲染,同输入同输出,可安全记忆)。
// Task 14:渲染输入是 stripAliangOptions(content)——正文永远先剔除
// aliang-options 围栏(坏 JSON 块同样剔原文:extract 已跳过不产卡,原文
// 也不再以代码块形态露出)。
let lastContent = '';
let lastHtml = '';
function render(content) {
  const stripped = stripAliangOptions(content);
  if (stripped === lastContent) return lastHtml;
  lastContent = stripped;
  lastHtml = renderMarkdownSafe(stripped);
  return lastHtml;
}

// 围栏 → 结构化块（extract 坏 JSON 跳过）：驱动静态卡第二段渲染。
const optionBlocks = computed(() => extractAliangOptions(props.content));

const hiddenOptionIdSet = computed(() =>
  new Set((props.hiddenOptionIds || []).map(v => String(v)))
);

// 隐藏比对仅对带 option_id 的块生效（Task 4 质量审查交接：模型漏写
// option_id 的静态块照渲染，不进比对）。
const visibleOptionBlocks = computed(() =>
  optionBlocks.value.filter((block) => {
    const id = block.option_id ? String(block.option_id) : '';
    return !id || !hiddenOptionIdSet.value.has(id);
  })
);

function blockTitle(block) {
  const title = typeof block?.title === 'string' ? block.title.trim() : '';
  return title || t('chat_optionTitle');
}

// 只读选项列表：extract 已保证 options 非空数组，但元素形态不保证——
// 逐项取 id/label/description 字符串化，无 label 的行跳过（与后端
// normalize 同口径的容错，坏行不炸卡）；description 透传渲染（缺失为空串，
// 模板不渲染副行）。
function blockOptions(block) {
  const list = Array.isArray(block?.options) ? block.options : [];
  const out = [];
  for (const item of list) {
    if (!item || typeof item !== 'object') continue;
    const id = String(item.id ?? '');
    const label = String(item.label ?? '').trim();
    if (id && label) {
      out.push({
        id,
        label,
        description: typeof item.description === 'string' ? item.description : '',
      });
    }
  }
  return out;
}

// 初始渲染同步完成(ref 初始化),挂载即有内容——不参与节流。
const html = ref(render(props.content));
const rootRef = ref(null);
let renderTimer = null;
let lastRenderAt = 0;
const feedbackTimers = new Set();
// 用户已展开的折叠代码块(pre 序号)。v-html 重渲染会整体换子树,enhance
// 按序号恢复展开态(不再加折叠类);序号以 querySelectorAll('pre') 顺序
// 为准,组件实例存活期间保留,跨流式重渲染生效。
const expandedPres = new Set();
// copyText 返回的 promise 不可取消,卸载后 then 回调不得再回灌按钮文案/
// 定时器池,disposed 置位后一律丢弃。
let disposed = false;

// DOM 级代码块增强:每个 pre 包一层 wrapper(语言标签 + 复制按钮),
// >20 行加折叠。幂等策略:v-html 每次 html 变更会整体替换容器子树,旧
// wrapper 随之销毁,enhance 只需跳过「已被包过」的 pre(父节点带
// chat-md-pre-wrap 类),不会嵌套叠加。
function enhance() {
  const root = rootRef.value;
  if (!root) return;
  root.querySelectorAll('pre').forEach((pre, preIndex) => {
    const code = pre.querySelector('code');
    // ```aliang-options 围栏的原文代码块永不露出(Task 14 静态卡实现的唯一
    // 兜底位):已闭合围栏在渲染前已被 stripAliangOptions 从正文剔除,正常
    // 不会走到这里;能到达的只剩流式半截围栏(markdown-it 按未闭合代码块
    // 渲染,extract 匹配不到闭合围栏、无静态卡)——隐藏防闪烁,也不包
    // wrapper,避免语言标签/按钮闪现。闭合瞬间正文重渲染(strip 后无此 pre)
    // 自然换成静态卡。
    if (code && code.classList.contains('language-aliang-options')) {
      pre.hidden = true;
      return;
    }
    if (pre.parentElement && pre.parentElement.classList.contains('chat-md-pre-wrap')) return;

    const lang = (code ? Array.from(code.classList) : [])
      .find((c) => c.startsWith('language-'))?.slice('language-'.length) || 'text';

    const wrap = document.createElement('div');
    wrap.className = 'chat-md-pre-wrap';
    const label = document.createElement('span');
    label.className = 'lang-label';
    label.textContent = lang;
    const copy = document.createElement('button');
    copy.type = 'button';
    copy.className = 'chat-md-copy';
    // 复制反馈文案变化对读屏可感知(异步回填,非即时区域)。
    copy.setAttribute('aria-live', 'polite');
    copy.textContent = t('chat_md_copy');
    pre.replaceWith(wrap);
    wrap.appendChild(pre);
    wrap.appendChild(label);
    wrap.appendChild(copy);

    // 尾部换行是围栏语法的一部分,计行前剥掉,阈值才是真实行数。用户已
    // 展开过的块(按 pre 序号)不再重新加折叠,保持流式重渲染后仍展开。
    const lines = (code ? code.textContent : pre.textContent).replace(/\n$/, '').split('\n').length;
    if (lines > FOLD_LINE_THRESHOLD && !expandedPres.has(preIndex)) {
      wrap.classList.add('chat-md-fold');
      const unfold = document.createElement('button');
      unfold.type = 'button';
      unfold.className = 'chat-md-unfold';
      unfold.textContent = t('chat_md_unfold');
      wrap.appendChild(unfold);
    }
  });
}

// http 局域网直连(56431 无 TLS)下 navigator.clipboard 不存在,退
// execCommand;两者都尽力而为,按钮反馈以不抛错为前提。
function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text).catch(() => {});
  }
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.style.position = 'fixed';
  ta.style.opacity = '0';
  document.body.appendChild(ta);
  ta.select();
  try { document.execCommand('copy'); } catch (_) { /* 尽力而为 */ }
  ta.remove();
  return Promise.resolve();
}

function onClick(e) {
  const copy = e.target.closest ? e.target.closest('.chat-md-copy') : null;
  if (copy) {
    const code = copy.parentElement && copy.parentElement.querySelector('pre code');
    // 原文案取 i18n 真值而非 live textContent:反馈窗口内二次点击会把
    // 「已复制」误捕成 original,复位后按钮就永远停留错误文案。
    const original = t('chat_md_copy');
    copyText(code ? code.textContent : '').then(() => {
      // copyText 的 promise 不可取消,卸载后不得回灌已销毁的 DOM。
      if (disposed) return;
      copy.textContent = t('chat_md_copied');
      const feedbackTimer = setTimeout(() => {
        copy.textContent = original;
        feedbackTimers.delete(feedbackTimer);
      }, FEEDBACK_RESET_MS);
      feedbackTimers.add(feedbackTimer);
    });
    return;
  }
  const unfold = e.target.closest ? e.target.closest('.chat-md-unfold') : null;
  if (unfold) {
    const wrapEl = unfold.parentElement;
    if (wrapEl) {
      // 记下 pre 序号,流式重渲染换子树后 enhance 按序号保持展开态。
      const preEl = wrapEl.querySelector('pre');
      const pres = rootRef.value ? rootRef.value.querySelectorAll('pre') : [];
      const idx = Array.prototype.indexOf.call(pres, preEl);
      if (idx >= 0) expandedPres.add(idx);
      wrapEl.classList.remove('chat-md-fold');
    }
    unfold.remove();
  }
}

watch(
  () => props.content,
  (v) => {
    const elapsed = Date.now() - lastRenderAt;
    if (elapsed >= CHAT_MD_RENDER_INTERVAL_MS) {
      // leading:距上次渲染已超阈值,立即渲染;挂起的 trailing 定时器
      // 内容已过期,一并清掉。
      if (renderTimer) { clearTimeout(renderTimer); renderTimer = null; }
      lastRenderAt = Date.now();
      html.value = render(v);
      return;
    }
    // trailing:未到阈值,复用单个定时器在剩余时间后渲染最新值。
    if (renderTimer) clearTimeout(renderTimer);
    renderTimer = setTimeout(() => {
      renderTimer = null;
      lastRenderAt = Date.now();
      html.value = render(v);
    }, CHAT_MD_RENDER_INTERVAL_MS - elapsed);
  },
);

// flush:'post' 确保 v-html 已写回 DOM 后再增强;watch 非 immediate,
// 初始路径由 onMounted 覆盖,两者不会对同一份 DOM 重复包。
watch(html, enhance, { flush: 'post' });
onMounted(enhance);
onUnmounted(() => {
  disposed = true;
  if (renderTimer) clearTimeout(renderTimer);
  feedbackTimers.forEach((t) => clearTimeout(t));
  feedbackTimers.clear();
});
</script>

<!-- scoped 编译会给选择器前段追加 [data-v-x],而 v-html 与 DOM 创建的
     元素不带 scope 属性,故全部选择器挂在模板根 .chat-md 上、逻辑整体
     放进单个 :deep() 内。 -->
<style scoped>
/* ---- 块级排版基线(spec §E:标题/列表/表格/引用)。此前只有代码块包装样式,
     标题/列表/表格/行内代码全是浏览器默认形态——「看似 markdown、渲染怪」
     的根因。基线字号 text-sm(0.875rem),标题只比正文大半档,不占气泡。 */
.chat-md :deep(p) {
  margin: 0.4em 0;
  line-height: 1.7;
}

.chat-md :deep(> p:first-child),
.chat-md :deep(> :first-child) {
  margin-top: 0;
}

.chat-md :deep(> :last-child) {
  margin-bottom: 0;
}

.chat-md :deep(h1),
.chat-md :deep(h2),
.chat-md :deep(h3),
.chat-md :deep(h4),
.chat-md :deep(h5),
.chat-md :deep(h6) {
  margin: 0.9em 0 0.4em;
  font-weight: 600;
  line-height: 1.4;
  color: rgb(15 23 42);
}

.dark .chat-md :deep(h1),
.dark .chat-md :deep(h2),
.dark .chat-md :deep(h3),
.dark .chat-md :deep(h4),
.dark .chat-md :deep(h5),
.dark .chat-md :deep(h6) {
  color: rgb(241 245 249);
}

.chat-md :deep(h1) { font-size: 1.25em; }
.chat-md :deep(h2) { font-size: 1.125em; }
.chat-md :deep(h3) { font-size: 1em; }
.chat-md :deep(h4),
.chat-md :deep(h5),
.chat-md :deep(h6) { font-size: 0.95em; }

.chat-md :deep(h1:first-child),
.chat-md :deep(h2:first-child),
.chat-md :deep(h3:first-child),
.chat-md :deep(h4:first-child),
.chat-md :deep(h5:first-child),
.chat-md :deep(h6:first-child) {
  margin-top: 0;
}

.chat-md :deep(ul),
.chat-md :deep(ol) {
  margin: 0.4em 0;
  padding-left: 1.4em;
}

.chat-md :deep(li) {
  margin: 0.2em 0;
  line-height: 1.65;
}

.chat-md :deep(li > ul),
.chat-md :deep(li > ol) {
  margin: 0.2em 0;
}

.chat-md :deep(blockquote) {
  margin: 0.5em 0;
  padding: 0.1em 0 0.1em 0.8em;
  border-left: 3px solid rgb(203 213 225);
  color: rgb(100 116 139);
}

.dark .chat-md :deep(blockquote) {
  border-left-color: rgb(71 85 105);
  color: rgb(148 163 184);
}

.chat-md :deep(blockquote p) {
  margin: 0.25em 0;
}

.chat-md :deep(hr) {
  margin: 0.9em 0;
  border: 0;
  border-top: 1px solid rgb(226 232 240);
}

.dark .chat-md :deep(hr) {
  border-top-color: rgb(51 65 85);
}

.chat-md :deep(a) {
  /* 浅色下压深到 emerald-700 级（primary 亮绿 #21C45D 白底对比 ~2.2:1 不达
     WCAG AA）；暗色底上亮绿对比充足，保持品牌色。 */
  color: rgb(4 120 87);
  text-decoration: underline;
  text-underline-offset: 2px;
}

.dark .chat-md :deep(a) {
  color: rgb(var(--color-primary-rgb));
}

.chat-md :deep(a:hover) {
  text-decoration-thickness: 2px;
}

/* 行内代码(不在 pre 里的 code):浅底胶囊。 */
.chat-md :deep(code) {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 0.85em;
}

.chat-md :deep(:not(pre) > code) {
  margin: 0 0.1em;
  padding: 0.15em 0.4em;
  border-radius: 4px;
  background: rgb(241 245 249);
  border: 1px solid rgb(226 232 240);
  color: rgb(190 24 93);
}

.dark .chat-md :deep(:not(pre) > code) {
  background: rgb(30 41 59);
  border-color: rgb(51 65 85);
  color: rgb(244 114 182);
}

/* ---- 代码块底座:此前 pre 无背景无边框,裸文本混在气泡里。 */
.chat-md :deep(.chat-md-pre-wrap pre) {
  margin: 0;
  padding: 1.6rem 0.75rem 0.6rem;
  border-radius: 0.5rem;
  border: 1px solid rgb(226 232 240);
  background: rgb(248 250 252);
  overflow-x: auto;
  font-size: 0.8125rem;
  line-height: 1.6;
}

.dark .chat-md :deep(.chat-md-pre-wrap pre) {
  border-color: rgb(51 65 85);
  background: rgb(2 6 23);
}

.chat-md :deep(.chat-md-pre-wrap pre code) {
  background: none;
  border: none;
  padding: 0;
  font-size: 1em;
}

/* 语言标签/复制按钮要压在 pre 上沿 padding 区(1.6rem 顶垫是它们的位置)。
   折叠态下 pre 被 max-height 裁切,按钮仍浮在 wrap 层不受影响。 */
.chat-md :deep(.chat-md-pre-wrap) {
  position: relative;
  margin: 0.5rem 0;
}

.chat-md :deep(.chat-md-fold pre) {
  max-height: 6rem;
  overflow: hidden;
}

.chat-md :deep(.lang-label),
.chat-md :deep(.chat-md-copy) {
  position: absolute;
  top: 6px;
  font-size: 11px;
  line-height: 1;
  color: rgb(148 163 184);
}

.chat-md :deep(.lang-label) {
  left: 8px;
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.chat-md :deep(.chat-md-copy) {
  right: 8px;
  cursor: pointer;
  background: none;
  border: none;
  padding: 2px 4px;
  border-radius: 4px;
}

.chat-md :deep(.chat-md-copy:hover) {
  color: rgb(100 116 139);
  background: rgb(226 232 240 / 0.5);
}

/* 暗色代码块底近黑，浅灰 hover 底会把 slate-500 文字混没（对比 1.06:1）
   ——换亮文字+深底。 */
.dark .chat-md :deep(.chat-md-copy:hover) {
  color: rgb(203 213 225);
  background: rgb(51 65 85 / 0.6);
}

.chat-md :deep(.chat-md-unfold) {
  display: block;
  margin-top: 4px;
  font-size: 12px;
  color: rgb(100 116 139);
  background: none;
  border: none;
  padding: 2px 4px;
  cursor: pointer;
  text-decoration: underline;
}

/* ---- 表格:markdown-it 输出裸 table,无样式时边框全无、挤成一行。 */
.chat-md :deep(table) {
  margin: 0.5em 0;
  border-collapse: collapse;
  font-size: 0.875em;
  display: block;
  overflow-x: auto;
  max-width: 100%;
}

.chat-md :deep(th),
.chat-md :deep(td) {
  border: 1px solid rgb(226 232 240);
  padding: 0.3em 0.7em;
  text-align: left;
  line-height: 1.5;
}

.dark .chat-md :deep(th),
.dark .chat-md :deep(td) {
  border-color: rgb(51 65 85);
}

.chat-md :deep(th) {
  background: rgb(248 250 252);
  font-weight: 600;
}

.dark .chat-md :deep(th) {
  background: rgb(15 23 42);
}

/* ---- highlight.js 配色(手写亮暗双态):不引入 hljs 主题文件——主题是
     单态的,与 .dark 切换冲突;这里按 github / github-dark 官方色板摘录
     常用 token 类,未覆盖的类回落正文色。 */
.chat-md :deep(.hljs-comment),
.chat-md :deep(.hljs-quote) {
  color: rgb(106 115 125);
  font-style: italic;
}

.chat-md :deep(.hljs-keyword),
.chat-md :deep(.hljs-selector-tag) {
  color: rgb(215 58 73);
}

.chat-md :deep(.hljs-string),
.chat-md :deep(.hljs-regexp),
.chat-md :deep(.hljs-addition) {
  color: rgb(3 47 98);
}

.chat-md :deep(.hljs-number),
.chat-md :deep(.hljs-built_in),
.chat-md :deep(.hljs-literal),
.chat-md :deep(.hljs-type),
.chat-md :deep(.hljs-params),
.chat-md :deep(.hljs-meta),
.chat-md :deep(.hljs-link) {
  color: rgb(0 92 197);
}

.chat-md :deep(.hljs-title),
.chat-md :deep(.hljs-section),
.chat-md :deep(.hljs-name),
.chat-md :deep(.hljs-selector-id),
.chat-md :deep(.hljs-selector-class) {
  color: rgb(111 66 193);
}

.chat-md :deep(.hljs-attribute),
.chat-md :deep(.hljs-symbol),
.chat-md :deep(.hljs-variable),
.chat-md :deep(.hljs-template-variable),
.chat-md :deep(.hljs-bullet) {
  color: rgb(227 98 9);
}

.chat-md :deep(.hljs-deletion) {
  color: rgb(179 29 40);
  background: rgb(255 238 240);
}

.dark .chat-md :deep(.hljs-comment),
.dark .chat-md :deep(.hljs-quote) {
  color: rgb(139 148 158);
}

.dark .chat-md :deep(.hljs-keyword),
.dark .chat-md :deep(.hljs-selector-tag) {
  color: rgb(255 123 114);
}

.dark .chat-md :deep(.hljs-string),
.dark .chat-md :deep(.hljs-regexp),
.dark .chat-md :deep(.hljs-addition) {
  color: rgb(165 214 255);
}

.dark .chat-md :deep(.hljs-number),
.dark .chat-md :deep(.hljs-built_in),
.dark .chat-md :deep(.hljs-literal),
.dark .chat-md :deep(.hljs-type),
.dark .chat-md :deep(.hljs-params),
.dark .chat-md :deep(.hljs-meta),
.dark .chat-md :deep(.hljs-link) {
  color: rgb(121 192 255);
}

.dark .chat-md :deep(.hljs-title),
.dark .chat-md :deep(.hljs-section),
.dark .chat-md :deep(.hljs-name),
.dark .chat-md :deep(.hljs-selector-id),
.dark .chat-md :deep(.hljs-selector-class) {
  color: rgb(210 168 255);
}

.dark .chat-md :deep(.hljs-attribute),
.dark .chat-md :deep(.hljs-symbol),
.dark .chat-md :deep(.hljs-variable),
.dark .chat-md :deep(.hljs-template-variable),
.dark .chat-md :deep(.hljs-bullet) {
  color: rgb(255 166 87);
}

.dark .chat-md :deep(.hljs-deletion) {
  color: rgb(255 220 215);
  background: rgb(128 32 32 / 0.35);
}
</style>
