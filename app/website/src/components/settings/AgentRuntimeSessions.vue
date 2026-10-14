<template>
  <section aria-labelledby="agent-runtime-title">
    <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
      <div>
        <div class="flex flex-wrap items-center gap-2">
          <h2 id="agent-runtime-title" class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('agent_live_terminalTitle') }}
          </h2>
          <span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-bold text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300">
            <span class="h-1.5 w-1.5 rounded-full bg-emerald-500 motion-safe:animate-pulse"></span>
            {{ t('agent_live_realtime') }}
          </span>
          <span class="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">{{ terminals.length }}</span>
        </div>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('agent_live_terminalDesc') }}</p>
      </div>
      <div class="flex items-center gap-3">
        <span v-if="lastUpdatedLabel" class="hidden text-[10px] text-slate-400 sm:inline">
          {{ t('agent_live_updated', { time: lastUpdatedLabel }) }}
        </span>
        <button
          type="button"
          class="inline-flex h-11 items-center justify-center gap-1 rounded-lg border border-slate-200 bg-white px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:bg-background-dark dark:text-slate-300 dark:hover:bg-primary/10"
          :disabled="loading"
          @click="loadRuntimeSessions"
        >
          <span class="material-symbols-outlined text-base" :class="{ 'motion-safe:animate-spin': loading }">refresh</span>
          {{ t('agent_live_refresh') }}
        </button>
      </div>
    </div>

    <article aria-live="polite" class="overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-background-dark">
      <div v-if="loading && !hasLoaded" class="flex min-h-36 items-center justify-center text-xs text-slate-400">
        {{ t('agent_live_loading') }}
      </div>
      <div v-else-if="!terminals.length" class="flex min-h-36 flex-col items-center justify-center px-5 text-center">
        <span class="material-symbols-outlined text-2xl text-slate-300 dark:text-slate-600">terminal</span>
        <p class="mt-2 text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_live_terminalEmpty') }}</p>
        <p class="mt-1 text-[10px] leading-4 text-slate-400">{{ t('agent_live_terminalEmptyDesc') }}</p>
      </div>
      <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800">
        <li
          v-for="terminal in terminals"
          :key="terminal.id"
          v-memo="[terminal.id, terminal.last_active_at]"
          class="flex items-start gap-3 px-4 py-3.5"
        >
          <span class="mt-1 h-2 w-2 shrink-0 rounded-full bg-cyan-500 shadow-[0_0_0_3px_rgba(6,182,212,0.12)]"></span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
              <p class="text-xs font-bold text-slate-800 dark:text-slate-100">{{ terminalTitle(terminal) }}</p>
              <span class="shrink-0 text-[10px] text-slate-400">{{ formatTime(terminal.last_active_at) }}</span>
            </div>
            <div class="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-slate-500 dark:text-slate-400">
              <span>{{ terminal.pty ? t('agent_live_pty') : t('agent_live_pipe') }}</span>
              <span aria-hidden="true">·</span>
              <span class="font-mono">{{ shortID(terminal.id) }}</span>
            </div>
            <p v-if="terminal.cwd" class="mt-1 truncate font-mono text-[10px] text-slate-400" :title="terminal.cwd">
              {{ terminal.cwd }}
            </p>
            <button
              type="button"
              class="mt-2 inline-flex min-h-10 md:min-h-9 items-center gap-1 rounded-lg border border-slate-200 px-2.5 text-[10px] font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              @click.stop="openScrollback(terminal)"
            >
              <span class="material-symbols-outlined text-sm">visibility</span>
              {{ t('agent_live_output') }}
            </button>
          </div>
        </li>
      </ul>
    </article>

    <p v-if="error" class="mt-2 text-[11px] text-rose-600 dark:text-rose-300" role="status">{{ error }}</p>

    <div
      v-if="scrollbackOpen"
      class="fixed inset-0 z-[1090] flex items-end justify-center bg-slate-950/65 backdrop-blur-sm sm:items-center sm:p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('agent_live_outputTitle')"
      @click.self="closeScrollback"
    >
      <div class="flex h-[85vh] max-h-[85dvh] w-full max-w-3xl flex-col overflow-hidden rounded-t-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900 sm:h-[80vh] sm:max-h-[80dvh] sm:rounded-2xl">
        <div class="flex shrink-0 items-center justify-between gap-3 border-b border-slate-200 bg-slate-50/85 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/65">
          <div class="flex min-w-0 items-center gap-2">
            <h3 class="truncate text-sm font-bold text-slate-900 dark:text-slate-100">{{ t('agent_live_outputTitle') }}</h3>
            <span
              v-if="scrollbackStatus"
              class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
              :class="scrollbackStatus === 'live'
                ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
                : 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300'"
            >
              {{ scrollbackStatus === 'live' ? t('agent_live_outputLive') : t('agent_live_outputExited') }}
            </span>
            <span v-if="scrollbackTruncated" class="hidden text-[10px] text-slate-400 sm:inline">{{ t('agent_live_outputTruncated') }}</span>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <button
              type="button"
              class="inline-flex h-11 items-center gap-1 rounded-lg px-2.5 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 disabled:opacity-60 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="scrollbackLoading"
              @click="fetchScrollback"
            >
              <span class="material-symbols-outlined text-base" :class="{ 'motion-safe:animate-spin': scrollbackLoading }">refresh</span>
              {{ t('agent_live_outputRefresh') }}
            </button>
            <button
              type="button"
              class="flex h-11 w-11 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              :aria-label="t('agent_closeSettings')"
              @click="closeScrollback"
            >
              <span class="material-symbols-outlined text-lg">close</span>
            </button>
          </div>
        </div>
        <div class="min-h-0 flex-1 overflow-auto bg-slate-950 p-4">
          <p v-if="scrollbackLoading && !scrollbackData" class="text-xs text-slate-400">{{ t('agent_live_loading') }}</p>
          <p v-else-if="scrollbackError" class="text-xs text-rose-300">{{ scrollbackError }}</p>
          <p v-else-if="!scrollbackData" class="text-xs text-slate-500">{{ t('agent_live_outputEmpty') }}</p>
          <pre v-else class="whitespace-pre-wrap break-words font-mono text-[11px] leading-5 text-slate-100">{{ scrollbackData }}</pre>
        </div>
        <p v-if="scrollbackTruncated" class="shrink-0 border-t border-slate-800 px-4 py-2 text-[10px] text-slate-400 sm:hidden">{{ t('agent_live_outputTruncated') }}</p>
      </div>
    </div>
  </section>
