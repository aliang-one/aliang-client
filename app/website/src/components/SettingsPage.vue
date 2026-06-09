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
              :class="currentPage === 'user' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('user')"
            >
              {{ t('settings_tabUserCenter') }}
            </a>
            <a
              class="py-5 text-sm font-medium"
              :class="currentPage === 'log' ? 'border-b-2 border-primary text-primary' : 'text-slate-500 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100'"
              href="javascript:void(0)"
              @click="showPage('log')"
            >
              {{ t('settings_tabLogs') }}
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
              class="flex h-8 w-8 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 dark:hover:bg-slate-800"
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

        <div class="mb-6 grid grid-cols-3 gap-2 md:hidden">
          <button
            type="button"
            class="rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'settings' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('settings')"
          >
            {{ t('settings_tabSettings') }}
          </button>
          <button
            type="button"
            class="rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'user' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('user')"
          >
            {{ t('settings_pageUser') }}
          </button>
          <button
            type="button"
            class="rounded-lg border px-3 py-2 text-sm font-semibold transition"
            :class="currentPage === 'log' ? 'border-primary bg-primary text-white' : 'border-slate-200 bg-white text-slate-700 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-100'"
            @click="showPage('log')"
          >
            {{ t('settings_tabLogs') }}
          </button>
        </div>

        <div
          v-if="currentPage !== 'user'"
          class="mb-6 rounded-xl border px-4 py-3 text-sm"
          :class="isAuthenticated ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300' : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300'"
        >
          {{ authNotice }}
        </div>

        <div v-if="currentPage === 'settings'" class="grid grid-cols-1 gap-8 lg:grid-cols-12">
          <section class="flex flex-col gap-4 lg:col-span-8">
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
            <RulesSettings
              v-else
              :config="customerConfig"
              :preset-providers="presetProviders"
              :loading="isLoadingCustomerConfig"
              :saving="isSavingCustomerConfig"
              :error="customerConfigError"
              :success-message="customerConfigSuccess"
              :version="customerConfigVersion"
              @save="saveCustomerConfig"
            />
          </section>

          <aside class="flex flex-col gap-6 lg:col-span-4">
            <SystemSettings v-if="isAuthenticated" />
            <AgentSettings v-if="isAuthenticated" />
          </aside>
        </div>

        <section v-else-if="currentPage === 'user'" class="flex flex-col gap-4">
          <UserInfoSettings />
        </section>

        <section v-else class="flex flex-col gap-4">
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
          <LogsSettings v-if="isAuthenticated" />
        </section>
      </main>

      <footer class="border-t border-slate-100 py-6 dark:border-slate-800">
        <div class="mx-auto flex max-w-7xl items-center justify-between px-4 text-[10px] font-medium text-slate-400 sm:px-6 lg:px-8">
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
import RulesSettings from './settings/RulesSettings.vue';
import UserInfoSettings from './settings/UserInfoSettings.vue';
import LogsSettings from './settings/LogsSettings.vue';
import SystemSettings from './settings/SystemSettings.vue';
import AgentSettings from './settings/AgentSettings.vue';
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
      password: ''
    },
    ai_rules: {},
    proxy_rules: []
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

function normalizeCustomerConfig(payload = {}) {
  const defaults = createDefaultCustomerConfig();
  return {
    proxy: {
      enable: typeof payload?.proxy?.enable === 'boolean' ? payload.proxy.enable : defaults.proxy.enable,
      type: payload?.proxy?.type === 'http' ? 'http' : defaults.proxy.type,
      server: typeof payload?.proxy?.server === 'string' ? payload.proxy.server : defaults.proxy.server,
      username: typeof payload?.proxy?.username === 'string' ? payload.proxy.username : defaults.proxy.username,
      password: typeof payload?.proxy?.password === 'string' ? payload.proxy.password : defaults.proxy.password
    },
    ai_rules: normalizeAiRules(payload?.ai_rules),
    proxy_rules: normalizeProxyRules(payload?.proxy_rules)
  };
}

function cloneCustomerConfig(config) {
  return JSON.parse(JSON.stringify(config));
}

function isPlainObject(value) {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function areArraysEqual(left = [], right = []) {
  if (left.length !== right.length) {
    return false;
  }
  return left.every((value, index) => value === right[index]);
}

function buildCustomerConfigPatch(nextConfig, currentConfig) {
  const normalizedNext = normalizeCustomerConfig(nextConfig);
  const normalizedCurrent = normalizeCustomerConfig(currentConfig);
  const patch = buildConfigPatch(normalizedNext, normalizedCurrent) || {};

  for (const key of ['username', 'password']) {
    const nextValue = String(normalizedNext.proxy?.[key] ?? '');
    const currentValue = String(normalizedCurrent.proxy?.[key] ?? '');
    if (!nextValue && currentValue) {
      patch.proxy = isPlainObject(patch.proxy) ? patch.proxy : {};
      patch.proxy[key] = '';
    }
  }

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


export default {
  name: 'SettingsPage',
  components: {
    RulesSettings,
    UserInfoSettings,
    LogsSettings,
    SystemSettings,
    AgentSettings
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
      return ['settings', 'user', 'log'].includes(this.currentPage);
    },
    pageTitle() {
      if (this.currentPage === 'user') {
        return this.t('settings_pageUser');
      }
      if (this.currentPage === 'log') {
        return this.t('settings_pageLog');
      }
      return this.t('settings_pageConfiguration');
    },
    pageAccent() {
      if (this.currentPage === 'user') {
        return this.t('settings_pageCenter');
      }
      if (this.currentPage === 'log') {
	      return this.t('settings_pageOverview');
      }
      return this.t('settings_pageCenter');
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
        { key: 'cursor', label: 'Cursor', default_include: ['api.cursor.com'], editable: true },
        { key: 'copilot', label: 'Copilot', default_include: ['copilot.microsoft.com'], editable: true }
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

      const normalizedConfig = normalizeCustomerConfig(nextConfig);
      const patch = buildCustomerConfigPatch(normalizedConfig, this.customerConfig);

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
        this.customerConfig = normalizeCustomerConfig(data?.customer || normalizedConfig);
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
