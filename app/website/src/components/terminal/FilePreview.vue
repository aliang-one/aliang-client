<template>
  <!--
    文件预览区(P3.5,spec §7.4):watch entry(null→空提示/dir→目录提示/
    file→fsRead 有界读)。二进制:仅「二进制文件」徽章+下载,不渲染正文;
    文本:扩展名命中已注册语言→highlight.js 高亮,未知扩展→纯 <pre>;
    截断徽章;下载=fsDownload→Blob→a[download] 点击。
    entry 为 store.selected 的投影({path,type,name}|null)——由
    FileBrowserWindow 从共享 store 绑定传入,与左侧树选中同源。
    竞态防护:请求序号 ticket——条目切换后丢弃陈旧回包(慢响应不覆盖新选中)。
  -->
  <div data-testid="file-preview" class="flex h-full min-h-0 flex-col bg-white dark:bg-slate-900">
    <!-- 空态:未选任何条目 -->
    <div
      v-if="!entry"
      data-testid="file-preview-hint"
      class="flex flex-1 items-center justify-center px-4 text-center text-xs text-slate-400 dark:text-slate-500"
    >{{ t('fs_empty_hint') }}</div>
    <!-- 目录:无预览正文 -->
    <div
      v-else-if="isDir"
      data-testid="file-preview-dir-hint"
      class="flex flex-1 items-center justify-center px-4 text-center text-xs text-slate-400 dark:text-slate-500"
    >{{ t('fs_dir_hint') }}</div>
    <!-- 读取中 -->
    <div v-else-if="loading" data-testid="file-preview-loading" class="flex flex-1 items-center justify-center">
      <span class="inline-block size-4 animate-spin rounded-full border-2 border-slate-300 border-t-primary"></span>
    </div>
    <!-- 读取失败:错误行+重试(重试=再走一次 load,即 fsRead) -->
    <div
      v-else-if="error"
      data-testid="file-preview-error"
      class="m-3 rounded border border-red-200 bg-red-50 px-3 py-2 text-xs text-red-600 dark:border-red-800 dark:bg-red-500/10 dark:text-red-400"
    >
      <div class="flex items-center gap-2">
        <span class="min-w-0 flex-1 break-all">{{ t('fs_load_error') }}：{{ error }}</span>
        <button
          data-testid="file-preview-retry"
          type="button"
          class="shrink-0 rounded border border-red-300 px-1.5 py-px text-[11px] leading-4 transition hover:bg-red-100 dark:border-red-700 dark:hover:bg-red-500/10"
          @click="load"
        >{{ t('fs_retry') }}</button>
      </div>
    </div>
    <template v-else>
      <!-- 头部:文件名 + 二进制/截断徽章 + 下载 -->
      <div class="flex shrink-0 items-center gap-2 border-b border-slate-200 px-3 py-2 dark:border-slate-700">
        <span class="min-w-0 flex-1 truncate font-mono text-xs" :title="entry.path">{{ entry.name }}</span>
        <span
          v-if="binary"
          data-testid="file-preview-badge-binary"
          class="shrink-0 rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-slate-600 dark:bg-slate-700 dark:text-slate-300"
        >{{ t('fs_binary') }}</span>
        <span
          v-if="truncated"
          data-testid="file-preview-badge-truncated"
          class="shrink-0 rounded bg-amber-400/15 px-1.5 py-0.5 text-[10px] font-medium leading-4 text-amber-600 dark:bg-amber-400/10 dark:text-amber-400"
        >{{ t('fs_truncated') }}</span>
        <button
          data-testid="file-preview-download"
          type="button"
          class="shrink-0 rounded border border-slate-300 px-2 py-0.5 text-[11px] text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800"
          :disabled="downloading"
          @click="download"
        >{{ t('fs_download') }}</button>
      </div>
      <p v-if="downloadError" data-testid="file-preview-download-error" class="shrink-0 px-3 pt-2 text-xs text-red-500 dark:text-red-400">{{ downloadError }}</p>
      <!-- 正文:binary 不渲染内容(仅徽章+下载);文本高亮或纯文本,恒深色码面 -->
      <div v-if="!binary" data-testid="file-preview-content" class="min-h-0 flex-1 overflow-auto bg-slate-950">
        <pre v-if="renderedHtml" class="file-preview-code p-3 font-mono text-xs leading-5 text-slate-100" tabindex="0"><code v-html="renderedHtml"></code></pre>
        <pre v-else data-testid="file-preview-plain" class="p-3 font-mono text-xs leading-5 text-slate-100" tabindex="0">{{ content }}</pre>
      </div>
    </template>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import hljs from 'highlight.js/lib/core';
import hljsJavascript from 'highlight.js/lib/languages/javascript';
import hljsTypescript from 'highlight.js/lib/languages/typescript';
import hljsGo from 'highlight.js/lib/languages/go';
import hljsJson from 'highlight.js/lib/languages/json';
import hljsBash from 'highlight.js/lib/languages/bash';
import hljsYaml from 'highlight.js/lib/languages/yaml';
import hljsPython from 'highlight.js/lib/languages/python';
import hljsSql from 'highlight.js/lib/languages/sql';
import { useI18n } from '../../i18n';
import { fsRead, fsDownload } from '../../services/agentApi';

