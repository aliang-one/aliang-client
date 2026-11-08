<template>
  <!--
    子agent单条详情（Task 8, spec 2026-11-07-subagent-panel §三）：纯内容组件，
    面板壳（drawer/列表切换/移动端返回键）由 SubagentPanel 承载。三段结构：
    状态头（实时字段）→ 工具时间线（详情 tools[]，is_error 标红）→ 完整报告
    （result 优先 summary 兜底，markdown 走仓内既有 ChatMarkdown 渲染管线）。
    条目形状 = reducer 条目（详情来源补 result/tools；WS 实时条目无
    result/tools，渲染层判空兜底为占位）。
  -->
  <div data-testid="chat-subagent-detail" class="flex min-h-0 flex-1 flex-col">
    <!-- 状态头 -->
    <div class="shrink-0 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
      <div class="flex items-start gap-2">
        <div class="min-w-0 flex-1">
          <p data-testid="chat-subagent-detail-name" class="truncate text-sm font-semibold text-slate-900 dark:text-white">
            {{ subagent?.name || subagent?.taskId || '' }}
          </p>
          <p v-if="description" class="mt-0.5 line-clamp-2 text-xs text-slate-500 dark:text-slate-400">
            {{ description }}
          </p>
        </div>
        <span
          data-testid="chat-subagent-detail-status"
          class="flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium"
          :class="toneClass"
        >
          <span class="material-symbols-outlined text-sm leading-none" :class="{ 'animate-spin': meta.tone === 'running' }">{{ meta.icon }}</span>
          {{ t(meta.labelKey) }}
        </span>
      </div>
      <dl class="mt-2 grid grid-cols-2 gap-x-3 gap-y-1 text-xs">
        <div v-if="subagent?.currentStep" data-testid="chat-subagent-detail-step" class="min-w-0">
          <dt class="inline text-slate-400 dark:text-slate-500">{{ t('chat_subagentCurrentStep') }}：</dt>
          <dd class="inline truncate text-slate-700 dark:text-slate-300">{{ subagent.currentStep }}</dd>
        </div>
        <div v-if="subagent?.lastTool" data-testid="chat-subagent-detail-lasttool" class="min-w-0">
          <dt class="inline text-slate-400 dark:text-slate-500">{{ t('chat_subagentLastTool') }}：</dt>
          <dd class="inline text-slate-700 dark:text-slate-300">{{ subagent.lastTool }}</dd>
        </div>
        <div v-if="durationText" data-testid="chat-subagent-detail-duration" class="min-w-0">
          <dt class="inline text-slate-400 dark:text-slate-500">{{ t('chat_subagentDuration') }}：</dt>
          <dd class="inline text-slate-700 dark:text-slate-300">{{ durationText }}</dd>
        </div>
        <div v-if="tokensText" data-testid="chat-subagent-detail-tokens" class="min-w-0">
          <dt class="inline text-slate-400 dark:text-slate-500">{{ t('chat_subagentTokens') }}：</dt>
          <dd class="inline text-slate-700 dark:text-slate-300">{{ tokensText }}</dd>
        </div>
        <div v-if="Number(subagent?.toolUses) > 0" data-testid="chat-subagent-detail-tooluses" class="min-w-0">
          <dt class="inline text-slate-400 dark:text-slate-500">{{ t('chat_subagentToolUses') }}：</dt>
          <dd class="inline text-slate-700 dark:text-slate-300">{{ subagent.toolUses }}</dd>
        </div>
      </dl>
    </div>

    <!-- 工具时间线（详情 API 专供字段；WS 实时条目为空 → 占位文案） -->
    <div class="shrink-0 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
      <p class="text-xs font-semibold text-slate-400 uppercase tracking-wider dark:text-slate-500">
        {{ t('chat_subagentTimeline') }}
      </p>
      <ol v-if="tools.length" class="mt-2 space-y-1">
        <li
          v-for="(tool, i) in tools"
          :key="`${tool.ts || ''}-${i}`"
          data-testid="chat-subagent-detail-tool-item"
          class="flex items-start gap-1.5 text-xs leading-relaxed"
          :class="tool.isError ? 'text-red-600 dark:text-red-400' : 'text-slate-700 dark:text-slate-300'"
        >
          <span class="mt-0.5 shrink-0 material-symbols-outlined text-sm leading-none" :class="tool.isError ? 'text-red-500' : 'text-slate-400 dark:text-slate-500'">
            {{ tool.isError ? 'error' : 'build' }}
          </span>
          <span class="min-w-0">
            <span class="font-medium">{{ tool.tool }}</span>
            <span v-if="tool.summary" class="block break-words text-slate-500 dark:text-slate-400">{{ tool.summary }}</span>
            <span
              v-if="tool.isError"
              data-testid="chat-subagent-detail-tool-error"
              class="mt-0.5 block text-[11px] font-medium text-red-600 dark:text-red-400"
            >{{ t('chat_subagentToolError') }}</span>
          </span>
        </li>
      </ol>
      <p v-else class="mt-1 text-xs text-slate-400 dark:text-slate-500">{{ t('chat_subagentNoTools') }}</p>
    </div>

    <!-- 完整报告：result 优先（详情 API 专供），summary 兜底；运行中无报告 → 占位 -->
    <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3">
      <div class="flex items-center justify-between gap-2">
        <p class="text-xs font-semibold text-slate-400 uppercase tracking-wider dark:text-slate-500">
          {{ t('chat_subagentReport') }}
        </p>
        <button
          v-if="reportText"
          type="button"
          data-testid="chat-subagent-detail-copy"
          class="flex min-h-[40px] shrink-0 items-center gap-1 rounded px-2 text-xs text-slate-500 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
          :aria-live="copied ? 'polite' : undefined"
          @click="copyReport"
        >
          <span class="material-symbols-outlined text-sm leading-none">content_copy</span>
          {{ copied ? t('chat_subagentCopied') : t('chat_subagentCopyReport') }}
        </button>
      </div>
      <div v-if="reportText" data-testid="chat-subagent-detail-report" class="mt-1">
        <ChatMarkdown :content="reportText" />
      </div>
      <p v-else class="mt-2 text-xs text-slate-400 dark:text-slate-500">{{ t('chat_subagentReportPending') }}</p>
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { useI18n } from '../../i18n';
import { subagentPhaseMeta, formatSubagentDuration, formatSubagentTokens } from '../../utils/subagentPanel';
import ChatMarkdown from './ChatMarkdown.vue';

