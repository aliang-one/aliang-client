<template>
  <div v-if="noticeVisible" class="pointer-events-none fixed right-5 top-5 z-[1050] flex flex-col items-end gap-3">
    <button
      type="button"
      class="pointer-events-auto inline-flex items-center gap-3 rounded-2xl border px-4 py-3 shadow-xl backdrop-blur-md transition hover:-translate-y-0.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-offset-2 dark:focus-visible:ring-offset-slate-950"
      :class="indicatorClass"
      :aria-label="indicatorAriaLabel"
      @click="openSoftwareUpdateModal"
    >
      <span class="relative flex size-11 items-center justify-center rounded-xl" :class="iconWrapClass">
        <span class="material-symbols-outlined text-[22px]">{{ indicatorIcon }}</span>
        <span class="absolute -right-1 -top-1 inline-flex size-3 rounded-full" :class="dotClass"></span>
      </span>
      <span class="min-w-0 text-left">
        <span class="block text-[11px] font-bold uppercase tracking-[0.24em]" :class="eyebrowClass">{{ t('update_softwareUpdate') }}</span>
        <span class="mt-1 block text-sm font-semibold leading-5" :class="titleClass">{{ indicatorTitle }}</span>
        <span class="mt-0.5 block text-xs leading-5" :class="subtitleClass">{{ indicatorSubtitle }}</span>
      </span>
    </button>

    <div
      v-if="softwareUpdateModalOpen"
      class="pointer-events-auto fixed inset-0 z-[1060] flex items-center justify-center bg-slate-950/72 p-4 backdrop-blur-sm"
      @click.self="handleOverlayClose"
    >
      <div class="w-full max-w-2xl overflow-hidden rounded-[28px] border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
        <div class="relative overflow-hidden border-b border-slate-200 dark:border-slate-700">
          <div class="absolute inset-0 bg-[radial-gradient(circle_at_top_right,_rgba(248,113,113,0.22),_transparent_45%),radial-gradient(circle_at_left,_rgba(251,191,36,0.18),_transparent_38%)]"></div>
          <div class="relative px-6 py-6 sm:px-7">
            <div class="flex items-start justify-between gap-4">
              <div class="max-w-xl">
                <p class="text-xs font-bold uppercase tracking-[0.28em]" :class="eyebrowClass">{{ t('update_versionAlert') }}</p>
                <h3 class="mt-3 text-2xl font-semibold tracking-tight text-slate-900 dark:text-slate-50">
                  {{ modalTitle }}
                </h3>
                <p class="mt-3 text-sm leading-6 text-slate-600 dark:text-slate-300">
                  {{ modalSummary }}
                </p>
              </div>
              <button
                v-if="!softwareUpdateStatus.force_update"
                type="button"
                class="rounded-xl p-2 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-slate-800 dark:hover:text-slate-100"
                :disabled="softwareUpdateDismissPending"
                :aria-label="t('update_closeAria')"
                @click="handleDismiss"
              >
                <span class="material-symbols-outlined text-xl">close</span>
              </button>
            </div>
            <div class="mt-5 flex flex-wrap items-center gap-3">
              <span class="rounded-full px-3 py-1 text-xs font-bold uppercase tracking-[0.22em]" :class="badgeClass">
                {{ badgeText }}
              </span>
              <span class="rounded-full border border-slate-200 px-3 py-1 text-xs font-semibold text-slate-600 dark:border-slate-700 dark:text-slate-300">
                {{ t('update_current', { version: softwareUpdateStatus.current_version || '--' }) }}
              </span>
              <span class="rounded-full border border-slate-200 px-3 py-1 text-xs font-semibold text-slate-600 dark:border-slate-700 dark:text-slate-300">
                {{ t('update_latest', { version: softwareUpdateStatus.latest_version || '--' }) }}
              </span>
            </div>
          </div>
        </div>

        <div class="space-y-5 px-6 py-6 sm:px-7">
          <div class="grid gap-4 sm:grid-cols-2">
            <div class="rounded-2xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-700 dark:bg-slate-800/60">
              <p class="text-[11px] font-bold uppercase tracking-[0.24em] text-slate-400">{{ t('update_statusTitle') }}</p>
              <p class="mt-2 text-base font-semibold text-slate-900 dark:text-slate-100">{{ modalStatusTitle }}</p>
              <p class="mt-2 text-sm leading-6 text-slate-600 dark:text-slate-300">{{ modalStatusDescription }}</p>
            </div>
            <div class="rounded-2xl border p-4" :class="softwareUpdateStatus.force_update ? 'border-red-200 bg-red-50/80 dark:border-red-500/30 dark:bg-red-500/10' : 'border-amber-200 bg-amber-50/80 dark:border-amber-500/30 dark:bg-amber-500/10'">
              <p class="text-[11px] font-bold uppercase tracking-[0.24em]" :class="softwareUpdateStatus.force_update ? 'text-red-500' : 'text-amber-500'">{{ t('update_proxyGuard') }}</p>
              <p class="mt-2 text-base font-semibold text-slate-900 dark:text-slate-100">{{ proxyGuardTitle }}</p>
              <p class="mt-2 text-sm leading-6 text-slate-600 dark:text-slate-300">{{ proxyGuardDescription }}</p>
            </div>
          </div>

          <div class="rounded-2xl border border-slate-200 bg-white p-4 dark:border-slate-700 dark:bg-slate-950/40">
            <div class="flex items-center justify-between gap-3">
              <p class="text-sm font-semibold text-slate-900 dark:text-slate-100">{{ t('update_releaseNotes') }}</p>
              <span class="text-[11px] font-medium text-slate-400">{{ softwareUpdateStatus.file_type || 'installer' }}</span>
            </div>
            <div class="mt-3 rounded-2xl bg-slate-50 px-4 py-4 text-sm leading-7 text-slate-600 dark:bg-slate-800/60 dark:text-slate-300">
              <template v-if="softwareUpdateStatus.changelog">
                <p class="whitespace-pre-wrap break-words">{{ softwareUpdateStatus.changelog }}</p>
              </template>
              <template v-else>
                <p>{{ t('update_noChangelog') }}</p>
              </template>
            </div>
          </div>

          <p v-if="softwareUpdateError" class="rounded-2xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200">
            {{ softwareUpdateError }}
          </p>

          <div class="flex flex-col-reverse gap-3 sm:flex-row sm:items-center sm:justify-between">
            <button
              v-if="!softwareUpdateStatus.force_update"
              type="button"
              class="inline-flex h-12 items-center justify-center rounded-2xl border border-slate-200 px-5 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
              :disabled="softwareUpdateDismissPending"
              @click="handleDismiss"
            >
              {{ softwareUpdateDismissPending ? t('update_processing') : softwareUpdateStatus.dismissed ? t('update_closeWindow') : t('update_skipForNow') }}
            </button>
            <div class="flex flex-1 justify-end">
              <a
                class="inline-flex h-12 items-center justify-center rounded-2xl px-5 text-sm font-semibold text-white shadow-lg transition hover:opacity-95"
                :class="softwareUpdateStatus.force_update ? 'bg-red-500 shadow-red-500/30' : 'bg-primary shadow-primary/30'"
                :href="softwareUpdateStatus.download_url || '#'"
                target="_blank"
                rel="noopener noreferrer"
              >
                {{ softwareUpdateStatus.force_update ? t('update_downloadNow') : t('update_downloadUpdate') }}
              </a>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted } from 'vue';
