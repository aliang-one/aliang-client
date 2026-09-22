<template>
  <div class="space-y-5">
    <!-- 标题区 -->
    <div class="flex items-start gap-3">
      <span class="material-symbols-outlined mt-0.5 text-2xl text-emerald-500">check_circle</span>
      <div class="min-w-0">
        <h3 class="text-xl font-semibold text-slate-900 dark:text-white">{{ t('qs_result_title') }}</h3>
        <p class="mt-0.5 truncate text-sm text-slate-500 dark:text-slate-400">{{ softwareName }}</p>
      </div>
    </div>

    <!-- 写入清单 -->
    <div class="rounded-2xl border border-slate-200 bg-slate-50/70 p-4 dark:border-slate-800 dark:bg-slate-950/40">
      <div class="flex items-center justify-between gap-3">
        <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_written_files') }}</p>
        <span class="text-[11px] text-slate-400 dark:text-slate-500">{{ writtenPaths.length }}</span>
      </div>
      <ul class="mt-2 space-y-1">
        <li
          v-for="path in writtenPaths"
          :key="path"
          class="flex items-center justify-between gap-2"
        >
          <code class="min-w-0 flex-1 truncate font-mono text-[12px] leading-6 text-slate-700 dark:text-slate-200" :title="path">
            {{ path }}
          </code>
          <button
            type="button"
            :aria-label="t('qs_copyPath')"
            class="inline-flex size-7 shrink-0 items-center justify-center rounded-lg text-slate-400 transition hover:bg-slate-200/70 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            @click="copyValue(path, path)"
          >
            <span class="material-symbols-outlined text-base">
              {{ copiedKey === path ? 'check' : 'content_copy' }}
            </span>
          </button>
        </li>
      </ul>
    </div>

    <!-- 备份卡片 -->
    <div class="rounded-2xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
      <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_backed_up') }}</p>
      <p v-if="!backupEntries.length" class="mt-2 text-sm text-slate-400 dark:text-slate-500">
        {{ t('qs_no_backups') }}
      </p>
      <ul v-else class="mt-2 space-y-1.5">
        <li
          v-for="backup in backupEntries"
          :key="backup.original_path"
          class="flex flex-wrap items-center gap-x-2 gap-y-1"
        >
          <code class="min-w-0 truncate font-mono text-[12px] text-slate-700 dark:text-slate-200" :title="backup.original_path">
            {{ backup.original_path }}
          </code>
          <span class="material-symbols-outlined text-base text-slate-300 dark:text-slate-600">arrow_right_alt</span>
          <template v-if="backup.existed_before">
            <code class="min-w-0 truncate font-mono text-[12px] text-slate-500 dark:text-slate-400" :title="backup.backup_path">
              {{ backup.backup_path }}
            </code>
          </template>
          <span
            v-else
            class="inline-flex items-center rounded-md bg-emerald-50 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:bg-emerald-950/40 dark:text-emerald-400"
          >
            {{ t('qs_result_new_file') }}
          </span>
        </li>
      </ul>
    </div>

    <!-- 最终内容 -->
    <div class="space-y-2">
      <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_final_content') }}</p>
      <details
        v-for="file in appliedFiles"
        :key="file.path"
        open
        class="group rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
      >
        <summary class="flex cursor-pointer list-none items-center gap-2 px-3.5 py-2.5 [&::-webkit-details-marker]:hidden">
          <span class="material-symbols-outlined text-base text-slate-400 transition group-open:rotate-90">chevron_right</span>
          <code class="min-w-0 flex-1 truncate font-mono text-[12px] text-slate-700 dark:text-slate-200" :title="file.path">
            {{ file.path }}
          </code>
        </summary>
        <div class="border-t border-slate-100 px-3.5 py-2.5 dark:border-slate-800">
          <div class="mb-1.5 flex justify-end">
            <button
              type="button"
              class="inline-flex min-h-8 items-center justify-center rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
              @click="copyValue(file.content, file.path)"
            >
              {{ copiedKey === file.path ? t('qs_copied_short') : t('qs_copyFile') }}
            </button>
          </div>
          <pre class="code-editor max-h-64 overflow-auto rounded-xl border border-slate-200 bg-slate-950 px-3.5 py-2.5 text-[12px] leading-6 text-slate-100 custom-scrollbar dark:border-slate-700">{{ file.content }}</pre>
        </div>
      </details>
    </div>

    <!-- 底部按钮行 -->
    <div class="flex items-center justify-end gap-2 border-t border-slate-100 pt-4 dark:border-slate-800">
      <button
        type="button"
        class="inline-flex min-h-10 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
        @click="emit('back')"
      >
        {{ t('qs_result_back') }}
      </button>
      <button
        type="button"
        class="inline-flex min-h-10 items-center justify-center rounded-lg bg-primary px-4 text-xs font-semibold text-white transition hover:bg-primary/90"
        @click="emit('view-current')"
      >
        {{ t('qs_view_current') }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { useI18n } from '../i18n';

const props = defineProps({
  // 展示名（如 "Codex"）
  softwareName: {
    type: String,
    default: '',
  },
  // 后端 result.written：写入的绝对路径
  written: {
    type: Array,
    default: () => [],
  },
  // 后端 result.backups：[{ original_path, backup_path, existed_before }]（snake_case）
  backups: {
    type: Array,
    default: () => [],
  },
  // 应用的最终文件 [{ path, content }]
  files: {
    type: Array,
    default: () => [],
  },
});
const emit = defineEmits(['back', 'view-current']);
const { t } = useI18n();

const writtenPaths = computed(() => (Array.isArray(props.written) ? props.written : []));
const backupEntries = computed(() => (Array.isArray(props.backups) ? props.backups : []));
const appliedFiles = computed(() => (Array.isArray(props.files) ? props.files : []));

// 复制反馈：短暂高亮已复制项（面板独立于 Modal 的 statusMessage，用局部状态即可）
const copiedKey = ref('');
let copiedTimer = null;
async function copyValue(value, key) {
  try {
    await navigator.clipboard.writeText(String(value ?? ''));
    copiedKey.value = key;
    if (copiedTimer) {
      window.clearTimeout(copiedTimer);
    }
    copiedTimer = window.setTimeout(() => {
      copiedTimer = null;
      copiedKey.value = '';
    }, 1600);
  } catch (error) {
    copiedKey.value = '';
  }
}
onUnmounted(() => {
  if (copiedTimer) {
    window.clearTimeout(copiedTimer);
  }
});
</script>
