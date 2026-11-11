<template>
  <div
    v-if="isVisible"
    id="settings-page"
    class="page-container content-section active flex-1 min-w-0 h-full overflow-hidden bg-background-light dark:bg-background-dark"
  >
    <div class="relative flex h-full w-full flex-col overflow-y-auto">
      <header class="sticky top-0 z-50 w-full border-b border-primary/10 bg-white/80 backdrop-blur-md dark:bg-background-dark/80">
        <div class="mx-auto flex h-16 w-full max-w-7xl items-center justify-between px-4 sm:px-6 lg:px-8">
          <div class="flex items-center gap-3">
            <div class="flex h-8 w-8 items-center justify-center rounded bg-primary text-white">
              <span class="material-symbols-outlined text-xl">shield_with_heart</span>
            </div>
            <span class="text-lg font-bold tracking-tight">{{ t('settings_title') }}</span>
          </div>
          <nav class="hidden items-center gap-8 md:flex">
            <a
              class="py-5 text-sm font-medium"
              :class="currentPage === 'settings' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('settings')"
            >
              {{ t('settings_tabSettings') }}
            </a>
            <a
              class="py-5 text-sm font-medium"
              :class="currentPage === 'agent' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('agent')"
            >
              {{ t('settings_tabAgent') }}
            </a>
            <a
              class="py-5 text-sm font-medium"
              :class="currentPage === 'log' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('log')"
            >
              {{ t('settings_tabLogs') }}
            </a>
            <a
              class="py-5 text-sm font-medium"
              :class="currentPage === 'user' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('user')"
            >
              {{ t('settings_tabUserCenter') }}
            </a>
          </nav>
          <div class="flex items-center gap-4">
            <div class="hidden text-right text-xs lg:flex lg:flex-col">
              <span class="font-bold text-slate-900 dark:text-white">{{ userDisplayName }}</span>
              <span class="flex items-center gap-1 text-primary">
                <span class="h-1.5 w-1.5 rounded-full bg-primary"></span>
                {{ planLabel }}
              </span>
            </div>
            <div
              class="flex h-10 w-10 items-center justify-center rounded border border-primary/20 bg-primary/10 text-xs font-bold uppercase tracking-wide text-primary"
            >
              {{ userAvatarText }}
            </div>
          </div>
        </div>
      </header>

      <main class="mx-auto w-full max-w-7xl grow p-4 sm:p-6 lg:p-8">
        <div class="mb-6 flex items-center justify-between gap-4 rounded-xl border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
          <div class="flex items-center gap-4">
            <button
              type="button"
              id="backToDashboard"
              class="flex h-10 w-10 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 dark:hover:bg-slate-800 md:h-8 md:w-8"
              @click="goDashboard"
            >
              <span class="material-symbols-outlined">arrow_back</span>
            </button>
            <h1 class="text-lg font-bold tracking-tight sm:text-xl">
              {{ pageTitle }} <span class="font-medium text-primary">{{ pageAccent }}</span>
            </h1>
          </div>
          <div class="rounded bg-primary/10 px-3 py-1 text-xs font-bold text-primary">{{ t('settings_live') }}</div>
        </div>

        <div class="mb-6 grid grid-cols-2 gap-2 md:hidden">
          <button
            type="button"
            class="min-h-10 rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'settings' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('settings')"
          >
            {{ t('settings_tabSettings') }}
          </button>
          <button
            type="button"
            class="min-h-10 rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'agent' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('agent')"
          >
            {{ t('settings_tabAgent') }}
          </button>
          <button
            type="button"
            class="min-h-10 rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'log' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('log')"
          >
            {{ t('settings_tabLogs') }}
          </button>
          <button
            type="button"
            class="min-h-10 rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'user' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('user')"
          >
            {{ t('settings_tabUserCenter') }}
          </button>
        </div>

        <div
          v-if="currentPage !== 'user' && currentPage !== 'agent'"
          class="mb-6 rounded-xl border px-4 py-3 text-sm"
          :class="isAuthenticated ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300' : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300'"
        >
          {{ authNotice }}
        </div>

        <!-- ① 代理设置（设备级）：主区=出站代理/AI 规则/分流；右栏=运行模式 +
             模型映射（2026-11-07 双栏迭代，布局对齐 Agent 页）。
             仅影响未单独设置的新会话与代理层；会话级覆盖在聊天页会话设置条。 -->
        <section v-if="currentPage === 'settings'" class="flex flex-col gap-5">
          <SectionHeader :title="t('settings_tabSettings')" :desc="t('settings_sectionProxyDesc')" scope="device" />
          <!-- 代理设置页内两子 tab（用户需求 2026-11-10）：出口代理常用居默认页；
               入口(SSH)与组网(mesh)低频收进第二页,样式对齐 Agent 页三子 tab。 -->
          <div class="flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-slate-200 dark:border-slate-700" role="tablist" :aria-label="t('settings_tabSettings')">
            <button
              v-for="tab in proxySubTabs"
              :key="tab.key"
              type="button"
              role="tab"
              :aria-selected="proxySubTab === tab.key"
              class="min-h-10 border-b-2 px-1 py-2 text-sm font-medium transition"
              :class="proxySubTab === tab.key
                ? 'border-primary text-primary'
                : 'border-transparent text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              @click="proxySubTab = tab.key"
            >
              {{ t(tab.labelKey) }}
            </button>
          </div>

          <div v-if="proxySubTab === 'ingress'" class="flex flex-col gap-5">
            <div
              v-if="!isAuthenticated"
              class="rounded-2xl border border-dashed border-slate-300 bg-white px-6 py-8 text-center shadow-sm dark:border-slate-700 dark:bg-slate-900"
            >
              <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('settings_loginRequiredSettings') }}</h2>
              <p class="text-sm text-slate-500 dark:text-slate-300">{{ t('settings_loginRequiredSettingsDesc') }}</p>
            </div>
            <template v-else>
              <!-- SSH 入口卡片（可选专用口/统一口双模式;默认折叠,详见卡片内注释） -->
              <SshIngressSettings
                :config="customerConfig"
                :loading="isLoadingCustomerConfig"
                :saving="isSavingCustomerConfig"
                :error="customerConfigError"
                :success-message="customerConfigSuccess"
                @save="saveCustomerConfig"
              />
              <!-- mesh 组网卡（写路径=customer.mesh patch 链） -->
              <MeshSettings
                :config="customerConfig"
                :loading="isLoadingCustomerConfig"
                :saving="isSavingCustomerConfig"
                :error="customerConfigError"
                :success-message="customerConfigSuccess"
                @save="saveCustomerConfig"
              />
            </template>
          </div>

          <div v-else class="flex flex-col gap-5 lg:flex-row lg:items-start">
            <div class="flex min-w-0 flex-1 flex-col gap-5">
              <div
                v-if="!isAuthenticated"
                class="rounded-2xl border border-dashed border-slate-300 bg-white px-6 py-8 text-center shadow-sm dark:border-slate-700 dark:bg-slate-900"
              >
                <div class="mx-auto max-w-2xl space-y-3">
                  <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('settings_loginRequiredSettings') }}</h2>
                  <p class="text-sm text-slate-500 dark:text-slate-300">
                    {{ t('settings_loginRequiredSettingsDesc') }}
                  </p>
                </div>
              </div>
              <template v-else>
                <RulesSettings
                  :config="customerConfig"
                  :preset-providers="presetProviders"
                  :loading="isLoadingCustomerConfig"
                  :saving="isSavingCustomerConfig"
                  :error="customerConfigError"
                  :success-message="customerConfigSuccess"
                  :version="customerConfigVersion"
                  @save="saveCustomerConfig"
                />
              </template>
            </div>
            <!-- 右栏:运行模式免登录(设备本地,与电源按钮同级);模型映射依赖
                 customerConfig,登录后可用。 -->
            <aside class="w-full shrink-0 space-y-5 lg:w-96">
              <SystemRunModeSettings />
              <template v-if="isAuthenticated">
                <ModelMappingSettings
                  :config="customerConfig"
                  :loading="isLoadingCustomerConfig"
                  :saving="isSavingCustomerConfig"
                  :error="customerConfigError"
                  :success-message="customerConfigSuccess"
                  @save="saveCustomerConfig"
                />
              </template>
              <div
                v-else
                class="rounded-xl border border-dashed border-slate-300 bg-white px-4 py-6 text-center dark:border-slate-700 dark:bg-slate-900"
              >
                <p class="text-sm font-bold text-slate-900 dark:text-white">{{ t('settings_loginRequiredSettings') }}</p>
                <p class="mt-1 text-xs text-slate-500 dark:text-slate-300">{{ t('settings_loginRequiredSettingsDesc') }}</p>
              </div>
            </aside>
          </div>
        </section>

        <!-- ② Agent（设备级）：三子 tab = 记录 / 用户态 agent / aliangcode。
             子 tab 状态在导航层（useNavigation.agentSubTab），支持深链；
             登录占位语义见 spec §5.4（占位收在 aliangcode 子 tab 内）。 -->
        <section v-else-if="currentPage === 'agent'" class="flex flex-col gap-5">
          <SectionHeader :title="t('settings_tabAgent')" :desc="t('settings_sectionAgentDesc')" scope="device" />
          <AgentPage
            :config="customerConfig"
            :loading="isLoadingCustomerConfig"
            :saving="isSavingCustomerConfig"
            :error="customerConfigError"
            :success-message="customerConfigSuccess"
            @save="saveCustomerConfig"
          />
        </section>

        <!-- ④ 账户与界面（账户级 + 本机） -->
        <section v-else-if="currentPage === 'user'" class="flex flex-col gap-5">
          <SectionHeader :title="t('settings_tabUserCenter')" :desc="t('settings_sectionAccountDesc')" scope="account" />
          <UserInfoSettings />
          <AppearanceSettings />
        </section>

        <!-- ③ 状态（只读观测）：实时日志。系统状态/文件授权已收编 Agent 页右栏
             （2026-11-07 三轮迭代定案）。 -->
        <section v-else class="flex flex-col gap-5">
          <SectionHeader :title="t('settings_tabLogs')" :desc="t('settings_sectionStatusDesc')" scope="readonly" />
          <div
            v-if="!isAuthenticated"
            class="rounded-2xl border border-dashed border-slate-300 bg-white px-6 py-8 text-center shadow-sm dark:border-slate-700 dark:bg-slate-900"
          >
            <div class="mx-auto max-w-2xl space-y-3">
              <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('settings_loginRequiredLogs') }}</h2>
              <p class="text-sm text-slate-500 dark:text-slate-300">
                {{ t('settings_loginRequiredLogsDesc') }}
              </p>
            </div>
          </div>
          <template v-else>
            <LogsSettings />
          </template>
        </section>
      </main>

      <footer class="border-t border-slate-100 py-6 dark:border-slate-800">
        <div class="mx-auto flex max-w-7xl flex-wrap items-center justify-between gap-x-4 gap-y-1.5 px-4 text-[10px] font-medium text-slate-400 sm:px-6 lg:px-8">
          <div>{{ t('settings_copyright') }}</div>
          <div class="flex gap-4 uppercase tracking-tighter">
            <a class="hover:text-primary" href="javascript:void(0)" @click.prevent="openTutorialDocs('usage-guide')">{{ t('settings_docs') }}</a>
            <a class="hover:text-primary" href="javascript:void(0)">{{ t('settings_privacy') }}</a>
            <a class="hover:text-primary" href="javascript:void(0)">{{ t('settings_github') }}</a>
          </div>
        </div>
      </footer>

      <div class="compat-anchors hidden" aria-hidden="true">
        <button type="button" class="settings-tab active" data-tab="rules"></button>
        <button type="button" class="settings-tab" data-tab="logs"></button>
        <button type="button" class="settings-tab" data-tab="userinfo"></button>
        <button type="button" class="settings-tab" data-tab="system"></button>
        <button type="button" class="settings-tab" data-tab="config-sync"></button>
        <div class="settings-content active" data-content="rules"></div>
        <div class="settings-content hidden" data-content="logs"></div>
        <div class="settings-content hidden" data-content="userinfo"></div>
        <div class="settings-content hidden" data-content="system"></div>
        <div class="settings-content hidden" data-content="config-sync"></div>
      </div>
    </div>
  </div>
