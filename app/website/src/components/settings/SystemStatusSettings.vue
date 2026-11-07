<template>
  <div class="settings-pane" data-pane="system-status">
    <div class="rounded-xl border border-slate-200 bg-white p-4 md:p-5 dark:border-slate-800 dark:bg-background-dark">
      <h3 class="mb-4 flex items-center gap-2 font-bold">
        <span class="material-symbols-outlined text-primary">settings</span>
        {{ t('sys_systemSettings') }}
      </h3>

      <div class="space-y-6">
        <div
          v-if="showSystemServiceConfirm"
          class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/65 p-4 backdrop-blur-sm"
          role="dialog"
          aria-modal="true"
          @click.self="closeSystemServiceConfirm"
        >
          <div class="max-h-[85dvh] w-full max-w-lg overflow-y-auto overflow-x-hidden rounded-3xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
            <div class="border-b border-slate-200 bg-slate-50/85 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/65">
              <div class="flex items-start justify-between gap-4">
                <div class="flex items-start gap-3">
                  <div
                    class="mt-0.5 flex h-11 w-11 items-center justify-center rounded-2xl border text-sm shadow-sm"
                    :class="systemServiceConfirmAction === 'uninstall'
                      ? 'border-red-200 bg-red-50 text-red-600 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200'
                      : 'border-primary/20 bg-primary/10 text-primary dark:border-primary/30 dark:bg-primary/15'"
                  >
                    <span class="material-symbols-outlined text-[20px]">
                      {{ systemServiceConfirmAction === 'uninstall' ? 'delete' : 'deployed_code' }}
                    </span>
                  </div>
                  <div>
                    <p
                      class="text-[11px] font-bold uppercase tracking-[0.22em]"
                      :class="systemServiceConfirmAction === 'uninstall'
                        ? 'text-red-500 dark:text-red-300'
                        : 'text-primary'"
                    >
                      {{ systemServiceConfirmAction === 'uninstall' ? t('sys_removeService') : t('sys_registerService') }}
                    </p>
                    <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">
                      {{ systemServiceConfirmTitle }}
                    </h3>
                    <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
                      {{ systemServiceConfirmDescription }}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  class="rounded-xl p-3 md:p-2 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                  :disabled="serviceActionLoading"
                  @click="closeSystemServiceConfirm"
                >
                  <span class="material-symbols-outlined text-lg">close</span>
                </button>
              </div>
            </div>

            <div class="space-y-4 p-5">
              <div
                class="rounded-2xl border px-4 py-3 text-sm"
                :class="systemServiceConfirmAction === 'uninstall'
                  ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200'
                  : 'border-primary/20 bg-primary/5 text-slate-700 dark:border-primary/30 dark:bg-primary/10 dark:text-slate-100'"
              >
                {{ systemServiceConfirmHighlight }}
              </div>

              <div class="rounded-2xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-700 dark:bg-slate-800/45">
                <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('sys_operationSummary') }}</p>
                <div class="mt-3 space-y-2">
                  <div
                    v-for="item in systemServiceConfirmChecklist"
                    :key="item"
                    class="flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200"
                  >
                    <span class="material-symbols-outlined mt-0.5 text-[16px] text-primary">check_circle</span>
                    <span>{{ item }}</span>
                  </div>
                </div>
              </div>

              <div class="flex gap-3">
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-xl border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
                  :disabled="serviceActionLoading"
                  @click="closeSystemServiceConfirm"
                >
                  {{ t('sys_cancel') }}
                </button>
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-xl px-4 text-sm font-semibold text-white transition disabled:cursor-not-allowed disabled:opacity-60"
                  :class="systemServiceConfirmAction === 'uninstall'
                    ? 'bg-red-600 hover:bg-red-500 dark:bg-red-500 dark:hover:bg-red-400'
                    : 'bg-primary hover:bg-primary/90'"
                  :disabled="serviceActionLoading"
                  @click="confirmSystemServiceAction"
                >
                  {{ systemServiceConfirmButtonLabel }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="space-y-2">
          <p class="text-sm font-semibold">{{ t('sys_softwareStatus') }}</p>
          <div class="rounded border-l-4 p-3" :class="serviceCardClass">
            <div class="flex items-start justify-between gap-3">
              <div>
                <p class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ systemServiceTitle }}</p>
                <p class="mt-1 text-[10px] leading-relaxed text-slate-600 dark:text-slate-400">
                  {{ systemServiceDescription }}
                </p>
                <p
                  v-if="showServicePrivilegeHint"
                  class="mt-2 text-[10px] font-semibold text-amber-700 dark:text-amber-300"
                >
                  {{ t('sys_adminRequired') }}
                </p>
              </div>
              <span class="rounded-full px-2 py-0.5 text-[10px] font-bold" :class="serviceBadgeClass">
                {{ systemServiceBadge }}
              </span>
            </div>

            <div v-if="systemServiceInfo.warning" class="mt-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-[10px] text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
              {{ systemServiceInfo.warning }}
            </div>

            <div v-if="systemServiceInfo.supported && systemServiceInfo.installed" class="mt-3 grid grid-cols-2 gap-2 rounded border border-slate-200 bg-white/70 p-3 text-[10px] text-slate-600 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-300">
              <div>
                <p class="text-slate-400">{{ t('sys_status') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ systemServiceInfo.status || 'unknown' }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_pid') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ systemServiceInfo.pid || '-' }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_platform') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ friendlyPlatformLabel }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_kind') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ friendlyServiceLabel }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_scope') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ systemServiceScopeLabel }}</p>
              </div>
            </div>

            <p v-if="systemServiceError" class="mt-3 text-[11px] text-red-500">{{ systemServiceError }}</p>
            <p v-if="systemServiceMessage" class="mt-3 text-[11px] text-emerald-600 dark:text-emerald-400">{{ systemServiceMessage }}</p>

            <div class="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
              <button
                type="button"
                class="rounded bg-slate-900 py-2.5 md:py-1.5 text-[11px] font-bold text-white transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-primary"
                :disabled="serviceActionLoading || !systemServiceInfo.supported || systemServiceInfo.installed || showServicePrivilegeHint"
                @click="openSystemServiceConfirm('install')"
              >
                {{ serviceActionLoading && serviceAction === 'install' ? t('sys_registering') : t('sys_registerServiceBtn') }}
              </button>
              <button
                type="button"
                class="rounded border border-red-200 py-2.5 md:py-1.5 text-[11px] font-bold text-red-600 transition hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-red-500/30 dark:text-red-300 dark:hover:bg-red-500/10"
                :disabled="serviceActionLoading || !systemServiceInfo.supported || !systemServiceInfo.installed || systemScopeServiceBlocked"
                @click="openSystemServiceConfirm('uninstall')"
              >
                {{ serviceActionLoading && serviceAction === 'uninstall' ? t('sys_uninstalling') : t('sys_uninstallServiceBtn') }}
              </button>
            </div>

            <button
              type="button"
              class="mt-2 w-full rounded border border-slate-200 py-2.5 md:py-1.5 text-[11px] font-bold text-slate-700 transition hover:bg-white disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-900/50"
              :disabled="serviceActionLoading"
              @click="refreshSystemServiceStatus"
            >
              {{ t('sys_refreshServiceStatus') }}
            </button>
          </div>
        </div>

        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <p class="text-sm font-semibold">{{ t('sys_certCa') }}</p>
            <span class="flex items-center gap-1 text-[10px] font-bold" :class="sharedCertStatus?.is_trusted ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-300'">
              <span class="material-symbols-outlined text-xs">verified_user</span>
              {{ sharedCertStatus?.is_trusted ? t('sys_trusted') : t('cert_notTrusted') }}
            </span>
          </div>
          <div class="space-y-1 rounded border border-slate-100 p-2 font-mono text-[10px] text-slate-500 dark:border-slate-800">
            <div class="truncate"><span class="text-slate-400">{{ t('sys_subject') }}</span> {{ sharedCertStatus?.subject || '-' }}</div>
            <div class="truncate"><span class="text-slate-400">{{ t('sys_validity') }}</span> {{ sharedCertStatus?.not_before || '-' }} - {{ sharedCertStatus?.not_after || '-' }}</div>
            <div class="truncate"><span class="text-slate-400">{{ t('sys_finger') }}</span> {{ sharedCertStatus?.fingerprint || '-' }}</div>
          </div>
          <div class="grid grid-cols-2 gap-2">
            <button type="button" class="flex min-h-11 cursor-pointer items-center justify-center gap-1 rounded border border-slate-200 py-1.5 text-[10px] font-bold hover:bg-slate-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:border-slate-800 dark:hover:bg-slate-800" @click="downloadCurrentCertificate">
              <span class="material-symbols-outlined text-sm">download</span>
              {{ t('sys_export') }}
            </button>
            <button type="button" class="flex min-h-11 cursor-pointer items-center justify-center gap-1 rounded border border-primary py-1.5 text-[10px] font-bold text-primary hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary" @click="openCertificateModal">
              <span class="material-symbols-outlined text-sm">install_desktop</span>
              {{ t('sys_install') }}
            </button>
          </div>
          <button type="button" class="flex min-h-11 w-full cursor-pointer items-center justify-center gap-1 text-[10px] font-semibold text-slate-600 hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary dark:text-slate-300" @click="openCertificateModal">
            <span class="material-symbols-outlined text-sm">admin_panel_settings</span>
            {{ t('sys_manageCertificates') }}
          </button>
        </div>

        <div class="compat-anchors" aria-hidden="true">
          <button type="button" id="openCertManagementModalBtn"></button>
          <button type="button" id="btn-install-cert"></button>
          <button type="button" id="btn-export-cert"></button>
          <button type="button" id="btn-remove-cert"></button>
          <button type="button" id="btn-download-cert"></button>
          <button type="button" id="btn-check-cert"></button>
          <span id="cert-status"></span>
          <span id="cert-source"></span>
          <span id="cert-validity"></span>
          <span id="cert-fingerprint"></span>
          <span id="cert-subject"></span>
          <span id="cert-issuer"></span>
          <span id="cert-san"></span>
          <span id="cert-not-before"></span>
          <span id="cert-not-after"></span>
          <span id="cert-last-check"></span>
          <span id="cert-status-badge"></span>
          <div id="cert-raw-detail"></div>
          <span id="cert-trust"></span>
          <span id="cert-device"></span>
          <span id="cert-error"></span>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
