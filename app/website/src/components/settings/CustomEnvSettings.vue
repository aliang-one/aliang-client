<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">data_object</span>
      </div>
      <div class="min-w-0">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('customEnv_title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('customEnv_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span>
        <span class="block text-xs font-semibold">{{ t('customEnv_enable') }}</span>
        <span class="mt-0.5 block text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('customEnv_hint') }}</span>
      </span>
      <input
        type="checkbox"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="enabled"
        :disabled="loading || saving"
        @change="enabled = $event.target.checked"
      />
    </label>

    <div class="mt-4 flex flex-col gap-3">
      <div
        v-for="(row, index) in rows"
        :key="index"
        class="flex items-start gap-2"
      >
        <div class="grid min-w-0 flex-1 grid-cols-1 gap-2 sm:grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] sm:items-end">
          <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
            <span>{{ t('customEnv_key') }}</span>
            <input
              v-model="row.key"
              type="text"
              :placeholder="t('customEnv_keyPlaceholder')"
              :disabled="loading || saving"
              class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            />
          </label>
          <span class="hidden h-10 items-center text-slate-400 sm:flex">=</span>
          <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
            <span>{{ t('customEnv_value') }}</span>
            <input
              v-model="row.value"
              type="text"
              :placeholder="t('customEnv_valuePlaceholder')"
              :disabled="loading || saving"
              class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
            />
          </label>
        </div>
        <button
          type="button"
          class="mt-5 flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-400 transition-colors hover:bg-red-50 hover:text-red-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-red-200 disabled:opacity-40 dark:hover:bg-red-900/20"
          :disabled="loading || saving"
          :aria-label="t('customEnv_remove')"
          @click="removeRow(index)"
        >
          <span class="material-symbols-outlined text-base">close</span>
        </button>
      </div>
    </div>

    <button
      type="button"
      class="mt-3 inline-flex min-h-11 self-start items-center gap-1 rounded-lg border border-dashed border-slate-300 px-3 text-xs font-medium text-slate-500 transition-colors hover:border-primary hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-40 dark:border-slate-600 dark:text-slate-300"
      :disabled="loading || saving"
      @click="addRow"
    >
      <span class="material-symbols-outlined text-base">add</span>
      {{ t('customEnv_addVar') }}
    </button>

    <div v-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      <span class="material-symbols-outlined text-base">save</span>
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
