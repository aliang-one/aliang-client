<template>
  <section aria-labelledby="agent-runtime-title">
    <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
      <div>
        <div class="flex flex-wrap items-center gap-2">
          <h2 id="agent-runtime-title" class="text-base font-bold text-slate-900 dark:text-white">
            {{ t('agent_live_title') }}
          </h2>
          <span class="inline-flex items-center gap-1.5 rounded-full bg-emerald-100 px-2 py-0.5 text-[10px] font-bold text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300">
            <span class="h-1.5 w-1.5 rounded-full bg-emerald-500 motion-safe:animate-pulse"></span>
            {{ t('agent_live_realtime') }}
          </span>
        </div>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('agent_live_desc') }}</p>
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

    <div class="grid grid-cols-1 gap-5 lg:grid-cols-2" aria-live="polite">
      <article class="overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-background-dark">
        <header class="flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-3.5 dark:border-slate-800">
          <div class="flex items-center gap-2.5">
            <div class="flex h-9 w-9 items-center justify-center rounded-lg bg-primary/10 text-primary">
              <span class="material-symbols-outlined text-lg">smart_toy</span>
            </div>
            <div>
              <h3 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('agent_live_aiTitle') }}</h3>
              <p class="text-[10px] text-slate-500 dark:text-slate-400">{{ t('agent_live_aiDesc') }}</p>
            </div>
          </div>
          <span class="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">
            {{ aiConversations.length }}
          </span>
        </header>

        <div v-if="loading && !hasLoaded" class="flex min-h-36 items-center justify-center text-xs text-slate-400">
          {{ t('agent_live_loading') }}
        </div>
        <div v-else-if="!aiConversations.length" class="flex min-h-36 flex-col items-center justify-center px-5 text-center">
          <span class="material-symbols-outlined text-2xl text-slate-300 dark:text-slate-600">forum</span>
          <p class="mt-2 text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('agent_live_aiEmpty') }}</p>
          <p class="mt-1 text-[10px] leading-4 text-slate-400">{{ t('agent_live_aiEmptyDesc') }}</p>
        </div>
        <ul v-else class="divide-y divide-slate-100 dark:divide-slate-800">
          <li
            v-for="session in aiConversations"
            :key="session.id"
            v-memo="[session.id, session.updated_at, session.message_count]"
            class="flex items-start gap-3 px-4 py-3.5"
          >
            <span class="mt-1 h-2 w-2 shrink-0 rounded-full bg-emerald-500 shadow-[0_0_0_3px_rgba(16,185,129,0.12)]"></span>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1">
                <p class="min-w-0 truncate text-xs font-bold text-slate-800 dark:text-slate-100" :title="sessionTitle(session)">
                  {{ sessionTitle(session) }}
                </p>
                <span class="shrink-0 text-[10px] text-slate-400">{{ formatTime(session.updated_at) }}</span>
              </div>
              <p class="mt-1 truncate text-[10px] text-slate-500 dark:text-slate-400">
                {{ aiMeta(session) }}
              </p>
              <p v-if="session.project_path" class="mt-1 truncate font-mono text-[10px] text-slate-400" :title="session.project_path">
                {{ session.project_path }}
              </p>
            </div>
          </li>
        </ul>
      </article>

      <article class="overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-background-dark">
        <header class="flex items-center justify-between gap-3 border-b border-slate-100 px-4 py-3.5 dark:border-slate-800">
          <div class="flex items-center gap-2.5">
            <div class="flex h-9 w-9 items-center justify-center rounded-lg bg-cyan-100 text-cyan-700 dark:bg-cyan-500/15 dark:text-cyan-300">
              <span class="material-symbols-outlined text-lg">terminal</span>
            </div>
            <div>
              <h3 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('agent_live_terminalTitle') }}</h3>
              <p class="text-[10px] text-slate-500 dark:text-slate-400">{{ t('agent_live_terminalDesc') }}</p>
            </div>
          </div>
          <span class="rounded-full bg-slate-100 px-2 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-800 dark:text-slate-300">
            {{ terminals.length }}
          </span>
        </header>

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
            </div>
          </li>
        </ul>
      </article>
    </div>

    <p v-if="error" class="mt-2 text-[11px] text-rose-600 dark:text-rose-300" role="status">{{ error }}</p>
  </section>
</template>

<script>
import { getAgentRuntimeSessions, getAgentSessions } from '../../services/agentApi';
import { useI18n } from '../../i18n';

export default {
  name: 'AgentRuntimeSessions',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      aiConversations: [],
      terminals: [],
      collectedAt: '',
      loading: false,
      hasLoaded: false,
      error: '',
      pollTimer: null,
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
        this.aiConversations = Array.isArray(data?.ai_conversations) ? data.ai_conversations : [];
        this.terminals = Array.isArray(data?.terminals) ? data.terminals : [];
        this.collectedAt = data?.collected_at || new Date().toISOString();
      } catch (_) {
        try {
          const history = await getAgentSessions();
          this.aiConversations = Array.isArray(history?.sessions)
            ? history.sessions.filter((session) => session?.status === 'running')
            : [];
          this.terminals = [];
          this.collectedAt = new Date().toISOString();
        } catch (_) {
          this.error = this.t('agent_live_loadFailed');
        }
      } finally {
        this.hasLoaded = true;
        this.loading = false;
      }
    },
    sessionTitle(session) {
      return session?.title || session?.project_path || session?.id || this.t('agent_live_untitled');
    },
    aiMeta(session) {
      return [session?.provider || session?.tool, session?.model, session?.message_count ? this.t('agent_live_messages', { count: session.message_count }) : '']
        .filter(Boolean)
        .join(' · ');
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
