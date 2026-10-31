<template>
  <div class="mb-3 max-w-[85%] rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-950 shadow-sm dark:border-amber-700/60 dark:bg-amber-950/40 dark:text-amber-100">
    <div class="flex flex-wrap items-center gap-2">
      <span class="material-symbols-outlined text-base">{{ approvalIcon(approval) }}</span>
      <span class="font-semibold">{{ approval.title || t('chat_approvalTitle') }}</span>
      <span class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/60 dark:text-amber-200">
        {{ approval.status === 'responding' ? t('chat_approvalResponding') : t('chat_approvalPending') }}
      </span>
      <!-- expires_at 倒计时（Task 5）：缺失/已过不渲染；nowTick 每 30s 重算 -->
      <span
        v-if="approvalExpiresLabel(approval)"
        data-testid="approval-expires"
        class="rounded bg-white/70 px-1.5 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/60 dark:text-amber-200"
      >
        {{ approvalExpiresLabel(approval) }}
      </span>
    </div>
    <p v-if="approval.sourceId" class="mt-1 text-xs font-medium text-amber-700 dark:text-amber-200">
      {{ t('chat_approvalFromSession', { id: approval.sourceId }) }}
    </p>
    <p v-if="approval.reason" class="mt-1 whitespace-pre-wrap text-xs text-amber-800 dark:text-amber-200">
      {{ approval.reason }}
    </p>
    <pre
      v-if="approval.command"
      class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
    >{{ approval.command }}</pre>
    <pre
      v-else-if="approvalDetailText(approval)"
      class="mt-2 max-h-40 overflow-auto rounded border border-amber-200 bg-white px-2 py-1.5 text-xs text-slate-800 dark:border-amber-700/60 dark:bg-slate-950 dark:text-slate-100"
    >{{ approvalDetailText(approval) }}</pre>
    <div v-if="approval.cwd || approval.toolName" class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-amber-700 dark:text-amber-200">
      <span v-if="approval.toolName">{{ t('chat_approvalTool') }}: {{ approval.toolName }}</span>
      <span v-if="approval.cwd">{{ t('chat_approvalCwd') }}: {{ approval.cwd }}</span>
    </div>
    <div class="mt-2 flex flex-wrap gap-2">
      <button
        v-if="approvalAllows(approval, 'accept_for_session')"
        type="button"
        class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'accept_for_session')"
      >
        <span class="material-symbols-outlined text-sm">verified_user</span>{{ t('chat_approvalAcceptSession') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'accept')"
        type="button"
        class="inline-flex items-center gap-1 rounded bg-emerald-600 px-2.5 py-1 text-xs font-semibold text-white transition hover:bg-emerald-700 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'accept')"
      >
        <span class="material-symbols-outlined text-sm">check</span>{{ t('chat_approvalAccept') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'decline')"
        type="button"
        class="inline-flex items-center gap-1 rounded border border-amber-300 bg-white px-2.5 py-1 text-xs font-semibold text-amber-800 transition hover:bg-amber-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-amber-700 dark:bg-slate-900 dark:text-amber-100 dark:hover:bg-amber-900/50"
        :disabled="approval.status === 'responding'"
        @click="emit('respond', 'decline')"
      >
        <span class="material-symbols-outlined text-sm">block</span>{{ t('chat_approvalDecline') }}
      </button>
      <button
        v-if="approvalAllows(approval, 'cancel')"
        type="button"
        class="inline-flex items-center gap-1 rounded border border-slate-300 bg-white px-2.5 py-1 text-xs font-semibold text-slate-600 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-slate-800"
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

function approvalIcon(approval) {
  switch (approval?.kind) {
    case 'command':
      return 'terminal';
    case 'file_change':
      return 'edit_document';
    case 'permissions':
      return 'lock';
    default:
      return 'rule';
  }
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