</template>

<script>
// 四栏重组 2026-11-03：代理设置(settings) → Agent(agent,三子 tab) → 状态(log) → 用户(user)。
// Agent 子组件收拢在 settings/agent/AgentPage.vue；设备级杂项已于 2026-11-05
// 迁出：运行模式→代理设置首卡，系统状态/文件访问授权→仪表盘侧栏紧凑卡。
import RulesSettings from './settings/RulesSettings.vue';
import SshIngressSettings, { normalizeSSHIngressConfig } from './settings/SshIngressSettings.vue';
import MeshSettings from './settings/MeshSettings.vue';
import { normalizeMeshConfig } from './settings/MeshSettings.js';
import UserInfoSettings from './settings/UserInfoSettings.vue';
import LogsSettings from './settings/LogsSettings.vue';
import ModelMappingSettings from './settings/ModelMappingSettings.vue';
import SystemRunModeSettings from './settings/SystemRunModeSettings.vue';
import AppearanceSettings from './settings/AppearanceSettings.vue';
import AgentPage from './settings/agent/AgentPage.vue';
import SectionHeader from './settings/SectionHeader.vue';
import { normalizeAgentCoreLLMConfig, applyAgentCoreLLMClearing, normalizeAgentCoreTuning, normalizePeerMCPConfig } from '../utils/agentCoreSettings';
import { useNavigation } from '../composables/useNavigation';
import { openTutorialDocs } from '../composables/useTutorialDocs';
import { useAuthStore } from '../stores/auth';
import { useI18n } from '../i18n';

