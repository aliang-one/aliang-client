<template>
  <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-4">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <span class="material-symbols-outlined">history</span>
        </div>
        <div class="min-w-0">
          <div class="flex flex-wrap items-center gap-2">
            <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_activity_title') }}</h3>
            <span
              v-if="status"
              class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
              :class="statusBadgeClass"
            >{{ statusLabel }}</span>
          </div>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('agent_activity_desc') }}
          </p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-11 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
        :disabled="loading"
        @click="loadSessions"
      >
        <span class="material-symbols-outlined text-sm">refresh</span>
        {{ t('agent_activity_refresh') }}
      </button>
    </div>

    <!-- 注册 / 连接健康度 -->
    <div
      v-if="status && healthVisible"
      class="mt-5 grid grid-cols-1 overflow-hidden rounded-lg border border-slate-200 bg-slate-50/60 sm:grid-cols-2 sm:divide-x sm:divide-slate-200 dark:border-slate-700 dark:bg-slate-900/40 dark:sm:divide-slate-700"
    >
      <div class="px-3 py-2.5">
        <div class="flex items-center gap-2">
          <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_registration_label') }}</span>
          <span
            class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
            :class="registrationBadgeClass"
          >{{ registrationLabel }}</span>
        </div>
        <p
          v-if="status.registration_message"
          class="mt-1 break-words text-[10px] leading-4 text-slate-500 dark:text-slate-400"
        >{{ status.registration_message }}</p>
      </div>
      <div class="border-t border-slate-200 px-3 py-2.5 sm:border-t-0 dark:border-slate-700">
        <div class="flex items-center gap-2">
          <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_connection_label') }}</span>
          <span
            class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
            :class="connectionBadgeClass"
          >{{ connectionLabel }}</span>
        </div>
        <p
          v-if="status.connection_message"
          class="mt-1 break-words text-[10px] leading-4 text-slate-500 dark:text-slate-400"
        >{{ status.connection_message }}</p>
      </div>
      <div v-if="canRetry" class="border-t border-slate-200 px-3 py-2 sm:col-span-2 dark:border-slate-700">
        <button
          type="button"
          class="inline-flex min-h-11 items-center gap-1 rounded-lg px-2 text-[11px] font-semibold text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60"
          :disabled="retrying"
          @click="retryRegistration"
        >
          <span class="material-symbols-outlined text-sm">{{ retrying ? 'progress_activity' : 'sync' }}</span>
          {{ retrying ? t('agent_health_retrying') : t('agent_health_retry') }}
        </button>
      </div>
    </div>

    <div v-if="loading" class="mt-5 flex min-h-28 items-center justify-center text-xs text-slate-500 dark:text-slate-400">
      <span class="material-symbols-outlined mr-2 text-base motion-safe:animate-spin">progress_activity</span>
      {{ t('agent_activity_loading') }}
    </div>
    <div
      v-else-if="error"
      class="mt-5 rounded-lg border border-rose-200 bg-rose-50 px-4 py-3 text-xs text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ error }}</div>
    <div v-else-if="!sessions.length" class="mt-5 flex min-h-28 flex-col items-center justify-center text-center text-slate-400">
      <span class="material-symbols-outlined text-2xl text-slate-300 dark:text-slate-600">history</span>
      <p class="mt-2 text-xs">{{ offline ? t('agent_activity_runtimeOffline') : t('agent_activity_noSessions') }}</p>
    </div>

    <template v-else>
      <div class="mt-5 flex flex-wrap items-end justify-between gap-2 border-t border-slate-100 pt-5 dark:border-slate-800">
        <div>
          <h4 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('agent_activity_recentTitle') }}</h4>
          <p class="mt-0.5 text-[10px] text-slate-400">{{ t('agent_activity_recentCount', { count: sessions.length }) }}</p>
        </div>
        <span class="text-[10px] text-slate-400">{{ t('agent_activity_pageCompact', { page: currentPage, total: totalPages }) }}</span>
      </div>

      <div class="mt-3 overflow-hidden rounded-lg border border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900/20">
        <section v-for="group in groupedPagedSessions" :key="group.key" :aria-label="group.label">
          <div class="flex items-center justify-between border-b border-slate-200 bg-slate-50/80 px-4 py-2 dark:border-slate-700 dark:bg-slate-900/60">
            <h5 class="text-[10px] font-bold text-slate-500 dark:text-slate-400">{{ group.label }}</h5>
            <span class="text-[10px] text-slate-400">{{ group.sessions.length }}</span>
          </div>
          <ul class="divide-y divide-slate-100 dark:divide-slate-800">
            <li
              v-for="s in group.sessions"
              :key="s.id"
              v-memo="[s.id, s.updated_at, s.message_count, expandedId === s.id]"
            >
              <button
                type="button"
                class="group flex min-h-[4.5rem] w-full cursor-pointer items-center gap-3 px-4 py-3 text-left transition-colors hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary/30 motion-reduce:transition-none dark:hover:bg-slate-800/45"
                :aria-expanded="expandedId === s.id"
                @click="toggle(s)"
              >
                <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg" :class="sessionIconClass(s)">
                  <span class="material-symbols-outlined text-lg">{{ sessionIcon(s) }}</span>
                </span>
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-xs font-semibold text-slate-800 dark:text-slate-100" :title="sessionTitle(s)">
                    {{ sessionTitle(s) }}
                  </span>
                  <span class="mt-1 flex min-w-0 flex-wrap items-center gap-x-1.5 gap-y-0.5 text-[10px] text-slate-500 dark:text-slate-400">
                    <span>{{ sessionMeta(s) }}</span>
                    <span class="sm:hidden">· {{ formatSessionTime(s.updated_at) }}</span>
                  </span>
                </span>
                <span class="hidden shrink-0 text-[10px] tabular-nums text-slate-400 sm:block">{{ formatSessionTime(s.updated_at) }}</span>
                <span
                  class="material-symbols-outlined shrink-0 text-base text-slate-400 transition-transform duration-200 motion-reduce:transition-none"
                  :class="{ 'rotate-180': expandedId === s.id }"
                >expand_more</span>
              </button>

              <div v-if="expandedId === s.id" class="border-t border-slate-100 bg-slate-50/55 px-4 py-4 dark:border-slate-800 dark:bg-slate-950/25">
                <div v-if="detailLoading" class="py-3 text-center text-[11px] text-slate-500">
                  {{ t('agent_activity_loading') }}
                </div>
                <div v-else-if="!visibleDetailMessages.length" class="py-3 text-center text-[11px] text-slate-400">
                  {{ t('agent_activity_noSessions') }}
                </div>
                <template v-else>
                  <div class="space-y-2">
                    <div
                      v-for="m in visibleDetailMessages"
                      :key="m.id || m.index"
                      class="flex"
                      :class="m.role === 'user' ? 'justify-end' : 'justify-start'"
                    >
                      <div
                        class="max-w-[88%] whitespace-pre-wrap break-words rounded-lg px-3 py-2 text-[11px] leading-5"
                        :class="m.role === 'user'
                          ? 'bg-primary text-white'
                          : 'bg-white text-slate-700 ring-1 ring-slate-200 dark:bg-slate-800 dark:text-slate-200 dark:ring-slate-700'"
                      >
                        <span class="mb-0.5 block text-[9px] font-bold uppercase opacity-70">
                          {{ m.role === 'user' ? t('agent_activity_you') : t('agent_activity_ai') }}
                        </span>
                        <span :class="m.role === 'assistant' ? 'line-clamp-2' : ''">{{ m.content }}</span>
                      </div>
                    </div>
                  </div>
                  <div class="mt-3 text-center">
                    <button
                      v-if="hasMore"
                      type="button"
                      class="inline-flex min-h-11 items-center gap-1 rounded-lg px-3 text-[10px] font-semibold text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60"
                      :disabled="detailLoading"
                      @click="loadMore(s)"
                    >
                      <span class="material-symbols-outlined text-sm">expand_more</span>
                      {{ t('agent_activity_loadMore') }}
                    </button>
                    <p v-else class="text-[10px] text-slate-400">{{ t('agent_activity_noMore') }}</p>
                  </div>
                </template>
              </div>
            </li>
          </ul>
        </section>
      </div>
    </template>

    <nav
      v-if="!loading && !error && sessions.length && totalPages > 1"
      class="mt-4 flex flex-wrap items-center justify-center gap-1.5 text-[11px]"
      :aria-label="t('agent_activity_paginationLabel')"
    >
      <button
        type="button"
        class="inline-flex h-11 items-center gap-0.5 rounded-lg border border-slate-200 px-3 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        :disabled="currentPage <= 1"
        @click="goToPage(currentPage - 1)"
      >
        <span class="material-symbols-outlined text-sm">chevron_left</span>
        {{ t('agent_activity_prev') }}
      </button>
      <template v-for="(p, i) in pageNumbers" :key="`pager-${i}`">
        <span v-if="p === '...'" class="px-1 text-slate-400">…</span>
        <button
          v-else
          type="button"
          class="inline-flex h-11 min-w-11 items-center justify-center rounded-lg border px-2 font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20"
          :class="p === currentPage
            ? 'border-primary bg-primary text-white'
            : 'border-slate-200 text-slate-600 hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800'"
          @click="goToPage(p)"
        >{{ p }}</button>
      </template>
      <button
        type="button"
        class="inline-flex h-11 items-center gap-0.5 rounded-lg border border-slate-200 px-3 font-semibold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        :disabled="currentPage >= totalPages"
        @click="goToPage(currentPage + 1)"
      >
        {{ t('agent_activity_next') }}
        <span class="material-symbols-outlined text-sm">chevron_right</span>
      </button>
      <span class="w-full pt-1 text-center text-slate-400 sm:ml-1 sm:w-auto sm:pt-0">
        {{ t('agent_activity_pageInfo', { page: currentPage, total: totalPages, count: sessions.length }) }}
      </span>
    </nav>
  </div>
