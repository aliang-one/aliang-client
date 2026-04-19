<template>
  <div class="settings-pane" data-pane="logs">
    <div class="mt-4 flex flex-col gap-4">
      <section class="space-y-4">
        <div class="rounded-3xl border border-slate-200/70 bg-white/88 px-5 py-5 shadow-[0_20px_60px_rgba(15,23,42,0.10)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/75">
          <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div class="min-w-0 space-y-3">
              <div
                class="inline-flex items-center gap-2 rounded-full border px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.18em]"
                :class="aiStatusBadgeClass"
              >
                <span class="h-2 w-2 rounded-full" :class="aiStatusDotClass"></span>
                {{ t('status_liveSignal') }}
              </div>
              <div>
                <h2 class="text-2xl font-semibold tracking-tight text-slate-950 dark:text-white">
                  {{ activeSummaryTitle }}
                </h2>
                <p class="mt-2 text-sm leading-6 text-slate-600 dark:text-slate-300">
                  {{ activeSummaryText }}
                </p>
              </div>
              <div class="flex flex-wrap gap-2 text-xs text-slate-500 dark:text-slate-400">
                <span class="rounded-full border border-slate-200 bg-slate-50 px-3 py-1 dark:border-slate-800 dark:bg-slate-900/80">
                  {{ t('status_lastUpdatedCompact') }} {{ summaryUpdatedAgoText }}
                </span>
                <span class="rounded-full border border-slate-200 bg-slate-50 px-3 py-1 dark:border-slate-800 dark:bg-slate-900/80">
                  {{ t('status_trackingWindow', {
                    seconds: statusTTLSeconds,
                    minutes: Math.max(1, Math.round(statusVisibleWindowSeconds / 60))
                  }) }}
                </span>
              </div>
            </div>

            <div class="grid min-w-0 gap-3 sm:grid-cols-3 lg:min-w-[380px]">
              <article
                v-for="metric in compactMetrics"
                :key="metric.key"
                class="rounded-2xl border border-slate-200/80 bg-slate-50/90 p-4 dark:border-slate-800/80 dark:bg-slate-900/70"
              >
                <div class="text-[11px] font-semibold uppercase tracking-[0.16em] text-slate-500 dark:text-slate-400">
                  {{ metric.label }}
                </div>
                <div class="mt-2 text-xl font-semibold text-slate-950 dark:text-white">
                  {{ metric.value }}
                </div>
                <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
                  {{ metric.caption }}
                </p>
              </article>
            </div>
          </div>
        </div>

        <div class="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
          <div class="grid gap-4 sm:grid-cols-2">
            <article
              v-for="card in trafficCards"
              :key="card.key"
              class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
            >
              <div class="flex items-start justify-between gap-3">
                <div>
                  <p class="text-sm font-medium text-slate-500 dark:text-slate-400">{{ card.label }}</p>
                  <h3 class="mt-2 text-2xl font-semibold tracking-tight text-slate-950 dark:text-white">
                    {{ card.value }}
                  </h3>
                </div>
                <div :class="card.badgeClass" class="rounded-full px-3 py-1 text-xs font-medium">
                  {{ card.badge }}
                </div>
              </div>
              <p class="mt-3 text-sm leading-6 text-slate-500 dark:text-slate-400">
                {{ card.description }}
              </p>
            </article>
          </div>

          <article
            class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="text-base font-semibold text-slate-950 dark:text-white">
                  {{ t('status_providerSectionTitle') }}
                </h3>
                <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">
                  {{ t('status_providerSectionDescription') }}
                </p>
              </div>
              <span class="rounded-full border border-slate-200 bg-slate-100 px-3 py-1 text-xs font-medium text-slate-600 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300">
                {{ t('status_hitsBadge', { count: aiSummary.totalHits || 0 }) }}
              </span>
            </div>

            <div v-if="providerCards.length" class="mt-4 grid gap-3 sm:grid-cols-2">
              <div
                v-for="provider in providerCards"
                :key="provider.key"
                class="rounded-2xl border p-4 transition-colors"
                :class="provider.active
                  ? 'border-emerald-200 bg-emerald-50/80 dark:border-emerald-500/30 dark:bg-emerald-500/10'
                  : 'border-slate-200/70 bg-slate-50/90 dark:border-slate-800 dark:bg-slate-900/70'"
              >
                <div class="flex items-start justify-between gap-3">
                  <div>
                    <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">{{ provider.label }}</div>
                    <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
                      {{ provider.caption }}
                    </div>
                  </div>
                  <span
                    class="rounded-full px-3 py-1 text-xs font-medium"
                    :class="provider.active
                      ? 'bg-emerald-600 text-white dark:bg-emerald-400 dark:text-slate-950'
                      : 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900'"
                  >
                    {{ provider.badge }}
                  </span>
                </div>
                <div class="mt-4 flex items-center justify-between text-xs text-slate-500 dark:text-slate-400">
                  <span>{{ provider.lastSeenText }}</span>
                  <span>{{ provider.detail }}</span>
                </div>
              </div>
            </div>

            <div
              v-else
              class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400"
            >
              {{ t('status_noProviderData') }}
            </div>
          </article>
        </div>
      </section>

      <div v-if="statusError" class="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-900 dark:bg-rose-950/30 dark:text-rose-300">
        {{ statusError }}
      </div>

      <section class="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-background-dark">
        <button
          type="button"
          class="flex w-full items-center justify-between gap-4 px-5 py-4 text-left transition hover:bg-slate-50 dark:hover:bg-slate-900/40 sm:px-6"
          :aria-expanded="logsExpanded ? 'true' : 'false'"
          @click="toggleLogsExpanded"
        >
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-lg font-bold text-slate-900 dark:text-white">{{ t('status_logsTitle') }}</h3>
              <span class="rounded-full bg-primary/10 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide text-primary">{{ logsExpanded ? t('status_logsCollapse') : t('status_logsExpand') }}</span>
              <span class="text-xs text-slate-400">{{ t('logs_entries', { count: filteredEntries.length }) }}</span>
            </div>
            <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ t('status_logsDesc') }}</p>
          </div>
          <span class="material-symbols-outlined text-2xl text-slate-400">{{ logsExpanded ? 'keyboard_arrow_up' : 'keyboard_arrow_down' }}</span>
        </button>

        <div v-if="logsExpanded" class="border-t border-slate-200 px-5 py-5 dark:border-slate-800 sm:px-6">
          <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <div class="flex items-center gap-2">
              <span class="rounded-full bg-primary/20 px-2 py-0.5 text-[10px] font-bold uppercase text-primary">
                {{ isLogsPolling ? t('logs_live') : t('logs_paused') }}
              </span>
            </div>
            <div class="flex flex-wrap items-center gap-2 rounded-lg border border-slate-200 bg-slate-50 p-2 dark:border-slate-700 dark:bg-slate-800/60">
              <span class="px-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
                {{ t('logs_filterLevel') }}
              </span>
              <select
                id="logLevelSelect"
                v-model="selectedLevel"
                class="rounded-md border-0 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-600 shadow-sm ring-1 ring-slate-200 focus:outline-none focus:ring-2 focus:ring-primary/40 dark:bg-slate-900 dark:text-slate-200 dark:ring-slate-600"
              >
                <option v-for="option in filterLevelOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </option>
              </select>
              <label class="inline-flex min-h-10 items-center gap-2 rounded-md bg-white px-2.5 py-1.5 text-xs font-medium text-slate-600 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:text-slate-200 dark:ring-slate-600">
                <input id="logAutoScroll" v-model="autoScroll" type="checkbox" class="rounded border-slate-300 text-primary focus:ring-primary/40" />
                {{ t('logs_autoScroll') }}
              </label>
              <button
                id="logsClearBtn"
                type="button"
                class="inline-flex min-h-10 items-center rounded-md px-2.5 py-1.5 text-xs font-medium text-slate-500 transition hover:bg-slate-200 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-100"
                @click="clearLogs"
              >
                {{ t('logs_clear') }}
              </button>
              <button
                id="wsDisconnectBtn"
                type="button"
                class="flex min-h-10 min-w-10 items-center justify-center rounded-md p-1.5 text-slate-400 transition-all hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-500/10"
                @click.stop="toggleLogsPolling"
              >
                <span class="material-symbols-outlined text-lg">{{ isLogsPolling ? 'stop_circle' : 'play_circle' }}</span>
              </button>
              <button
                id="logsRefreshBtn"
                type="button"
                class="flex min-h-10 min-w-10 items-center justify-center rounded-md p-1.5 text-slate-400 transition-all hover:bg-primary/10 hover:text-primary"
                :disabled="isLogsLoading"
                @click.stop="loadLogs"
              >
                <span class="material-symbols-outlined text-lg">{{ isLogsLoading ? 'hourglass_top' : 'refresh' }}</span>
              </button>
            </div>
          </div>

          <div
            ref="logsContainer"
            class="mt-4 overflow-y-auto rounded-xl border border-slate-200 bg-slate-950 p-4 font-mono text-xs text-slate-300 dark:border-slate-800"
            style="height: 460px;"
          >
            <div v-if="logsError" class="mb-3 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-200">
              {{ logsError }}
            </div>
            <div v-if="!filteredEntries.length" class="rounded-lg border border-dashed border-slate-700 bg-slate-900/60 px-4 py-6 text-sm text-slate-500">
              {{ t('logs_noData') }}
            </div>
            <div v-else id="logsOutput" class="space-y-1 whitespace-pre-wrap break-all">
              <div
                v-for="(entry, index) in filteredEntries"
                :key="`${entry.timestamp}-${entry.source}-${index}`"
                class="rounded px-2 py-1.5"
                :class="entryRowClass(entry.level)"
              >
                <span class="text-slate-500">[{{ entry.timestamp }}]</span>
                <span class="ml-2 font-semibold" :class="entryLevelClass(entry.level)">{{ entry.level }}</span>
                <span class="ml-2 text-slate-400">({{ entry.source }})</span>
                <span class="ml-2">{{ entry.message }}</span>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>

    <div class="compat-anchors" aria-hidden="true">
      <select id="logSourceSelect"><option value="all">All</option></select>
      <button id="logFilterBtn" type="button"></button>
      <button id="wsConnectBtn" type="button"></button>
      <button id="logsFullscreenBtn" type="button"><i class="bi bi-fullscreen"></i></button>
      <div id="logs-page"></div>
      <div id="wsConnectionStatus">{{ isLogsPolling ? 'connected' : 'paused' }}</div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import { getStatusSummary } from '../../services/statusApi';