import { useSoftwareUpdate } from '../composables/useSoftwareUpdate';
import { useI18n } from '../i18n';

const {
  softwareUpdateStatus,
  softwareUpdateError,
  softwareUpdateLoaded,
  softwareUpdateModalOpen,
  softwareUpdateDismissPending,
  refreshSoftwareUpdateStatus,
  openSoftwareUpdateModal,
  closeSoftwareUpdateModal
} = useSoftwareUpdate();

const { t } = useI18n();

const indicatorVisible = computed(() => softwareUpdateStatus.value.indicator_visible);
const noticeVisible = computed(() => indicatorVisible.value || softwareUpdateModalOpen.value);

const indicatorClass = computed(() => (
  softwareUpdateStatus.value.force_update
    ? 'border-red-200 bg-white/96 text-red-600 dark:border-red-500/30 dark:bg-slate-900/95 dark:text-red-200'
    : 'border-amber-200 bg-white/96 text-amber-600 dark:border-amber-500/30 dark:bg-slate-900/95 dark:text-amber-200'
));

const iconWrapClass = computed(() => (
  softwareUpdateStatus.value.force_update
    ? 'bg-red-100 text-red-600 dark:bg-red-500/15 dark:text-red-200'
    : 'bg-amber-100 text-amber-600 dark:bg-amber-500/15 dark:text-amber-200'
));

