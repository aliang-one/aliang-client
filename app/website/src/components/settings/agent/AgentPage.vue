<template>
  <div class="flex flex-col gap-5">
    <!-- 子 tab：key 词表唯一真值源 = useNavigation.AGENT_SUB_TABS，状态在导航层，
         支持 showPage('agent', { sub }) 深链定向（2026-11-03 四栏重组）。 -->
    <div class="flex flex-wrap gap-2" role="tablist" :aria-label="t('settings_tabAgent')">
      <button
        v-for="tab in agentSubTabs"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="active === tab.key"
        class="min-h-10 rounded-full px-4 text-xs font-bold transition"
        :class="active === tab.key
          ? 'bg-primary text-white shadow-sm'
          : 'bg-slate-100 text-slate-600 hover:bg-slate-200 dark:bg-slate-800 dark:text-slate-300 dark:hover:bg-slate-700/70'"
        @click="switchTab(tab.key)"
      >
        {{ t(tab.labelKey) }}
      </button>
    </div>

    <template v-if="active === 'records'">
      <AgentActivitySettings />
      <AgentRuntimeSessions />
    </template>

    <template v-else-if="active === 'user-agent'">
      <AgentSettings />
      <AgentScanDirectoriesSettings />
      <AgentIntegrationsSettings />
    </template>

    <!-- aliangcode：customerConfig 依赖登录态，唯一带登录占位的子 tab
         （records / user-agent 未登录也可看，占位语义见 spec §5.4）。 -->
    <template v-else>
      <div
        v-if="!isAuthenticated"
        class="rounded-2xl border border-dashed border-slate-300 bg-white px-6 py-8 text-center shadow-sm dark:border-slate-700 dark:bg-slate-900"
      >
        <div class="mx-auto max-w-2xl space-y-3">
          <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('settings_loginRequiredCapability') }}</h2>
          <p class="text-sm text-slate-500 dark:text-slate-300">
            {{ t('settings_loginRequiredCapabilityDesc') }}
          </p>
        </div>
      </div>
      <template v-else>
        <AgentCoreSettings
          :config="config"
          :loading="loading"
          :saving="saving"
          :error="error"
          :success-message="successMessage"
          @save="emitSave"
        />
        <div class="grid grid-cols-1 items-stretch gap-5 lg:grid-cols-2">
          <AgentMCPGlobalSettings
            :config="config"
            :loading="loading"
            :saving="saving"
            :error="error"
            :success-message="successMessage"
            @save="emitSave"
          />
          <AgentSkillsGlobalSettings
            :config="config"
            :loading="loading"
            :saving="saving"
            :error="error"
            :success-message="successMessage"
            @save="emitSave"
          />
        </div>
        <CustomEnvSettings
          :config="config"
          :loading="loading"
          :saving="saving"
          :error="error"
          :success-message="successMessage"
          @save="emitSave"
        />
      </template>
    </template>
  </div>
</template>

<script>
// 注意路径：本组件在 settings/agent/ 下，全部同层卡片在 settings/ 上一级。
import AgentActivitySettings from '../AgentActivitySettings.vue';
import AgentRuntimeSessions from '../AgentRuntimeSessions.vue';
import AgentSettings from '../AgentSettings.vue';
import AgentScanDirectoriesSettings from '../AgentScanDirectoriesSettings.vue';
import AgentIntegrationsSettings from '../AgentIntegrationsSettings.vue';
import AgentCoreSettings from '../AgentCoreSettings.vue';
import AgentMCPGlobalSettings from '../AgentMCPGlobalSettings.vue';
import AgentSkillsGlobalSettings from '../AgentSkillsGlobalSettings.vue';
import CustomEnvSettings from '../CustomEnvSettings.vue';
import { useNavigation } from '../../../composables/useNavigation';
import { useAuthStore } from '../../../stores/auth';
import { useI18n } from '../../../i18n';

// 子 tab 词表（labels）；key 真值源在 useNavigation.AGENT_SUB_TABS，两处
// 一一对应（词表钉死在 useNavigation.test.js）。
const agentSubTabs = [
  { key: 'records', labelKey: 'agent_subRecords' },
  { key: 'user-agent', labelKey: 'agent_subUserAgent' },
  { key: 'aliangcode', labelKey: 'agent_subAliangCode' }
];

export default {
  name: 'AgentPage',
  components: {
    AgentActivitySettings,
    AgentRuntimeSessions,
    AgentSettings,
    AgentScanDirectoriesSettings,
    AgentIntegrationsSettings,
    AgentCoreSettings,
    AgentMCPGlobalSettings,
    AgentSkillsGlobalSettings,
    CustomEnvSettings
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  setup() {
    const { agentSubTab } = useNavigation();
    const { isAuthenticated } = useAuthStore();
    const { t } = useI18n();

    // 写导航层原始 ref（setup 返回值经 this 访问会解包，写回必须走闭包）。
    function switchTab(key) {
      agentSubTab.value = key;
    }

    return { agentSubTab, isAuthenticated, t, agentSubTabs, switchTab };
  },
  computed: {
    active() {
      // 导航层已归一，这里兜底未知值回默认（records）。
      return agentSubTabs.some((tab) => tab.key === this.agentSubTab) ? this.agentSubTab : 'records';
    }
  },
  methods: {
    emitSave(payload) {
      this.$emit('save', payload);
    }
  }
};
</script>