function createDefaultCustomerConfig() {
  return {
    proxy: {
      enable: true,
      type: 'socks5',
      server: '',
      username: '',
      password: '',
      // SSH 专属字段（线格式 customer.proxy；0/''/[] = 未设置）
      private_key: '',
      key_passphrase: '',
      pool_size: 0,
      jump_servers: []
    },
    ai_rules: {},
    proxy_rules: [],
    model_mapping: {
      enable: false,
      rules: {}
    },
    custom_env_vars: {
      enable: false,
      vars: {}
    },
    // 线格式键 enabled(Go tag),与 normalizeAgentCoreLLMConfig 输出一致——
    // 不要写 enable,否则 patch 里的布尔会被后端 encoding/json 静默丢弃
    agentcore_llm: {
      enabled: false,
      base_url: '',
      api_key: ''
    },
    // SSH 入口服务面(customer.ssh_ingress;三态指针 nil=false,前端恒产出显式
    // 布尔——nil 与 false 行为等价;port 0=后端归一默认 56442)
    ssh_ingress: normalizeSSHIngressConfig(),
    // mesh 节点转发网(customer.mesh;三态指针 enable nil=true /
    // forward_enable nil=false / relay nil=true / fallback.enable nil=false,
    // 归一层恒产出显式布尔——ssh_ingress 先例)。⚠️ 必须在
    // normalizeCustomerConfig 同步挂 normalizeMeshConfig,漏挂 = 保存 200
    // 但永不落盘(peer_mcp 2026-10-31 验收缺陷①同族)。
    mesh: normalizeMeshConfig()
  };
}

