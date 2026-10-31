<template>
  <!-- 撤回编辑态 banner（Task 13）：amber 系（比 HINT_BAR_CLASS 同族的灰更醒目）
       常驻输入区上方，承载「截断警告 + 取消出口」。文案经 i18n 渲染（随语言
       切换），取消事件经 ChatComposer 透传回 useAliangChat.cancelRecall。 -->
  <div
    data-testid="chat-recall-banner"
    class="mb-2 flex items-center justify-between gap-2 rounded-lg border border-amber-300 bg-amber-50 px-3 py-1.5 text-xs text-amber-700 dark:border-amber-700 dark:bg-amber-950/40 dark:text-amber-300"
  >
    <span class="min-w-0 truncate">{{ t('chat_recallBanner') }}</span>
    <button
      type="button"
      data-testid="chat-recall-cancel"
      class="min-h-[40px] shrink-0 rounded-md px-2 font-semibold text-amber-700 transition hover:bg-amber-100 md:min-h-0 dark:text-amber-300 dark:hover:bg-amber-900/50"
      @click="emit('cancel')"
    >{{ t('chat_recallCancel') }}</button>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';

// 纯渲染：撤回编辑态的截断警告条。状态（recallActive）由 ChatComposer 以
// v-if 决定挂载，本组件不持有任何状态。
const emit = defineEmits(['cancel']);
const { t } = useI18n();
</script>
