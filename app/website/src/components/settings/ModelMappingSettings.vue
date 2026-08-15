<template>
  <div class="flex flex-col gap-3 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
    <div class="flex flex-col gap-1">
      <h2 class="text-lg font-bold text-slate-900 dark:text-white">{{ t('modelMapping_title') }}</h2>
      <p class="text-xs text-slate-500 dark:text-slate-300">{{ t('modelMapping_description') }}</p>
    </div>

    <label class="flex cursor-pointer items-center gap-2 text-sm text-slate-700 dark:text-slate-200">
      <input
        type="checkbox"
        class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="enabled"
        :disabled="loading || saving"
        @change="enabled = $event.target.checked"
      />
      <span>{{ t('modelMapping_enable') }}</span>
    </label>

    <p class="rounded-md bg-slate-50 px-3 py-2 text-[11px] leading-relaxed text-slate-500 dark:bg-slate-900/60 dark:text-slate-400">
      {{ t('modelMapping_hint') }}
    </p>

    <div class="flex flex-col gap-2">
      <div class="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
        <span>{{ t('modelMapping_original') }}</span>
        <span></span>
        <span>{{ t('modelMapping_replacement') }}</span>
        <span></span>
      </div>

      <div
        v-for="(row, index) in rows"
        :key="index"
        class="grid grid-cols-[1fr_auto_1fr_auto] items-center gap-2"
      >
        <input
          v-model="row.from"
          type="text"
          :placeholder="t('modelMapping_originalPlaceholder')"
          :disabled="loading || saving"
          class="w-full rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-800 focus:border-primary focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        <span class="text-slate-400">→</span>
        <input
          v-model="row.to"
          type="text"
          :placeholder="t('modelMapping_replacementPlaceholder')"
          :disabled="loading || saving"
          class="w-full rounded-md border border-slate-300 bg-white px-2.5 py-1.5 text-sm text-slate-800 focus:border-primary focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        <button
          type="button"
          class="rounded-md px-2 py-1 text-xs text-slate-400 hover:bg-red-50 hover:text-red-600 disabled:opacity-40 dark:hover:bg-red-900/20"
          :disabled="loading || saving"
          :title="t('modelMapping_remove')"
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
      + {{ t('modelMapping_addRule') }}
    </button>

    <div v-if="error" class="text-xs text-red-600 dark:text-red-400">{{ error }}</div>
    <div v-else-if="successMessage" class="text-xs text-emerald-600 dark:text-emerald-400">{{ successMessage }}</div>

    <button
      type="button"
      class="self-end rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white shadow-sm hover:opacity-90 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      {{ saving ? t('modelMapping_saving') : t('modelMapping_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

function defaultModelMapping() {
  return { enable: false, rules: {} };
}

function rowsFromRules(rules) {
  const entries = rules && typeof rules === 'object' && !Array.isArray(rules)
    ? Object.entries(rules)
    : [];
  const rows = entries
    .map(([from, to]) => ({ from: String(from ?? ''), to: String(to ?? '') }))
    .filter((row) => row.from || row.to);
  if (rows.length === 0) {
    rows.push({ from: '', to: '' });
  }
  return rows;
}

export default {
  name: 'ModelMappingSettings',
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
    const mapping = this.config?.model_mapping || defaultModelMapping();
    return {
      enabled: !!mapping.enable,
      rows: rowsFromRules(mapping.rules)
    };
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        const mapping = nextConfig?.model_mapping || defaultModelMapping();
        this.enabled = !!mapping.enable;
        this.rows = rowsFromRules(mapping.rules);
      }
    }
  },
  methods: {
    addRow() {
      this.rows.push({ from: '', to: '' });
    },
    removeRow(index) {
      this.rows.splice(index, 1);
      if (this.rows.length === 0) {
        this.rows.push({ from: '', to: '' });
      }
    },
    save() {
      const rules = {};
      for (const row of this.rows) {
        const from = String(row.from || '').trim();
        const to = String(row.to || '').trim();
        if (from && to) {
          rules[from] = to;
        }
      }
      const nextConfig = {
        ...(this.config || {}),
        model_mapping: {
          enable: !!this.enabled,
          rules
        }
      };
      this.$emit('save', nextConfig);
    }
  }
};
</script>
