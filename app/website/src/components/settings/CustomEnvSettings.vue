<template>
  <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
    <div class="flex flex-col gap-1">
      <h2 class="text-lg font-bold text-slate-900 dark:text-white">{{ t('customEnv_title') }}</h2>
      <p class="text-xs text-slate-500 dark:text-slate-300">{{ t('customEnv_description') }}</p>
    </div>

    <label class="flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
      <input
        type="checkbox"
        class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="enabled"
        :disabled="loading || saving"
        @change="enabled = $event.target.checked"
      />
      <span>{{ t('customEnv_enable') }}</span>
    </label>

    <p class="rounded-md bg-slate-50 px-3 py-2 text-[11px] leading-relaxed text-slate-500 dark:bg-slate-900/60 dark:text-slate-400">
      {{ t('customEnv_hint') }}
    </p>

    <div class="flex flex-col gap-2">
      <div class="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
        <span>{{ t('customEnv_key') }}</span>
        <span></span>
        <span>{{ t('customEnv_value') }}</span>
        <span></span>
      </div>

      <div
        v-for="(row, index) in rows"
        :key="index"
        class="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2"
      >
        <input
          v-model="row.key"
          type="text"
          :placeholder="t('customEnv_keyPlaceholder')"
          :disabled="loading || saving"
          class="w-full rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs text-slate-800 focus:border-primary focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        <span class="text-slate-400">=</span>
        <input
          v-model="row.value"
          type="text"
          :placeholder="t('customEnv_valuePlaceholder')"
          :disabled="loading || saving"
          class="w-full rounded-md border border-slate-300 bg-white px-2.5 py-1.5 font-mono text-xs text-slate-800 focus:border-primary focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        <button
          type="button"
          class="rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-40 dark:hover:bg-red-900/20"
          :disabled="loading || saving"
          :title="t('customEnv_remove')"
          @click="removeRow(index)"
        >
          ✕
        </button>
      </div>
    </div>

    <button
      type="button"
      class="self-start rounded-md border border-dashed border-slate-300 px-3 py-1.5 text-xs font-medium text-slate-500 hover:border-primary hover:text-primary disabled:opacity-40 dark:border-slate-600 dark:text-slate-300"
      :disabled="loading || saving"
      @click="addRow"
    >
      + {{ t('customEnv_addVar') }}
    </button>

    <div v-if="error" class="text-xs text-red-600 dark:text-red-400">{{ error }}</div>
    <div v-else-if="successMessage" class="text-xs text-emerald-600 dark:text-emerald-400">{{ successMessage }}</div>

    <button
      type="button"
      class="self-end rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white shadow-sm hover:opacity-90 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      {{ saving ? t('customEnv_saving') : t('customEnv_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

function defaultCustomEnv() {
  return { enable: false, vars: {} };
}

function rowsFromVars(vars) {
  const entries = vars && typeof vars === 'object' && !Array.isArray(vars)
    ? Object.entries(vars)
    : [];
  const rows = entries
    .map(([key, value]) => ({ key: String(key ?? ''), value: String(value ?? '') }))
    .filter((row) => row.key || row.value);
  if (rows.length === 0) {
    rows.push({ key: '', value: '' });
  }
  return rows;
}

export default {
  name: 'CustomEnvSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: {
      type: Object,
      default: () => ({})
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
    }
  },
  emits: ['save'],
  data() {
    const env = this.config?.custom_env_vars || defaultCustomEnv();
    return {
      enabled: !!env.enable,
      rows: rowsFromVars(env.vars)
    };
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        const env = nextConfig?.custom_env_vars || defaultCustomEnv();
        this.enabled = !!env.enable;
        this.rows = rowsFromVars(env.vars);
      }
    }
  },
  methods: {
    addRow() {
      this.rows.push({ key: '', value: '' });
    },
    removeRow(index) {
      this.rows.splice(index, 1);
      if (this.rows.length === 0) {
        this.rows.push({ key: '', value: '' });
      }
    },
    save() {
      const vars = {};
      for (const row of this.rows) {
        const key = String(row.key || '').trim();
        if (key) {
          // value preserved verbatim — it may legitimately be empty or contain '='
          vars[key] = String(row.value ?? '');
        }
      }
      const nextConfig = {
        ...(this.config || {}),
        custom_env_vars: {
          enable: !!this.enabled,
          vars
        }
      };
      this.$emit('save', nextConfig);
    }
  }
};
</script>
