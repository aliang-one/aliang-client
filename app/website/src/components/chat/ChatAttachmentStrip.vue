<template>
  <!-- 消息图片附件缩略图条（spec 2026-11-05-chat-image-pipeline §3.6）：
       ref → 本机 agent 回环端点 URL（useAliangChat.chatAttachmentUrl 已在消息
       归一化时组装为 a.url）。点击新窗看原图（noopener）；h-24 保证 ≥40px
       触控目标；横向滚动兜底多图溢出。 -->
  <div class="flex flex-wrap gap-2 py-1">
    <button
      v-for="(attachment, index) in attachments"
      :key="(attachment.url || '') + '-' + index"
      type="button"
      class="overflow-hidden rounded-xl border border-slate-200 bg-white transition hover:border-primary/60 focus:outline-none focus:ring-2 focus:ring-primary/40 dark:border-slate-700 dark:bg-slate-800"
      :aria-label="attachment.name || t('chat_attachOpen')"
      :title="attachment.name || t('chat_attachOpen')"
      @click="openImage(attachment.url)"
    >
      <img
        :src="attachment.url"
        :alt="attachment.name || 'image'"
        loading="lazy"
        class="h-24 w-auto max-w-[180px] object-cover"
      />
    </button>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';

defineProps({
  attachments: { type: Array, default: () => [] },
});

const { t } = useI18n();

function openImage(url) {
  if (url) window.open(url, '_blank', 'noopener');
}
</script>