function normalizeStringList(items = []) {
  return Array.isArray(items)
    ? items.map(item => String(item).trim()).filter(Boolean)
    : [];
}

function normalizeAiRules(aiRules = {}) {
  if (!aiRules || typeof aiRules !== 'object' || Array.isArray(aiRules)) {
    return {};
  }

  return Object.fromEntries(
    Object.entries(aiRules).map(([provider, incoming]) => [provider, {
      enble: Boolean(incoming?.enble ?? incoming?.enable),
      include: normalizeStringList(incoming?.include ?? incoming?.exclude),
      editable: typeof incoming?.editable === 'boolean' ? incoming.editable : undefined
    }])
  );
}

function normalizeProxyRules(raw) {
  return normalizeStringList(raw);
}

function normalizeModelMappingConfig(payload = {}) {
  const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
  const rawRules = source.rules && typeof source.rules === 'object' && !Array.isArray(source.rules) ? source.rules : {};
  const rules = {};
  for (const [from, to] of Object.entries(rawRules)) {
    const key = String(from ?? '').trim();
    const value = String(to ?? '').trim();
    if (key && value) {
      rules[key] = value;
    }
  }
  return {
    enable: typeof source.enable === 'boolean' ? source.enable : false,
    rules
  };
}

function normalizeCustomEnvVarsConfig(payload = {}) {
  const source = payload && typeof payload === 'object' && !Array.isArray(payload) ? payload : {};
  const rawVars = source.vars && typeof source.vars === 'object' && !Array.isArray(source.vars) ? source.vars : {};
  const vars = {};
  for (const [key, value] of Object.entries(rawVars)) {
    const trimmedKey = String(key ?? '').trim();
    if (trimmedKey) {
      // value preserved verbatim — it may legitimately be empty or contain '='
      vars[trimmedKey] = String(value ?? '');
    }
  }
  return {
    enable: typeof source.enable === 'boolean' ? source.enable : false,
    vars
  };
}

