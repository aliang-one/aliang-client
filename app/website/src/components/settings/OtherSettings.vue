<template>
  <div class="flex flex-col gap-5">
    <SectionHeader :title="t('settings_otherTitle')" :desc="t('settings_otherDesc')" scope="device" />
    <!-- 设备互访两卡（托管开关 + 门禁占位）已于 2026-11-04 收编至
         Agent 栏「设备互访」子 tab（settings/agent/ 下新组件）；
         本区块保留运行模式与文件访问授权两卡原地不动。 -->
    <SystemSettings />
    <AgentFileAccessSettings />
  </div>
</template>

<script>
import SystemSettings from './SystemSettings.vue';
import AgentFileAccessSettings from './AgentFileAccessSettings.vue';
import SectionHeader from './SectionHeader.vue';
import { useI18n } from '../../i18n';

export default {
  name: 'OtherSettings',
  components: { SystemSettings, AgentFileAccessSettings, SectionHeader },
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
