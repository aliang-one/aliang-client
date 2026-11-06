<template>
  <!-- composer 底部工具行（2026-11-05,学 Cursor/VSCode）：权限模式 / 模型 /
       effort 三个 chip。单入口纪律:顶栏 ChatSettingsBar 的模型/effort 选择器
       已随本组件迁入移除,状态源唯一(conversation 经 applySessionSettings)。
       绝对定位浮层挂本组件根节点:relative 在外层,防上游 overflow 裁剪
       (ChatSettingsBar 目录面板同款教训)。 -->
  <div class="relative mt-2 flex flex-wrap items-center gap-1.5 text-xs">
    <!-- 权限模式 chip -->
    <div class="relative">
      <button
        type="button"
        data-testid="chat-approval-mode-chip"
        class="flex min-h-[28px] items-center gap-1 rounded-full border px-2.5 font-semibold transition"
        :class="bypassActive
          ? 'border-red-300 bg-red-50 text-red-600 dark:border-red-500/40 dark:bg-red-900/30 dark:text-red-300'
          : 'border-slate-200 bg-white text-slate-600 hover:border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700'"
        :disabled="settingsDisabled"
        :aria-expanded="openPanel === 'mode' ? 'true' : 'false'"
        :aria-label="t('chat_approvalMode')"
        @click="togglePanel('mode')"
      >
        <span class="material-symbols-outlined text-sm leading-none">shield</span>
        <span>{{ modeLabel }}</span>
        <span class="material-symbols-outlined text-xs leading-none">expand_more</span>
      </button>
      <div
        v-if="openPanel === 'mode'"
        class="absolute bottom-full left-0 z-30 mb-2 w-56 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-600 dark:bg-slate-800"
      >
        <button
          v-for="opt in APPROVAL_MODES"
          :key="opt.value"
          type="button"
          class="flex w-full items-center justify-between px-3 py-2 text-left text-xs transition hover:bg-primary/10"
          :class="approvalMode === opt.value ? 'font-bold text-primary' : 'text-slate-600 dark:text-slate-300'"
          @click="pickMode(opt.value)"
        >
          <span>{{ t(opt.labelKey) }}</span>
          <span v-if="approvalMode === opt.value" class="material-symbols-outlined text-sm">check</span>
        </button>
      </div>
    </div>

    <!-- 模型 chip（自 ChatSettingsBar 迁入,含自定义输入） -->
    <div class="relative">
      <button
        type="button"
        data-testid="chat-composer-model"
        class="flex min-h-[28px] items-center gap-1 rounded-full border border-slate-200 bg-white px-2.5 font-semibold text-slate-600 transition hover:border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
        :disabled="settingsDisabled"
        :aria-expanded="openPanel === 'model' ? 'true' : 'false'"
        :aria-label="t('chat_settingsModel')"
        @click="togglePanel('model')"
      >
        <span class="material-symbols-outlined text-sm leading-none"> neurology </span>
        <span class="max-w-[10rem] truncate">{{ modelChipLabel }}</span>
        <span class="material-symbols-outlined text-xs leading-none">expand_more</span>
      </button>
      <div
        v-if="openPanel === 'model'"
        class="absolute bottom-full left-0 z-30 mb-2 w-56 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-600 dark:bg-slate-800"
      >
        <button
          v-for="opt in modelOptions"
          :key="opt.value"
          type="button"
          class="flex w-full items-center justify-between px-3 py-2 text-left text-xs transition hover:bg-primary/10"
          :class="modelSelectValue === opt.value ? 'font-bold text-primary' : 'text-slate-600 dark:text-slate-300'"
          @click="onModelSelect(opt.value)"
        >
          <span class="truncate">{{ opt.label }}</span>
          <span v-if="modelSelectValue === opt.value" class="material-symbols-outlined text-sm">check</span>
        </button>
        <input
          v-if="modelCustomActive"
          v-model="modelCustomDraft"
          type="text"
          class="w-full border-t border-slate-100 bg-transparent px-3 py-2 font-mono text-xs outline-none placeholder:text-slate-400 dark:border-slate-700"
          :placeholder="t('chat_customModel')"
          @keydown.enter="onCustomModelEnter"
          @blur="commitCustomModel"
        />
      </div>
    </div>

    <!-- effort chip -->
    <div class="relative">
      <button
        type="button"
        data-testid="chat-composer-effort"
        class="flex min-h-[28px] items-center gap-1 rounded-full border border-slate-200 bg-white px-2.5 font-semibold text-slate-600 transition hover:border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700"
        :disabled="settingsDisabled"
        :aria-expanded="openPanel === 'effort' ? 'true' : 'false'"
        :aria-label="t('chat_settingsEffort')"
        @click="togglePanel('effort')"
      >
        <span class="material-symbols-outlined text-sm leading-none">speed</span>
        <span>{{ effortLabel }}</span>
        <span class="material-symbols-outlined text-xs leading-none">expand_more</span>
      </button>
      <div
        v-if="openPanel === 'effort'"
        class="absolute bottom-full left-0 z-30 mb-2 w-40 overflow-hidden rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-600 dark:bg-slate-800"
      >
        <button
          v-for="opt in EFFORT_OPTIONS"
          :key="opt.value"
          type="button"
          class="flex w-full items-center justify-between px-3 py-2 text-left text-xs transition hover:bg-primary/10"
          :class="effort === opt.value ? 'font-bold text-primary' : 'text-slate-600 dark:text-slate-300'"
          @click="pickEffort(opt.value)"
        >
          <span>{{ opt.value || t('chat_modelDefault') }}</span>
          <span v-if="effort === opt.value" class="material-symbols-outlined text-sm">check</span>
        </button>
      </div>
    </div>

    <!-- 「下轮生效」角标随行(与顶栏 badge 同源 conversation.pendingConfig) -->
    <span
      v-if="pendingConfig"
      data-testid="chat-effort-pending"
      class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
    >{{ t('chat_effortPending') }}</span>
  </div>
