<template>
  <button type="button" data-testid="chat-operation-entry" aria-haspopup="dialog" :aria-expanded="open"
    class="inline-flex min-h-11 min-w-11 items-center justify-center gap-1 rounded-md px-2 text-sm text-slate-600 hover:bg-slate-100 focus-visible:ring-2 focus-visible:ring-primary dark:text-slate-300 dark:hover:bg-slate-800"
    :aria-label="t('operation_title')" @click="$emit('open', $event.currentTarget)">
    <span class="material-symbols-outlined text-lg" aria-hidden="true">pending_actions</span>
    <span v-if="count" class="font-semibold tabular-nums text-primary">{{ count }}</span>
  </button>
</template>
<script setup>
import { useI18n } from '../../i18n';
defineProps({ count: Number, open: Boolean });
defineEmits(['open']);
const { t } = useI18n();
</script>