// highlight.js/lib/core 是模块单例——与 utils/chatMarkdown.js 共享同一实例。
// 那边已注册同 8 语言(spec §7.4「复用已注册语言」),但本组件不得依赖聊天
// 模块的加载顺序:此处缺啥补啥(registerLanguage 幂等,重复注册同一定义无害)。
const HLJS_LANG_DEFS = {
  javascript: hljsJavascript,
  typescript: hljsTypescript,
  go: hljsGo,
  json: hljsJson,
  bash: hljsBash,
  yaml: hljsYaml,
  python: hljsPython,
  sql: hljsSql,
};
for (const [name, def] of Object.entries(HLJS_LANG_DEFS)) {
  if (!hljs.getLanguage(name)) hljs.registerLanguage(name, def);
}

// 扩展名→已注册语言(js/ts/go/py/sh/bash/yaml/yml/json/sql);未知扩展→纯文本
const EXT_LANG = {
  js: 'javascript', mjs: 'javascript', cjs: 'javascript',
  ts: 'typescript',
  go: 'go',
  py: 'python',
  sh: 'bash', bash: 'bash', zsh: 'bash',
  yaml: 'yaml', yml: 'yaml',
  json: 'json',
  sql: 'sql',
};

const props = defineProps({
  // 选中条目 {path,type,name}|null(store.selected 投影;每次选中都是新对象,
  // 引用变化即触发重读,无需 deep)
  entry: { type: Object, default: null },
  // 保险箱令牌(逐调用透传给 fsRead/fsDownload)
  token: { type: String, default: '' },
});

const { t } = useI18n();

const loading = ref(false);
const error = ref('');
const content = ref('');
const truncated = ref(false);
const binary = ref(false);
const downloading = ref(false);
const downloadError = ref('');

const isDir = computed(() => props.entry?.type === 'dir');

// 扩展名语言探测:仅文本且非二进制时用
const detectedLang = computed(() => {
  const name = props.entry?.name || '';
  const dot = name.lastIndexOf('.');
  if (dot <= 0 || dot === name.length - 1) return '';
  const lang = EXT_LANG[name.slice(dot + 1).toLowerCase()];
  return lang && hljs.getLanguage(lang) ? lang : '';
});

// 高亮 HTML:命中语言才渲染(hljs 输出对输入做 HTML 转义,可安全 v-html;
// ignoreIllegals 防怪内容 throw,异常再兜底纯文本)
const renderedHtml = computed(() => {
  if (binary.value || !content.value || !detectedLang.value) return '';
  try {
    return hljs.highlight(content.value, { language: detectedLang.value, ignoreIllegals: true }).value;
  } catch {
    return '';
  }
});

let loadSeq = 0; // 请求序号(非响应式簿记):条目切换后丢弃陈旧回包

async function load() {
  const path = props.entry?.path;
  if (!path || props.entry?.type === 'dir') return;
  const ticket = ++loadSeq;
  loading.value = true;
  error.value = '';
  content.value = '';
  truncated.value = false;
  binary.value = false;
  try {
    const data = await fsRead(path, props.token);
    if (ticket !== loadSeq) return; // 期间已切换条目:陈旧回包丢弃
    binary.value = !!data?.binary;
    truncated.value = !!data?.truncated;
    content.value = typeof data?.content === 'string' ? data.content : '';
  } catch (e) {
    if (ticket !== loadSeq) return;
    error.value = e?.message || String(e);
  } finally {
    if (ticket === loadSeq) loading.value = false;
  }
}

// 条目变化即重读(immediate 覆盖首挂载即已选中的场景;dir/null 在 load 内短路)
watch(() => props.entry, () => { void load(); }, { immediate: true });

// 下载:fsDownload→Blob→objectURL→临时 <a download> 点击→回收(jsdom 无
// createObjectURL,测试侧打桩;真浏览器常态路径)。
async function download() {
  if (!props.entry || downloading.value) return;
  downloading.value = true;
  downloadError.value = '';
  try {
    const blob = await fsDownload(props.entry.path, props.token);
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = props.entry.name || 'download';
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch (e) {
    downloadError.value = e?.message || String(e);
  } finally {
    downloading.value = false;
  }
}
</script>

<style scoped>
/* 高亮码面色板:恒深色码面(对齐仓内代码展示惯例——chat 码块/ConfigDiff
   编辑器皆深底),不随明暗主题翻转,故单套 token 色即可。色值摘录
   github-dark(与 ChatMarkdown.vue 手摘色板同源取向)。 */
.file-preview-code .hljs-keyword,
.file-preview-code .hljs-selector-tag,
.file-preview-code .hljs-built_in,
.file-preview-code .hljs-meta {
  color: #ff7b72;
}
.file-preview-code .hljs-string,
.file-preview-code .hljs-regexp,
.file-preview-code .hljs-addition {
  color: #a5d6ff;
}
.file-preview-code .hljs-comment {
  color: #8b949e;
  font-style: italic;
}
.file-preview-code .hljs-number,
.file-preview-code .hljs-literal,
.file-preview-code .hljs-symbol {
  color: #79c0ff;
}
.file-preview-code .hljs-title,
.file-preview-code .hljs-title.function_,
.file-preview-code .hljs-section {
  color: #d2a8ff;
}
.file-preview-code .hljs-title.class_,
.file-preview-code .hljs-type,
.file-preview-code .hljs-attr,
.file-preview-code .hljs-attribute,
.file-preview-code .hljs-variable,
.file-preview-code .hljs-name {
  color: #ffa657;
}
.file-preview-code .hljs-deletion {
  color: #ffdcd7;
}
</style>
