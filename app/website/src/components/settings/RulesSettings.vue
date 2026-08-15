<template>
  <div class="settings-pane flex min-h-[calc(100vh-14rem)] flex-1 flex-col" data-pane="rules">
    <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
      <div class="flex flex-col gap-4 md:flex-row md:items-start md:justify-between">
        <div>
          <h2 class="text-xl font-bold text-slate-900 dark:text-white">{{ t('rules_title') }}</h2>
          <p class="text-sm text-slate-500">
            {{ t('rules_description') }}
          </p>
        </div>
        <div class="flex flex-col items-start gap-2 md:items-end">
          <span class="rounded bg-primary/10 px-3 py-1 text-[11px] font-bold uppercase tracking-wide text-primary">
            {{ loading ? t('rules_loading') : t('rules_customerOnly') }}
          </span>
          <span v-if="version" class="text-[11px] text-slate-400">{{ t('rules_version', { version }) }}</span>
        </div>
      </div>

      <div
        v-if="error"
        class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200"
        role="alert"
      >
        {{ error }}
      </div>

      <div v-if="loading" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-8 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
        {{ t('rules_loadingConfig') }}
      </div>

      <form v-else class="space-y-6" @submit.prevent="handleSubmit">
        <section
          class="rounded-xl border p-4 transition-all"
          :class="form.proxy.enable
            ? 'border-slate-200 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-900/40'
            : 'border-slate-200 bg-slate-100/90 opacity-70 saturate-[0.8] dark:border-slate-800 dark:bg-slate-900/70'"
        >
          <div class="mb-4 flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
            <div class="flex items-center gap-2">
              <span class="material-symbols-outlined text-primary">vpn_key</span>
              <div>
                <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_customerProxy') }}</h3>
                <p class="text-xs text-slate-500">{{ t('rules_proxyDesc') }}</p>
              </div>
            </div>

            <label
              class="inline-flex cursor-pointer items-center justify-between gap-3 rounded-full border px-3 py-2 text-sm font-medium shadow-sm transition md:min-w-[210px]"
              :class="form.proxy.enable
                ? 'border-primary/20 bg-white text-slate-700 dark:border-primary/30 dark:bg-slate-900 dark:text-slate-200'
                : 'border-slate-200 bg-white/70 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300'"
            >
              <span>{{ t('rules_enableProxy') }}</span>
              <span class="relative">
                <input v-model="form.proxy.enable" class="peer sr-only" type="checkbox" />
                <span class="relative block h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
              </span>
            </label>
          </div>

          <div class="grid gap-4 md:grid-cols-[180px_minmax(0,1fr)]">
            <label class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyType') }}</span>
              <select
                v-model="form.proxy.type"
                :disabled="!form.proxy.enable"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
              >
                <option value="socks5">SOCKS5</option>
                <option value="http">HTTP</option>
              </select>
            </label>

            <label class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_server') }}</span>
              <input
                v-model.trim="form.proxy.server"
                :disabled="!form.proxy.enable"
                class="w-full rounded-lg border px-3 py-2 text-sm shadow-sm transition focus:outline-none focus:ring-2 dark:text-slate-100"
                :class="serverFieldClass"
                placeholder="127.0.0.1:1080"
                type="text"
                @keydown.enter.prevent
              />
              <p v-if="serverError" class="text-xs text-red-500">{{ serverError }}</p>
            </label>

            <label class="space-y-2 md:col-start-1">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyUsername') }}</span>
              <input
                v-model.trim="form.proxy.username"
                :disabled="!form.proxy.enable"
                autocomplete="username"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                :placeholder="t('rules_proxyUsernamePlaceholder')"
                type="text"
                @keydown.enter.prevent
              />
            </label>

            <label class="space-y-2">
              <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_proxyPassword') }}</span>
              <input
                v-model="form.proxy.password"
                :disabled="!form.proxy.enable"
                autocomplete="current-password"
                class="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                :placeholder="t('rules_proxyPasswordPlaceholder')"
                type="password"
                @keydown.enter.prevent
              />
            </label>
          </div>
        </section>

        <section class="rounded-xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-800 dark:bg-slate-900/40">
          <div class="mb-4 flex items-center gap-2">
            <span class="material-symbols-outlined text-primary">robot_2</span>
            <div>
              <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_aiRules') }}</h3>
              <p class="text-xs text-slate-500">{{ t('rules_aiRulesDesc') }}</p>
            </div>
          </div>

          <div v-if="!providerOrder.length" class="rounded-lg border border-dashed border-slate-300 bg-white px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-background-dark dark:text-slate-400">
            {{ t('rules_noProviders') }}
          </div>

          <div v-else class="grid gap-4 xl:grid-cols-2">
            <template v-for="provider in providerOrder" :key="provider">
            <article
              v-if="form.ai_rules[provider]"
              class="relative rounded-lg border border-slate-200 bg-white p-4 shadow-sm dark:border-slate-800 dark:bg-background-dark"
            >
              <div class="mb-3 flex items-center justify-between gap-3">
                <div>
                  <h4 class="font-semibold text-slate-900 dark:text-white">{{ providerLabel(provider, presetProviders) }}</h4>
                  <p class="text-xs text-slate-500">{{ provider }}</p>
                </div>
                <label class="inline-flex cursor-pointer items-center gap-2 text-xs font-semibold text-slate-600 dark:text-slate-200">
                  <input v-model="form.ai_rules[provider].enble" class="peer sr-only" type="checkbox" />
                  <span class="relative h-6 w-11 rounded-full bg-slate-300 transition-colors after:absolute after:left-0.5 after:top-0.5 after:h-5 after:w-5 after:rounded-full after:bg-white after:transition-transform after:content-[''] peer-checked:bg-primary peer-checked:after:translate-x-5 peer-focus-visible:outline peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-primary dark:bg-slate-700"></span>
                  
                </label>
              </div>

              <div v-if="providerEditable(provider)" class="relative" :data-provider-editor-root="provider">
                <button
                  type="button"
                  class="flex w-full items-center justify-between gap-3 rounded-lg border border-slate-200 bg-slate-50 px-3 py-2 text-left transition hover:border-primary/20 hover:bg-primary/5 dark:border-slate-800 dark:bg-slate-900/60 dark:hover:border-primary/30 dark:hover:bg-primary/10"
                  :aria-expanded="isProviderEditorOpen(provider)"
                  @click.stop="toggleProviderEditor(provider)"
                >
                  <span class="text-xs font-normal text-slate-500 dark:text-slate-400">{{ t('rules_includeDomains') }}</span>
                  <span class="inline-flex items-center rounded-full bg-white px-2 py-0.5 text-[11px] font-medium text-slate-500 shadow-sm ring-1 ring-slate-200 dark:bg-slate-950 dark:text-slate-400 dark:ring-slate-800">
                    {{ isProviderEditorOpen(provider) ? t('rules_includeDomainsClose') : t('rules_includeDomainsEdit') }}
                  </span>
                </button>

                <div
                  v-if="isProviderEditorOpen(provider)"
                  class="absolute inset-x-0 top-full z-20 mt-2 rounded-2xl border border-slate-200 bg-white p-4 shadow-2xl dark:border-slate-700 dark:bg-slate-950"
                  @click.stop
                >
                  <div class="mb-3 flex items-start justify-between gap-3">
                    <h5 class="text-xs font-medium text-slate-600 dark:text-slate-300">{{ t('rules_includeDomainsDialogTitle', { provider: providerLabel(provider, presetProviders) }) }}</h5>
                    <button
                      type="button"
                      class="rounded-lg p-1 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-800 dark:hover:text-slate-300"
                      @click="cancelProviderEditor"
                    >
                      <span class="material-symbols-outlined text-base">close</span>
                    </button>
                  </div>

                  <textarea
                    :ref="`providerEditor-${provider}`"
                    v-model="providerEditor.draft"
                    class="min-h-32 w-full rounded-xl border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                    :placeholder="t('rules_includeDomainsPlaceholder')"
                  ></textarea>

                  <p class="mt-2 text-xs text-slate-500">{{ t('rules_includeDomainsDialogHint') }}</p>

                  <div class="mt-4 flex items-center justify-end gap-2">
                    <button
                      type="button"
                      class="rounded-lg border border-slate-200 px-2.5 py-1.5 text-xs font-normal text-slate-500 transition hover:bg-slate-100 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
                      @click="cancelProviderEditor"
                    >
                      {{ t('rules_includeDomainsCancel') }}
                    </button>
                    <button
                      type="button"
                      class="rounded-lg bg-primary px-2.5 py-1.5 text-xs font-normal text-white transition hover:bg-primary/90"
                      @click="saveProviderEditor"
                    >
                      {{ t('rules_includeDomainsSave') }}
                    </button>
                  </div>
                </div>
              </div>
            </article>
            </template>
          </div>
        </section>

        <section class="rounded-xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-800 dark:bg-slate-900/40">
          <div class="mb-4 flex items-center gap-2">
            <span class="material-symbols-outlined text-primary">rule_settings</span>
            <div>
              <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('rules_proxyRules') }}</h3>
              <p class="text-xs text-slate-500">{{ t('rules_proxyRulesDesc') }}</p>
            </div>
          </div>

          <label class="space-y-2">
            <span class="text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('rules_rulesList') }}</span>
            <textarea
              :value="_proxyRulesText"
              class="min-h-40 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-slate-700 shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
              placeholder="domain,example.com,proxy&#10;api.aliang.one"
              @input="_proxyRulesText = $event.target.value"
            ></textarea>
          </label>
        </section>

        <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-background-dark md:flex-row md:items-center md:justify-between">
          <div class="text-xs text-slate-500">
            {{ t('rules_formHint') }}
          </div>
          <button
            id="rulesConfigSaveBtn"
            :disabled="saving || !!serverError"
            class="inline-flex min-h-11 items-center justify-center gap-2 rounded bg-primary px-4 py-2 text-sm font-medium text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
            type="submit"
          >
            <span class="material-symbols-outlined text-sm">save</span>
            {{ saving ? t('rules_saving') : t('rules_saveConfig') }}
          </button>
        </div>
      </form>
    </div>

    <div
      v-if="_showSuccessDialog"
      class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/45 p-4 backdrop-blur-sm"
      @click.self="hideSuccessDialog"
    >
      <div class="relative w-full max-w-sm rounded-2xl border border-emerald-200 bg-white p-5 shadow-2xl dark:border-emerald-500/30 dark:bg-slate-900">
        <div class="flex items-start gap-3">
          <div class="flex h-11 w-11 items-center justify-center rounded-full bg-emerald-100 text-emerald-600 dark:bg-emerald-500/15 dark:text-emerald-300">
            <span class="material-symbols-outlined">check_circle</span>
          </div>
          <div class="min-w-0 flex-1">
            <h3 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('rules_configSaved') }}</h3>
            <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ successMessage }}</p>
          </div>
          <button
            type="button"
            aria-label="Close"
            class="relative inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-full text-slate-400 transition hover:bg-slate-100 hover:text-slate-700 focus-visible:outline focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-emerald-500 dark:hover:bg-slate-800 dark:hover:text-slate-200"
            @click="hideSuccessDialog"
          >
            <svg class="pointer-events-none absolute inset-0 h-full w-full -rotate-90" viewBox="0 0 36 36" aria-hidden="true">
              <circle
                cx="18" cy="18" r="15.5"
                fill="none"
                stroke="currentColor"
                stroke-width="2.75"
                class="text-emerald-100 dark:text-emerald-900/60"
              />
              <circle
                cx="18" cy="18" r="15.5"
                fill="none"
                stroke="currentColor"
                stroke-width="2.75"
                stroke-linecap="round"
                :stroke-dasharray="successRingCircumference"
                :stroke-dashoffset="successRingDashoffset"
                class="text-emerald-500 transition-none"
              />
            </svg>
            <span class="material-symbols-outlined relative z-10 text-lg">close</span>
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

