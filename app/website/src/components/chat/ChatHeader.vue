<template>
  <!-- Header -->
  <div class="flex items-center gap-2 border-b border-slate-200 px-4 py-3 dark:border-slate-700">
    <button
      type="button"
      class="rounded p-1 text-slate-500 hover:bg-slate-100 md:hidden dark:hover:bg-slate-800"
      @click="emit('open-sidebar')"
    >
      <span class="material-symbols-outlined text-lg">menu</span>
    </button>
    <input
      v-if="activeId && renamingId === activeId && renameSurface === 'header'"
      v-model="renameModel"
      :ref="setRenameInput"
      type="text"
      maxlength="60"
      :placeholder="t('chat_renamePlaceholder')"
      class="min-w-0 flex-1 rounded border border-primary/40 bg-white px-2 py-1 text-sm outline-none dark:bg-slate-800"
      @click.stop
      @keydown.enter="onRenameEnter"
      @keydown.esc="onRenameEscape"
      @blur="emit('rename-commit')"
    />
    <h3 v-else class="flex-1 truncate text-sm font-semibold text-slate-900 dark:text-white">
      {{ conversation?.title || t('chat_emptyTitle') }}
    </h3>
    <!-- 设备会话不可在本端重命名/删除（后端无端点，title 由清单权威） -->
    <button
      v-if="activeId && renamingId !== activeId && !activeIsDevice"
      type="button"
      class="relative rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-slate-600 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 dark:hover:bg-slate-800 before:absolute before:-inset-2 before:content-['']"
      :aria-label="t('chat_rename')"
      @click="emit('rename-start', conversation, 'header')"
    >
      <span class="material-symbols-outlined text-lg">edit</span>
    </button>
    <span
      v-if="activeId && activeRunning"
      class="inline-flex items-center gap-1 rounded-full bg-primary/10 px-2 py-0.5 text-xs font-medium text-primary"
    >
      <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
    </span>
    <button
      v-if="activeId && !activeIsDevice"
      type="button"
      class="rounded p-1 text-slate-400 hover:bg-slate-100 hover:text-rose-500 dark:hover:bg-slate-800"
      @click="emit('delete')"
    >
      <span class="material-symbols-outlined text-lg">delete</span>
    </button>
    <button
      type="button"
      class="rounded p-1 text-slate-500 hover:bg-slate-100 dark:hover:bg-slate-800"
      @click="emit('close')"
    >
      <span class="material-symbols-outlined text-lg">close</span>
    </button>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';
import { isComposingEvent } from '../../composables/useAliangChat';

const props = defineProps({
  conversation: { type: Object, default: null },
  activeId: { type: String, default: null },
  activeIsDevice: { type: Boolean, default: false },
  activeRunning: { type: Boolean, default: false },
  // 顶栏编辑态判定需要原始 renamingId/renameSurface（编辑按钮的
  // `renamingId !== activeId` 条件在「列表在编、顶栏非在编」时也必须成立，
  // 单个 renaming 布尔推导不出该语义——与拆分前判定逐字一致）。
  renamingId: { type: String, default: '' },
  renameSurface: { type: String, default: '' },
  renameDraft: { type: String, default: '' },
  setRenameInput: { type: Function, required: true },
});

const emit = defineEmits([
  'open-sidebar',
  'rename-start',
  'rename-commit',
  'rename-cancel',
  'rename-draft',
  'delete',
  'close',
]);

const { t } = useI18n();

// 同 ChatSidebar：经 computed 承接原生 v-model，保留 IME 组合期守卫语义。
const renameModel = computed({
  get: () => props.renameDraft,
  set: (v) => emit('rename-draft', v),
});

function onRenameEnter(e) {
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  emit('rename-commit');
}

function onRenameEscape(e) {
  if (isComposingEvent(e)) {
    e.stopPropagation();
    return;
  }
  e.preventDefault();
  e.stopPropagation();
  emit('rename-cancel');
}
</script>
