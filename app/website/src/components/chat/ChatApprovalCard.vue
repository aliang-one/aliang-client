<template>
  <div class="mb-3 max-w-[85%] rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-950 shadow-sm dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-100">
    <div class="flex flex-wrap items-center gap-2">
      <!-- 盾形徽章：warning 色调圆底，内嵌 shield 图标 -->
      <span class="flex size-8 shrink-0 items-center justify-center rounded-full bg-amber-100 text-amber-600 dark:bg-amber-900/40 dark:text-amber-300">
        <span class="material-symbols-outlined text-lg">shield</span>
      </span>
      <span class="font-semibold">{{ approval.title || t('chat_approvalTitle') }}</span>
      <!-- 状态 chip：pending=warning / responding=info 胶囊 -->
      <span
        class="rounded-full px-2 py-0.5 text-[11px] font-medium"
        :class="approval.status === 'responding'
          ? 'bg-sky-100 text-sky-700 dark:bg-sky-900/60 dark:text-sky-200'
          : 'bg-amber-100 text-amber-700 dark:bg-amber-900/60 dark:text-amber-200'"
      >
        {{ approval.status === 'responding' ? t('chat_approvalResponding') : t('chat_approvalPending') }}
      </span>
      <!-- expires_at 倒计时（Task 5）：缺失/已过不渲染；nowTick 每 30s 重算 -->
      <span
        v-if="approvalExpiresLabel(approval)"
        data-testid="approval-expires"
        class="rounded-full bg-white/70 px-2 py-0.5 text-[11px] font-medium text-amber-700 dark:bg-amber-900/60 dark:text-amber-200"
      >
        {{ approvalExpiresLabel(approval) }}
      </span>
    </div>
    <p v-if="approval.sourceId" class="mt-1.5 text-xs font-medium text-amber-700 dark:text-amber-200">
      {{ t('chat_approvalFromSession', { id: approval.sourceId }) }}
    </p>
    <!-- 摘要默认 4 行截断，点按展开/收起（局部状态，不发帧） -->
    <template v-if="approval.reason">
      <p
        :ref="bindSummaryEl"
        class="mt-1.5 whitespace-pre-wrap text-xs text-amber-800 dark:text-amber-200"
        :class="summaryExpanded ? '' : 'line-clamp-4'"
      >{{ approval.reason }}</p>
      <button
        v-if="summaryOverflowing"
        type="button"
        class="mt-0.5 text-xs font-medium text-amber-700 underline underline-offset-2 transition hover:text-amber-900 dark:text-amber-200 dark:hover:text-amber-100"
        @click="summaryExpanded = !summaryExpanded"
      >{{ summaryExpanded ? t('chat_approvalLess') : t('chat_approvalMore') }}</button>
    </template>
    <pre
      v-if="approval.command"
      class="mt-2 max-h-40 overflow-auto rounded-lg bg-slate-950 px-3 py-2 font-mono text-xs text-slate-100"
    >{{ approval.command }}</pre>
    <pre
      v-else-if="approvalDetailText(approval)"
      class="mt-2 max-h-40 overflow-auto rounded-lg bg-slate-950 px-3 py-2 font-mono text-xs text-slate-100"
    >{{ approvalDetailText(approval) }}</pre>
    <div v-if="approval.cwd || approval.toolName" class="mt-1.5 flex flex-wrap gap-x-3 gap-y-1 text-xs text-amber-700 dark:text-amber-200">
      <span v-if="approval.toolName">{{ t('chat_approvalTool') }}: {{ approval.toolName }}</span>
      <span v-if="approval.cwd">{{ t('chat_approvalCwd') }}: {{ approval.cwd }}</span>
    </div>
    <div class="mt-2.5 flex flex-wrap gap-2">
      <button
        v-if="approvalAllows(approval, 'accept_for_session')"
        type="button"
        class="inline-flex items-center gap-1 rounded-lg border border-emerald-300 bg-emerald-50 px-2.5 py-1 text-xs font-semibold text-emerald-700 transition hover:bg-emerald-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-emerald-700/60 dark:bg-emerald-900/30 dark:text-emerald-300 dark:hover:bg-emerald-900/50"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'accept_for_session')"
      >
        <span class="material-symbols-outlined text-sm">verified_user</span>{{ t('chat_approvalAcceptSession') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'accept')"
        type="button"
        class="inline-flex items-center gap-1 rounded-lg bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'accept')"
      >
        <span class="material-symbols-outlined text-sm">check</span>{{ t('chat_approvalAccept') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'decline')"
        type="button"
        class="inline-flex items-center gap-1 rounded-lg border border-amber-300 bg-white px-2.5 py-1 text-xs font-semibold text-amber-800 transition hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-700 dark:bg-slate-900 dark:text-amber-100 dark:hover:bg-amber-900/50"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'decline')"
      >
        <span class="material-symbols-outlined text-sm">block</span>{{ t('chat_approvalDecline') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'cancel')"
        type="button"
        class="inline-flex items-center gap-1 rounded-lg border border-slate-300 bg-white px-2.5 py-1 text-xs font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'cancel')"
      >
        <span class="material-symbols-outlined text-sm">close</span>{{ t('chat_approvalCancel') }}
      </button>
    </div>
    <p v-if="approval.error" class="mt-1 text-xs text-rose-600 dark:text-rose-300">{{ approval.error }}</p>
  </div>
</template>

<script setup>
import { nextTick, onMounted, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import { normalizeApprovalDecision, normalizeApprovalDecisions } from '../../composables/useAliangChat';

// 纯渲染：respond 事件上抛后由 useAliangChat.respondApproval 发帧
// （status='responding' 落位、handledApprovalIds 去重、socket 重试均在彼处）。
const props = defineProps({
  approval: { type: Object, required: true },
  nowTick: { type: Number, default: 0 },
});

const emit = defineEmits(['respond']);

const { t } = useI18n();

// --- 摘要 4 行截断/展开（Task 8，局部 UI 状态） ---
const summaryEl = ref(null);
const summaryExpanded = ref(false);
const summaryOverflowing = ref(false);

// 函数 ref 收集口：v-if 下的元素挂载/卸载都会回调（null 落值）。
function bindSummaryEl(el) {
  summaryEl.value = el;
}

function measureSummaryOverflow() {
  const el = summaryEl.value;
  summaryOverflowing.value = !!el && el.scrollHeight > el.clientHeight + 1;
}

watch(
  () => [props.approval?.reason, props.approval?.key],
  () => {
    // 内容变化后重测溢出；切卡时展开态复位（不跨卡串状态）。
    summaryExpanded.value = false;
    nextTick(measureSummaryOverflow);
  },
);

onMounted(() => nextTick(measureSummaryOverflow));

function serializeApprovalValue(value) {
  if (value === null || value === undefined || value === '') return '';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value, null, 2);
  } catch (_) {
    return String(value);
  }
}

function approvalDetailText(approval) {
  return serializeApprovalValue(approval?.toolInput) || serializeApprovalValue(approval?.fileChanges);
}

function approvalAllows(approval, decision) {
  const normalized = normalizeApprovalDecision(decision);
  return normalizeApprovalDecisions(approval?.availableDecisions).includes(normalized);
}

// 剩余时间文案（nowTick 驱动 30s 重算）：>1h「约 X 小时」、≤1h「X 分钟」、
// <2 分钟「即将超时」；缺失/已过返回空（徽标不渲染）。
function approvalExpiresLabel(approval) {
  const expiresMs = Date.parse(approval?.expiresAt || '');
  if (!Number.isFinite(expiresMs)) return '';
  const remainMs = expiresMs - props.nowTick;
  if (remainMs <= 0) return '';
  if (remainMs < 2 * 60 * 1000) {
    return t('chat_approvalExpiringSoon');
  }
  const minutes = Math.ceil(remainMs / 60000);
  if (minutes <= 60) {
    return t('chat_approvalExpiresMinutes', { minutes });
  }
  return t('chat_approvalExpiresHours', { hours: Math.round(remainMs / 3600000) });
}
</script>