</template>

<script>
import { getAgentRuntimeSessions, getAgentTerminalScrollback } from '../../services/agentApi';
import { useI18n } from '../../i18n';

export default {
  name: 'AgentRuntimeSessions',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      terminals: [],
      collectedAt: '',
      loading: false,
      hasLoaded: false,
      error: '',
      pollTimer: null,
      scrollbackOpen: false,
      scrollbackLoading: false,
      scrollbackData: '',
      scrollbackStatus: '',
      scrollbackTruncated: false,
      scrollbackError: '',
      scrollbackSessionId: '',
    };
  },
  computed: {
    lastUpdatedLabel() {
      return this.formatTime(this.collectedAt);
    },
  },
  mounted() {
    this.loadRuntimeSessions();
    this.pollTimer = window.setInterval(() => {
      if (document.visibilityState === 'visible') this.loadRuntimeSessions();
    }, 5000);
  },
  beforeUnmount() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
  },
  methods: {
    async loadRuntimeSessions() {
      if (this.loading) return;
      this.loading = true;
      this.error = '';
      try {
        const data = await getAgentRuntimeSessions();
        this.terminals = Array.isArray(data?.terminals) ? data.terminals : [];
        this.collectedAt = data?.collected_at || new Date().toISOString();
      } catch (_) {
        this.error = this.t('agent_live_loadFailed');
      } finally {
        this.hasLoaded = true;
        this.loading = false;
      }
    },
    async openScrollback(terminal) {
      this.scrollbackSessionId = terminal.id;
      this.scrollbackOpen = true;
      this.scrollbackData = '';
      this.scrollbackError = '';
      this.scrollbackStatus = '';
      this.scrollbackTruncated = false;
      await this.fetchScrollback();
    },
    async fetchScrollback() {
      const requested = this.scrollbackSessionId;
      if (!requested) return;
      this.scrollbackLoading = true;
      try {
        const data = await getAgentTerminalScrollback(requested);
        if (requested !== this.scrollbackSessionId) return;
        this.scrollbackData = data?.data || '';
        this.scrollbackStatus = data?.status || '';
        this.scrollbackTruncated = Boolean(data?.truncated);
        this.scrollbackError = '';
      } catch (_) {
        if (requested !== this.scrollbackSessionId) return;
        this.scrollbackError = this.t('agent_live_outputLoadFailed');
      } finally {
        if (requested === this.scrollbackSessionId) this.scrollbackLoading = false;
      }
    },
    closeScrollback() {
      this.scrollbackOpen = false;
    },
    terminalTitle(terminal) {
      const shell = String(terminal?.shell || '').split(/[\\/]/).filter(Boolean).pop();
      return shell || this.t('agent_live_terminalDefault');
    },
    shortID(value) {
      const id = String(value || '');
      return id.length > 12 ? `${id.slice(0, 8)}...${id.slice(-4)}` : id;
    },
    formatTime(value) {
      if (!value) return '';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return value;
      return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' });
    },
  },
};
</script>