const { t } = useI18n();

const isProdBuild = import.meta.env.PROD || ['prod', 'production'].includes(String(import.meta.env.MODE || '').toLowerCase());
const frontEndLogsFetchLimit = isProdBuild ? 200 : 800;

const LOG_LEVEL_OPTIONS = [
  { value: 'trace', labelKey: 'logs_levelTrace' },
  { value: 'debug', labelKey: 'logs_levelDebug' },
  { value: 'info', labelKey: 'logs_levelInfo' },
  { value: 'warn', labelKey: 'logs_levelWarning' },
  { value: 'error', labelKey: 'logs_levelError' }
];

const entries = ref([]);
const statusSummary = ref({ ai: {}, traffic: {}, http: {} });
const selectedLevel = ref('all');
const autoScroll = ref(true);
const isLogsLoading = ref(false);
const isStatusLoading = ref(false);
const isLogsPolling = ref(true);
const logsExpanded = ref(false);
const logsError = ref('');
const statusError = ref('');
const lastUpdatedAt = ref(0);
const logsContainer = ref(null);

let logsPollTimer = null;
let statusPollTimer = null;

const runtimeLevelOptions = computed(() => {
  return LOG_LEVEL_OPTIONS
    .map((option) => ({
      value: option.value,
      label: t(option.labelKey)
    }));
});

