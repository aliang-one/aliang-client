<template>
  <div class="flex flex-col gap-5">
    <SectionHeader :title="t('settings_otherTitle')" :desc="t('settings_otherDesc')" scope="device" />
    <!-- PeerMCPSettings 行为零改动，仅从原「能力接入」落位至此（设备互访家族集中）。 -->
    <PeerMCPSettings
      :config="config"
      :loading="loading"
      :saving="saving"
      :error="error"
      :success-message="successMessage"
      @save="emitSave"
    />
    <SystemSettings />
    <AgentFileAccessSettings />
    <!-- 设备门禁开关（peer_exec_enabled / peer_fetch_enabled）：真值源在服务端
         设备注册表，写通道待跨仓（agent + PhoneServer）落地；先置灰占位零请求。 -->
    <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
      <div class="space-y-5">
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
              {{ t('settings_peerExecPendingTitle') }}
              <span class="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700 dark:text-slate-300">
                {{ t('settings_comingSoon') }}
              </span>
            </p>
            <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
              {{ t('settings_peerExecPendingDesc') }}
            </p>
          </div>
          <input
            type="checkbox"
            disabled
            :aria-label="t('settings_peerExecPendingTitle')"
            class="h-4 w-4 shrink-0 cursor-not-allowed rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          />
        </div>
        <div class="flex items-center justify-between gap-3">
          <div class="min-w-0">
            <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
              {{ t('settings_peerTransferPendingTitle') }}
              <span class="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700 dark:text-slate-300">
                {{ t('settings_comingSoon') }}
              </span>
            </p>
            <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
              {{ t('settings_peerTransferPendingDesc') }}
            </p>
          </div>
          <input
            type="checkbox"
            disabled
            :aria-label="t('settings_peerTransferPendingTitle')"
            class="h-4 w-4 shrink-0 cursor-not-allowed rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          />
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import PeerMCPSettings from './PeerMCPSettings.vue';
import SystemSettings from './SystemSettings.vue';
import AgentFileAccessSettings from './AgentFileAccessSettings.vue';
import SectionHeader from './SectionHeader.vue';
import { useI18n } from '../../i18n';

export default {
  name: 'OtherSettings',
  components: { PeerMCPSettings, SystemSettings, AgentFileAccessSettings, SectionHeader },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  setup() {
    const { t } = useI18n();
    return { t };
  },
  methods: {
    emitSave(payload) {
      this.$emit('save', payload);
    }
  }
};
</script>
