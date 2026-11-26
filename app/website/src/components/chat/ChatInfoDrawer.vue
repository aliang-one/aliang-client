<template>
  <!--
    会话信息抽屉（spec 2026-11-26-chat-info-drawer-design）：贴右侧滑出的对话
    相关信息面板，内容对齐手机端 DiagnosticsPanel + BackgroundTasksPanel
    （AliangVibeCodingPhone/src/components/vibecoding/）：
      ① 子agent状态（ai.subagent → activeSubagents，行点击上抛开面板看详情）
      ② 上下文用量（progress 心跳 diagnostics：context_tokens/context_window）
      ③ 运行状态（回合数/触碰文件/Git 变更/耗时/停止原因）
      ④ 无进展守卫（dup 3 / err 5 / trunc 2 连击进度）
      ⑤ 失败原因（journal end 终态快照：status/reason/error）
    滑出范式镜像 ChatSidebar：面板常驻挂载（v-if 只挂遮罩），transition-transform
    duration-200 + translate-x-full ↔ translate-x-0。层序低于子agent面板
    （z-920/930 < 940/950）——抽屉里点「查看详情」时子agent面板盖在上面。
    移动端（<sm）全宽滑入；数据为空时各区块显示空态文案不画假表。
  -->
  <div>
    <!-- 遮罩：点空白处关闭 -->
    <div
      v-if="open"
      data-testid="chat-info-drawer-backdrop"
      class="fixed inset-0 z-[920] bg-black/50"
      @click="emit('close')"
    ></div>

    <aside
      data-testid="chat-info-drawer"
      class="fixed right-0 top-0 bottom-0 z-[930] flex w-full flex-col border-l border-slate-200 bg-white shadow-xl transition-transform duration-200 sm:w-96 dark:border-slate-700 dark:bg-slate-900"
      :class="open ? 'translate-x-0' : 'translate-x-full'"
      role="dialog"
      :aria-label="t('chat_infoEntry')"
      :aria-hidden="!open"
    >
      <!-- 头：标题+关闭（触控≥40px，同子agent面板口径） -->
      <header class="flex shrink-0 items-center gap-2 border-b border-slate-200 px-4 py-2 dark:border-slate-700">
        <h2 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('chat_infoEntry') }}</h2>
        <span
          v-if="running"
          class="inline-flex items-center gap-1 text-xs text-primary"
        >
          <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-primary"></span>{{ t('chat_statusRunning') }}
        </span>
        <button
          type="button"
          data-testid="chat-info-drawer-close"
          class="ml-auto flex min-h-[40px] min-w-[40px] items-center justify-center rounded text-slate-500 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800"
          :aria-label="t('chat_close')"
          @click="emit('close')"
        >
          <span class="material-symbols-outlined text-lg leading-none">close</span>
        </button>
      </header>

      <div class="min-h-0 flex-1 overflow-y-auto">
        <!-- ① 子agent状态（手机 BackgroundTasksPanel 对齐：活跃在前、行点击看详情） -->
        <section class="border-b border-slate-100 dark:border-slate-800">
          <h3 class="px-4 pt-3 text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {{ t('chat_infoSubagents') }}
          </h3>
          <button
            v-for="s in subagents"
            :key="s.taskId"
            type="button"
            data-testid="chat-info-drawer-subagent-item"
            class="flex min-h-[44px] w-full items-center gap-2 px-4 py-2 text-left transition hover:bg-slate-100 dark:hover:bg-slate-800"
            :aria-label="t('chat_subagentEntryView')"
            @click="emit('open-subagent-panel')"
          >
            <span
              class="material-symbols-outlined shrink-0 text-base leading-none"
              :class="subagentToneClass(s)"
            >{{ subagentPhaseMeta(s.phase).icon }}</span>
            <span
              v-if="subagentPhaseMeta(s.phase).tone === 'running'"
              class="material-symbols-outlined shrink-0 animate-spin text-sm leading-none text-primary"
            >progress_activity</span>
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm font-medium text-slate-900 dark:text-white">{{ s.name || s.taskId }}</span>
              <span v-if="subagentStep(s)" class="block truncate text-xs text-slate-500 dark:text-slate-400">{{ subagentStep(s) }}</span>
            </span>
            <span class="shrink-0 text-right text-[11px] leading-tight text-slate-400 dark:text-slate-500">
              <span v-if="formatSubagentDuration(s.durationMs)" class="block">{{ formatSubagentDuration(s.durationMs) }}</span>
              <span v-if="formatSubagentTokens(s.totalTokens)" class="block">{{ formatSubagentTokens(s.totalTokens) }}</span>
            </span>
          </button>
          <p
            v-if="!subagents.length"
            class="px-4 py-2 text-xs text-slate-400 dark:text-slate-500"
          >{{ t('chat_infoSubagentsEmpty') }}</p>
          <button
            v-else
            type="button"
            data-testid="chat-info-drawer-subagent-detail"
            class="mt-1 flex w-full items-center gap-1 border-t border-slate-100 px-4 py-2 text-xs text-primary transition hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800"
            @click="emit('open-subagent-panel')"
          >
            {{ t('chat_subagentEntryView') }}
            <span class="material-symbols-outlined text-sm">chevron_right</span>
          </button>
        </section>

        <!-- ② 上下文用量（手机 DiagnosticsPanel 上下文卡对齐） -->
        <section data-testid="chat-info-drawer-context" class="border-b border-slate-100 px-4 py-3 dark:border-slate-800">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {{ t('chat_infoContext') }}
          </h3>
          <template v-if="diag">
            <p class="mt-2 text-lg font-semibold text-slate-900 dark:text-white">
              {{ fmtNum(diag.contextTokens) }}<span
                v-if="Number.isFinite(Number(diag.contextWindow)) && Number(diag.contextWindow) > 0"
                class="text-sm font-normal text-slate-400 dark:text-slate-500"
              > / {{ fmtNum(diag.contextWindow) }}</span>
            </p>
            <div
              v-if="contextBarPct !== null"
              class="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800"
            >
              <div class="h-full rounded-full bg-primary" :style="{ width: contextBarPct + '%' }"></div>
            </div>
            <dl class="mt-2 space-y-1 text-xs text-slate-500 dark:text-slate-400">
              <div v-if="remainingTokens !== null" class="flex justify-between gap-2">
                <dt>{{ t('chat_infoContextRemaining') }}</dt>
                <dd class="text-slate-700 dark:text-slate-300">{{ fmtNum(remainingTokens) }}</dd>
              </div>
              <div v-if="fmtNum(diag.turns)" class="flex justify-between gap-2">
                <dt>{{ t('chat_infoTurns') }}</dt>
                <dd class="text-slate-700 dark:text-slate-300">{{ fmtNum(diag.turns) }}</dd>
              </div>
            </dl>
            <p class="mt-2 text-[11px] leading-relaxed text-slate-400 dark:text-slate-500">
              {{ t('chat_infoCompactHint') }}
            </p>
          </template>
          <p v-else class="mt-2 text-xs text-slate-400 dark:text-slate-500">{{ t('chat_infoContextNone') }}</p>
        </section>

        <!-- ③ 运行状态（手机 DiagnosticsPanel 状态卡对齐） -->
        <section data-testid="chat-info-drawer-runtime" class="border-b border-slate-100 px-4 py-3 dark:border-slate-800">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {{ t('chat_infoRuntime') }}
          </h3>
          <dl v-if="diag" class="mt-2 space-y-1 text-xs text-slate-500 dark:text-slate-400">
            <div v-if="Number.isFinite(Number(diag.filesTouchedCount))" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoRuntimeFiles') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ fmtNum(diag.filesTouchedCount) }}</dd>
            </div>
            <div v-if="Number.isFinite(Number(diag.gitChangedCount))" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoRuntimeGit') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ fmtNum(diag.gitChangedCount) }}</dd>
            </div>
            <div v-if="formatSubagentDuration(diag.elapsedMs)" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoRuntimeElapsed') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ formatSubagentDuration(diag.elapsedMs) }}</dd>
            </div>
            <div v-if="diag.stopReason" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoStopReason') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ diag.stopReason }}</dd>
            </div>
          </dl>
          <p v-else class="mt-2 text-xs text-slate-400 dark:text-slate-500">{{ t('chat_infoRuntimeNone') }}</p>
        </section>

        <!-- ④ 无进展守卫（手机 DiagnosticsPanel 守卫卡对齐：连击/阈值进度条） -->
        <section v-if="diag" data-testid="chat-info-drawer-guards" class="border-b border-slate-100 px-4 py-3 dark:border-slate-800">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {{ t('chat_infoGuards') }}
          </h3>
          <div class="mt-2 space-y-2">
            <div v-for="g in guardRows" :key="g.key">
              <div class="flex items-center justify-between gap-2 text-xs text-slate-500 dark:text-slate-400">
                <span>{{ g.label }}</span>
                <span class="text-slate-700 dark:text-slate-300">{{ g.value }} / {{ g.max }}</span>
              </div>
              <div class="mt-0.5 h-1 w-full overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
                <div
                  class="h-full rounded-full"
                  :class="g.value >= g.max ? 'bg-rose-500' : 'bg-sky-500'"
                  :style="{ width: g.pct + '%' }"
                ></div>
              </div>
            </div>
          </div>
        </section>

        <!-- ⑤ 失败原因（journal end 终态快照；仅失败态渲染，成功不画区块） -->
        <section v-if="diag && (diag.status === 'error' || diag.reason || diag.error)" data-testid="chat-info-drawer-error" class="px-4 py-3">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-rose-400 dark:text-rose-400">
            {{ t('chat_infoFailure') }}
          </h3>
          <dl class="mt-2 space-y-1 text-xs text-slate-500 dark:text-slate-400">
            <div v-if="diag.reason" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoFailureReason') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ diag.reason }}</dd>
            </div>
            <div v-if="diag.status" class="flex justify-between gap-2">
              <dt>{{ t('chat_infoFailureStatus') }}</dt>
              <dd class="text-slate-700 dark:text-slate-300">{{ diag.status }}</dd>
            </div>
          </dl>
          <p
            v-if="diag.error"
            class="mt-2 rounded bg-rose-50 px-2 py-1.5 text-xs leading-relaxed text-rose-600 dark:bg-rose-950/40 dark:text-rose-400"
          >{{ diag.error }}</p>
        </section>
      </div>
    </aside>
  </div>
