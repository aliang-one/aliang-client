<template>
  <div class="space-y-5">
    <!-- 标题 + 刷新 -->
    <div class="flex items-start justify-between gap-3">
      <div class="flex items-start gap-3">
        <span class="material-symbols-outlined mt-0.5 text-2xl text-primary">fact_check</span>
        <div class="min-w-0">
          <h3 class="text-xl font-semibold text-slate-900 dark:text-white">{{ t('qs_tab_backup') }}</h3>
          <p class="mt-0.5 truncate font-mono text-sm text-slate-500 dark:text-slate-400">{{ software }}</p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex min-h-8 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-[11px] font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
        :disabled="loading || restoring"
        @click="refresh"
      >
        <span class="material-symbols-outlined text-base">refresh</span>
        {{ t('qs_state_refresh') }}
      </button>
    </div>

    <!-- 首次加载 -->
    <div
      v-if="loading && !state"
      class="rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-5 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400"
    >
      {{ t('qs_state_loading') }}
    </div>

    <!-- 加载失败 + 重试 -->
    <div
      v-else-if="loadError"
      class="rounded-2xl border border-rose-200 bg-rose-50 px-5 py-6 dark:border-rose-900/40 dark:bg-rose-950/20"
    >
      <p class="text-sm font-semibold text-rose-800 dark:text-rose-200">{{ t('qs_state_error') }}</p>
      <p class="mt-2 text-sm leading-6 text-rose-700 dark:text-rose-300">{{ loadError }}</p>
      <button
        type="button"
        class="mt-3 inline-flex min-h-8 items-center justify-center gap-1 rounded-lg border border-rose-300 px-3 text-[11px] font-semibold text-rose-700 transition hover:bg-rose-100/60 dark:border-rose-800 dark:text-rose-300 dark:hover:bg-rose-950/40"
        @click="load"
      >
        <span class="material-symbols-outlined text-base">refresh</span>
        {{ t('qs_state_refresh') }}
      </button>
    </div>

    <template v-else-if="state">
      <!-- 文件列表 -->
      <div class="space-y-2">
        <div class="flex items-center justify-between gap-3">
          <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_state_title') }}</p>
          <span class="text-[11px] text-slate-400 dark:text-slate-500">{{ state.files.length }}</span>
        </div>
        <div
          v-for="file in state.files"
          :key="file.path"
          class="rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900"
        >
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1 px-3.5 py-2.5">
            <code class="min-w-0 flex-1 truncate font-mono text-[12px] text-slate-700 dark:text-slate-200" :title="file.path">
              {{ file.path }}
            </code>
            <span
              v-if="file.managed_by_aliang"
              class="inline-flex items-center rounded-md bg-emerald-50 px-1.5 py-0.5 text-[10px] font-medium text-emerald-600 dark:bg-emerald-950/40 dark:text-emerald-400"
            >
              {{ t('qs_state_managed') }}
            </span>
            <!-- absent 文件不渲染「未管理」徽标：不存在与未管理语义矛盾 -->
            <span
              v-else-if="file.exists"
              class="inline-flex items-center rounded-md bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-500 dark:bg-slate-800 dark:text-slate-400"
            >
              {{ t('qs_state_unmanaged') }}
            </span>
          </div>
          <div class="border-t border-slate-100 px-3.5 py-2.5 dark:border-slate-800">
            <p v-if="fileMeta(file)" class="mb-2 text-[11px] text-slate-400 dark:text-slate-500">{{ fileMeta(file) }}</p>
            <pre
              v-if="file.exists && file.content"
              class="code-editor max-h-64 overflow-auto rounded-xl border border-slate-200 bg-slate-950 px-3.5 py-2.5 text-[12px] leading-6 text-slate-100 custom-scrollbar dark:border-slate-700"
            >{{ file.content }}</pre>
            <!-- 后端语义：!exists=不存在；exists+size>0+无内容=超限或读失败（不再折叠成不存在）；exists+size=0=空文件 -->
            <p
              v-else-if="!file.exists"
              class="text-[12px] italic text-slate-400 dark:text-slate-500"
            >
              {{ t('qs_state_absent') }}
            </p>
            <p
              v-else-if="Number(file.size)"
              class="text-[12px] italic text-slate-400 dark:text-slate-500"
            >
              {{ t('qs_state_too_large') }}
            </p>
            <p v-else class="text-[12px] italic text-slate-400 dark:text-slate-500">
              {{ t('qs_state_empty_file') }}
            </p>
          </div>
        </div>
      </div>

      <!-- 原始备份区 -->
      <div class="rounded-2xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="flex items-center gap-2">
            <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_backup_original') }}</p>
            <span class="text-[11px] text-slate-400 dark:text-slate-500">{{ state.backups.length }}</span>
          </div>
          <button
            type="button"
            class="inline-flex min-h-8 items-center justify-center gap-1 rounded-lg bg-rose-600 px-3 text-[11px] font-semibold text-white transition hover:bg-rose-500 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="!state.backups.length || restoring"
            @click="openConfirm"
          >
            <span class="material-symbols-outlined text-base">restore</span>
            {{ t('qs_restore') }}
          </button>
        </div>
        <p v-if="!state.backups.length" class="mt-2 text-sm text-slate-400 dark:text-slate-500">
          {{ t('qs_no_backups') }}
        </p>
        <ul v-else class="mt-2 space-y-1.5">
          <li
            v-for="backup in state.backups"
            :key="`${backup.original_path}-${backup.backed_up_at}`"
            class="flex flex-wrap items-center gap-x-2 gap-y-1"
          >
            <code class="min-w-0 truncate font-mono text-[12px] text-slate-700 dark:text-slate-200" :title="backup.original_path">
              {{ backup.original_path }}
            </code>
            <span class="inline-flex items-center rounded-md bg-slate-100 px-1.5 py-0.5 text-[10px] font-medium text-slate-500 dark:bg-slate-800 dark:text-slate-400">
              {{ formatBackupKind(backup.kind) }}
            </span>
            <span v-if="formatTime(backup.backed_up_at)" class="text-[11px] text-slate-400 dark:text-slate-500">
              {{ formatTime(backup.backed_up_at) }}
            </span>
          </li>
        </ul>
      </div>

      <!-- 恢复反馈（轻量提示：常驻到下一次操作） -->
      <div
        v-if="restoreSuccess"
        class="rounded-xl border border-emerald-200 bg-emerald-50/70 px-3.5 py-2.5 text-[12px] font-medium leading-5 text-emerald-700 dark:border-emerald-900/40 dark:bg-emerald-950/20 dark:text-emerald-300"
      >
        <span class="mr-1.5 inline-flex align-[-3px]"><span class="material-symbols-outlined text-base">check_circle</span></span>
        {{ t('qs_restore_success') }}
      </div>
      <div
        v-else-if="restoreFailures.length"
        class="rounded-xl border border-rose-200 bg-rose-50/70 px-3.5 py-2.5 text-[12px] leading-5 text-rose-700 dark:border-rose-900/40 dark:bg-rose-950/20 dark:text-rose-300"
      >
        <p class="font-semibold">{{ t('qs_restore_failed') }}</p>
        <p
          v-for="failure in restoreFailures"
          :key="`failure-${failure.path}`"
          class="mt-1 break-all font-mono text-[11px]"
        >
          {{ failure.path }}: {{ failure.error }}
        </p>
      </div>
      <div
        v-else-if="restoreError"
        class="rounded-xl border border-rose-200 bg-rose-50/70 px-3.5 py-2.5 text-[12px] leading-5 text-rose-700 dark:border-rose-900/40 dark:bg-rose-950/20 dark:text-rose-300"
      >
        {{ restoreError }}
      </div>
    </template>

    <!-- 恢复确认弹窗（内嵌 overlay，层级高于 QuickSetupModal 的 z-[130]） -->
    <div
      v-if="showConfirm"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('qs_restore_confirm_title')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeConfirm"></div>
      <div class="relative z-10 w-full max-w-md rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('qs_restore_confirm_title') }}</h4>
        <p class="mt-2 text-[12px] leading-5 text-slate-500 dark:text-slate-400">{{ t('qs_restore_confirm_desc') }}</p>

        <div v-if="restoreTargets.restore.length" class="mt-3">
          <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_restore_will_restore') }}</p>
          <ul class="mt-1 space-y-1">
            <li v-for="path in restoreTargets.restore" :key="`restore-${path}`">
              <code class="block truncate font-mono text-[11px] text-slate-700 dark:text-slate-200" :title="path">{{ path }}</code>
            </li>
          </ul>
        </div>
        <div v-if="restoreTargets.remove.length" class="mt-3">
          <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-500">{{ t('qs_restore_will_delete') }}</p>
          <ul class="mt-1 space-y-1">
            <li v-for="path in restoreTargets.remove" :key="`remove-${path}`">
              <code class="block truncate font-mono text-[11px] text-slate-700 dark:text-slate-200" :title="path">{{ path }}</code>
            </li>
          </ul>
        </div>

        <div class="mt-5 flex items-center justify-end gap-2">
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
            :disabled="restoring"
            @click="closeConfirm"
          >
            {{ t('qs_cancel') }}
          </button>
          <button
            type="button"
            class="inline-flex min-h-9 items-center justify-center rounded-lg bg-rose-600 px-4 text-xs font-semibold text-white transition hover:bg-rose-500 disabled:cursor-not-allowed disabled:opacity-50"
            :disabled="restoring"
            @click="confirmRestore"
          >
            {{ restoring ? t('qs_state_restoring') : t('qs_restore_confirm') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, ref, watch } from 'vue';
import { fetchConfigState, restoreConfig } from '../services/quickSetupApi';
import { useI18n } from '../i18n';

const props = defineProps({
  // software code（如 "codex"）；仅对检测到已安装的内置 agent 渲染本组件（Modal 侧保证）
  software: {
    type: String,
    required: true,
  },
});
// 嵌套确认弹窗占用期（打开或恢复进行中）通知宿主 Modal，屏蔽外层 Esc 关闭
const emit = defineEmits(['confirm-open-change']);
const { t } = useI18n();

// 后端 QuickSetupConfigStateResponse（snake_case）：
// files: [{ path, exists, size, modified_at, format, content, managed_by_aliang }]
// backups: [{ original_path, backup_path, backed_up_at, sha256, kind }]
const state = ref(null);
const loading = ref(false);
const loadError = ref('');
const restoring = ref(false);
const showConfirm = ref(false);
const restoreSuccess = ref(false);
const restoreFailures = ref([]);
const restoreError = ref('');

// 卸载守卫：unmount 后确认/恢复流程不再写 ref（孤儿更新）；loadSeq 同步失效在途请求
let disposed = false;
// load 竞态守卫：software 快速切换时只允许最后一次请求提交结果
let loadSeq = 0;
async function load() {
  const seq = ++loadSeq;
  loading.value = true;
  loadError.value = '';
  try {
    const result = await fetchConfigState(props.software);
    if (seq !== loadSeq) {
      return;
    }
    state.value = {
      files: Array.isArray(result?.files) ? result.files : [],
      backups: Array.isArray(result?.backups) ? result.backups : [],
    };
  } catch (error) {
    if (seq !== loadSeq) {
      return;
    }
    state.value = null;
    loadError.value = error instanceof Error ? error.message : '';
  } finally {
    if (seq === loadSeq) {
      loading.value = false;
    }
  }
}

// 恢复反馈常驻到下一次操作（刷新/重新打开确认/再次恢复/切 software），不设自动消失定时器
function resetRestoreFeedback() {
  restoreSuccess.value = false;
  restoreFailures.value = [];
  restoreError.value = '';
}
function refresh() {
  resetRestoreFeedback();
  load();
}
function openConfirm() {
  resetRestoreFeedback();
  showConfirm.value = true;
}
function closeConfirm() {
  if (!restoring.value) {
    showConfirm.value = false;
  }
}
async function confirmRestore() {
  if (restoring.value) {
    return;
  }
  restoring.value = true;
  try {
    // 单文件失败不抛错（HTTP 200 + failed 数组）；整体失败（如 manifest 损坏）才走 catch
    const result = await restoreConfig(props.software);
    if (disposed) {
      return;
    }
    showConfirm.value = false;
    const failed = Array.isArray(result?.failed) ? result.failed : [];
    if (failed.length) {
      restoreSuccess.value = false;
      restoreFailures.value = failed;
    } else {
      restoreFailures.value = [];
      restoreSuccess.value = true;
    }
    await load();
    if (disposed) {
      return;
    }
  } catch (error) {
    if (disposed) {
      return;
    }
    showConfirm.value = false;
    restoreSuccess.value = false;
    // 优先展示具体错误信息（rawRequest 恒抛带 message 的 Error）；
    // 无 message 才回落通用文案，避免「部分文件恢复失败」误用于整体失败语义
    restoreError.value = error?.message || t('qs_restore_failed');
  } finally {
    restoring.value = false;
  }
}

// manifest 里 backup_path 为空 ⟺ existed_before=false（由 Aliang 新建，恢复时删除）
const restoreTargets = computed(() => {
  const restore = [];
  const remove = [];
  const seen = new Set();
  for (const backup of state.value?.backups || []) {
    if (!backup?.original_path || seen.has(backup.original_path)) {
      continue;
    }
    seen.add(backup.original_path);
    if (backup.backup_path) {
      restore.push(backup.original_path);
    } else {
      remove.push(backup.original_path);
    }
  }
  return { restore, remove };
});

function formatTime(value) {
  if (!value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return '';
  }
  return date.toLocaleString();
}
function formatSize(bytes) {
  const size = Number(bytes);
  if (!Number.isFinite(size) || size <= 0) {
    return '';
  }
  if (size < 1024) {
    return `${size} B`;
  }
  if (size < 1024 * 1024) {
    return `${(size / 1024).toFixed(1)} KB`;
  }
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
function fileMeta(file) {
  const parts = [formatSize(file.size), formatTime(file.modified_at)].filter(Boolean);
  return parts.join(' · ');
}

// 备份种类徽标：已知 kind 走 i18n，未知 kind 回落原值
const backupKindLabelKeys = {
  original: 'qs_backup_kind_original',
};
function formatBackupKind(kind) {
  const key = backupKindLabelKeys[kind];
  return key ? t(key) : kind;
}

// 确认弹窗打开或恢复进行中都视为嵌套弹窗占用期，宿主 Modal 据此屏蔽 Esc 关闭
const confirmOverlayActive = computed(() => showConfirm.value || restoring.value);
watch(confirmOverlayActive, (value) => {
  emit('confirm-open-change', value);
});

watch(
  () => props.software,
  () => {
    showConfirm.value = false;
    resetRestoreFeedback();
    load();
  },
  { immediate: true },
);
onUnmounted(() => {
  disposed = true;
  loadSeq += 1;
  emit('confirm-open-change', false);
});
</script>
