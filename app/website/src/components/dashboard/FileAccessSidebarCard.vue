<template>
  <div class="rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
    <div class="flex items-center justify-between gap-3">
      <p class="flex min-w-0 items-center gap-2 text-xs font-bold uppercase tracking-wider text-slate-500">
        <span class="material-symbols-outlined text-base text-primary">folder_shared</span>
        <span class="truncate">{{ t('agent_fileAccess_title') }}</span>
      </p>
      <span
        v-if="hasDenied"
        data-testid="file-access-denied"
        class="size-2 shrink-0 rounded-full bg-amber-400"
        :title="t('agent_fileAccess_deniedHint')"
      ></span>
      <span v-else-if="allGranted" class="size-2 shrink-0 rounded-full bg-emerald-500"></span>
    </div>
    <div class="mt-3 flex min-w-0 items-center justify-between gap-2 text-xs">
      <span class="min-w-0 truncate text-slate-500">{{ t('agent_fileAccess_summary', summaryParams) }}</span>
    </div>
    <button
      type="button"
      data-testid="file-access-manage"
      class="mt-3 flex min-h-10 w-full items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition hover:border-primary hover:text-primary dark:border-slate-700 dark:text-slate-300"
      @click="openModal"
    >
      <span class="material-symbols-outlined text-sm">verified_user</span>
      {{ t('dash_fileAccessManage') }}
    </button>

    <!-- 弹窗承载 AgentFileAccessSettings 原卡(自取数据自轮询零 props)。
         关闭后刷新一次紧凑卡摘要:授权申请可能在弹窗内完成。 -->
    <div v-if="modalOpen" class="fixed inset-0 z-[120] flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeModal"></div>
      <div
        data-testid="file-access-modal"
        class="relative z-10 flex max-h-[85dvh] w-full max-w-[560px] flex-col overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      >
        <div class="flex shrink-0 items-center justify-between border-b border-slate-200 px-5 py-4 dark:border-slate-700">
          <h3 class="text-base font-bold text-slate-900 dark:text-slate-100">{{ t('agent_fileAccess_title') }}</h3>
          <button
            type="button"
            data-testid="file-access-modal-close"
            :aria-label="t('agent_fileAccess_title')"
            class="flex size-11 items-center justify-center rounded-full text-slate-500 transition hover:bg-slate-100 dark:hover:bg-slate-800"
            @click="closeModal"
          >
            <span class="material-symbols-outlined">close</span>
          </button>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto p-4">
          <AgentFileAccessSettings />
        </div>
      </div>
    </div>
  </div>
</template>

<script>
// 侧栏「文件访问授权」紧凑卡(2026-11-05 spec §4.2B):授权进度摘要 +
// 「查看 / 申请…」弹窗承载 AgentFileAccessSettings 原卡。桌面 aside 与
// 移动端 data-mobile-panel 同组件复用;状态自取(mount + 弹窗关闭后刷新)。
// ⚠️ categories 必须防御性默认空数组:mobile e2e 对 /api/** 回 { code:0, data:{} },
// 无守卫的 .categories.length 会在哨兵页抛 pageerror。
import AgentFileAccessSettings from '../settings/AgentFileAccessSettings.vue';
import { getAgentFileAccess } from '../../services/agentApi';
import { useI18n } from '../../i18n';

const EMPTY_SNAPSHOT = {
  supported: false,
  attended: false,
  state: 'idle',
  categories: []
};

export default {
  name: 'FileAccessSidebarCard',
  components: { AgentFileAccessSettings },
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      snapshot: { ...EMPTY_SNAPSHOT, categories: [] },
      modalOpen: false
    };
  },
  computed: {
    categories() {
      return Array.isArray(this.snapshot.categories) ? this.snapshot.categories : [];
    },
    grantedCount() {
      return this.categories.filter((c) => c.state === 'granted').length;
    },
    hasDenied() {
      return this.categories.some((c) => c.state === 'denied');
    },
    allGranted() {
      return this.categories.length > 0 && this.grantedCount === this.categories.length;
    },
    summaryParams() {
      const count = (s) => this.categories.filter((c) => c.state === s).length;
      return {
        granted: count('granted'),
        denied: count('denied'),
        absent: count('absent'),
        total: this.categories.length
      };
    }
  },
  mounted() {
    this.load();
  },
  methods: {
    applySnapshot(data) {
      if (!data || typeof data !== 'object') {
        return;
      }
      this.snapshot = {
        ...EMPTY_SNAPSHOT,
        ...data,
        categories: Array.isArray(data.categories) ? data.categories : []
      };
    },
    async load() {
      try {
        this.applySnapshot(await getAgentFileAccess());
      } catch {
        // 静默:紧凑卡留在默认态,完整态在弹窗内可刷新
      }
    },
    openModal() {
      this.modalOpen = true;
    },
    closeModal() {
      this.modalOpen = false;
      this.load();
    }
  }
};
</script>
