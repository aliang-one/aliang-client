<template>
  <!-- 分区标题：一级分区的标题 + 一句话说明 + 作用域徽章。
       作用域徽章是本次重整的关键表达——同一个概念（模型/effort）在设备级与
       会话级各有一份，用户必须一眼看出「这页改的东西存在哪、换机还在不在」。 -->
  <div class="flex flex-col gap-1.5 border-b border-slate-200 pb-3 dark:border-slate-800">
    <div class="flex flex-wrap items-center gap-2">
      <h2 class="text-lg font-bold tracking-tight text-slate-900 dark:text-white">{{ title }}</h2>
      <span
        v-if="scopeLabel"
        class="rounded-full px-2 py-0.5 text-[10px] font-bold"
        :class="scopeClass"
      >{{ scopeLabel }}</span>
    </div>
    <p v-if="desc" class="text-xs leading-5 text-slate-500 dark:text-slate-400">{{ desc }}</p>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';

export default {
  name: 'SectionHeader',
  props: {
    title: { type: String, required: true },
    desc: { type: String, default: '' },
    // device=本机落盘(换机不同步) / account=账户级 / local=仅本机浏览器 / readonly=只读
    scope: { type: String, default: '' },
  },
  setup() {
    const { t } = useI18n();
    return { t };
  },
  computed: {
    scopeLabel() {
      if (this.scope === 'device') return this.t('settings_scopeDevice');
      if (this.scope === 'local') return this.t('settings_scopeLocal');
      return '';
    },
    scopeClass() {
      if (this.scope === 'readonly') {
        return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400';
      }
      return 'bg-primary/10 text-primary';
    },
  },
};
</script>
