<template>
  <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <h3 class="flex items-center gap-2 font-bold">
          <span class="material-symbols-outlined text-primary">history</span>
          {{ t('agent_activity_title') }}
          <span
            v-if="status"
            class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
            :class="statusBadgeClass"
          >{{ statusLabel }}</span>
        </h3>
        <p class="mt-1 text-[11px] leading-5 text-slate-500 dark:text-slate-400">
          {{ t('agent_activity_desc') }}
        </p>
      </div>
      <button
        type="button"
        class="inline-flex h-8 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-2.5 text-[11px] font-bold text-slate-600 transition hover:bg-slate-50 disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        :disabled="loading"
        @click="loadSessions"
      >
        <span class="material-symbols-outlined text-sm">refresh</span>
        {{ t('agent_activity_refresh') }}
      </button>
    </div>

    <!-- loading / error / empty -->
    <div v-if="loading" class="mt-4 text-center text-[11px] text-slate-500 dark:text-slate-400">
      {{ t('agent_activity_loading') }}
    </div>
    <div
      v-else-if="error"
      class="mt-4 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-[11px] text-rose-700 dark:border-rose-500/30 dark:bg-rose-500/10 dark:text-rose-300"
    >{{ error }}</div>
    <div v-else-if="!sessions.length" class="mt-4 text-center text-[11px] text-slate-400">
      {{ offline ? t('agent_activity_runtimeOffline') : t('agent_activity_noSessions') }}
    </div>

    <!-- 会话列表 -->
    <ul v-else class="mt-4 space-y-2">
      <li
        v-for="s in pagedSessions"
        :key="s.id"
        class="rounded-lg border border-slate-200 bg-slate-50/60 dark:border-slate-700 dark:bg-slate-900/40"
      >
        <button
          type="button"
          class="flex w-full items-center gap-3 px-3 py-2.5 text-left transition hover:bg-slate-100/70 dark:hover:bg-slate-800/50"
          @click="toggle(s)"
        >
          <span class="material-symbols-outlined shrink-0 text-lg text-slate-400">{{ sessionIcon(s) }}</span>
          <div class="min-w-0 flex-1">
            <p class="truncate text-xs font-semibold text-slate-800 dark:text-slate-100">{{ sessionTitle(s) }}</p>
            <p class="mt-0.5 truncate text-[10px] text-slate-500 dark:text-slate-400">
              {{ sessionMeta(s) }}
            </p>
          </div>
          <span class="shrink-0 text-[10px] text-slate-400">{{ formatTime(s.updated_at) }}</span>
          <span
            v-if="s.message_count"
            class="shrink-0 rounded-full bg-slate-200 px-1.5 py-0.5 text-[10px] font-semibold text-slate-600 dark:bg-slate-700 dark:text-slate-300"
          >{{ s.message_count }} {{ t('agent_activity_messages') }}</span>
          <span
            class="material-symbols-outlined shrink-0 text-base text-slate-400 transition"
            :class="{ 'rotate-180': expandedId === s.id }"
          >expand_more</span>
        </button>

        <!-- 展开详情 -->
        <div v-if="expandedId === s.id" class="border-t border-slate-200 px-3 py-3 dark:border-slate-700">
          <div v-if="detailLoading" class="py-2 text-center text-[11px] text-slate-500">
            {{ t('agent_activity_loading') }}
          </div>
          <div
            v-else-if="!detailMessages.length"
            class="py-2 text-center text-[11px] text-slate-400"
          >{{ t('agent_activity_noSessions') }}</div>
          <template v-else>
            <div class="space-y-2">
              <div
                v-for="m in detailMessages"
                :key="m.id || m.index"
                class="flex"
                :class="m.role === 'user' ? 'justify-end' : 'justify-start'"
              >
                <div
                  class="max-w-[85%] whitespace-pre-wrap break-words rounded-lg px-2.5 py-1.5 text-[11px] leading-5"
                  :class="m.role === 'user'
                    ? 'bg-primary text-white'
                    : 'bg-slate-200/80 text-slate-700 dark:bg-slate-700 dark:text-slate-200'"
                >
                  <span
                    class="mb-0.5 block text-[9px] font-bold uppercase tracking-wide opacity-70"
                  >{{ m.role === 'user' ? t('agent_activity_you') : t('agent_activity_ai') }}</span>
                  <span :class="m.role === 'assistant' ? 'line-clamp-2' : ''">{{ m.content }}</span>
                </div>
              </div>
            </div>
            <div class="mt-2 text-center">
              <button
                v-if="hasMore"
                type="button"
                class="inline-flex items-center gap-1 rounded-md px-2 py-1 text-[10px] font-semibold text-primary hover:underline disabled:opacity-60"
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

    <!-- 分页：仅当超过一页时展示 -->
    <div
      v-if="!loading && !error && sessions.length && totalPages > 1"
      class="mt-4 flex flex-wrap items-center justify-center gap-1.5 text-[11px]"
    >
      <button
        type="button"
        class="inline-flex h-7 items-center gap-0.5 rounded-md border border-slate-200 px-2 font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
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
          class="inline-flex h-7 min-w-[1.75rem] items-center justify-center rounded-md border px-1.5 font-semibold transition"
          :class="p === currentPage
            ? 'border-primary bg-primary text-white'
            : 'border-slate-200 text-slate-600 hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800'"
          @click="goToPage(p)"
        >{{ p }}</button>
      </template>
      <button
        type="button"
        class="inline-flex h-7 items-center gap-0.5 rounded-md border border-slate-200 px-2 font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-40 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
        :disabled="currentPage >= totalPages"
        @click="goToPage(currentPage + 1)"
      >
        {{ t('agent_activity_next') }}
        <span class="material-symbols-outlined text-sm">chevron_right</span>
      </button>
      <span class="ml-1 text-slate-400">
        {{ t('agent_activity_pageInfo', { page: currentPage, total: totalPages, count: sessions.length }) }}
      </span>
    </div>
  </div>
</template>

<script>
import { getAgentStatus, getAgentSessions, getAgentSessionDetail } from '../../services/agentApi';
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
    totalPages() {
      return Math.max(1, Math.ceil(this.sessions.length / this.pageSize));
    },
    pagedSessions() {
      const start = (this.currentPage - 1) * this.pageSize;
      return this.sessions.slice(start, start + this.pageSize);
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
      return s.title || s.project_path || s.id || '';
    },
    sessionMeta(s) {
      const parts = [];
      if (s.tool) parts.push(s.tool);
      else if (s.provider) parts.push(s.provider);
      if (s.model) parts.push(s.model);
      if (s.branch) parts.push(s.branch);
      return parts.join(' · ');
    },
    sessionIcon(s) {
      const name = (s.tool || s.provider || '').toLowerCase();
      if (name.includes('claude')) return 'smart_toy';
      if (name.includes('codex')) return 'terminal';
      return 'memory';
    },
    formatTime(value) {
      if (!value) return '';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return value;
      return date.toLocaleString();
    },
  },
};
</script>
