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

    <label class="mt-5 min-w-0 text-[10px] font-bold uppercase text-slate-400">
      <span>{{ t('agentCore_guardTitle') }}</span>
      <input
        v-model="form.maxTurns"
        type="number"
        min="0"
        max="50"
        step="1"
        :placeholder="t('agentCore_guardPlaceholder')"
        :disabled="loading || saving"
        class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
      />
      <span class="mt-1.5 block text-[11px] font-normal normal-case leading-4 text-slate-500 dark:text-slate-400">{{ t('agentCore_guardHint') }}</span>
    </label>

    <p class="mt-3 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('agentCore_hint') }}</p>

    <div v-if="localError" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ localError }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <!-- 旧版内核端点配置(agentcore_llm)灰字提示(endpoint 二期 Task 8):
         卡片已删端点半卡,后端 POST 白名单仍接受该键但值被忽略(spec §9);
         GET /api/config/customer 现有读取链回显该字段,SettingsPage 从原始
         响应取 enabled 传 prop——只读检测,不新增 API,不改写配置。 -->
    <p
      v-if="legacyAgentCoreLLM"
      data-agentcore-legacy-hint
      class="mt-3 text-[11px] leading-4 text-slate-400 dark:text-slate-500"
    >{{ t('agentCore_legacyHint') }}</p>

    <button
      type="button"
      data-agentcore-save
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
import { showErrorDialog } from '../../composables/useErrorDialog';
import {
  agentCoreTuningToLineFormat,
  validateAgentCoreTuning,
} from '../../utils/agentCoreSettings';

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
    successMessage: { type: String, default: '' },
    // 旧版内核端点配置(customer.agentcore_llm.enabled=true)只读检测位,
    // 由 SettingsPage 从 GET /api/config/customer 原始响应取值传入——
    // 端点半卡退役后仅剩灰字提示职责,本卡不再读写该键。
    legacyAgentCoreLLM: { type: Boolean, default: false }
  },
  emits: ['save'],
  data() {
    return {
      form: this.formFromConfig(this.config)
    };
  },
  computed: {
    localError() {
      const guardKey = validateAgentCoreTuning(this.form.maxTurns);
      return guardKey ? this.t(guardKey) : '';
    }
  },
  watch: {
    // 加载/保存失败(error prop)走全局报错弹窗(2026-11-17 定案);
    // localError 是实时门禁校验 computed,仍行内展示并阻止提交。
    error: {
      immediate: true,
      handler(next) {
        if (next) {
          showErrorDialog(next);
        }
      }
    },
    config: {
      deep: true,
      handler(nextConfig) {
        this.form = this.formFromConfig(nextConfig);
      }
    }
  },
  methods: {
    formFromConfig(config) {
      const guard = config?.agentcore?.max_identical_tool_turns;
      // 守卫阈值:0/缺省 = 未配置,表单显示空串(placeholder 提示默认值)。
      // agentcore_llm(端点)已退役:本卡不读该键,旧配置提示走
      // legacyAgentCoreLLM prop(SettingsPage 从原始 GET 响应取值)。
      return {
        maxTurns: typeof guard === 'number' && guard > 0 ? String(guard) : ''
      };
    },
    save() {
      if (this.localError) return;
      const nextConfig = {
        ...(this.config || {}),
        agentcore: {
          // 线格式键 max_identical_tool_turns(Go tag)。空串/0 归一成 0 =
          // 用内核默认;清空语义经数字比较天然可达(0≠旧值进 patch)。
          max_identical_tool_turns: agentCoreTuningToLineFormat(this.form.maxTurns)
        }
        // ⚠️ 不再写 agentcore_llm(endpoint 二期 Task 8):后端 POST 白名单
        // 保留接受该键但值被忽略,前端保存链不再读写它。
      };
      this.$emit('save', nextConfig);
    }
  }
};
</script>
