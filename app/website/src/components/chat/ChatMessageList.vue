<template>
  <!-- Messages -->
  <div :ref="setContainer" class="flex-1 overflow-y-auto bg-slate-50 p-4 dark:bg-slate-800/40">
    <div v-if="!activeId" class="flex h-full items-center justify-center">
      <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
    </div>
    <template v-else>
      <div
        v-if="conversation?.deviceLoadError"
        data-testid="device-session-load-error"
        class="mb-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-600 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-300"
      >
        <!-- deviceLoadError 只存布尔标记，文案在渲染时走 i18n（随语言切换） -->
        {{ t('chat_deviceSessionLoadFailed') }}
      </div>
      <div v-if="activeMessages.length === 0" class="flex h-full items-center justify-center">
        <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
      </div>
      <ChatMessageBubble
        v-for="(msg, idx) in activeMessages"
        :key="idx"
        :msg="msg"
        :now-tick="nowTick"
      />
      <!-- 插话轻量条（Task 4）：运行中回合的旁路注入，不插独立 user 气泡——
           快照/详情装载的转录才是权威流，避免回灌重复渲染。 -->
      <div
        v-for="item in steerItems"
        :key="item.messageId"
        data-testid="chat-steer-item"
        class="mb-3 ml-auto max-w-[85%] rounded-lg border border-primary/30 bg-primary/5 px-3 py-2 text-xs"
      >
        <div class="flex flex-wrap items-center gap-2">
          <span class="material-symbols-outlined text-sm text-primary">bolt</span>
          <span class="font-semibold text-slate-600 dark:text-slate-200">
            {{ t('chat_steerLabel') }}
          </span>
          <span
            data-testid="chat-steer-status"
            :class="item.status === 'failed' ? 'text-rose-500' : 'font-medium text-primary'"
          >{{ steerStatusLabel(item) }}</span>
        </div>
        <p class="mt-1 whitespace-pre-wrap break-all text-slate-600 dark:text-slate-300">{{ item.content }}</p>
        <p v-if="item.status === 'failed' && item.detail" class="mt-1 text-rose-500">{{ item.detail }}</p>
      </div>
      <ChatApprovalCard
        v-for="approval in approvals"
        :key="approval.key"
        :approval="approval"
        :now-tick="nowTick"
        @respond="(decision) => emit('approval-respond', approval, decision)"
      />
    </template>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';
import ChatMessageBubble from './ChatMessageBubble.vue';
import ChatApprovalCard from './ChatApprovalCard.vue';

const props = defineProps({
  conversation: { type: Object, default: null },
  activeId: { type: String, default: null },
  activeMessages: { type: Array, default: () => [] },
  steerItems: { type: Array, default: () => [] },
  // 已过滤的活跃审批（pending/responding），过滤逻辑在 useAliangChat。
  approvals: { type: Array, default: () => [] },
  nowTick: { type: Number, default: 0 },
  // 滚动容器元素交接：scrollToBottom 由 useAliangChat 在流式增量/转录装载/
  // 发送等时点驱动（与拆分前 ref="messagesContainer" 的字符串 ref 语义等价）。
  setContainer: { type: Function, required: true },
});

const emit = defineEmits(['approval-respond']);

const { t } = useI18n();

function steerStatusLabel(item) {
  switch (item?.status) {
    case 'sent':
      return t('chat_steerSent');
    case 'queued':
      return t('chat_steerQueued');
    case 'applied':
      return t('chat_steerApplied');
    case 'not_running':
      return t('chat_steerNotRunning');
    case 'unknown':
      // 未知 result（含未来新增值如 held）：中性渲染，不误标失败（评审折叠项）
      return t('chat_steerUnknown', { result: item?.result || t('chat_steerUnknownResult') });
    default:
      return t('chat_steerFailed');
  }
}
</script>