const filterLevelOptions = computed(() => [
  { value: 'all', label: t('logs_levelAll') },
  ...runtimeLevelOptions.value
]);

const aiSummary = computed(() => statusSummary.value?.ai || {});
const trafficStats = computed(() => statusSummary.value?.traffic || {});
const httpStats = computed(() => statusSummary.value?.http || {});
const activeProviders = computed(() => Array.isArray(aiSummary.value?.activeDetections) ? aiSummary.value.activeDetections : []);
const visibleProviders = computed(() => {
  if (Array.isArray(aiSummary.value?.recentProviderTraffic)) {
    return aiSummary.value.recentProviderTraffic;
  }
  return activeProviders.value;
});
const totalTrafficBytes = computed(() => Number(httpStats.value?.totalTrafficBytes || 0));
const statusTTLSeconds = computed(() => Number(aiSummary.value?.ttlSeconds || 15));
const statusVisibleWindowSeconds = computed(() => Number(aiSummary.value?.visibleWindowSeconds || 600));
const totalActiveConnections = computed(() => activeProviders.value.reduce((total, provider) => total + Number(provider?.activeConnectionCount || 0), 0));
const hasRecentAIActivity = computed(() => Boolean(aiSummary.value?.active));
const aiStatusDotClass = computed(() => hasRecentAIActivity.value ? 'bg-emerald-400 shadow-[0_0_16px_rgba(52,211,153,0.85)]' : 'bg-slate-400');
const aiStatusBadgeClass = computed(() => hasRecentAIActivity.value
  ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200'
  : 'border-slate-200 bg-slate-50 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300');
