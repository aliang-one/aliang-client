<template>
  <div class="mb-4">
    <!-- 系统提示条（审批取消 / run notice）：不是对话气泡，居中弱化胶囊 -->
    <div
      v-if="msg.kind === 'system'"
      :data-testid="msg.note === 'run_notice' ? 'chat-run-notice' : 'chat-approval-cancelled-note'"
      class="mx-auto w-fit max-w-full rounded-full border border-slate-200 bg-white/80 px-3 py-1 text-center text-xs text-slate-500 dark:border-slate-700 dark:bg-slate-800/80 dark:text-slate-400"
    >{{ msg.content }}</div>
    <template v-else>
      <!-- eyebrow 行：角色名 + 相对时间（title 显绝对时间）+ assistant 状态 -->
      <div
        class="mb-1 flex items-center gap-2 text-[11px] text-slate-400"
        :class="msg.role === 'user' ? 'justify-end' : 'pl-[34px]'"
      >
        <span>{{ msg.role === 'user' ? t('dash_me') : t('dash_ai') }}</span>
        <span
          v-if="msg.ts"
          :title="absoluteTime(msg.ts)"
        >{{ relativeTime(msg.ts) }}</span>
        <span v-if="msg.role === 'assistant' && msg.status === 'running'" class="inline-flex items-center gap-1 text-primary">
          <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
        </span>
        <span v-else-if="msg.role === 'assistant' && msg.status === 'done'" class="text-emerald-500">{{ t('chat_statusDone') }}</span>
        <span v-else-if="msg.role === 'assistant' && msg.status === 'error'" class="font-semibold text-rose-500">!</span>
      </div>
      <!-- user 气泡：品牌绿实底、右对齐 -->
      <div
        v-if="msg.role === 'user'"
        class="ml-auto w-fit max-w-[85%] whitespace-pre-wrap break-words rounded-2xl bg-primary px-4 py-2.5 text-sm text-white shadow-sm"
      >{{ msg.content }}</div>
      <!-- assistant：头像 + 内容卡 -->
      <div v-else class="flex gap-2.5">
        <div class="flex size-6 shrink-0 items-center justify-center rounded-full bg-gradient-to-br from-primary to-emerald-500 text-white">
          <span class="material-symbols-outlined text-[12px] leading-none">auto_awesome</span>
        </div>
        <div
          class="min-w-0 max-w-[85%] rounded-2xl border px-4 py-2.5 text-sm shadow-sm"
          :class="msg.status === 'error'
            ? 'border-rose-200 bg-rose-50 text-rose-600 dark:border-rose-800 dark:bg-rose-900/30 dark:text-rose-300'
            : 'border-slate-200 bg-white text-slate-700 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100'"
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
          <template v-else-if="msg.role === 'assistant' && msg.status !== 'error'">
            <ChatMarkdown :content="msg.content" />
            <!-- 流式光标：running 且已有正文时缀在内容尾部闪烁
                 （reduced-motion 下静态常亮不动画）。 -->
            <span
              v-if="msg.status === 'running'"
              class="chat-caret"
              aria-hidden="true"
            ></span>
          </template>
          <template v-else>{{ msg.content }}</template>
        </div>
      </div>
    </template>
  </div>
</template>

<script setup>
import { useI18n } from '../../i18n';
import ChatMarkdown from './ChatMarkdown.vue';

// 纯渲染：单条消息块（系统胶囊 / eyebrow 行 / 气泡）。nowTick 驱动相对时间
// 随 30s 心跳重算（不做本地定时器，复用列表已有的时钟节拍）。
const props = defineProps({
  msg: { type: Object, required: true },
  nowTick: { type: Number, default: 0 },
});

const { t } = useI18n();

// 相对时间：<60s「刚刚」/<60m「X 分钟前」/<24h「X 小时前」/否则「M-D HH:mm」。
// 绝对时间走 toLocaleString 挂 hover title（不加 i18n 键——数值格式无文案）。
function relativeTime(ts) {
  const ms = Date.parse(ts);
  if (!Number.isFinite(ms)) return '';
  const diff = Math.max(0, (props.nowTick || Date.now()) - ms);
  if (diff < 60 * 1000) return t('chat_justNow');
  if (diff < 60 * 60 * 1000) {
    return t('chat_minutesAgo', { minutes: Math.floor(diff / 60000) });
  }
  if (diff < 24 * 60 * 60 * 1000) {
    return t('chat_hoursAgo', { hours: Math.floor(diff / 3600000) });
  }
  const d = new Date(ms);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return `${d.getMonth() + 1}-${d.getDate()} ${hh}:${mm}`;
}

function absoluteTime(ts) {
  const ms = Date.parse(ts);
  if (!Number.isFinite(ms)) return '';
  return new Date(ms).toLocaleString();
}
</script>

<!-- 思考点/流式光标样式随气泡下沉（scoped 样式只作用于本组件模板内的元素，
     与拆分前 ChatPage 作用于同一批节点等价）。 -->
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

/* 流式光标：内联块，缀在正文后（markdown 块级渲染后单独成行，作「输出中」
   指示）。prefers-reduced-motion 下不闪烁、静态常亮。 */
.chat-caret {
  display: inline-block;
  width: 0.5rem;
  height: 1rem;
  margin-top: 0.25rem;
  vertical-align: middle;
  border-radius: 2px;
  background-color: rgb(var(--color-primary-rgb) / 1);
  animation: chat-caret-blink 1s steps(1) infinite;
}

@media (prefers-reduced-motion: reduce) {
  .chat-caret {
    animation: none;
    opacity: 1;
  }
}

@keyframes chat-caret-blink {
  0%, 100% { opacity: 1; }
  50% { opacity: 0; }
}
</style>