</template>

<script setup>
// 纯渲染+轻派生：数据归一在 useAliangChat（实时心跳 + 详情回填双通道），
// 展示数学在 utils/chatDiagnostics（阈值/百分比），子agent行内格式化复用
// utils/subagentPanel 的同源函数（与子agent面板同一套数字口径）。
import { computed } from 'vue';
import { useI18n } from '../../i18n';
import {
  subagentPhaseMeta,
  formatSubagentDuration,
  formatSubagentTokens,
} from '../../utils/subagentPanel';
import { GUARD_MAXES, guardPct, contextPct } from '../../utils/chatDiagnostics';

const props = defineProps({
  open: { type: Boolean, default: false },
  subagents: { type: Array, default: () => [] },
  // 归一化后的诊断快照（useAliangChat.activeDiagnostics），null=尚无数据。
  diagnostics: { type: Object, default: null },
  running: { type: Boolean, default: false },
});
const emit = defineEmits(['close', 'open-subagent-panel']);
const { t } = useI18n();

const diag = computed(() => props.diagnostics);

// 阶段色调与子agent面板同源（RUNNING_TONE_CLASSES 同一套映射）。组件内不维护
// 第二份真相——图标/文案取 subagentPhaseMeta，色调按 tone 键选。
const TONE_CLASSES = {
  running: 'text-primary',
  queued: 'text-sky-500',
  done: 'text-primary',
  failed: 'text-red-500',
  interrupted: 'text-amber-500',
};
function subagentToneClass(s) {
  return TONE_CLASSES[subagentPhaseMeta(s?.phase).tone] || 'text-slate-400';
}
// 行内第二行：currentStep 优先，缺则 description（手机 TaskRow 同口径）。
function subagentStep(s) {
  return (s && (s.currentStep || s.description)) || '';
}