</template>

<script>
import { getAgentStatus, getAgentSessions, getAgentSessionDetail, enableAgent } from '../../services/agentApi';
import { useI18n } from '../../i18n';

export default {
  name: 'AgentActivitySettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      status: null,
      sessions: [],
      currentPage: 1,
      pageSize: 20,
      loading: false,
      error: '',
      offline: false,
      retrying: false,
      expandedId: '',
      detailMessages: [],
      detailLoading: false,
      hasMore: false,
      nextBefore: '',
    };
  },
  computed: {
    runtimeOnline() {
      if (!this.status?.runtime) return true;
      return Boolean(this.status.runtime.online);
    },
    agentEnabled() {
      return this.runtimeOnline && Boolean(this.status?.enabled && this.status?.bound);
    },
    statusLabel() {
      if (!this.runtimeOnline) return this.t('agent_runtimeOffline');
      if (this.agentEnabled) return this.t('agent_enabled');
      return this.t('agent_disabled');
    },
    statusBadgeClass() {
      if (!this.runtimeOnline) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
      if (this.agentEnabled) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
      return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
    },
    // —— 注册 / 连接健康度（来自后端派生的 registration_state / connection_state）——
    healthVisible() {
      return Boolean(this.status?.registration_state || this.status?.connection_state);
    },
    registrationLabel() {
      const map = {
        registered: 'agent_reg_registered',
        rejected: 'agent_reg_rejected',
        login_required: 'agent_reg_login_required',
        unregistered: 'agent_reg_unregistered',
        not_configured: 'agent_reg_not_configured',
      };
      const key = map[this.status?.registration_state];
      return key ? this.t(key) : '';
    },
    registrationBadgeClass() {
      switch (this.status?.registration_state) {
        case 'registered': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'rejected': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        case 'login_required': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    connectionLabel() {
      const map = {
        connected: 'agent_conn_connected',
        connecting: 'agent_conn_connecting',
        error: 'agent_conn_error',
        disconnected: 'agent_conn_disconnected',
      };
      const key = map[this.status?.connection_state];
      return key ? this.t(key) : '';
    },
    connectionBadgeClass() {
      switch (this.status?.connection_state) {
        case 'connected': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'connecting': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        case 'error': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    canRetry() {
      const r = this.status?.registration_state;
      const c = this.status?.connection_state;
      // login_required / not_configured need user action (login / config), not an API retry.
      if (r === 'login_required' || r === 'not_configured') return false;
      return r !== 'registered' || c !== 'connected';
    },
    totalPages() {
      return Math.max(1, Math.ceil(this.sessions.length / this.pageSize));
    },
    pagedSessions() {
      const start = (this.currentPage - 1) * this.pageSize;
      return this.sessions.slice(start, start + this.pageSize);
    },
    groupedPagedSessions() {
      const groups = [];
      const byKey = new Map();
      for (const session of this.pagedSessions) {
        const key = this.sessionGroupKey(session.updated_at);
        let group = byKey.get(key);
        if (!group) {
          group = { key, label: this.sessionGroupLabel(session.updated_at), sessions: [] };
          byKey.set(key, group);
          groups.push(group);
        }
        group.sessions.push(session);
      }
      return groups;
    },
    visibleDetailMessages() {
      return this.detailMessages
        .filter((message) => !this.isInternalMessage(message))
        .map((message) => ({
          ...message,
          content: this.sanitizeVisibleText(message.content),
        }))
        .filter((message) => message.content);
    },
    // 生成页码按钮序列：首尾各一页 + 当前页左右各一页，超出用省略号占位
    pageNumbers() {
      const total = this.totalPages;
      const cur = this.currentPage;
      if (total <= 7) {
        return Array.from({ length: total }, (_, i) => i + 1);
      }
      const pages = [1];
      const start = Math.max(2, cur - 1);
      const end = Math.min(total - 1, cur + 1);
      if (start > 2) pages.push('...');
      for (let i = start; i <= end; i += 1) pages.push(i);
      if (end < total - 1) pages.push('...');
      pages.push(total);
      return pages;
    },
  },
  mounted() {
    this.loadStatus();
    this.loadSessions();
  },
  methods: {
    async loadStatus() {
      try {
        this.status = await getAgentStatus();
      } catch (_) {
        // 状态读取失败不影响会话列表展示
      }
    },
    // 重新注册 + 重连：触发后端 Enable（registerAndSync + 远程连接），再刷新健康度。
    async retryRegistration() {
      this.retrying = true;
      try {
        await enableAgent();
        await this.loadStatus();
      } catch (_) {
        // 忽略：刷新后的 registration/connection_state 会反映结果
      } finally {
        this.retrying = false;
      }
    },
    async loadSessions() {
      this.loading = true;
      this.error = '';
      try {
        const data = await getAgentSessions();
        this.sessions = (data && data.sessions) || [];
        this.currentPage = 1;
        // 后端在 user_agent runtime 不可达时返回空数组，据此提示运行时离线
        this.offline = this.sessions.length === 0;
      } catch (_) {
        this.error = this.t('agent_activity_loadFailed');
      } finally {
        this.loading = false;
      }
    },
    async toggle(s) {
      if (this.expandedId === s.id) {
        this.expandedId = '';
        return;
      }
      this.expandedId = s.id;
      this.detailMessages = [];
      this.hasMore = false;
      this.nextBefore = '';
      await this.fetchDetail(s.id);
    },
    async fetchDetail(id, before) {
      this.detailLoading = true;
      try {
        const detail = await getAgentSessionDetail(id, { limit: 40, before });
        const msgs = (detail && detail.transcript) || [];
        // transcript 按时间升序；加载更早的消息应插到已有消息之前
        this.detailMessages = before ? [...msgs, ...this.detailMessages] : msgs;
        const page = detail && detail.transcript_page;
        this.hasMore = Boolean(page && page.has_more);
        this.nextBefore = page && page.next_before_message_id ? page.next_before_message_id : '';
      } catch (_) {
        // 单会话读取失败静默，保留已加载内容
      } finally {
        this.detailLoading = false;
      }
    },
    loadMore(s) {
      if (this.nextBefore) this.fetchDetail(s.id, this.nextBefore);
    },
    goToPage(n) {
      const clamped = Math.min(Math.max(1, n), this.totalPages);
      if (clamped === this.currentPage) return;
      this.currentPage = clamped;
      // 切页后收起已展开的详情，避免展开行不在当前页造成状态错位
      this.expandedId = '';
      this.detailMessages = [];
    },
    sessionTitle(s) {
      const raw = String(s.title || s.summary || '').trim();
      const internalContext = /local-command-caveat|Caveat:\s*The messages below were generated/i.test(raw);
      const clean = internalContext
        ? ''
        : this.sanitizeVisibleText(raw.replace(/<[^>]+>/g, ' ')).replace(/\s+/g, ' ').trim();
      if (clean) return clean;
      const project = String(s.project_path || '').split(/[\\/]/).filter(Boolean).pop();
      return project || this.t('agent_activity_untitled');
    },
    isInternalMessage(message) {
      const content = String(message?.content || '').trim();
      return /<(?:local-command-caveat|command-name|command-message|command-args|local-command-(?:stdout|stderr))\b/i.test(content)
        || /Caveat:\s*The messages below were generated by the user while running local commands/i.test(content);
    },
    sanitizeVisibleText(value) {
      return String(value || '')
        .replace(/https?:\/\/ws-vb-phone\.aliang\.one(?:\/[^\s<]*)?/gi, '')
        .replace(/ws-vb-phone\.aliang\.one/gi, '')
        .trim();
    },
    sessionMeta(s) {
      const parts = [];
      const tool = String(s.tool || s.provider || '').toLowerCase();
      if (tool.includes('claude')) parts.push('Claude');
      else if (tool.includes('codex')) parts.push('Codex');
      else if (tool.includes('opencode')) parts.push('OpenCode');
      else if (tool) parts.push(tool);
      if (s.branch) parts.push(s.branch);
      if (s.message_count) parts.push(this.t('agent_activity_messageCount', { count: s.message_count }));
      return parts.join(' · ');
    },
    sessionIcon(s) {
      const name = (s.tool || s.provider || '').toLowerCase();
      if (name.includes('claude')) return 'smart_toy';
      if (name.includes('codex')) return 'terminal';
      return 'memory';
    },
    sessionIconClass(s) {
      const name = (s.tool || s.provider || '').toLowerCase();
      if (name.includes('claude')) return 'bg-orange-100 text-orange-700 dark:bg-orange-500/15 dark:text-orange-300';
      if (name.includes('codex')) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
      return 'bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300';
    },
    sessionGroupKey(value) {
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return 'unknown';
      return `${date.getFullYear()}-${date.getMonth()}-${date.getDate()}`;
    },
    sessionGroupLabel(value) {
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return this.t('agent_activity_earlier');
      const today = new Date();
      today.setHours(0, 0, 0, 0);
      const target = new Date(date);
      target.setHours(0, 0, 0, 0);
      const dayDiff = Math.round((today.getTime() - target.getTime()) / 86400000);
      if (dayDiff === 0) return this.t('agent_activity_today');
      if (dayDiff === 1) return this.t('agent_activity_yesterday');
      return date.toLocaleDateString([], { month: 'long', day: 'numeric', weekday: 'short' });
    },
    formatSessionTime(value) {
      if (!value) return '';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return value;
      return date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
    },
  },
};
</script>
