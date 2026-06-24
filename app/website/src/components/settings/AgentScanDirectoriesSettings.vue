<template>
  <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <h3 class="flex items-center gap-2 font-bold">
      <span class="material-symbols-outlined text-primary">folder_managed</span>
      {{ t('agent_scan_title') }}
    </h3>
    <p class="mt-1 text-[11px] leading-5 text-slate-500 dark:text-slate-400">
      {{ t('agent_scan_desc') }}
    </p>

    <label class="mt-4 flex items-center gap-2 text-xs font-semibold text-slate-700 dark:text-slate-200">
      <input type="checkbox" class="h-4 w-4 rounded border-slate-300" v-model="enabled" />
      {{ t('agent_scan_enable') }}
    </label>

    <div v-if="enabled" class="mt-3 space-y-2">
      <div v-for="(_, i) in directories" :key="i" class="flex items-center gap-2">
        <input
          v-model="directories[i]"
          type="text"
          class="min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-2.5 py-1.5 text-[11px] text-slate-700 focus:border-primary focus:outline-none dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
          :placeholder="t('agent_scan_dirPlaceholder')"
        />
        <button
          type="button"
          class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-lg text-slate-400 transition hover:bg-rose-50 hover:text-rose-600 dark:hover:bg-rose-500/10"
          @click="directories.splice(i, 1)"
        >
          <span class="material-symbols-outlined text-base">close</span>
        </button>
      </div>
      <button
        type="button"
        class="inline-flex items-center gap-1 rounded-lg border border-dashed border-slate-300 px-2.5 py-1.5 text-[11px] font-semibold text-slate-500 transition hover:border-primary hover:text-primary dark:border-slate-700"
        @click="directories.push('')"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        {{ t('agent_scan_addDir') }}
      </button>
    </div>

    <button
      type="button"
      class="mt-4 inline-flex h-9 items-center justify-center rounded-lg bg-primary px-4 text-[11px] font-bold text-white transition hover:bg-primary/90 disabled:opacity-60"
      :disabled="saving"
      @click="save"
    >
      {{ t('agent_scan_save') }}
    </button>
    <p v-if="feedback" class="mt-2 text-[11px]" :class="feedbackType === 'error' ? 'text-rose-600 dark:text-rose-300' : 'text-emerald-600 dark:text-emerald-300'">
      {{ feedback }}
    </p>
  </div>
</template>

<script>
import { getAgentScanDirectories, setAgentScanDirectories } from '../../services/agentApi';
import { useI18n } from '../../i18n';

export default {
  name: 'AgentScanDirectoriesSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      enabled: false,
      directories: [],
      saving: false,
      feedback: '',
      feedbackType: 'info',
    };
  },
  mounted() {
    this.load();
  },
  methods: {
    applyData(data) {
      this.enabled = Boolean(data && data.enabled);
      const dirs = data && Array.isArray(data.directories) ? data.directories : [];
      this.directories = dirs.slice();
    },
    async load() {
      try {
        this.applyData(await getAgentScanDirectories());
      } catch (_) {
        // 加载失败静默，不阻塞页面
      }
    },
    async save() {
      this.saving = true;
      this.feedback = '';
      try {
        const clean = this.directories.map((d) => (typeof d === 'string' ? d.trim() : '')).filter((d) => d !== '');
        const data = await setAgentScanDirectories({ enabled: this.enabled, directories: clean });
        this.applyData(data);
        this.feedback = this.t('agent_scan_saved');
        this.feedbackType = 'info';
      } catch (_) {
        this.feedback = this.t('agent_scan_saveFailed');
        this.feedbackType = 'error';
      } finally {
        this.saving = false;
      }
    },
  },
};
</script>
