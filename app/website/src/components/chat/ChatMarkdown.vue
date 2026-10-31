<template>
  <!--
    whitespace-normal:气泡容器带 whitespace-pre-wrap 且会被继承,markdown
    HTML 源里的块级换行会被 pre-wrap 渲染成多余空行,这里显式还原。
    交互(复制/展开)全部走根节点事件委托,v-html 内容不绑任何事件。
  -->
  <div
    ref="rootRef"
    class="chat-md whitespace-normal text-sm leading-relaxed break-words"
    v-html="html"
    @click="onClick"
  ></div>
</template>

<script setup>
import { ref, watch, onMounted, onUnmounted } from 'vue';
import { renderMarkdownSafe } from '../../utils/chatMarkdown';
import { useI18n } from '../../i18n';

const { t } = useI18n();

// 流式容忍(spec §E):delta 高频追加,80ms 防抖重渲染。测试/E2E 经
// globalThis.__CHAT_MD_DEBOUNCE_MS__ 注入正值可覆盖,0 会被 || 回退到默认
// (与 __CHAT_IDLE_TIMEOUT_MS__ 手法一致);测试需要同步渲染路径时走初始
// ref 渲染。未闭合围栏由 markdown-it 天然按代码块处理,半截内容不闪错。
const CHAT_MD_DEBOUNCE_MS = Number(globalThis.__CHAT_MD_DEBOUNCE_MS__) || 80;
const FOLD_LINE_THRESHOLD = 20;

const props = defineProps({ content: { type: String, default: '' } });

// 初始渲染同步完成(ref 初始化),挂载即有内容——不参与防抖。
const html = ref(renderMarkdownSafe(props.content));
const rootRef = ref(null);
let renderTimer = null;
const feedbackTimers = new Set();

// DOM 级代码块增强:每个 pre 包一层 wrapper(语言标签 + 复制按钮),
// >20 行加折叠。幂等策略:v-html 每次 html 变更会整体替换容器子树,旧
// wrapper 随之销毁,enhance 只需跳过「已被包过」的 pre(父节点带
// chat-md-pre-wrap 类),不会嵌套叠加。
function enhance() {
  const root = rootRef.value;
  if (!root) return;
  root.querySelectorAll('pre').forEach((pre) => {
    const code = pre.querySelector('code');
    // 未闭合/已闭合的 ```aliang-options 围栏都渲染成 language-aliang-options
    // 代码块:直接隐藏防流式闪烁(交互卡由上层渲染,原文块永不露出),
    // 也不给它包 wrapper,避免语言标签/按钮闪现。
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
    copy.textContent = t('chat_md_copy');
    pre.replaceWith(wrap);
    wrap.appendChild(pre);
    wrap.appendChild(label);
    wrap.appendChild(copy);

    // 尾部换行是围栏语法的一部分,计行前剥掉,阈值才是真实行数。
    const lines = (code ? code.textContent : pre.textContent).replace(/\n$/, '').split('\n').length;
    if (lines > FOLD_LINE_THRESHOLD) {
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
    const original = copy.textContent;
    copyText(code ? code.textContent : '').then(() => {
      copy.textContent = t('chat_md_copied');
      const t = setTimeout(() => {
        copy.textContent = original;
        feedbackTimers.delete(t);
      }, 1500);
      feedbackTimers.add(t);
    });
    return;
  }
  const unfold = e.target.closest ? e.target.closest('.chat-md-unfold') : null;
  if (unfold) {
    if (unfold.parentElement) unfold.parentElement.classList.remove('chat-md-fold');
    unfold.remove();
  }
}

watch(
  () => props.content,
  (v) => {
    if (renderTimer) clearTimeout(renderTimer);
    renderTimer = setTimeout(() => { html.value = renderMarkdownSafe(v); }, CHAT_MD_DEBOUNCE_MS);
  },
);

// flush:'post' 确保 v-html 已写回 DOM 后再增强;watch 非 immediate,
// 初始路径由 onMounted 覆盖,两者不会对同一份 DOM 重复包。
watch(html, enhance, { flush: 'post' });
onMounted(enhance);
onUnmounted(() => {
  if (renderTimer) clearTimeout(renderTimer);
  feedbackTimers.forEach((t) => clearTimeout(t));
  feedbackTimers.clear();
});
</script>

<!-- scoped 编译会给选择器前段追加 [data-v-x],而 v-html 与 DOM 创建的
     元素不带 scope 属性,故全部选择器挂在模板根 .chat-md 上、逻辑整体
     放进单个 :deep() 内。 -->
<style scoped>
.chat-md :deep(.chat-md-pre-wrap) {
  position: relative;
  margin: 0.5rem 0;
}

.chat-md :deep(.chat-md-pre-wrap pre) {
  margin: 0;
}

.chat-md :deep(.chat-md-fold pre) {
  max-height: 6rem;
  overflow: hidden;
}

.chat-md :deep(.lang-label),
.chat-md :deep(.chat-md-copy) {
  position: absolute;
  top: 4px;
  font-size: 11px;
  line-height: 1;
  color: rgb(148 163 184);
}

.chat-md :deep(.lang-label) {
  left: 8px;
}

.chat-md :deep(.chat-md-copy) {
  right: 8px;
  cursor: pointer;
  background: none;
  border: none;
  padding: 2px 4px;
}

.chat-md :deep(.chat-md-copy:hover) {
  color: rgb(100 116 139);
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
</style>