const activeSummaryTitle = computed(() => {
  if (hasRecentAIActivity.value) {
    return t('status_activeTitle', { count: aiSummary.value.activeCount || activeProviders.value.length || 1 });
  }
  return t('status_idleTitle');
});
const activeSummaryText = computed(() => {
  if (hasRecentAIActivity.value) {
    const latestLabel = aiSummary.value?.lastLabel || aiSummary.value?.lastProvider || '';
    if (latestLabel && totalActiveConnections.value > 0) {
      return t('status_activeConnectionDescriptionProvider', {
        provider: latestLabel,
        count: totalActiveConnections.value
      });
    }
    return latestLabel
      ? t('status_activeDescriptionProvider', {
          provider: latestLabel,
          ago: formatRelativeFromSeconds(aiSummary.value?.lastSeenAt)
        })
      : t('status_activeDescription', { ago: formatRelativeFromSeconds(aiSummary.value?.lastSeenAt) });
  }
  return t('status_idleDescription', {
    seconds: statusTTLSeconds.value,
    minutes: Math.max(1, Math.round(statusVisibleWindowSeconds.value / 60))
  });
});
const summaryUpdatedAgoText = computed(() => {
  if (!lastUpdatedAt.value) {
    return t('status_updatedJustNow');
  }
  return formatRelativeFromMilliseconds(lastUpdatedAt.value);
});
const compactMetrics = computed(() => [
  {
    key: 'providers',
    label: t('status_heroActiveProviders'),
    value: String(aiSummary.value.activeCount || activeProviders.value.length || 0),
    caption: hasRecentAIActivity.value ? t('status_heroActiveProvidersHint') : t('status_heroIdleHint')
  },
  {
    key: 'rate',
    label: t('status_heroTrafficRate'),
    value: formatRate(Number(trafficStats.value.upload_rate || 0) + Number(trafficStats.value.download_rate || 0)),
    caption: t('status_heroTrafficRateHint')
  },
  {
    key: 'last',
    label: t('status_heroLastSeen'),
    value: aiSummary.value.lastSeenAt ? formatRelativeFromSeconds(aiSummary.value.lastSeenAt) : t('status_heroWaiting'),
    caption: aiSummary.value.lastLabel || t('status_heroLastSeenHint')
  }
]);
const trafficCards = computed(() => [
  {
    key: 'connections',
    label: t('status_cardConnections'),
    value: formatInteger(trafficStats.value.active_connections),
    badge: t('status_cardConnectionsBadge'),
    badgeClass: 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300',
    description: t('status_cardConnectionsDesc')
  },
  {
    key: 'traffic',
    label: t('status_cardTotalTraffic'),
    value: formatBytes(totalTrafficBytes.value),
    badge: formatInteger(httpStats.value.totalRequests),
    badgeClass: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
    description: t('status_cardTotalTrafficDesc')
  },
  {
    key: 'upload',
    label: t('status_cardUploadRate'),
    value: formatRate(trafficStats.value.upload_rate),
    badge: t('status_cardUploadBadge'),
    badgeClass: 'bg-violet-100 text-violet-700 dark:bg-violet-500/15 dark:text-violet-300',
    description: t('status_cardUploadRateDesc')
  },
  {
    key: 'download',
    label: t('status_cardDownloadRate'),
    value: formatRate(trafficStats.value.download_rate),
    badge: t('status_cardDownloadBadge'),
    badgeClass: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
    description: t('status_cardDownloadRateDesc')
  }
]);
const providerCards = computed(() => {
  const latestSeenAt = Number(aiSummary.value.lastSeenAt || 0);
  const hiddenAfterMinutes = Math.max(1, Math.round(statusVisibleWindowSeconds.value / 60));
  return visibleProviders.value.slice(0, 6).map((provider) => {
    const lastSeenAt = Number(provider.lastSeenAt || 0);
    const label = provider.providerLabel || provider.providerKey || t('status_unknownProvider');
    const active = Boolean(provider.active);
    const activeConnectionCount = Number(provider.activeConnectionCount || 0);
    const lastConnectionDurationSeconds = Number(provider.lastConnectionDurationSeconds || 0);
    return {
      key: provider.providerKey || label,
      label,
      active,
      badge: active ? t('status_providerActiveBadge') : t('status_providerInactiveBadge'),
      caption: active
        ? (activeConnectionCount > 0
          ? t('status_providerConnectionActiveCaption', { count: activeConnectionCount })
          : t('status_providerActiveCaption', { seconds: statusTTLSeconds.value }))
        : t('status_providerInactiveCaption', {
            seconds: statusTTLSeconds.value,
            minutes: hiddenAfterMinutes
          }),
      lastSeenText: t('status_providerLastSeen', { value: formatRelativeFromSeconds(lastSeenAt) }),
      detail: active
        ? (activeConnectionCount > 0
          ? t('status_providerConnectionActiveDetail', { count: activeConnectionCount })
          : (latestSeenAt === lastSeenAt ? t('status_providerLatest') : t('status_providerRecent')))
        : (lastConnectionDurationSeconds > 0
          ? t('status_providerInactiveDetailWithDuration', {
              duration: formatDurationSeconds(lastConnectionDurationSeconds),
              minutes: hiddenAfterMinutes
            })
          : t('status_providerInactiveDetail', { minutes: hiddenAfterMinutes }))
    };
  });
});

