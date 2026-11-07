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
         设备注册表（GET /api/agent/peer/gates 读本机现值），写经
         POST /api/agent/peer/gates 部分更新落库。终值以服务端回显为准；
         读失败置灰不假装默认值；rebind 强制回落 false 如实提示。 -->
    <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
      <div class="space-y-5">
        <div
          v-for="row in gateRows"
          :key="row.key"
          class="flex items-center justify-between gap-3"
        >
          <div class="min-w-0">
            <p class="text-sm font-semibold text-slate-900 dark:text-white">
              {{ t(row.titleKey) }}
            </p>
            <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
              {{ t(row.descKey) }}
            </p>
          </div>
          <input
            type="checkbox"
            :data-testid="row.testid"
            :aria-label="t(row.titleKey)"
            class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
            :checked="gates[row.key]"
            :disabled="gatesLoading || gatesSaving || !gatesLoaded"
            @change="onToggleGate(row.key, $event.target.checked)"
          />
        </div>
      </div>
      <p class="mt-3 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
        {{ t('peerGates_rebindHint') }}
      </p>
      <div
        v-if="gatesError"
        role="status"
        data-testid="peer-gates-error"
        class="mt-2 break-words text-xs text-red-600 dark:text-red-400"
      >
        {{ gatesError }}
      </div>
    </div>
  </div>
</template>

<script>
// 注意路径：本组件在 settings/agent/ 下，PeerMCPSettings 在 settings/ 上一级。
import PeerMCPSettings from '../PeerMCPSettings.vue';
import { useI18n } from '../../../i18n';
import { getPeerDeviceGates, setPeerDeviceGates } from '../../../services/agentApi';

// 两枚门禁行的静态形状（i18n 键 + testid），键名与服务端契约一致。
const GATE_ROWS = [
  { key: 'peer_exec_enabled', titleKey: 'settings_peerExecTitle', descKey: 'settings_peerExecDesc', testid: 'peer-gate-exec' },
  { key: 'peer_fetch_enabled', titleKey: 'settings_peerTransferTitle', descKey: 'settings_peerTransferDesc', testid: 'peer-gate-fetch' }
];

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
  data() {
    return {
      gateRows: GATE_ROWS,
      gates: { peer_exec_enabled: false, peer_fetch_enabled: false },
      // gatesLoaded=false（读失败/未读到）时开关置灰：不拿零值假装读到「关」。
      gatesLoaded: false,
      gatesLoading: false,
      gatesSaving: false,
      gatesError: ''
    };
  },
  mounted() {
    this.loadGates();
  },
  methods: {
    emitSave(payload) {
      this.$emit('save', payload);
    },
    errorMessage(e) {
      return (e instanceof Error && e.message) || String(e || '');
    },
    async loadGates() {
      this.gatesLoading = true;
      this.gatesError = '';
      try {
        const data = await getPeerDeviceGates();
        this.gates = {
          peer_exec_enabled: !!data?.peer_exec_enabled,
          peer_fetch_enabled: !!data?.peer_fetch_enabled
        };
        this.gatesLoaded = true;
      } catch (e) {
        this.gatesError = `${this.t('peerGates_loadFailed')}:${this.errorMessage(e)}`;
      } finally {
        this.gatesLoading = false;
      }
    },
    async onToggleGate(key, checked) {
      // 未读到现值不许盲写（开关此时已因 !gatesLoaded 置灰，此为兜底）。
      if (!this.gatesLoaded) {
        this.gates = { ...this.gates, [key]: false };
        return;
      }
      const prev = this.gates[key];
      // 乐观更新让手感即时；失败回滚到读值，成功以服务端回显为准
      // （防本地乐观值与服务端实际落库漂移）。
      this.gates = { ...this.gates, [key]: checked };
      this.gatesSaving = true;
      this.gatesError = '';
      try {
        const updated = await setPeerDeviceGates({ [key]: checked });
        this.gates = { ...this.gates, [key]: !!updated?.[key] };
      } catch (e) {
        this.gates = { ...this.gates, [key]: prev };
        this.gatesError = `${this.t('peerGates_saveFailed')}:${this.errorMessage(e)}`;
      } finally {
        this.gatesSaving = false;
      }
    }
  }
};
</script>