function defaultConfig() {
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

function normalizeStringList(items) {
  return Array.isArray(items)
    ? items.map((entry) => String(entry).trim()).filter(Boolean)
    : [];
}

function sanitizeList(value) {
  return value
    .split(/[\n,;]+/)
    .map((entry) => normalizeIncludeEntry(entry))
    .filter(Boolean);
}

function normalizeIncludeEntry(entry) {
  const trimmed = String(entry || '').trim();
  if (!trimmed) {
    return '';
  }

  if (trimmed.startsWith('//')) {
    try {
      return new URL(`https:${trimmed}`).hostname || trimmed;
    } catch {
      return trimmed;
    }
  }

  if (/^[a-zA-Z][a-zA-Z\d+\-.]*:\/\//.test(trimmed)) {
    try {
      return new URL(trimmed).hostname || trimmed;
    } catch {
      return trimmed;
    }
  }

  return trimmed;
}

function sanitizeLines(value) {
  return value
    .split('\n')
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function normalizeAiRules(aiRules = {}) {
  if (!aiRules || typeof aiRules !== 'object' || Array.isArray(aiRules)) {
    return {};
  }

  return Object.fromEntries(
    Object.entries(aiRules).map(([provider, value]) => [provider, {
      enble: Boolean(value?.enble ?? value?.enable),
      include: normalizeStringList(value?.include ?? value?.exclude),
      editable: typeof value?.editable === 'boolean' ? value.editable : undefined
    }])
  );
}

function normalizeProxyRules(raw) {
  return normalizeStringList(raw);
}

function normalizeConfig(config = {}) {
  const defaults = defaultConfig();
  return {
    proxy: {
      enable: valueOrDefaultBoolean(config?.proxy?.enable, true),
      type: config?.proxy?.type === 'http' ? 'http' : defaults.proxy.type,
      server: typeof config?.proxy?.server === 'string' ? config.proxy.server : defaults.proxy.server,
      username: typeof config?.proxy?.username === 'string' ? config.proxy.username : defaults.proxy.username,
      password: typeof config?.proxy?.password === 'string' ? config.proxy.password : defaults.proxy.password
    },
    ai_rules: normalizeAiRules(config?.ai_rules),
    proxy_rules: normalizeProxyRules(config?.proxy_rules)
  };
}

function providerKeys(aiRules = {}) {
  return Object.keys(aiRules);
}

function mergeProviderOrder(configuredKeys, presetProviders) {
  const presetMap = {};
  for (const p of presetProviders) {
    presetMap[p.key] = p;
  }
  const ordered = [...configuredKeys];
  for (const p of presetProviders) {
    if (!presetMap[p.key] || configuredKeys.includes(p.key)) continue;
    ordered.push(p.key);
  }
  return ordered;
}

const HOST_PORT_RE = /^(.+):(\d{1,5})$/;
const DOMAIN_RE = /^(?=.{1,253}$)([a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$/;

function isValidServer(value, t) {
  if (!value) return '';
  const parsed = parseProxyServer(value);
  if (!parsed) return t('rules_serverError');
  const port = Number(parsed.port);
  if (port < 1 || port > 65535) return t('rules_serverPortRange');
  if (isValidHost(parsed.host)) return '';
  return t('rules_serverHostInvalid');
}

function parseProxyServer(value) {
  const raw = String(value || '').trim();
  if (!raw) return null;

  if (/^[a-zA-Z][a-zA-Z\d+\-.]*:\/\//.test(raw) || raw.startsWith('//') || raw.includes('@')) {
    try {
      const urlValue = raw.includes('@') && !raw.includes('://') && !raw.startsWith('//')
        ? `proxy://${raw}`
        : raw;
      const parsed = new URL(urlValue);
      if (!parsed.hostname || !parsed.port) return null;
      return { host: parsed.hostname, port: parsed.port };
    } catch {
      return null;
    }
  }

  if (raw.startsWith('[')) {
    const end = raw.indexOf(']');
    if (end <= 1 || raw[end + 1] !== ':') return null;
    return { host: raw.slice(1, end), port: raw.slice(end + 2) };
  }

  const match = raw.match(HOST_PORT_RE);
  if (!match) return null;
  return { host: match[1], port: match[2] };
}

function isValidHost(host) {
  if (!host) return false;
  if (/^[\da-fA-F:]+$/.test(host) && host.includes(':')) return true;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(host)) {
    return host.split('.').every((o) => Number(o) >= 0 && Number(o) <= 255);
  }
  return DOMAIN_RE.test(host);
}

function valueOrDefaultBoolean(value, defaultValue) {
  return typeof value === 'boolean' ? value : defaultValue;
}

const SUCCESS_RING_RADIUS = 15.5;
const SUCCESS_RING_CIRCUMFERENCE = 2 * Math.PI * SUCCESS_RING_RADIUS;

export default {
  name: 'RulesSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: {
      type: Object,
      default: () => defaultConfig()
    },
    presetProviders: {
      type: Array,
      default: () => []
    },
    loading: {
      type: Boolean,
      default: false
    },
    saving: {
      type: Boolean,
      default: false
    },
    error: {
      type: String,
      default: ''
    },
    successMessage: {
      type: String,
      default: ''
    },
    version: {
      type: String,
      default: ''
    }
  },
  emits: ['save'],
  data() {
    return {
      providerEditor: {
        provider: '',
        draft: ''
      },
      form: normalizeConfig(this.config),
      _proxyRulesText: '',
      _providerIncludeTexts: {},
      _showSuccessDialog: false,
      _successDialogTimer: null,
      _countdownProgress: 1,
      _providerEditorClickHandler: null
    };
  },
  created() {
    this.ensureProviders();
    this.syncTextFromForm();
  },
  mounted() {
    this._providerEditorClickHandler = (event) => {
      if (!this.providerEditor.provider) {
        return;
      }

      const target = event.target;
      if (!(target instanceof Element)) {
        return;
      }

      const selector = `[data-provider-editor-root="${this.providerEditor.provider}"]`;
      if (target.closest(selector)) {
        return;
      }

      this.cancelProviderEditor();
    };
    document.addEventListener('click', this._providerEditorClickHandler, true);
  },
  beforeUnmount() {
    this.clearSuccessDialogTimer();
    if (this._providerEditorClickHandler) {
      document.removeEventListener('click', this._providerEditorClickHandler, true);
      this._providerEditorClickHandler = null;
    }
  },
  computed: {
    providerOrder() {
      return mergeProviderOrder(providerKeys(this.form.ai_rules), this.presetProviders);
    },
    serverError() {
      if (!this.form.proxy.enable) return '';
      return isValidServer(this.form.proxy.server, this.t);
    },
    serverFieldClass() {
      if (!this.form.proxy.server) return 'border-slate-300 bg-white focus:border-primary focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900';
      return this.serverError
        ? 'border-red-400 bg-red-50 focus:border-red-500 focus:ring-red-500/20 dark:border-red-500/50 dark:bg-red-900/10'
        : 'border-emerald-400 bg-emerald-50/50 focus:border-emerald-500 focus:ring-emerald-500/20 dark:border-emerald-500/50 dark:bg-emerald-900/10';
    },
    successRingCircumference() {
      return SUCCESS_RING_CIRCUMFERENCE;
    },
    successRingDashoffset() {
      const clampedProgress = Math.min(Math.max(this._countdownProgress, 0), 1);
      return SUCCESS_RING_CIRCUMFERENCE * (1 - clampedProgress);
    }
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = normalizeConfig(nextConfig);
        this.ensureProviders();
        this.syncTextFromForm();
      }
    },
    presetProviders: {
      deep: true,
      handler() {
        this.ensureProviders();
        this.syncTextFromForm();
      }
    },
    successMessage(nextValue) {
      if (typeof nextValue === 'string' && nextValue.trim()) {
        this.showSuccessDialog();
        return;
      }
      this.hideSuccessDialog();
    }
  },
  methods: {
    clearSuccessDialogTimer() {
      if (this._successDialogTimer !== null) {
        window.clearInterval(this._successDialogTimer);
        this._successDialogTimer = null;
      }
    },
    showSuccessDialog() {
      this._showSuccessDialog = true;
      this.clearSuccessDialogTimer();
      const totalDuration = 1800;
      const interval = 50;
      const totalSteps = totalDuration / interval;
      let currentStep = 0;
      this._countdownProgress = 1;
      this._successDialogTimer = window.setInterval(() => {
        currentStep++;
        this._countdownProgress = 1 - (currentStep / totalSteps);
        if (currentStep >= totalSteps) {
          window.clearInterval(this._successDialogTimer);
          this._successDialogTimer = null;
          this._showSuccessDialog = false;
        }
      }, interval);
    },
    hideSuccessDialog() {
      this._showSuccessDialog = false;
      this.clearSuccessDialogTimer();
    },
    ensureProviders() {
      for (const p of this.presetProviders) {
        if (!(p.key in this.form.ai_rules)) {
          this.form.ai_rules[p.key] = { enble: false, include: [], editable: Boolean(p.editable) };
          continue;
        }

        if (typeof this.form.ai_rules[p.key].editable !== 'boolean' && typeof p.editable === 'boolean') {
          this.form.ai_rules[p.key].editable = p.editable;
        }
      }
    },
    providerLabel(key, presetProviders) {
      const preset = presetProviders.find(p => p.key === key);
      return preset ? preset.label : key;
    },
    providerEditable(provider) {
      const rule = this.form.ai_rules[provider];
      if (typeof rule?.editable === 'boolean') {
        return rule.editable;
      }
      const preset = this.presetProviders.find((entry) => entry.key === provider);
      return Boolean(preset?.editable);
    },
    syncTextFromForm() {
      this._proxyRulesText = (this.form.proxy_rules || []).join('\n');
      for (const key of Object.keys(this.form.ai_rules)) {
        this._providerIncludeTexts[key] = (this.form.ai_rules[key].include || []).join('\n');
      }
    },
    isProviderEditorOpen(provider) {
      return this.providerEditor.provider === provider;
    },
    toggleProviderEditor(provider) {
      if (!this.providerEditable(provider)) {
        return;
      }
      if (this.isProviderEditorOpen(provider)) {
        this.cancelProviderEditor();
        return;
      }

      this.providerEditor.provider = provider;
      this.providerEditor.draft = this._providerIncludeTexts[provider] || '';
      this.$nextTick(() => {
        const refValue = this.$refs[`providerEditor-${provider}`];
        const textarea = Array.isArray(refValue) ? refValue[0] : refValue;
        if (textarea && typeof textarea.focus === 'function') {
          textarea.focus();
        }
      });
    },
    cancelProviderEditor() {
      this.providerEditor.provider = '';
      this.providerEditor.draft = '';
    },
    saveProviderEditor() {
      const provider = this.providerEditor.provider;
      if (!provider) {
        return;
      }
      if (!this.providerEditable(provider)) {
        this.cancelProviderEditor();
        return;
      }

      const include = sanitizeList(this.providerEditor.draft);
      this._providerIncludeTexts[provider] = include.join('\n');
      if (this.form.ai_rules[provider]) {
        this.form.ai_rules[provider].include = include;
      }
      this.cancelProviderEditor();
    },
    async handleSubmit() {
      const normalized = normalizeConfig(this.form);
      normalized.proxy_rules = sanitizeLines(this._proxyRulesText);
      for (const [key, text] of Object.entries(this._providerIncludeTexts)) {
        if (normalized.ai_rules[key]) {
          normalized.ai_rules[key].include = sanitizeList(text);
        }
      }
      this.$emit('save', normalized);
    }
  }
}
</script>
