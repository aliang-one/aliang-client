<template>
  <div
    v-if="state.open"
    class="fixed inset-0 z-[1100] flex items-center justify-center bg-slate-950/45 p-4 backdrop-blur-sm"
    data-testid="error-dialog"
    @click.self="close"
  >
    <div
      role="alertdialog"
      aria-modal="true"
      :aria-label="t('error_dialog_title')"
      class="w-full max-w-md rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-800 dark:bg-slate-950"
    >
      <div class="flex items-start gap-3 px-5 py-4">
        <span class="material-symbols-outlined mt-0.5 text-2xl text-rose-500">error</span>
        <div class="min-w-0">
          <p class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('error_dialog_title') }}</p>
          <p
            data-testid="error-dialog-message"
            class="mt-1.5 break-words text-xs leading-5 text-slate-600 dark:text-slate-300"
          >{{ state.message }}</p>
        </div>
      </div>
      <div class="flex justify-end border-t border-slate-200 px-5 py-3 dark:border-slate-800">
        <button
          type="button"
          data-testid="error-dialog-ok"
          class="inline-flex min-h-10 items-center justify-center rounded-lg border border-slate-200 px-4 text-xs font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
          @click="close"
        >
          {{ t('error_dialog_ok') }}
        </button>
      </div>
    </div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted } from 'vue';
import { useI18n } from '../i18n';
import { closeErrorDialog, errorDialogState } from '../composables/useErrorDialog';

const { t } = useI18n();
const state = errorDialogState();

function close() {
  closeErrorDialog();
}

// Esc 关闭与仓库其他模态(CertManagementModal/滚回面板)同约定
function onKeydown(e) {
  if (e.key === 'Escape' && state.open) {
    close();
  }
}
onMounted(() => window.addEventListener('keydown', onKeydown));
onUnmounted(() => window.removeEventListener('keydown', onKeydown));
</script>
