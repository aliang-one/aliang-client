<template>
  <div class="flex flex-col gap-5">
    <!-- 子 tab：key 词表唯一真值源 = useNavigation.AGENT_SUB_TABS，状态在导航层，
         支持 showPage('agent', { sub }) 深链定向（2026-11-03 四栏重组）。
         样式为 navitem 下划线式（对齐顶导航先例）；非激活垫 border-transparent
         防切换跳动，min-h-10 保触控目标，flex-wrap 供 390px 哨兵兜底。 -->
    <div class="flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-slate-200 dark:border-slate-700" role="tablist" :aria-label="t('settings_tabAgent')">
      <button
        v-for="tab in agentSubTabs"
        :key="tab.key"
        type="button"
        role="tab"
        :aria-selected="active === tab.key"
        class="min-h-10 border-b-2 px-1 py-2 text-sm font-medium transition"
        :class="active === tab.key
          ? 'border-primary text-primary'
          : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
        @click="switchTab(tab.key)"
      >
        {{ t(tab.labelKey) }}
      </button>
    </div>

    <!-- 主内容 + 右侧栏（2026-11-07：系统状态/文件访问授权自仪表盘侧栏收编至
         Agent 页右侧；窄屏 <lg 时右栏叠到主内容下方，DOM 顺序=主内容在前）。 -->
    <div class="flex flex-col gap-5 lg:flex-row lg:items-start">
      <div class="flex min-w-0 flex-1 flex-col gap-5">
        <template v-if="active === 'records'">
          <AgentActivitySettings />
          <AgentRuntimeSessions />
        </template>

        <template v-else-if="active === 'user-agent'">
          <AgentSettings />
          <AgentScanDirectoriesSettings />
          <AgentMCPRegistrySettings />
          <AgentIntegrationsSettings />
        </template>

        <!-- 设备互访：customerConfig 依赖登录态的卡片（PeerMCPSettings 两段式保存
             需父页 config/save 透传；门禁卡为真开关，自取状态）。 -->
        <template v-else-if="active === 'peer-access'">
          <PeerAccessSettings
            :config="config"
            :loading="loading"
            :saving="saving"
            :error="error"
            :success-message="successMessage"
            @save="emitSave"
          />
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
            <AgentMemorySettings
              :config="config"
              :loading="loading"
              :saving="saving"
              :error="error"
              :success-message="successMessage"
              @save="emitSave"
            />
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

      <!-- 右侧栏:系统状态 + 文件访问授权两张完整卡(2026-11-07 三轮迭代:
           紧凑卡+弹窗退场,双全卡直嵌,窄栏已适配)。免登录(设备本地态)。 -->
      <aside class="w-full shrink-0 space-y-5 lg:w-96">
        <SystemStatusSettings />
        <AgentFileAccessSettings />
      </aside>
    </div>
  </div>
</template>

<script>
// 注意路径：本组件在 settings/agent/ 下，全部同层卡片在 settings/ 上一级。
import AgentActivitySettings from '../AgentActivitySettings.vue';
import AgentRuntimeSessions from '../AgentRuntimeSessions.vue';
import AgentSettings from '../AgentSettings.vue';
import AgentScanDirectoriesSettings from '../AgentScanDirectoriesSettings.vue';
import AgentIntegrationsSettings from '../AgentIntegrationsSettings.vue';
import AgentMCPRegistrySettings from '../AgentMCPRegistrySettings.vue';
import AgentCoreSettings from '../AgentCoreSettings.vue';
import AgentMCPGlobalSettings from '../AgentMCPGlobalSettings.vue';
import AgentSkillsGlobalSettings from '../AgentSkillsGlobalSettings.vue';
import AgentMemorySettings from '../AgentMemorySettings.vue';
import CustomEnvSettings from '../CustomEnvSettings.vue';
import PeerAccessSettings from './PeerAccessSettings.vue';
import SystemStatusSettings from '../SystemStatusSettings.vue';
import AgentFileAccessSettings from '../AgentFileAccessSettings.vue';
import { useNavigation } from '../../../composables/useNavigation';
import { useAuthStore } from '../../../stores/auth';
import { useI18n } from '../../../i18n';

// 子 tab 词表（labels）；key 真值源在 useNavigation.AGENT_SUB_TABS，两处
// 一一对应（词表钉死在 useNavigation.test.js）。
const agentSubTabs = [
  { key: 'records', labelKey: 'agent_subRecords' },
  { key: 'user-agent', labelKey: 'agent_subUserAgent' },
  { key: 'aliangcode', labelKey: 'agent_subAliangCode' },
  { key: 'peer-access', labelKey: 'agent_subPeerAccess' }
];

export default {
  name: 'AgentPage',
  components: {
    AgentActivitySettings,
    AgentRuntimeSessions,
    AgentSettings,
    AgentScanDirectoriesSettings,
    AgentIntegrationsSettings,
    AgentMCPRegistrySettings,
    AgentCoreSettings,
    AgentMCPGlobalSettings,
    AgentSkillsGlobalSettings,
    AgentMemorySettings,
    CustomEnvSettings,
    PeerAccessSettings,
    SystemStatusSettings,
    AgentFileAccessSettings
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