const dotClass = computed(() => (
  softwareUpdateStatus.value.force_update ? 'bg-red-500' : 'bg-amber-500'
));

const eyebrowClass = computed(() => (
  softwareUpdateStatus.value.force_update ? 'text-red-500' : 'text-amber-500'
));

const titleClass = computed(() => (
  softwareUpdateStatus.value.force_update ? 'text-slate-900 dark:text-slate-50' : 'text-slate-900 dark:text-slate-50'
));

const subtitleClass = computed(() => 'text-slate-500 dark:text-slate-300');

const indicatorIcon = computed(() => (
  softwareUpdateStatus.value.force_update ? 'system_update_alt' : 'notifications_active'
));

const indicatorTitle = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatoryVersion', { version: softwareUpdateStatus.value.latest_version || 'update' })
    : t('update_newVersion', { version: softwareUpdateStatus.value.latest_version || '' }).trim()
));

const indicatorSubtitle = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatorySubtitle')
    : softwareUpdateStatus.value.dismissed
      ? t('update_dismissedSubtitle')
      : t('update_availableSubtitle')
));

const indicatorAriaLabel = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatoryBadge')
    : t('update_softwareUpdate')
));

const modalTitle = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatoryTitle', { version: softwareUpdateStatus.value.latest_version || '...' })
    : t('update_availableTitle', { version: softwareUpdateStatus.value.latest_version || '...' })
));

const modalSummary = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatorySummary')
    : t('update_availableSummary')
));

const badgeText = computed(() => (
  softwareUpdateStatus.value.force_update ? t('update_mandatoryBadge') : t('update_availableBadge')
));

const badgeClass = computed(() => (
  softwareUpdateStatus.value.force_update
    ? 'bg-red-500/10 text-red-500'
    : 'bg-amber-500/10 text-amber-600 dark:text-amber-300'
));

const modalStatusTitle = computed(() => (
  softwareUpdateStatus.value.force_update ? t('update_mandatoryStatusTitle') : t('update_availableStatusTitle')
));

const modalStatusDescription = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_mandatoryStatusDesc')
    : softwareUpdateStatus.value.dismissed
      ? t('update_availableStatusDismissed')
      : t('update_availableStatusActive')
));

const proxyGuardTitle = computed(() => (
  softwareUpdateStatus.value.force_update ? t('update_proxyBlocked') : t('update_proxyAvailable')
));

const proxyGuardDescription = computed(() => (
  softwareUpdateStatus.value.force_update
    ? t('update_proxyBlockedDesc')
    : t('update_proxyAvailableDesc')
));

async function handleDismiss() {
  try {
    await closeSoftwareUpdateModal();
  } catch (_) {
    // Error state is already reflected by the composable.
  }
}

function handleOverlayClose() {
  if (softwareUpdateStatus.value.force_update) {
    return;
  }
  void handleDismiss();
}

let refreshRetryTimer = null;
let refreshPollTimer = null;

function refreshUpdateStatusSilently() {
  refreshSoftwareUpdateStatus().catch(() => {});
}

onMounted(() => {
  refreshUpdateStatusSilently();
  refreshRetryTimer = window.setTimeout(refreshUpdateStatusSilently, 3000);
  refreshPollTimer = window.setInterval(refreshUpdateStatusSilently, 60000);
});

onBeforeUnmount(() => {
  if (refreshRetryTimer !== null) {
    window.clearTimeout(refreshRetryTimer);
    refreshRetryTimer = null;
  }
  if (refreshPollTimer !== null) {
    window.clearInterval(refreshPollTimer);
    refreshPollTimer = null;
  }
});
</script>
