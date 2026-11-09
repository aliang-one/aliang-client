<template>
  <!-- mesh 组网卡骨架占位（Task 1 只挂壳；Task 2 填状态条+离线兜底常驻卡，
       Task 3 填 SVG 拓扑画布）。挂载与 patch 链接线在 SettingsPage 已就绪，
       本壳渲染标题与描述，不拉取任何数据。 -->
  <div class="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white p-5 shadow-sm dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-center gap-2">
      <span class="material-symbols-outlined text-primary">hub</span>
      <div>
        <h3 class="font-semibold text-slate-900 dark:text-white">{{ t('mesh_title') }}</h3>
        <p class="text-xs text-slate-500">{{ t('mesh_description') }}</p>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

export default {
  name: 'MeshSettings',
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
  emits: ['save']
};
</script>