const filteredEntries = computed(() => {
  if (selectedLevel.value === 'all') {
    return entries.value;
  }
  const expectedLevel = selectedLevel.value.toUpperCase();
  return entries.value.filter((entry) => String(entry.level || '').toUpperCase() === expectedLevel);
});

function levelRank(level) {
  switch (String(level || '').toLowerCase()) {
    case 'trace':
      return 0;
    case 'debug':
      return 1;
    case 'info':
      return 2;
    case 'warn':
      return 3;
    case 'error':
      return 4;
    default:
      return 2;
  }
}

function formatInteger(value) {
  return new Intl.NumberFormat('en-US').format(Number(value || 0));
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return '0 B';
  }

  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let current = bytes;
  let unitIndex = 0;
  while (current >= 1024 && unitIndex < units.length - 1) {
    current /= 1024;
    unitIndex += 1;
  }
  return `${current >= 10 || unitIndex === 0 ? current.toFixed(0) : current.toFixed(1)} ${units[unitIndex]}`;
}

function formatRate(value) {
  return `${formatBytes(value)}/s`;
}

function formatRelativeFromSeconds(seconds) {
  const timestamp = Number(seconds || 0) * 1000;
  if (!Number.isFinite(timestamp) || timestamp <= 0) {
    return '--';
  }
  return formatRelativeFromMilliseconds(timestamp);
}

function formatRelativeFromMilliseconds(timestamp) {
  const value = Number(timestamp || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '--';
  }
  const diff = Date.now() - value;
  if (diff < 5000) {
    return t('status_updatedJustNow');
  }
  const diffSeconds = Math.round(diff / 1000);
  if (diffSeconds < 60) {
    return t('status_relativeSeconds', { count: diffSeconds });
  }
  const diffMinutes = Math.round(diffSeconds / 60);
  if (diffMinutes < 60) {
    return t('status_relativeMinutes', { count: diffMinutes });
  }
  const diffHours = Math.round(diffMinutes / 60);
  return t('status_relativeHours', { count: diffHours });
}

function formatDurationSeconds(seconds) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '--';
  }
  if (value < 60) {
    return t('status_durationSeconds', { count: Math.round(value) });
  }
  const diffMinutes = Math.round(value / 60);
  if (diffMinutes < 60) {
    return t('status_durationMinutes', { count: diffMinutes });
  }
  const diffHours = Math.round(diffMinutes / 60);
  return t('status_durationHours', { count: diffHours });
}

