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
    <!-- 设备门禁真开关（peer_exec_enabled / peer_fetch_enabled，2026-11-07 转正）：
         真值源=服务端设备注册表，经内核 /api/agent/peer/gates 透传读写；
         拨动即生效，自取状态零 props。 -->
    <PeerGateSettings />
  </div>
</template>

<script>
// 注意路径：本组件在 settings/agent/ 下，PeerMCPSettings 在 settings/ 上一级。
import PeerMCPSettings from '../PeerMCPSettings.vue';
import PeerGateSettings from './PeerGateSettings.vue';

export default {
  name: 'PeerAccessSettings',
  components: { PeerMCPSettings, PeerGateSettings },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  methods: {
    emitSave(payload) {
      this.$emit('save', payload);
    }
  }
};
</script>
