<template>
  <div class="flex flex-col gap-5">
    <!-- PeerMCPSettings 行为零改动，从状态栏页尾「其他」收编至此（设备互访家族
         集中，2026-11-04）。两段式保存链路不变：emit save → AgentPage → SettingsPage。 -->
    <PeerMCPSettings
      :config="config"
      :loading="loading"
      :saving="saving"
      :error="error"
      :success-message="successMessage"
      @save="emitSave"
    />
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
// 注意路径：本组件在 settings/agent/ 下，PeerMCPSettings 在 settings/ 上一级。
import PeerMCPSettings from '../PeerMCPSettings.vue';
import { useI18n } from '../../../i18n';

export default {
  name: 'PeerAccessSettings',
  components: { PeerMCPSettings },
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
