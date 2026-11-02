<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">folder_shared</span>
      </div>
      <div class="min-w-0">
        <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_fileAccess_title') }}</h3>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
          {{ t('agent_fileAccess_desc') }}
        </p>
      </div>
    </div>

    <!-- 无人值守：核心保护——绝不制造没人能点的系统弹窗 -->
    <div
      v-if="!snapshot.supported"
      class="mt-4 rounded-lg border border-slate-200 bg-slate-50 px-4 py-3 text-xs text-slate-600 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300"
    >
      {{ t('agent_fileAccess_unsupported') }}
    </div>
    <div
      v-else-if="!snapshot.attended"
      class="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-xs leading-5 text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
    >
      {{ t('agent_fileAccess_unattended') }}
    </div>
    <div
      v-else-if="snapshot.state === 'running'"
      class="mt-4 rounded-lg border border-primary/30 bg-primary/5 px-4 py-3 text-xs leading-5 text-primary dark:border-primary/40 dark:bg-primary/10"
    >
      {{ t('agent_fileAccess_runningHint') }}
    </div>

    <!-- 类别清单 -->
    <ul class="mt-4 space-y-1.5" data-testid="file-access-status">
      <li
        v-for="cat in snapshot.categories"
        :key="cat.key"
        class="flex items-center gap-2 rounded-lg border border-slate-100 px-3 py-2 text-xs dark:border-slate-800"
      >
        <span class="material-symbols-outlined text-base" :class="iconClass(cat.state)">{{ iconName(cat.state) }}</span>
        <span class="min-w-0 flex-1">
          <span class="font-semibold text-slate-700 dark:text-slate-200">{{ t(categoryLabelKey(cat.key)) }}</span>
          <span class="ml-1 truncate font-mono text-[10px] text-slate-400 dark:text-slate-500">{{ cat.root }}</span>
        </span>
        <span
          class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
          :class="badgeClass(cat.state)"
          :title="cat.detail || ''"
        >
          {{ t(stateLabelKey(cat.state)) }}
        </span>
      </li>
    </ul>

    <!-- 汇总 -->
    <p v-if="snapshot.state === 'done'" class="mt-3 text-[11px] text-slate-500 dark:text-slate-400">
      {{ summaryText }}
    </p>

    <!-- 被拒后的补救：macOS 对被拒过的目录不再弹窗，只能去系统设置手动改 -->
    <div v-if="hasDenied" class="mt-3 flex flex-wrap items-center gap-2">
      <span class="text-[11px] text-amber-700 dark:text-amber-300">{{ t('agent_fileAccess_deniedHint') }}</span>
      <button
        type="button"
        class="inline-flex min-h-9 items-center gap-1 rounded-lg border border-amber-300 px-2.5 text-[11px] font-semibold text-amber-800 transition-colors hover:bg-amber-50 dark:border-amber-500/40 dark:text-amber-200 dark:hover:bg-amber-500/10"
        data-testid="file-access-open-settings"
        @click="openSettings('files')"
      >
        <span class="material-symbols-outlined text-sm">open_in_new</span>
        {{ t('agent_fileAccess_openSettings') }}
      </button>
    </div>

    <div class="mt-4 flex flex-wrap items-center gap-2">
      <button
        type="button"
        class="inline-flex h-11 items-center justify-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
        :disabled="!canRequest"
        data-testid="file-access-request"
        @click="requestAccess"
      >
        <span class="material-symbols-outlined text-base" :class="{ 'animate-spin': busy }">{{ busy ? 'progress_activity' : 'verified_user' }}</span>
        {{ busy ? t('agent_fileAccess_requesting') : t('agent_fileAccess_request') }}
      </button>
      <button
        type="button"
        class="inline-flex h-11 items-center gap-1 rounded-lg border border-slate-300 px-3 text-xs font-semibold text-slate-600 transition-colors hover:border-primary hover:text-primary disabled:opacity-60 dark:border-slate-700 dark:text-slate-300"
        :disabled="busy"
        data-testid="file-access-refresh"
        @click="load"
      >
        <span class="material-symbols-outlined text-sm">refresh</span>
        {{ t('agent_fileAccess_refresh') }}
      </button>
      <button
        type="button"
        class="inline-flex h-11 items-center gap-1 rounded-lg border border-slate-300 px-3 text-xs font-semibold text-slate-600 transition-colors hover:border-primary hover:text-primary disabled:opacity-60 dark:border-slate-700 dark:text-slate-300"
        :disabled="!snapshot.supported"
        data-testid="file-access-fda"
        @click="openSettings('allfiles')"
      >
        <span class="material-symbols-outlined text-sm">shield</span>
        {{ t('agent_fileAccess_openFda') }}
      </button>
    </div>

    <p class="mt-3 text-[11px] leading-5 text-slate-400 dark:text-slate-500">
      {{ t('agent_fileAccess_footnote') }}
    </p>
    <p v-if="feedback" class="mt-2 text-[11px]" :class="feedbackType === 'error' ? 'text-rose-600 dark:text-rose-300' : 'text-emerald-600 dark:text-emerald-300'">
      {{ feedback }}
    </p>
  </div>
</template>

<script>
import {
  getAgentFileAccess,
  requestAgentFileAccess,
  openAgentFileAccessSettings,
} from '../../services/agentApi';
import { useI18n } from '../../i18n';