// jump_servers 线格式（SSHJumpServerRaw）：server 单串，只透传五个线格式字段
function normalizeProxyJumpServers(raw) {
  if (!Array.isArray(raw)) {
    return [];
  }
  return raw.map((entry) => ({
    server: typeof entry?.server === 'string' ? entry.server : '',
    username: typeof entry?.username === 'string' ? entry.username : '',
    password: typeof entry?.password === 'string' ? entry.password : '',
    private_key: typeof entry?.private_key === 'string' ? entry.private_key : '',
    key_passphrase: typeof entry?.key_passphrase === 'string' ? entry.key_passphrase : ''
  }));
}

function normalizeCustomerConfig(payload = {}) {
  const defaults = createDefaultCustomerConfig();
  // 三态布尔:显式布尔才产出 peer_mcp 键(缺键/非布尔不进 patch 与回显,
  // 后端 nil 视为开启)。漏挂此键 = 保存 200 但永不落盘(验收缺陷①同族)。
  const peerMcp = normalizePeerMCPConfig(payload?.peer_mcp);
  const rawPoolSize = payload?.proxy?.pool_size;
  return {
    proxy: {
      enable: typeof payload?.proxy?.enable === 'boolean' ? payload.proxy.enable : defaults.proxy.enable,
      type: ['http', 'ssh'].includes(payload?.proxy?.type) ? payload.proxy.type : defaults.proxy.type,
      server: typeof payload?.proxy?.server === 'string' ? payload.proxy.server : defaults.proxy.server,
      username: typeof payload?.proxy?.username === 'string' ? payload.proxy.username : defaults.proxy.username,
      password: typeof payload?.proxy?.password === 'string' ? payload.proxy.password : defaults.proxy.password,
      private_key: typeof payload?.proxy?.private_key === 'string' ? payload.proxy.private_key : defaults.proxy.private_key,
      key_passphrase: typeof payload?.proxy?.key_passphrase === 'string' ? payload.proxy.key_passphrase : defaults.proxy.key_passphrase,
      // 0 = 未设置（RulesSettings 输入框显示空 + 「默认 3」placeholder；默认与钳位由后端承担）
      pool_size: typeof rawPoolSize === 'number' && Number.isFinite(rawPoolSize) && rawPoolSize > 0 ? rawPoolSize : 0,
      jump_servers: normalizeProxyJumpServers(payload?.proxy?.jump_servers)
    },
    ai_rules: normalizeAiRules(payload?.ai_rules),
    proxy_rules: normalizeProxyRules(payload?.proxy_rules),
    model_mapping: normalizeModelMappingConfig(payload?.model_mapping),
    custom_env_vars: normalizeCustomEnvVarsConfig(payload?.custom_env_vars),
    agentcore_llm: normalizeAgentCoreLLMConfig(payload?.agentcore_llm),
    // memory_enabled(记忆总开关)/ai_mcp(agent-as-MCP 工具面总开关)与
    // mcp_enabled/skills_enabled 同挂 normalizeAgentCoreTuning 的透传
    // allowlist——改布尔键清单只动 agentCoreSettings.js,此处共用同一出口,
    // GET 回显与 patch 同源(漏挂 = 保存 200 但永不落盘,缺陷①同族)。
    agentcore: normalizeAgentCoreTuning(payload?.agentcore),
    ssh_ingress: normalizeSSHIngressConfig(payload?.ssh_ingress),
    // mesh:漏挂此键 = 卡片保存 200 但永不落盘(见 createDefaultCustomerConfig
    // 同款注释;normalizeMeshConfig 恒产出显式布尔,默认值与 GET 回显同构,
    // 未改动不产生伪 patch)
    mesh: normalizeMeshConfig(payload?.mesh),
    ...(peerMcp ? { peer_mcp: peerMcp } : {})
  };
}

