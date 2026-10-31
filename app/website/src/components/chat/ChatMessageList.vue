<template>
  <!-- Messages：外层 relative 壳承载「回到底部」浮钮（absolute 只相对可视区，
       不随内容滚走），内层才是滚动容器。@scroll 几何判定贴底状态经
       setNearBottom 反向同步给 useAliangChat（自动滚动的守卫源）。 -->
  <div class="relative min-h-0 flex-1">
    <div
      :ref="setContainer"
      class="h-full overflow-y-auto bg-slate-50 dark:bg-slate-800/40"
      @scroll.passive="onScroll"
    >
      <div class="mx-auto w-full max-w-3xl px-4 py-6">
        <!-- deviceLoadError 独立于空态渲染（首页失败 =「空转录 + 错误条」并存） -->
        <div
          v-if="conversation?.deviceLoadError"
          data-testid="device-session-load-error"
          class="mb-3 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-600 dark:border-rose-800 dark:bg-rose-950/40 dark:text-rose-300"
        >
          <!-- deviceLoadError 只存布尔标记，文案在渲染时走 i18n（随语言切换） -->
          {{ t('chat_deviceSessionLoadFailed') }}
        </div>
        <!-- 品牌空状态（无活跃会话 / 空转录且无插话/审批在挂）：渐变圆徽 + 标题 +
             副文案。空转录但已有审批/插话时不显示 hero（内容列表照常渲染）。 -->
        <div
          v-if="!activeId || (activeMessages.length === 0 && steerItems.length === 0 && approvals.length === 0)"
          class="flex min-h-[45vh] flex-col items-center justify-center gap-3 py-10 text-center"
        >
          <div class="flex size-16 items-center justify-center rounded-full bg-gradient-to-br from-primary to-emerald-500 text-white shadow-lg shadow-primary/20">
            <span class="material-symbols-outlined text-3xl">auto_awesome</span>
          </div>
          <p class="text-base font-semibold text-slate-700 dark:text-slate-200">{{ t('chat_heroTitle') }}</p>
          <p class="text-sm text-slate-400">{{ t('chat_startNew') }}</p>
        </div>
        <template v-else>
          <!-- 统一时间线（Task 11）：messages（权威流）与 steerItems（瞬态插话）
               按 ts 升序归并渲染——插话以 user 气泡形态出现在它被发出的时间点，
               不再是消息流外的独立区块。归并纯渲染层：数据结构零变化，steer
               仍不写 messages、不进持久化。key 现状：messages 一律 'm-'+idx 兜底
               （messageId 分支为 Task 13 预留——落地后本地/设备壳消息将带 id）；
               Task 13 撤回 splice 前需确认稳定 id 已引入，否则 idx 键会让后续
               气泡错位复用。 -->
          <template v-for="(item, idx) in timeline" :key="item.messageId || 'm-' + idx">
            <ChatMessageBubble
              v-if="item.type === 'message'"
              :msg="item"
              :now-tick="nowTick"
              :recall-enabled="recallEnabled"
              @recall="emit('recall', $event)"
            />
            <!-- 插话气泡（原 Task 4 轻量条融入时间线）：⚡ 徽 + 插话标签 +
                 状态 chip（steerStatusLabel 六态）+ 正文/失败详情。v-else-if
                 显式钉 steer：未来第三种 type 不得静默套用插话气泡形态。 -->
            <div
              v-else-if="item.type === 'steer'"
              data-testid="chat-steer-item"
              class="mb-3 ml-auto w-fit max-w-[85%] rounded-2xl border border-primary/40 bg-primary/5 px-3 py-2 text-xs"
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
          </template>
          <ChatApprovalCard
            v-for="approval in approvals"
            :key="approval.key"
            :approval="approval"
            :now-tick="nowTick"
            @respond="(decision) => emit('approval-respond', approval, decision)"
          />
        </template>
      </div>
    </div>

    <!-- 回到底部浮钮：仅上翻（非贴底）时出现；徽标 = 上次置底后到达的消息/
         插话/审批数，回底即清零。z 序 60（≤999，不压审批卡上方的全局层）。 -->
    <button
      v-if="!nearBottom"
      type="button"
      class="absolute bottom-24 right-6 z-[60] flex size-10 items-center justify-center rounded-full bg-primary text-white shadow-lg transition hover:bg-primary/90"
      :aria-label="t('chat_scrollToBottom')"
      :title="t('chat_scrollToBottom')"
      @click="jumpToBottom"
    >
      <span class="material-symbols-outlined text-xl">arrow_downward</span>
      <span
        v-if="unseenCount > 0"
        class="absolute -right-1 -top-1 flex min-w-[18px] items-center justify-center rounded-full bg-rose-500 px-1 text-[10px] font-semibold leading-[18px] text-white"
      >{{ unseenCount > 99 ? '99+' : unseenCount }}</span>
    </button>
  </div>
</template>

<script setup>
import { computed, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import { mergeTimeline } from '../../utils/chatTimeline';
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
  // 贴底守卫（Task 8）：nearBottom 由 composable 持有（scrollToBottom 的守卫源），
  // 本组件 @scroll 几何判定后经 setNearBottom 反向写回——与 setContainer 同一套
  // 函数-prop 交接模式。
  nearBottom: { type: Boolean, default: true },
  setNearBottom: { type: Function, required: true },
  // 「回到底部」：force 滚动（上翻状态下唯一允许的无条件拽底入口）。
  scrollToBottom: { type: Function, required: true },
});

const emit = defineEmits(['approval-respond', 'recall']);

const { t } = useI18n();

// 统一渲染时间线（Task 11）：messages + steerItems 按 ts 归并、打 type 标。
// 归并函数单测已覆盖（chatTimeline.test.js），组件层不加重复单测。
const timeline = computed(() => mergeTimeline(props.activeMessages, props.steerItems));

// 撤回可用门（spec §6 定案）：设备壳会话整体禁用撤回——壳续聊 create 落在
// 新建的 local-* journal，而壳气泡锚点 id 来自原 entry.id journal，truncate
// 锚点未命中是 no-op 非报错 → UI 假成功、上下文静默分叉；待服务端跨 journal
// 锚点解析后解锁（messageId 接线保留，canRecall 纯函数不动，门控收在渲染层）。
const recallEnabled = computed(() => !props.conversation?.deviceSession);

// 未读徽标：非贴底期间到达的消息数（user/assistant 气泡 + 插话条 + 审批卡）。
// 回底（滚动事件判定贴底，或点浮钮）即清零。
const unseenCount = ref(0);

function feedTotal() {
  return props.activeMessages.length + props.steerItems.length + props.approvals.length;
}

watch(feedTotal, (n, o) => {
  if (o === undefined || n <= o) return;
  if (!props.nearBottom) unseenCount.value += n - o;
});

function onScroll(e) {
  const el = e?.target;
  if (!el) return;
  const near = el.scrollHeight - el.scrollTop - el.clientHeight < 120;
  if (near) unseenCount.value = 0;
  props.setNearBottom(near);
}

// 点击回底：先置回贴底态（浮钮立即消失、守卫解除），force 滚动由
// scrollToBottom 的 nextTick 落地；随后真实 scroll 事件会以几何值再确认一次。
function jumpToBottom() {
  unseenCount.value = 0;
  props.setNearBottom(true);
  props.scrollToBottom(true);
}

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
