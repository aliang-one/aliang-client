<template>
  <!-- OpenViking 跨设备记忆(Phase2 spec §7.7)。范式照 PeerMCPSettings 两段式:
       ①emit 让父页持久化 customer.openviking;②apply 端点立即对齐三面(不等
       下个 boot)。三态=托管/BYO/关;状态灯=拨测态(§7.5)。数据删除按钮挂起:
       卸载数据语义是 §13.2 未决拍板项,不开挖。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">psychology</span>
      </div>
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('openViking_title') }}</h2>
          <span class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary">{{ t('openViking_managed') }}</span>
        </div>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('openViking_description') }}</p>
      </div>
    </div>

    <!-- 三态接入模式 -->
    <fieldset class="mt-5 border-y border-slate-100 py-3 dark:border-slate-800" data-testid="openviking-mode">
      <legend class="text-xs font-semibold text-slate-700 dark:text-slate-200">{{ t('openViking_mode') }}</legend>
      <label
        v-for="opt in modeOptions"
        :key="opt.value"
        class="mt-2 flex min-h-9 cursor-pointer items-center gap-2.5 text-sm text-slate-700 dark:text-slate-200"
      >
        <input
          type="radio"
          name="openviking-mode"
          :data-testid="'openviking-mode-' + opt.value"
          class="h-4 w-4 shrink-0 border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
          :checked="form.mode === opt.value"
          :disabled="loading || saving"
          @change="form.mode = opt.value"
        />
        <span class="min-w-0">
          <span class="block text-xs font-semibold">{{ t(opt.labelKey) }}</span>
          <span class="mt-0.5 block text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t(opt.descKey) }}</span>
        </span>
      </label>
    </fieldset>

    <!-- BYO 字段(仅自带模式) -->
    <template v-if="form.mode === 'byo'">
      <label class="flex min-h-11 flex-col justify-center gap-1 border-b border-slate-100 py-3 dark:border-slate-800">
        <span class="text-xs font-semibold text-slate-700 dark:text-slate-200">{{ t('openViking_endpoint') }}</span>
        <input
          v-model.trim="form.byoEndpoint"
          type="text"
          data-testid="openviking-endpoint"
          :placeholder="t('openViking_endpointPlaceholder')"
          class="h-9 rounded-lg border border-slate-200 px-2.5 text-xs text-slate-800 focus:border-primary/40 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          :disabled="loading || saving"
        />
      </label>
      <label class="flex min-h-11 flex-col justify-center gap-1 border-b border-slate-100 py-3 dark:border-slate-800">
        <span class="text-xs font-semibold text-slate-700 dark:text-slate-200">{{ t('openViking_apiKey') }}</span>
        <input
          v-model.trim="form.byoApiKey"
          type="password"
          data-testid="openviking-api-key"
          autocomplete="off"
          class="h-9 rounded-lg border border-slate-200 px-2.5 text-xs text-slate-800 focus:border-primary/40 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          :disabled="loading || saving"
        />
        <span class="text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('openViking_apiKeyHint') }}</span>
      </label>
    </template>

    <!-- 状态灯:拨测态 + 注册态(诚实回显,不假装绿) -->
    <div class="mt-3 flex flex-wrap items-center gap-2 text-[11px]" data-testid="openviking-status" aria-live="polite">
      <span
        class="inline-flex items-center gap-1 rounded px-2 py-0.5 font-bold"
        :class="statusLight.className"
      >{{ t(statusLight.key) }}</span>
      <span
        class="rounded px-2 py-0.5 font-bold"
        :class="status.registered ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300' : 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400'"
      >{{ t(status.registered ? 'openViking_registered' : 'openViking_notRegistered') }}</span>
      <span
        v-if="status.byo_detected"
        class="rounded bg-amber-50 px-2 py-0.5 font-bold text-amber-700 dark:bg-amber-900/30 dark:text-amber-300"
      >{{ t('openViking_byoHint') }}</span>
      <span v-if="statusError" class="w-full break-words text-amber-600 dark:text-amber-400">{{ statusError }}</span>
    </div>

    <button
      type="button"
      data-testid="openviking-save"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      {{ saving ? t('openViking_saving') : t('openViking_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../../i18n';
import { showErrorDialog } from '../../../composables/useErrorDialog';
import { getOpenVikingStatus, applyOpenVikingNow } from '../../../services/agentApi';

const MODE_OPTIONS = [
  { value: 'managed', labelKey: 'openViking_modeManaged', descKey: 'openViking_modeManagedDesc' },
  { value: 'byo', labelKey: 'openViking_modeByo', descKey: 'openViking_modeByoDesc' },
  { value: 'off', labelKey: 'openViking_modeOff', descKey: 'openViking_modeOffDesc' },
];

export default {
  name: 'OpenVikingSettings',
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
      // off 态下记住最近的非 off 模式:回开不丢 BYO 输入
      lastOnMode: this.formFromConfig(this.config).mode === 'off' ? 'managed' : this.formFromConfig(this.config).mode,
      status: { registered: false, probe_healthy: false, probe_fails: 0, byo_detected: false },
      statusError: ''
    };
  },
  computed: {
    modeOptions() {
      return MODE_OPTIONS;
    },
    statusLight() {
      if (this.form.mode === 'off') {
        return { key: 'openViking_statusOff', className: 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400' };
      }
      if (Number(this.status.probe_fails) >= 3) {
        return { key: 'openViking_statusFailing', className: 'bg-red-50 text-red-600 dark:bg-red-900/30 dark:text-red-300' };
      }
      if (this.status.probe_healthy) {
        return { key: 'openViking_statusHealthy', className: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300' };
      }
      return { key: 'openViking_statusUnknown', className: 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400' };
    }
  },
  watch: {
    // 父页持久化失败走全局弹窗(2026-11-17 定案)
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
        if (this.form.mode !== 'off') {
          this.lastOnMode = this.form.mode;
        }
      }
    }
  },
  mounted() {
    this.loadStatus();
  },
  methods: {
    formFromConfig(cfg) {
      const ov = cfg?.openviking || {};
      // 显式 enabled 才视为开(后端契约默认关);历史数据 mode 缺省按 managed。
      const enabled = ov.enabled === true;
      const rawMode = typeof ov.mode === 'string' && ov.mode.trim() ? ov.mode.trim().toLowerCase() : 'managed';
      const mode = !enabled ? 'off' : (rawMode === 'byo' ? 'byo' : 'managed');
      return {
        mode,
        byoEndpoint: typeof ov.endpoint === 'string' ? ov.endpoint.trim() : '',
        byoApiKey: typeof ov.api_key === 'string' ? ov.api_key.trim() : ''
      };
    },
    async loadStatus() {
      this.statusError = '';
      try {
        const data = await getOpenVikingStatus();
        this.status = {
          registered: !!data?.registered,
          probe_healthy: !!data?.probe_healthy,
          probe_fails: Number(data?.probe_fails) || 0,
          byo_detected: !!data?.byo_detected
        };
      } catch (e) {
        // 状态读失败不弹窗(卡主流程是保存);行内黄字透出真实原因。
        this.statusError = (e instanceof Error && e.message) || String(e || '');
      }
    },
    async save() {
      if (this.form.mode === 'byo' && !this.form.byoApiKey) {
        showErrorDialog(this.t('openViking_byoKeyRequired'));
        return;
      }
      const on = this.form.mode !== 'off';
      const payload = {
        enabled: on,
        // off 保留最近非 off 模式:回开不丢 BYO 输入(前端便利,后端默认关语义不变)
        mode: this.form.mode === 'off' ? this.lastOnMode : this.form.mode,
        endpoint: on && this.form.mode === 'byo' ? this.form.byoEndpoint : '',
        api_key: on && this.form.mode === 'byo' ? this.form.byoApiKey : ''
      };
      this.$emit('save', { ...(this.config || {}), openviking: payload });
      try {
        await applyOpenVikingNow();
        await this.loadStatus();
      } catch (e) {
        // 持久化成功但 apply 失败要红显(runtime 三面可能仍是旧状态)
        showErrorDialog(`${this.t('openViking_applyFailed')}:${(e instanceof Error && e.message) || String(e || '')}`);
      }
    }
  }
};
</script>
