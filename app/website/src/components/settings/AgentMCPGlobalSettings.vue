<template>
  <!-- MCP 全局开关（2026-10-31）。放在「能力接入」分区最上方：这是「要不要让
       内核用 MCP」的总闸，下面那张清单是「有哪些 server」，顺序必须是先闸后清单。
       只作用于 AliangCode 内核；claude/codex 走各自 CLI 的配置。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">toggle_on</span>
      </div>
      <div class="min-w-0">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('mcpGlobal_title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('mcpGlobal_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span class="block text-xs font-semibold">{{ t('mcpGlobal_enable') }}</span>
      <input
        type="checkbox"
        data-testid="mcp-global-enable"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="form.enabled"
        :disabled="loading || saving"
        @change="form.enabled = $event.target.checked"
      />
    </label>

    <p class="mt-3 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('mcpGlobal_hint') }}</p>

    <div v-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving || !dirty"
      @click="save"
    >
      <span class="material-symbols-outlined text-base">save</span>
      {{ saving ? t('mcpGlobal_saving') : t('mcpGlobal_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

export default {
  name: 'AgentMCPGlobalSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' },
  },
  emits: ['save'],
  data() {
    return { form: this.formFromConfig(this.config) };
  },
  computed: {
    // 三态：后端 nil=默认开启。面板只暴露开/关两态，未配置时显示「开」——
    // 与内核 EffectiveAgentCoreMCPEnabled 的默认值一致，避免面板说「关」而
    // 内核实际在开。
    dirty() {
      const current = this.config?.agentcore?.mcp_enabled;
      const isCurrentlyEnabled = typeof current === 'boolean' ? current : true;
      return this.form.enabled !== isCurrentlyEnabled;
    },
  },
  watch: {
    config: {
      deep: true,
      handler(next) {
        this.form = this.formFromConfig(next);
      },
    },
  },
  methods: {
    formFromConfig(config) {
      const raw = config?.agentcore?.mcp_enabled;
      return { enabled: typeof raw === 'boolean' ? raw : true };
    },
    save() {
      const next = {
        ...(this.config || {}),
        agentcore: {
          ...(this.config?.agentcore || {}),
          // 线格式键 mcp_enabled（Go tag）。显式布尔——这里必须写 true/false，
          // 不能省略，否则「关闭」永远存不下去（后端 nil 视为开启）。
          mcp_enabled: !!this.form.enabled,
        },
      };
      this.$emit('save', next);
    },
  },
};
</script>
