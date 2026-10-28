<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">hub</span>
      </div>
      <div class="min-w-0">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agentCore_title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('agentCore_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span class="block text-xs font-semibold">{{ t('agentCore_enable') }}</span>
      <input
        type="checkbox"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="form.enable"
        :disabled="loading || saving"
        @change="form.enable = $event.target.checked"
      />
    </label>

    <div class="mt-4 flex flex-col gap-3">
      <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
        <span>{{ t('agentCore_baseUrl') }}</span>
        <input
          v-model="form.baseUrl"
          type="text"
          :placeholder="t('agentCore_baseUrlPlaceholder')"
          :disabled="loading || saving"
          class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
      </label>
      <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
        <span>{{ t('agentCore_apiKey') }}</span>
        <span class="relative mt-1.5 block">
          <input
            v-model="form.apiKey"
            :type="showKey ? 'text' : 'password'"
            autocomplete="off"
            :placeholder="t('agentCore_apiKeyPlaceholder')"
            :disabled="loading || saving"
            class="h-10 w-full rounded-lg border border-slate-200 bg-white px-3 pr-16 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          />
          <button
            type="button"
            class="absolute right-2 top-1/2 -translate-y-1/2 text-[11px] font-medium text-slate-400 hover:text-primary"
            :aria-pressed="showKey"
            :disabled="loading || saving"
            @click="showKey = !showKey"
          >
            {{ showKey ? t('agentCore_hideKey') : t('agentCore_showKey') }}
          </button>
        </span>
      </label>
    </div>

    <p class="mt-3 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('agentCore_hint') }}</p>

    <div v-if="localError" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ localError }}</div>
    <div v-else-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving || !!localError"
      @click="save"
    >
      <span class="material-symbols-outlined text-base">save</span>
      {{ saving ? t('agentCore_saving') : t('agentCore_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { normalizeAgentCoreLLMConfig, validateAgentCoreLLMConfig } from '../../utils/agentCoreSettings';

export default {
  name: 'AgentCoreSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  data() {
    return {
      form: this.formFromConfig(this.config),
      showKey: false
    };
  },
  computed: {
    localError() {
      const key = validateAgentCoreLLMConfig({
        enable: this.form.enable,
        baseUrl: this.form.baseUrl,
        apiKey: this.form.apiKey
      });
      return key ? this.t(key) : '';
    }
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = this.formFromConfig(nextConfig);
      }
    }
  },
  methods: {
    formFromConfig(config) {
      const llm = normalizeAgentCoreLLMConfig(config?.agentcore_llm);
      // llm 是线格式(enabled);面板表单内部状态键用 enable
      return { enable: llm.enabled, baseUrl: llm.base_url, apiKey: llm.api_key };
    },
    save() {
      if (this.localError) return;
      const nextConfig = {
        ...(this.config || {}),
        agentcore_llm: {
          // 线格式开关键是 enabled(Go tag);面板内部状态键是 enable,见
          // utils/agentCoreSettings.js 的注释。发错键会被 encoding/json 静默
          // 丢弃,层永远激活不了。
          enabled: !!this.form.enable,
          base_url: String(this.form.baseUrl || ''),
          api_key: String(this.form.apiKey || '')
        }
      };
      this.$emit('save', nextConfig);
    }
  }
};
</script>