</template>

<script setup>
import { computed, ref } from 'vue';
import { useI18n } from '../../i18n';
import { isComposingEvent } from '../../composables/useAliangChat';
import {
  AGENTCORE_MODEL_OPTIONS,
  APPROVAL_MODES,
  EFFORT_OPTIONS,
  MODEL_CUSTOM_VALUE,
  normalizeApprovalMode,
} from '../../utils/chatSettings';

const props = defineProps({
  conversation: { type: Object, default: null },
  // settingsDisabled(applySessionSettings 的 running 守卫源=activeRunning):
  // 与 ChatComposer 的 running prop(发送/停止交替语义)刻意分开——后者含
  // 本地流式气泡并集,语义不同,勿合并。
  settingsDisabled: { type: Boolean, default: false },
  pendingConfig: { type: Boolean, default: false },
});

const emit = defineEmits(['apply-settings']);

const { t } = useI18n();

const openPanel = ref('');
const modelCustomActive = ref(false);
const modelCustomDraft = ref('');

function togglePanel(name) {
  openPanel.value = openPanel.value === name ? '' : name;
}

const approvalMode = computed(() => normalizeApprovalMode(props.conversation?.approvalMode));
const bypassActive = computed(() => approvalMode.value === 'bypass_all');

const modeLabel = computed(() => {
  const opt = APPROVAL_MODES.find((o) => o.value === approvalMode.value);
  return t(opt ? opt.labelKey : 'chat_approvalModeDefault');
});

function pickMode(value) {
  openPanel.value = '';
  if (props.settingsDisabled) return;
  if (value === 'bypass_all' && approvalMode.value !== 'bypass_all') {
    // 放过所有跳过手机审批卡——切换必须过一次显式确认(安全件)。
    if (!window.confirm(t('chat_approvalModeBypassConfirm'))) return;
  }
  emit('apply-settings', { approvalMode: value });
}

const modelOptions = computed(() => AGENTCORE_MODEL_OPTIONS.map((o) => ({
  ...o,
  label: o.value === '' ? t('chat_modelDefault')
    : o.value === MODEL_CUSTOM_VALUE ? t('chat_customModel')
    : o.label,
})));

// select 当前值:预设模型→自身;非预设自定义值→哨兵(输入框承接显示)。
const modelSelectValue = computed(() => {
  const m = props.conversation?.model || '';
  if (!m) return '';
  return isPresetModel(m) ? m : MODEL_CUSTOM_VALUE;
});

const modelChipLabel = computed(() => props.conversation?.model || t('chat_modelDefault'));

function isPresetModel(m) {
  return AGENTCORE_MODEL_OPTIONS.some((o) => o.value === m && o.value !== MODEL_CUSTOM_VALUE);
}

function onModelSelect(value) {
  openPanel.value = '';
  const conv = props.conversation;
  if (!conv || props.settingsDisabled) return;
  if (value === MODEL_CUSTOM_VALUE) {
    modelCustomDraft.value = conv.model && !isPresetModel(conv.model) ? conv.model : '';
    modelCustomActive.value = true;
    return;
  }
  modelCustomActive.value = false;
  emit('apply-settings', { model: value });
}

function commitCustomModel() {
  const conv = props.conversation;
  if (!conv) return;
  const model = modelCustomDraft.value.trim();
  modelCustomActive.value = false;
  if (!model) return; // 空输入视作取消，保持原值
  emit('apply-settings', { model });
}

function onCustomModelEnter(e) {
  if (isComposingEvent(e)) return;
  e.preventDefault();
  commitCustomModel();
}

const effortLabel = computed(() => props.conversation?.effort || t('chat_modelDefault'));

function pickEffort(value) {
  openPanel.value = '';
  if (props.settingsDisabled) return;
  emit('apply-settings', { effort: value });
}
</script>