// 单条详情（纯渲染+复制交互）。tone 色调类集中在 toneClass：
// running=品牌绿脉冲、done=品牌绿静态、failed/interrupted=红/琥珀。
const props = defineProps({
  subagent: { type: Object, default: null },
});
defineEmits(['back']);
const { t } = useI18n();

const meta = computed(() => subagentPhaseMeta(props.subagent?.phase));

const toneClass = computed(() => ({
  running: 'bg-primary/10 text-primary',
  done: 'bg-primary/10 text-primary',
  failed: 'bg-red-100 text-red-600 dark:bg-red-900/40 dark:text-red-400',
  interrupted: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-400',
}[meta.value.tone] || 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400'));

const description = computed(() => {
  const d = props.subagent?.description;
  return typeof d === 'string' && d ? d : '';
});

const durationText = computed(() => formatSubagentDuration(props.subagent?.durationMs));
const tokensText = computed(() => formatSubagentTokens(props.subagent?.totalTokens));
const tools = computed(() => (Array.isArray(props.subagent?.tools) ? props.subagent.tools : []));

// 报告文本：result 优先，summary 兜底（completed 静默重拉补齐 result 后此
// computed 随条目引用替换自动刷新——面板按 taskId 换选中对象，不换对象引用）。
const reportText = computed(() => {
  const result = props.subagent?.result;
  if (typeof result === 'string' && result.trim()) return result;
  const summary = props.subagent?.summary;
  return typeof summary === 'string' && summary.trim() ? summary : '';
});

// 复制（http 局域网直连下 navigator.clipboard 不存在，退 execCommand——
// ChatMarkdown 同款兜底）；成功反馈停留后还原，卸载后回灌丢弃。
const COPIED_RESET_MS = 1500;
const copied = ref(false);
let copiedTimer = null;
let disposed = false;

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

function copyReport() {
  const text = reportText.value;
  if (!text) return;
  copyText(text).then(() => {
    if (disposed) return;
    copied.value = true;
    if (copiedTimer) clearTimeout(copiedTimer);
    copiedTimer = setTimeout(() => {
      copiedTimer = null;
      copied.value = false;
    }, COPIED_RESET_MS);
  });
}

onUnmounted(() => {
  disposed = true;
  if (copiedTimer) clearTimeout(copiedTimer);
});
</script>