function cloneCustomerConfig(config) {
  return JSON.parse(JSON.stringify(config));
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

// 数组等值比较：原语元素用 ===；对象元素（mesh.peers / proxy.jump_servers）
// 用 JSON 串等值——两侧恒经同一归一化函数产出（键序固定），引用不等但值等
// 时不进 patch。修复对象数组「每次保存必带伪 diff」的噪声（deep-merge 幂等
// 故无害，但污染 patch 并放大配置版本抖动）。
function areArraysEqual(left = [], right = []) {
  if (left.length !== right.length) {
    return false;
  }
  return left.every((value, index) => {
    if (value === right[index]) {
      return true;
    }
    if (isPlainObject(value) && isPlainObject(right[index])) {
      return JSON.stringify(value) === JSON.stringify(right[index]);
    }
    return false;
  });
}

function buildCustomerConfigPatch(nextConfig, currentConfig) {
  const normalizedNext = normalizeCustomerConfig(nextConfig);
  const normalizedCurrent = normalizeCustomerConfig(currentConfig);
  const patch = buildConfigPatch(normalizedNext, normalizedCurrent) || {};

  // buildConfigPatch 对空串字符串字段默认跳过（视为未改动）；凭据类字段清空是
  // 显式意图（含 SSH 切回 socks5/http 时的 private_key/key_passphrase 残留清除），
  // 必须显式发空串让后端 deep-merge 覆盖旧值。pool_size(0)/jump_servers([]) 是
  // 数字/数组，buildConfigPatch 本身会带出差异，无需在此特判。
  for (const key of ['username', 'password', 'private_key', 'key_passphrase']) {
    const nextValue = String(normalizedNext.proxy?.[key] ?? '');
    const currentValue = String(normalizedCurrent.proxy?.[key] ?? '');
    if (!nextValue && currentValue) {
      patch.proxy = isPlainObject(patch.proxy) ? patch.proxy : {};
      patch.proxy[key] = '';
    }
  }

  // mesh 兜底地址清空同款（空串显式带出）：关兜底后清掉旧 addr,避免再次
  // 启用时旧地址经 deep-merge「复活」成静默兜底目标。
  const nextFallbackAddr = String(normalizedNext.mesh?.fallback?.addr ?? '');
  const currentFallbackAddr = String(normalizedCurrent.mesh?.fallback?.addr ?? '');
  if (!nextFallbackAddr && currentFallbackAddr) {
    patch.mesh = isPlainObject(patch.mesh) ? patch.mesh : {};
    patch.mesh.fallback = isPlainObject(patch.mesh.fallback) ? patch.mesh.fallback : {};
    patch.mesh.fallback.addr = '';
  }

  // mesh 凭据清空同款（SPEC §13.3.3）：private_key/key_passphrase/fallback.host_key
  // 为空是显式撤销意图,buildConfigPatch 会当「未改动」跳过,必须显式带出空串
  // （后端 deep-merge 才会清掉旧值）。判定用 !String(nextVal ?? '')——归一化层
  // （MeshSettings.js G1）空串不落键,next 侧两形态都有:undefined 与空串。
  // peers[].host_key 不走此路：peers 数组走整体替换分支,清空自然带出。
  for (const [key, nextVal, curVal] of [
    ['private_key',    normalizedNext.mesh?.private_key,    normalizedCurrent.mesh?.private_key],
    ['key_passphrase', normalizedNext.mesh?.key_passphrase, normalizedCurrent.mesh?.key_passphrase]
  ]) {
    if (!String(nextVal ?? '') && String(curVal ?? '')) {
      patch.mesh = isPlainObject(patch.mesh) ? patch.mesh : {};
      patch.mesh[key] = '';
    }
  }
  const nextFallbackHostKey    = String(normalizedNext.mesh?.fallback?.host_key ?? '');
  const currentFallbackHostKey = String(normalizedCurrent.mesh?.fallback?.host_key ?? '');
  if (!nextFallbackHostKey && currentFallbackHostKey) {
    patch.mesh = isPlainObject(patch.mesh) ? patch.mesh : {};
    patch.mesh.fallback = isPlainObject(patch.mesh.fallback) ? patch.mesh.fallback : {};
    patch.mesh.fallback.host_key = '';
  }

  applyAgentCoreLLMClearing(patch, normalizedNext.agentcore_llm, normalizedCurrent.agentcore_llm);

  return Object.keys(patch).length ? patch : undefined;
}

function buildConfigPatch(nextValue, currentValue) {
  if (typeof nextValue === 'string') {
    const trimmed = nextValue.trim();
    if (!trimmed || trimmed === String(currentValue ?? '').trim()) {
      return undefined;
    }
    return trimmed;
  }

  if (Array.isArray(nextValue)) {
    const normalizedCurrent = Array.isArray(currentValue) ? currentValue : [];
    return areArraysEqual(nextValue, normalizedCurrent) ? undefined : [...nextValue];
  }

  if (isPlainObject(nextValue)) {
    const patch = {};
    const currentObject = isPlainObject(currentValue) ? currentValue : {};

    for (const [key, value] of Object.entries(nextValue)) {
      const nextPatch = buildConfigPatch(value, currentObject[key]);
      if (nextPatch !== undefined) {
        patch[key] = nextPatch;
      }
    }

    return Object.keys(patch).length ? patch : undefined;
  }

  return Object.is(nextValue, currentValue) ? undefined : nextValue;
}

// 卡片 save 载荷与当前配置按顶层段浅合并（具名导出供归一化链单测使用,
// RulesSettings.vue 具名导出先例）。卡片载荷只覆盖自己拥有的顶层段,未随
// 载荷提交的段沿用 currentConfig——RulesSettings 只发 proxy/ai_rules/
// proxy_rules 三段,若无此兜底,归一化默认值会 diff 出置零 patch,保存代理
// 设置就会静默关掉 ssh_ingress（以及 model_mapping/agentcore_llm 等存量段,
// 同类潜伏缺陷一并由父层统一收口;peer_mcp 靠「缺键不进 patch」的三态规避,
// 与此不冲突）。
export function mergeCustomerConfig(nextConfig, currentConfig) {
  return { ...(currentConfig || {}), ...(nextConfig || {}) };
}

// 合并后的意图态 → 提交 patch（saveCustomerConfig 主链）。
export function buildCustomerSavePatch(nextConfig, currentConfig) {
  const normalizedConfig = normalizeCustomerConfig(mergeCustomerConfig(nextConfig, currentConfig));
  return buildCustomerConfigPatch(normalizedConfig, currentConfig);
}


export default {
  name: 'SettingsPage',
  components: {
    RulesSettings,
    SshIngressSettings,
    MeshSettings,
    UserInfoSettings,
    LogsSettings,
    ModelMappingSettings,
    SystemRunModeSettings,
    AppearanceSettings,
    AgentPage,
    SectionHeader
  },
  setup() {
    const { currentPage, showPage, showDashboard } = useNavigation();
    const { isAuthenticated, userDisplayName, planLabel, authNotice } = useAuthStore();
    const { t } = useI18n();
    return {
      currentPage,
      showPage,
      goDashboard: showDashboard,
      isAuthenticated,
      userDisplayName,
      planLabel,
      authNotice,
      openTutorialDocs,
      t
    };
  },
  data() {
    return {
      // 代理设置页内子 tab:proxy=出口代理(默认);ingress=入口(SSH)与组网(mesh)。
      proxySubTab: 'proxy',
      proxySubTabs: [
        { key: 'proxy', labelKey: 'settings_subProxy' },
        { key: 'ingress', labelKey: 'settings_subIngress' }
      ],
      presetProviders: [],
      customerConfig: createDefaultCustomerConfig(),
      customerConfigVersion: '',
      customerConfigError: '',
      customerConfigSuccess: '',
      isLoadingCustomerConfig: false,
      isSavingCustomerConfig: false,
      hasLoadedCustomerConfig: false
    };
  },
  computed: {
    isVisible() {
      return ['settings', 'agent', 'log', 'user'].includes(this.currentPage);
    },
    pageTitle() {
      // 标题直接复用分区名，避免页头与标签页两套措辞漂移。
      if (this.currentPage === 'user') {
        return this.t('settings_tabUserCenter');
      }
      if (this.currentPage === 'log') {
        return this.t('settings_tabLogs');
      }
      if (this.currentPage === 'agent') {
        return this.t('settings_tabAgent');
      }
      return this.t('settings_tabSettings');
    },
    pageAccent() {
      return '';
    },
    userAvatarText() {
      const normalized = String(this.userDisplayName || '').trim();
      if (!normalized) {
        return 'US';
      }
      return Array.from(normalized)
        .slice(0, 2)
        .join('')
        .toUpperCase();
    }
  },
  watch: {
    currentPage(page) {
      if (page === 'log') {
        this.syncLegacyTab('logs');
        return;
      }
      if (page === 'user') {
        this.syncLegacyTab('userinfo');
        return;
      }
      if (page === 'settings') {
        this.syncLegacyTab('rules');
      }
    }
  },
  async mounted() {
    try {
      await Promise.all([
        this.loadPresetProviders(),
        this.loadCustomerConfig()
      ]);
    } catch (err) {
      console.error('SettingsPage mounted error:', err);
    }
  },
  methods: {
    async loadPresetProviders() {
      const HARDCODED_PRESETS = [
        { key: 'openai', label: 'OpenAI', default_include: ['openai.com', 'chatgpt.com'], editable: true },
        { key: 'claude', label: 'Claude', default_include: ['claude.ai', 'anthropic.com'], editable: true },
        { key: 'cursor', label: 'Cursor', default_include: ['api.cursor.com'], editable: true }
      ];

      // Direct fetch — avoids relying on window.customerConfigGetProviders being injected
      try {
        const res = await fetch('/api/config/customer/providers');
        if (res.ok) {
          const json = await res.json();
          const providers = json?.data?.providers;
          if (Array.isArray(providers) && providers.length > 0) {
            this.presetProviders = providers;
            return;
          }
        }
      } catch (err) {
        console.warn('Failed to fetch preset providers from API, using fallback', err);
      }

      this.presetProviders = HARDCODED_PRESETS;
    },
    async loadCustomerConfig() {
      this.isLoadingCustomerConfig = true;
      this.customerConfigError = '';
      this.customerConfigSuccess = '';

      try {
        const res = await fetch('/api/config/customer');
        if (!res.ok) throw new Error(this.t('settings_configLoadFailed'));
        const json = await res.json();
        const data = json?.data || json;
        this.customerConfig = normalizeCustomerConfig(data?.customer);
        this.customerConfigVersion = typeof data?.version === 'string' ? data.version : '';
        this.hasLoadedCustomerConfig = true;
        this.customerConfigError = '';
      } catch (error) {
        this.customerConfig = createDefaultCustomerConfig();
        this.customerConfigVersion = '';
        this.customerConfigError = error instanceof Error ? error.message : this.t('settings_configLoadFailed');
      } finally {
        this.isLoadingCustomerConfig = false;
      }
    },
    async saveCustomerConfig(nextConfig) {
      this.isSavingCustomerConfig = true;
      this.customerConfigError = '';
      this.customerConfigSuccess = '';

      // 合并兜底见 buildCustomerSavePatch：卡片载荷只覆盖自己拥有的顶层段。
      // mergedConfig 兼作 POST 响应缺 customer 时的回显兜底（保存意图态）。
      const mergedConfig = mergeCustomerConfig(nextConfig, this.customerConfig);
      const patch = buildCustomerSavePatch(nextConfig, this.customerConfig);

      try {
        if (!patch || !Object.keys(patch).length) {
          this.customerConfigError = '';
          return;
        }

        const res = await fetch('/api/config/customer', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ customer: cloneCustomerConfig(patch) })
        });
        if (!res.ok) {
          const errJson = await res.json().catch(() => ({}));
          throw new Error(errJson?.msg || this.t('settings_configSaveFailed'));
        }
        const json = await res.json();
        const data = json?.data || json;
        this.customerConfig = normalizeCustomerConfig(data?.customer || mergedConfig);
        this.customerConfigVersion = typeof data?.version === 'string' ? data.version : this.customerConfigVersion;
        this.customerConfigError = '';
        this.customerConfigSuccess = this.t('settings_configSaved');
      } catch (error) {
        this.customerConfigError = error instanceof Error ? error.message : this.t('settings_configSaveFailed');
        throw error;
      } finally {
        this.isSavingCustomerConfig = false;
      }
    },
    syncLegacyTab(tabName) {
      const tab = document.querySelector(`.settings-tab[data-tab="${tabName}"]`);
      if (tab instanceof HTMLElement) {
        tab.click();
      }
    }
  }
}
</script>
