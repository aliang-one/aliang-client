<template>
  <!-- 界面偏好：语言 + 外观。从 SystemSettings 抽出（2026-10-31 设置 IA 重整）。
       这两项只落 localStorage、不上报后端、换设备不同步，与「设备与网络」
       那类真正影响出网的设置是两回事，不该混在同一张卡里。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
        <span class="material-symbols-outlined">palette</span>
      </div>
      <div class="min-w-0">
        <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('appearance_title') }}</h2>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('appearance_description') }}</p>
      </div>
    </div>

    <div class="mt-5 flex flex-col gap-4">
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <p class="text-sm font-semibold">{{ t('sys_language') }}</p>
          <p class="text-[10px] text-slate-500">{{ t('sys_languageDesc') }}</p>
        </div>
        <div class="flex rounded bg-slate-100 p-1 dark:bg-slate-800">
          <button
            v-for="lang in languages"
            :key="lang.value"
            type="button"
            :class="[
              'min-h-[32px] rounded px-3 py-2 text-[10px] font-bold transition md:min-h-0 md:py-1',
              locale === lang.value
                ? 'bg-primary text-white shadow-sm'
                : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70'
            ]"
            @click="setLocale(lang.value)"
          >
            {{ lang.label }}
          </button>
        </div>
      </div>

      <div class="flex flex-col gap-3 border-t border-slate-100 pt-4 sm:flex-row sm:items-center sm:justify-between sm:gap-4 dark:border-slate-800">
        <div>
          <p class="text-sm font-semibold">{{ t('sys_theme') }}</p>
          <p class="text-[10px] text-slate-500">{{ t('sys_themeDesc') }}</p>
          <p class="mt-1 text-[10px] text-slate-400 dark:text-slate-500">{{ themeStatusText }}</p>
        </div>
        <div class="flex rounded bg-slate-100 p-1 dark:bg-slate-800">
          <button
            v-for="option in themeOptions"
            :key="option.value"
            type="button"
            :class="[
              'min-h-[32px] rounded px-3 py-2 text-[10px] font-bold transition md:min-h-0 md:py-1',
              themeButtonClass(option.value)
            ]"
            @click="applyThemeMode(option.value)"
          >
            {{ t(option.labelKey) }}
          </button>
        </div>
      </div>
    </div>

    <p class="mt-4 text-[11px] leading-4 text-slate-400 dark:text-slate-500">{{ t('appearance_localOnlyHint') }}</p>
  </div>
</template>

<script>
import { useTheme } from '../../composables/useTheme';
import { useI18n } from '../../i18n';

export default {
  name: 'AppearanceSettings',
  setup() {
    const { locale, t, setLocale } = useI18n();
    const { themeMode, resolvedTheme, setThemeMode } = useTheme();
    return {
      locale,
      t,
      setLocale,
      themeMode,
      resolvedTheme,
      setThemeMode,
      languages: [
        { value: 'en', label: 'English' },
        { value: 'zh', label: '中文' },
      ],
      themeOptions: [
        { value: 'system', labelKey: 'sys_themeSystem' },
        { value: 'light', labelKey: 'sys_themeLight' },
        { value: 'dark', labelKey: 'sys_themeDark' },
      ],
    };
  },
  computed: {
    resolvedThemeLabel() {
      return this.resolvedTheme === 'dark'
        ? this.t('sys_themeDark')
        : this.t('sys_themeLight');
    },
    themeStatusText() {
      if (this.themeMode === 'system') {
        return this.t('sys_themeFollowingSystemState', { mode: this.resolvedThemeLabel });
      }
      return this.t('sys_themeManualState', { mode: this.resolvedThemeLabel });
    },
  },
  methods: {
    applyThemeMode(mode) {
      this.setThemeMode(mode);
    },
    themeButtonClass(mode) {
      return this.themeMode === mode
        ? 'bg-primary text-white shadow-sm'
        : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70';
    },
  },
};
</script>
