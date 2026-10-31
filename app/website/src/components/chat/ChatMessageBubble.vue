<template>
  <div class="mb-3">
    <!-- 系统提示条（审批取消 / run notice）：不是对话气泡，通栏弱化样式 -->
    <div
      v-if="msg.kind === 'system'"
      :data-testid="msg.note === 'run_notice' ? 'chat-run-notice' : 'chat-approval-cancelled-note'"
      class="rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs text-slate-500 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
    >{{ msg.content }}</div>
    <template v-else>
      <div class="mb-1 flex items-center gap-2 text-xs text-slate-400">
        <span>{{ msg.role === 'user' ? t('dash_me') : t('dash_ai') }}</span>
        <span v-if="msg.role === 'assistant' && msg.status === 'running'" class="inline-flex items-center gap-1 text-primary">
          <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
        </span>
        <span v-else-if="msg.role === 'assistant' && msg.status === 'done'" class="text-emerald-500">{{ t('chat_statusDone') }}</span>
        <span v-else-if="msg.role === 'assistant' && msg.status === 'error'" class="text-rose-500">!</span>
      </div>
      <div
        class="inline-block max-w-[85%] whitespace-pre-wrap rounded-lg px-3 py-2 text-sm"
        :class="msg.role === 'user'
          ? 'ml-auto block bg-primary text-white'
          : msg.status === 'error'
            ? 'bg-rose-50 text-rose-600 dark:bg-rose-900/30 dark:text-rose-300'
            : 'bg-white text-slate-700 dark:bg-slate-700 dark:text-slate-100'"
      >
        <span
          v-if="msg.role === 'assistant' && msg.status === 'running' && !msg.content"
          class="ai-thinking"
        >
          <span class="thinking-dot"></span><span class="thinking-dot"></span><span class="thinking-dot"></span>
          <span class="ml-1 align-middle">{{ t('chat_aiThinking') }}</span>
        </span>
        <!-- spec §E:仅 assistant 正常气泡走 markdown(消毒管线);user 与
             error 气泡铁律保持纯文本插值防注入,markdown 渲染不递给它们。 -->
        <ChatMarkdown v-else-if="msg.role === 'assistant' && msg.status !== 'error'" :content="msg.content" />
        <template v-else>{{ msg.content }}</template>
      </div>
    </template>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';
import ChatMarkdown from './ChatMarkdown.vue';

// 纯渲染：单条消息块（系统条 / 角色行 / 气泡）。nowTick 目前未被本组件消费
// （markdown 渲染不依赖时钟;该 prop 供后续消息级相对时间显示用），传入不产生任何行为。
defineProps({
  msg: { type: Object, required: true },
  nowTick: { type: Number, default: 0 },
});

const { t } = useI18n();
</script>

<!-- 思考点样式随气泡下沉（scoped 样式只作用于本组件模板内的元素，与拆分前
     ChatPage 作用于同一批节点等价）。 -->
<style scoped>
.ai-thinking {
  display: inline-flex;
  align-items: center;
}

.thinking-dot {
  display: inline-block;
  width: 5px;
  height: 5px;
  margin: 0 1px;
  border-radius: 9999px;
  background-color: rgb(148 163 184);
  animation: thinking-bounce 1.2s infinite ease-in-out both;
}

.thinking-dot:nth-child(2) { animation-delay: 0.15s; }
.thinking-dot:nth-child(3) { animation-delay: 0.3s; }

@keyframes thinking-bounce {
  0%, 80%, 100% { transform: scale(0.6); opacity: 0.4; }
  40% { transform: scale(1); opacity: 1; }
}
</style>