const fmtNum = (n) => {
  const v = Number(n);
  return Number.isFinite(v) ? v.toLocaleString('en-US') : '';
};

// 上下文进度条百分比（窗口缺失/非法时 null → 不画条，同 contextPct 契约）。
const contextBarPct = computed(() => contextPct(diag.value?.contextTokens, diag.value?.contextWindow));
// 剩余可用：窗口与已用都有效且非负才显示（压缩后估算回落可能短暂越窗，负值
// 不显示避免「剩余 -3k」的怪表）。
const remainingTokens = computed(() => {
  const used = Number(diag.value?.contextTokens);
  const win = Number(diag.value?.contextWindow);
  if (!Number.isFinite(used) || !Number.isFinite(win) || win <= 0) return null;
  const rest = win - used;
  return rest >= 0 ? rest : null;
});

// 三条守卫行：值/上限/百分比一次派生（阈值常量与手机端 DiagnosticsPanel 同源）。
const guardRows = computed(() => [
  { key: 'dup', label: t('chat_infoGuardDup'), value: Number(diag.value?.dupStreak) || 0, max: GUARD_MAXES.dupStreak },
  { key: 'err', label: t('chat_infoGuardErr'), value: Number(diag.value?.errStreak) || 0, max: GUARD_MAXES.errStreak },
  { key: 'trunc', label: t('chat_infoGuardTrunc'), value: Number(diag.value?.truncStreak) || 0, max: GUARD_MAXES.truncStreak },
].map((g) => ({ ...g, pct: guardPct(g.value, g.max) ?? 0 })));
</script>
