<template>
  <div class="mb-4">
    <!-- 系统提示条（审批取消 / run notice）：不是对话气泡，居中弱化胶囊 -->
    <div
      v-if="msg.kind === 'system'"
      :data-testid="systemNoteTestId(msg.note)"
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
      <!-- user 气泡：品牌绿实底、右对齐。group 包装层承载 hover 操作条
           （Task 13）：复制 + 撤回重发（撤回键需 canRecall 命中——user 角色 +
           有 messageId + provider=agentcore——且非设备壳会话，见撤回键处
           注释；存量旧消息/壳会话只有复制）。 -->
      <div v-if="msg.role === 'user'" class="group relative ml-auto w-fit max-w-[85%]">
        <!-- 图片附件缩略图条（spec §3.6）：兄弟 DOM 元素渲染，绝不进 markdown
             管线——user 气泡纯文本插值铁律不破；纯图消息（content 空）只渲染
             缩略图条。 -->
        <ChatAttachmentStrip
          v-if="msg.attachments && msg.attachments.length"
          :attachments="msg.attachments"
          class="mb-1 justify-end"
        />
        <div v-if="msg.content" class="whitespace-pre-wrap break-words rounded-2xl bg-primary px-4 py-2.5 text-sm text-white shadow-sm">{{ msg.content }}</div>
        <div
          class="absolute -top-3 right-0 z-10 flex items-center gap-0.5 rounded-lg border border-slate-200 bg-white px-1 py-0.5 opacity-100 shadow-sm transition-opacity md:opacity-0 md:group-hover:opacity-100 dark:border-slate-700 dark:bg-slate-800"
        >
          <button
            type="button"
            data-testid="chat-copy-button"
            :title="t('chat_copyMessage')"
            :aria-label="t('chat_copyMessage')"
            class="flex min-h-[40px] items-center justify-center rounded-md px-1.5 text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 md:min-h-0 dark:hover:bg-slate-700 dark:hover:text-slate-200"
            @click="copyMessage"
          >
            <span class="material-symbols-outlined text-[16px] leading-none">content_copy</span>
          </button>
          <!-- 撤回键门控（spec §6 壳禁用决策）：canRecall 纯函数语义不动（user +
               messageId + provider=agentcore），设备壳会话在此渲染层整体收掉——
               壳续聊 journal 键分叉（local-* vs 原 entry.id），锚点 no-op 致上下文
               静默分叉，待服务端跨 journal 解析后解锁。recallEnabled 由
               ChatMessageList 按 !conversation.deviceSession 下传。 -->
          <button
            v-if="canRecall(msg) && recallEnabled"
            type="button"
            data-testid="chat-recall-button"
            :title="t('chat_recallButton')"
            :aria-label="t('chat_recallButton')"
            class="flex min-h-[40px] items-center justify-center rounded-md px-1.5 text-slate-400 transition hover:bg-slate-100 hover:text-primary md:min-h-0 dark:hover:bg-slate-700"
            @click="emit('recall', msg)"
          >
            <span class="material-symbols-outlined text-[16px] leading-none">restart_alt</span>
          </button>
        </div>
      </div>
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
            v-if="msg.role === 'assistant' && msg.status === 'running' && !msg.content && !msg.thinking"
            class="ai-thinking"
          >
            <span class="thinking-dot"></span><span class="thinking-dot"></span><span class="thinking-dot"></span>
            <span class="ml-1 align-middle">{{ t('chat_aiThinking') }}</span>
          </span>
          <!-- spec §E:仅 assistant 正常气泡走 markdown(消毒管线);user 与
               error 气泡铁律保持纯文本插值防注入,markdown 渲染不递给它们。
               hiddenOptionIds（Task 14）:该会话在挂交互卡的 option_id 集合,
               透传给 markdown 的静态围栏卡做防双份隐藏比对。 -->
          <template v-else-if="msg.role === 'assistant' && msg.status !== 'error'">
            <!-- 思考过程(2026-11-05):agentcore 推理走 ai.thinking,此前
                 整段被 default 丢弃,思考阶段仪表盘全程隐形。running 时
                 默认展开随流滚动,结束收起为可展开摘要。纯文本插值防注入。 -->
            <details
              v-if="msg.thinking"
              class="mb-1.5 rounded-lg border border-slate-200 bg-slate-50 px-2.5 py-1.5 text-xs dark:border-slate-700 dark:bg-slate-800/60"
              :open="msg.status === 'running'"
            >
              <summary class="cursor-pointer select-none font-medium text-slate-400 dark:text-slate-500">
                <span
                  v-if="msg.status === 'running'"
                  class="mr-1 inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary align-middle"
                ></span>{{ msg.status === 'running' ? t('chat_thinkingRunning') : t('chat_thinkingDone') }}
              </summary>
              <div class="mt-1 max-h-40 overflow-y-auto whitespace-pre-wrap leading-relaxed text-slate-400 dark:text-slate-500">{{ msg.thinking }}</div>
            </details>
            <ChatMarkdown :content="msg.content" :hidden-option-ids="hiddenOptionIds" />
            <!-- 图片附件缩略图条（assistant 侧预留，任何角色带图均渲染）。 -->
            <ChatAttachmentStrip
              v-if="msg.attachments && msg.attachments.length"
              :attachments="msg.attachments"
              class="mt-1.5"
            />
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
import { canRecall } from '../../utils/chatRecall';
import ChatMarkdown from './ChatMarkdown.vue';
import ChatAttachmentStrip from './ChatAttachmentStrip.vue';

// 纯渲染：单条消息块（系统胶囊 / eyebrow 行 / 气泡）。nowTick 驱动相对时间
// 随 30s 心跳重算（不做本地定时器，复用列表已有的时钟节拍）。user 气泡的
// hover 操作条只发事件（recall），编辑态状态机与发送链都在 useAliangChat。
const props = defineProps({
  msg: { type: Object, required: true },
  nowTick: { type: Number, default: 0 },
  // 撤回入口可用门：设备壳会话（spec §6）整体禁用，由 ChatMessageList 计算下传。
  recallEnabled: { type: Boolean, default: true },
  // 在挂交互卡的 option_id 集合（Task 14 防双份），透传给 ChatMarkdown。
  hiddenOptionIds: { type: Array, default: () => [] },
});

const emit = defineEmits(['recall']);

const { t } = useI18n();

// 系统条 testid 按 note 种类分流（Task 14 新增 option_cancelled；未知种类
// 退审批取消条形态，与既有 run_notice/approval_cancelled 同族）。
// turn_failed（G5 错误终态）：journal end(error) 折出的失败说明条。
function systemNoteTestId(note) {
  if (note === 'run_notice') return 'chat-run-notice';
  if (note === 'option_cancelled') return 'chat-option-cancelled-note';
  if (note === 'approval_not_found') return 'chat-approval-not-found-note';
  if (note === 'turn_failed') return 'chat-turn-failed-note';
  return 'chat-approval-cancelled-note';
}

// 复制 user 气泡原文（assistant 正文已有 ChatMarkdown 的复制入口）。剪贴板
// 不可用（非安全上下文等）静默失败，与 chat_md 的复制行为同口径。
function copyMessage() {
  const text = props.msg.content || '';
  if (text && navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).catch(() => {});
  }
}

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