// 轮询节奏：探测是「用户在 Mac 上一个个点允许」，1.5s 足够及时又不吵。
const POLL_INTERVAL_MS = 1500;
// 硬停轮询的上限：8 类 × 单类 25s 上限 = 最坏 200s，再加一点缓冲。
// 取小了会在 sweep 还没跑完时停轮询，把用户推给「刷新状态」。
const POLL_DEADLINE_MS = 240000;

const EMPTY_SNAPSHOT = {
  supported: false,
  attended: false,
  state: 'idle',
  probeTimeoutSec: 25,
  categories: [],
};

export default {
  name: 'AgentFileAccessSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      snapshot: { ...EMPTY_SNAPSHOT },
      busy: false,
      feedback: '',
      feedbackType: 'info',
      pollTimer: null,
      pollDeadline: 0,
      // disposed：组件卸载后，仍在飞行的请求回来时不得再启动轮询
      // （否则定时器会挂在一个已销毁的组件上一直打后端）。
      disposed: false,
    };
  },
  computed: {
    canRequest() {
      return this.snapshot.supported && this.snapshot.attended && !this.busy;
    },
    hasDenied() {
      return this.snapshot.categories.some((c) => c.state === 'denied');
    },
    summaryText() {
      const cats = this.snapshot.categories || [];
      const count = (s) => cats.filter((c) => c.state === s).length;
      return this.t('agent_fileAccess_summary', {
        granted: count('granted'),
        denied: count('denied'),
        absent: count('absent'),
        total: cats.length,
      });
    },
  },
  mounted() {
    this.load();
  },
  beforeUnmount() {
    this.disposed = true;
    this.stopPolling();
  },
  methods: {
    applySnapshot(data) {
      if (this.disposed) return;
      if (!data || typeof data !== 'object') return;
      this.snapshot = {
        ...EMPTY_SNAPSHOT,
        ...data,
        categories: Array.isArray(data.categories) ? data.categories : [],
      };
      if (this.snapshot.state === 'running') {
        this.busy = true;
        this.startPolling();
      } else {
        this.busy = false;
        this.stopPolling();
      }
    },
    async load() {
      try {
        this.applySnapshot(await getAgentFileAccess());
      } catch (_) {
        // 加载失败静默：卡片留在默认态，不阻塞设置页
      }
    },
    async requestAccess() {
      if (!this.canRequest) return;
      this.busy = true;
      this.feedback = '';
      try {
        const data = await requestAgentFileAccess();
        const snap = data && data.snapshot ? data.snapshot : null;
        if (snap) this.applySnapshot(snap);
        // busy/轮询一律由 applySnapshot 按 snapshot.state 决定：这里再写 busy
        // 会把「已在跑」的 running 状态抹掉（后端幂等分支返回 started=false +
        // state=running），导致按钮在弹窗还挂着时又变回可点。
        if (!snap) {
          this.busy = false;
        }
      } catch (_) {
        this.feedback = this.t('agent_fileAccess_requestFailed');
        this.feedbackType = 'error';
        this.busy = false;
      }
    },
    startPolling() {
      if (this.disposed || this.pollTimer) return;
      this.pollDeadline = Date.now() + POLL_DEADLINE_MS;
      this.pollTimer = setInterval(async () => {
        if (this.disposed) {
          this.stopPolling();
          return;
        }
        if (Date.now() > this.pollDeadline) {
          this.stopPolling();
          this.busy = false;
          this.feedback = this.t('agent_fileAccess_pollStopped');
          this.feedbackType = 'info';
          return;
        }
        try {
          this.applySnapshot(await getAgentFileAccess());
        } catch (_) {
          // 单次失败不打断轮询
        }
      }, POLL_INTERVAL_MS);
    },
    stopPolling() {
      if (this.pollTimer) {
        clearInterval(this.pollTimer);
        this.pollTimer = null;
      }
    },
    async openSettings(pane) {
      try {
        await openAgentFileAccessSettings(pane);
        this.feedback = this.t('agent_fileAccess_settingsOpened');
        this.feedbackType = 'info';
      } catch (_) {
        this.feedback = this.t('agent_fileAccess_settingsFailed');
        this.feedbackType = 'error';
      }
    },
    categoryLabelKey(key) {
      return `agent_fileAccess_cat_${key}`;
    },
    stateLabelKey(state) {
      return `agent_fileAccess_state_${state || 'unknown'}`;
    },
    iconName(state) {
      switch (state) {
        case 'granted':
          return 'check_circle';
        case 'denied':
          return 'block';
        case 'absent':
          return 'remove';
        case 'pending':
          return 'hourglass_top';
        case 'skipped':
          return 'skip_next';
        default:
          return 'help';
      }
    },
    iconClass(state) {
      switch (state) {
        case 'granted':
          return 'text-emerald-500';
        case 'denied':
          return 'text-rose-500';
        case 'pending':
          return 'text-amber-500 animate-pulse';
        default:
          return 'text-slate-400';
      }
    },
    badgeClass(state) {
      switch (state) {
        case 'granted':
          return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300';
        case 'denied':
          return 'bg-rose-50 text-rose-700 dark:bg-rose-500/10 dark:text-rose-300';
        case 'pending':
          return 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300';
        default:
          return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400';
      }
    },
  },
};
</script>
