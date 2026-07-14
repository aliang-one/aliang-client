<template>
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">folder_managed</span>
      </div>
      <div class="min-w-0">
        <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_scan_title') }}</h3>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
          {{ t('agent_scan_desc') }}
        </p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-xs font-semibold text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span>{{ t('agent_scan_enable') }}</span>
      <input v-model="enabled" type="checkbox" class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20" />
    </label>

    <div v-if="enabled" class="mt-3 space-y-2">
      <div v-for="(_, i) in directories" :key="i" class="flex items-center gap-2">
        <input
          v-model="directories[i]"
          type="text"
          class="h-10 min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-700 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200"
          :placeholder="t('agent_scan_dirPlaceholder')"
        />
        <button
          type="button"
          class="inline-flex h-11 w-11 shrink-0 items-center justify-center rounded-lg text-slate-400 transition-colors hover:bg-rose-50 hover:text-rose-600 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-200 dark:hover:bg-rose-500/10"
          :aria-label="t('agent_scan_removeDir')"
          @click="directories.splice(i, 1)"
        >
          <span class="material-symbols-outlined text-base">close</span>
        </button>
      </div>
      <button
        type="button"
        class="inline-flex min-h-11 items-center gap-1 rounded-lg border border-dashed border-slate-300 px-3 text-xs font-semibold text-slate-500 transition-colors hover:border-primary hover:text-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700"
        @click="directories.push('')"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        {{ t('agent_scan_addDir') }}
      </button>
    </div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center justify-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
      :disabled="saving"
      @click="save"
    >
      <span class="material-symbols-outlined text-base">save</span>
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