// 拆自 SystemSettings.vue(2026-11-05 spec §4.1B):软件状态(系统服务注册/
// 卸载+确认弹窗)与证书 CA 两区块,逐方法照搬零行为改动。
// 宿主:仪表盘侧栏「系统状态」紧凑卡的弹窗(spec §4.2A)。
import { useI18n } from '../../i18n';
import { useCertStatus } from '../../composables/useCertStatus';

export default {
  name: 'SystemStatusSettings',
  setup() {
    const { t } = useI18n();
    const { certStatus, startPolling: startCertPolling, stopPolling: stopCertPolling } = useCertStatus();

    return {
      t,
      sharedCertStatus: certStatus,
      startCertStatusPolling: startCertPolling,
      stopCertStatusPolling: stopCertPolling
    };
  },
  data() {
    return {
      systemServiceInfo: {
        supported: false,
        installed: false,
        running: false,
        status: 'unknown',
        pid: 0,
        platform: '',
        service_kind: '',
        display_name: '',
        message: '',
        warning: '',
        requires_admin: false,
        has_privileges: false
      },
      serviceActionLoading: false,
      serviceAction: '',
      systemServiceError: '',
      systemServiceMessage: '',
      showSystemServiceConfirm: false,
      systemServiceConfirmAction: ''
    };
  },
  computed: {
    showServicePrivilegeHint() {
      if (!this.systemServiceInfo.supported) {
        return false;
      }
      if (this.systemServiceInfo.requires_admin && !this.systemServiceInfo.has_privileges) {
        return true;
      }
      return this.systemScopeServiceBlocked;
    },
    systemScopeServiceBlocked() {
      const info = this.systemServiceInfo;
      return Boolean(info.supported && info.installed && info.install_scope === 'system' && !info.has_privileges);
    },
    systemServiceScopeLabel() {
      switch (this.systemServiceInfo.install_scope) {
        case 'system':
          return this.t('sys_scopeSystem');
        case 'user':
          return this.t('sys_scopeUser');
        default:
          return this.t('sys_scopeNone');
      }
    },
    systemServiceTitle() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_svcNotSupported');
      }
      return this.systemServiceInfo.display_name || this.t('sys_svcAliangDefault');
    },
    systemServiceDescription() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_svcDescNotSupported');
      }
      if (!this.systemServiceInfo.installed) {
        return this.t('sys_svcDescNotInstalled', { label: this.friendlyServiceLabel });
      }
      return this.systemServiceInfo.message || this.t('sys_svcDescInstalled');
    },
    systemServiceBadge() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_badgeNotSupported');
      }
      if (!this.systemServiceInfo.installed) {
        return this.t('sys_badgeNotRegistered');
      }
      return this.systemServiceInfo.running ? this.t('sys_badgeRunning') : this.t('sys_badgeRegistered');
    },
    friendlyPlatformLabel() {
      return this.systemServiceInfo.platform_label || this.systemServiceInfo.platform || '-';
    },
    friendlyServiceLabel() {
      return this.systemServiceInfo.service_label || this.systemServiceInfo.service_kind || this.t('sys_serviceLabelFallback');
    },
    serviceBadgeClass() {
      if (!this.systemServiceInfo.supported) {
        return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
      if (!this.systemServiceInfo.installed) {
        return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
      }
      return this.systemServiceInfo.running
        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
        : 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300';
    },
    serviceCardClass() {
      if (!this.systemServiceInfo.supported) {
        return 'border-slate-300 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/50';
      }
      if (!this.systemServiceInfo.installed) {
        return 'border-primary bg-slate-50 dark:bg-slate-800/50';
      }
      return this.systemServiceInfo.running
        ? 'border-emerald-400 bg-emerald-50/70 dark:border-emerald-500/40 dark:bg-emerald-500/10'
        : 'border-sky-400 bg-sky-50/70 dark:border-sky-500/40 dark:bg-sky-500/10';
    },
    systemServiceConfirmTitle() {
      return this.systemServiceConfirmAction === 'uninstall'
        ? this.t('sys_confirmUninstallTitle')
        : this.t('sys_confirmRegisterTitle', { label: this.friendlyServiceLabel });
    },
    systemServiceConfirmDescription() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.t('sys_confirmUninstallDesc');
      }
      return this.t('sys_confirmRegisterDesc', { label: this.friendlyServiceLabel });
    },
    systemServiceConfirmHighlight() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.t('sys_confirmUninstallHighlight');
      }
      return this.t('sys_confirmRegisterHighlight');
    },
    systemServiceConfirmChecklist() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return [
          this.t('sys_confirmUninstallCheck1', { label: this.friendlyServiceLabel }),
          this.t('sys_confirmUninstallCheck2'),
          this.t('sys_confirmUninstallCheck3')
        ];
      }
      return [
        this.t('sys_confirmRegisterCheck1', { label: this.friendlyServiceLabel }),
        this.t('sys_confirmRegisterCheck2'),
        this.t('sys_confirmRegisterCheck3')
      ];
    },
    systemServiceConfirmButtonLabel() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.serviceActionLoading ? this.t('sys_uninstallingBtn') : this.t('sys_confirmUninstallBtn');
      }
      return this.serviceActionLoading ? this.t('sys_registeringBtn') : this.t('sys_confirmRegisterBtn');
    }
  },
  mounted() {
    this.startCertStatusPolling();
    this.refreshSystemServiceStatus();
  },
  beforeUnmount() {
    this.stopCertStatusPolling();
  },
  methods: {
    openCertificateModal() {
      window.dispatchEvent(new CustomEvent('aliang:open-cert-modal'));
    },
    async downloadCurrentCertificate() {
      try {
        const response = await fetch('/api/cert/download?cert_type=mitm-ca');
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const url = URL.createObjectURL(await response.blob());
        const anchor = document.createElement('a');
        anchor.href = url;
        anchor.download = 'mitm-ca.pem';
        anchor.click();
        URL.revokeObjectURL(url);
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('cert_downloadFailed');
      }
    },
    async requestJSON(url, options = {}) {
      const res = await fetch(url, options);
      const payload = await res.json().catch(() => ({}));
      if (!res.ok || payload?.code !== 0) {
        const msg = payload?.msg || payload?.message || `Request failed (${res.status})`;
        throw new Error(msg);
      }
      return payload?.data ?? payload;
    },
    clearSystemServiceMessages() {
      this.systemServiceError = '';
      this.systemServiceMessage = '';
    },
    openSystemServiceConfirm(action) {
      if (this.serviceActionLoading) {
        return;
      }
      this.systemServiceConfirmAction = action === 'uninstall' ? 'uninstall' : 'install';
      this.showSystemServiceConfirm = true;
    },
    closeSystemServiceConfirm() {
      if (this.serviceActionLoading) {
        return;
      }
      this.showSystemServiceConfirm = false;
      this.systemServiceConfirmAction = '';
    },
    async confirmSystemServiceAction() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        await this.uninstallSystemService();
        return;
      }
      await this.registerSystemService();
    },
    async registerSystemService() {
      this.clearSystemServiceMessages();
      this.serviceActionLoading = true;
      this.serviceAction = 'install';
      try {
        const result = await this.requestJSON('/api/system/service/install', {
          method: 'POST'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
        this.systemServiceMessage = this.systemServiceInfo.message || this.t('sys_svcRegistered');
        this.showSystemServiceConfirm = false;
        this.systemServiceConfirmAction = '';
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcRegisterFailed');
      } finally {
        this.serviceActionLoading = false;
        this.serviceAction = '';
      }
    },
    async uninstallSystemService() {
      this.clearSystemServiceMessages();
      this.serviceActionLoading = true;
      this.serviceAction = 'uninstall';
      try {
        const result = await this.requestJSON('/api/system/service/uninstall', {
          method: 'POST'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
        this.systemServiceMessage = this.systemServiceInfo.message || this.t('sys_svcUninstalled');
        this.showSystemServiceConfirm = false;
        this.systemServiceConfirmAction = '';
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcUninstallFailed');
      } finally {
        this.serviceActionLoading = false;
        this.serviceAction = '';
      }
    },
    async refreshSystemServiceStatus() {
      try {
        const result = await this.requestJSON('/api/system/service/status', {
          method: 'GET'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcStatusFailed');
      }
    }
  }
}
</script>