async function loadStatus() {
  isStatusLoading.value = true;
  statusError.value = '';

  try {
    const envelope = await getStatusSummary();
    statusSummary.value = envelope?.data || { ai: {}, traffic: {}, http: {} };
    lastUpdatedAt.value = Date.now();
  } catch (error) {
    statusError.value = error instanceof Error ? error.message : t('status_summaryLoadFailed');
  } finally {
    isStatusLoading.value = false;
  }
}

async function loadLogs() {
  if (!logsExpanded.value) {
    return;
  }

  isLogsLoading.value = true;
  logsError.value = '';

  try {
    const levelQuery = selectedLevel.value !== 'all' ? `&level=${encodeURIComponent(selectedLevel.value.toUpperCase())}` : '';
    const response = await fetch(`/api/logs?limit=${frontEndLogsFetchLimit}${levelQuery}`);
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }

    const nextEntries = Array.isArray(payload?.data?.entries) ? payload.data.entries : [];
    entries.value = nextEntries.map(normalizeLogEntry);
    await scrollToBottomIfNeeded();
  } catch (error) {
    logsError.value = error instanceof Error ? error.message : t('logs_loadFailed');
  } finally {
    isLogsLoading.value = false;
  }
}

async function clearLogs() {
  logsError.value = '';
  try {
    const response = await fetch('/api/logs/clear', { method: 'POST' });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }
    entries.value = [];
  } catch (error) {
    logsError.value = error instanceof Error ? error.message : t('logs_clearFailed');
  }
}

function normalizeLogEntry(entry) {
  const raw = entry && typeof entry === 'object' ? entry : {};
  return {
    level: typeof raw.level === 'string' ? raw.level.toUpperCase() : 'INFO',
    timestamp: typeof raw.timestamp === 'string' ? raw.timestamp : '',
    message: typeof raw.message === 'string' ? raw.message : '',
    source: typeof raw.source === 'string' ? raw.source : 'main'
  };
}

function entryLevelClass(level) {
  switch (String(level || '').toUpperCase()) {
    case 'ERROR':
    case 'FATAL':
    case 'PANIC':
      return 'text-red-300';
    case 'WARN':
      return 'text-amber-300';
    case 'DEBUG':
      return 'text-sky-300';
    case 'TRACE':
      return 'text-violet-300';
    default:
      return 'text-emerald-300';
  }
}

function entryRowClass(level) {
  switch (String(level || '').toUpperCase()) {
    case 'ERROR':
    case 'FATAL':
    case 'PANIC':
      return 'bg-red-500/5';
    case 'WARN':
      return 'bg-amber-500/5';
    case 'DEBUG':
      return 'bg-sky-500/5';
    case 'TRACE':
      return 'bg-violet-500/5';
    default:
      return 'bg-emerald-500/5';
  }
}

async function scrollToBottomIfNeeded() {
  if (!autoScroll.value) {
    return;
  }
  await nextTick();
  const element = logsContainer.value;
  if (element instanceof HTMLElement) {
    element.scrollTop = element.scrollHeight;
  }
}

function startStatusPolling() {
  stopStatusPolling();
  statusPollTimer = window.setInterval(() => {
    loadStatus();
  }, 5000);
}

function stopStatusPolling() {
  if (statusPollTimer) {
    window.clearInterval(statusPollTimer);
    statusPollTimer = null;
  }
}

function startLogsPolling() {
  stopLogsPolling();
  logsPollTimer = window.setInterval(() => {
    loadLogs();
  }, 5000);
}

function stopLogsPolling() {
  if (logsPollTimer) {
    window.clearInterval(logsPollTimer);
    logsPollTimer = null;
  }
}

function toggleLogsExpanded() {
  logsExpanded.value = !logsExpanded.value;
}

function toggleLogsPolling() {
  isLogsPolling.value = !isLogsPolling.value;
  if (isLogsPolling.value && logsExpanded.value) {
    startLogsPolling();
    loadLogs();
    return;
  }
  stopLogsPolling();
}

watch(selectedLevel, (nextLevel) => {
  if (logsExpanded.value) {
    loadLogs();
  }
});

watch(logsExpanded, (expanded) => {
  if (expanded) {
    loadLogs();
    if (isLogsPolling.value) {
      startLogsPolling();
    }
    return;
  }

  stopLogsPolling();
});

onMounted(async () => {
  await loadStatus();
  startStatusPolling();
});

onBeforeUnmount(() => {
  stopLogsPolling();
  stopStatusPolling();
});
</script>
