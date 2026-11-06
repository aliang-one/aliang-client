<template>
  <div class="rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900">
    <div class="flex items-center justify-between gap-3">
      <p class="flex min-w-0 items-center gap-2 text-xs font-bold uppercase tracking-wider text-slate-500">
        <span class="material-symbols-outlined text-base text-primary">dns</span>
        <span class="truncate">{{ t('dash_systemStatusTitle') }}</span>
      </p>
      <span class="size-2 shrink-0 rounded-full" :class="dotClass"></span>
    </div>
    <div class="mt-3 space-y-1.5 text-xs">
      <div class="flex min-w-0 items-center justify-between gap-2">
        <span class="min-w-0 truncate text-slate-500">{{ t('sys_softwareStatus') }}</span>
        <span class="shrink-0 font-semibold" :class="serviceTextClass">{{ serviceLabel }}</span>
      </div>
      <div class="flex min-w-0 items-center justify-between gap-2">
        <span class="min-w-0 truncate text-slate-500">{{ t('sys_certCa') }}</span>
        <span class="shrink-0 font-semibold" :class="certTextClass">{{ certLabel }}</span>
      </div>
    </div>
    <button
      type="button"
      data-testid="system-status-manage"
      class="mt-3 flex min-h-10 w-full items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition hover:border-primary hover:text-primary dark:border-slate-700 dark:text-slate-300"
      @click="openModal"
    >
      <span class="material-symbols-outlined text-sm">tune</span>
      {{ t('dash_systemManage') }}
    </button>

    <!-- 弹窗承载完整卡片(CertManagementModal 同款壳;~560px)。
         关闭后刷新一次紧凑卡摘要:弹窗内可能做了服务/证书操作。 -->
    <div v-if="modalOpen" class="fixed inset-0 z-[120] flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeModal"></div>
      <div
        data-testid="system-status-modal"
        class="relative z-10 flex max-h-[85dvh] w-full max-w-[560px] flex-col overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900"
      >
        <div class="flex shrink-0 items-center justify-between border-b border-slate-200 px-5 py-4 dark:border-slate-700">
          <h3 class="text-base font-bold text-slate-900 dark:text-slate-100">{{ t('dash_systemStatusTitle') }}</h3>
          <button
            type="button"
            data-testid="system-status-modal-close"
            :aria-label="t('dash_systemStatusTitle')"
            class="flex size-11 items-center justify-center rounded-full text-slate-500 transition hover:bg-slate-100 dark:hover:bg-slate-800"
            @click="closeModal"
          >
            <span class="material-symbols-outlined">close</span>
          </button>
        </div>
        <div class="min-h-0 flex-1 overflow-y-auto p-4">
          <SystemStatusSettings />
        </div>
      </div>
    </div>
  </div>
</template>

<script>
// 侧栏「系统状态」紧凑卡(2026-11-05 spec §4.2A):服务/证书两行摘要 +
// 「管理…」弹窗承载 SystemStatusSettings 完整卡。桌面 aside 与移动端
// data-mobile-panel 同组件复用;服务状态自取(mount + 弹窗关闭后刷新),
// 证书态走 useCertStatus 模块单例(勿另起轮询)。
import SystemStatusSettings from '../settings/SystemStatusSettings.vue';
import { useI18n } from '../../i18n';
import { useCertStatus } from '../../composables/useCertStatus';

const EMPTY_SERVICE_INFO = {
  supported: false,
  installed: false,
  running: false
};

export default {
  name: 'SystemStatusSidebarCard',
  components: { SystemStatusSettings },
  setup() {
    const { t } = useI18n();
    const { certStatus, startPolling: startCertPolling, stopPolling: stopCertPolling } = useCertStatus();
    return { t, certStatus, startCertPolling, stopCertPolling };
  },
  data() {
    return {
      serviceInfo: { ...EMPTY_SERVICE_INFO },
      modalOpen: false
    };
  },
  computed: {
    serviceLabel() {
      if (!this.serviceInfo.supported) {
        return this.t('sys_badgeNotSupported');
      }
      if (!this.serviceInfo.installed) {
        return this.t('sys_badgeNotRegistered');
      }
      return this.serviceInfo.running ? this.t('sys_running') : this.t('sys_badgeRegistered');
    },
    serviceTextClass() {
      if (!this.serviceInfo.supported) {
        return 'text-slate-400';
      }
      if (!this.serviceInfo.installed) {
        return 'text-amber-600 dark:text-amber-300';
      }
      return this.serviceInfo.running
        ? 'text-emerald-600 dark:text-emerald-400'
        : 'text-sky-600 dark:text-sky-300';
    },
    dotClass() {
      if (!this.serviceInfo.supported || !this.serviceInfo.installed) {
        return this.serviceInfo.installed ? 'bg-sky-400' : 'bg-slate-300';
      }
      return this.serviceInfo.running ? 'bg-emerald-500' : 'bg-sky-400';
    },
    certTrusted() {
      return Boolean(this.certStatus?.is_trusted);
    },
    certLabel() {
      return this.certTrusted ? this.t('dash_certTrusted') : this.t('dash_certNotTrusted');
    },
    certTextClass() {
      return this.certTrusted
        ? 'text-emerald-600 dark:text-emerald-400'
        : 'text-amber-600 dark:text-amber-300';
    }
  },
  mounted() {
    this.loadServiceStatus();
    // 无其他消费者时由本卡拉起证书轮询(refCount 单例,重复 start 无害)
    this.startCertPolling();
  },
  beforeUnmount() {
    this.stopCertPolling();
  },
  methods: {
    async loadServiceStatus() {
      try {
        const res = await fetch('/api/system/service/status', { method: 'GET' });
        const payload = await res.json().catch(() => ({}));
        if (res.ok && payload?.code === 0 && payload.data && typeof payload.data === 'object') {
          this.serviceInfo = { ...EMPTY_SERVICE_INFO, ...payload.data };
        }
      } catch {
        // 静默:紧凑卡留在默认态,完整态在弹窗内可刷新
      }
    },
    openModal() {
      this.modalOpen = true;
    },
    closeModal() {
      this.modalOpen = false;
      this.loadServiceStatus();
    }
  }
};
</script>
