<template>
  <!-- LLM 端点管理卡(spec 2026-11-25 §10):56432 多 endpoint 聚合路由的上游
       入口注册表。自管 REST 面(/api/endpoints),不进 customer 保存链;写路径
       走 utils/endpointSettings.js 的 endpointToPayload 线格式。列表 5s 轮询
       (ListenerSettings 同模式);弹窗形态对照 AgentMCPRegistrySettings;
       api_key 全程只显示服务端掩码(§17 不变量 7)。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <span class="material-symbols-outlined">lan</span>
        </div>
        <div class="min-w-0">
          <h3 class="text-base font-bold text-slate-900 dark:text-white">{{ t('endpointManager_title') }}</h3>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
            {{ t('endpointManager_hint') }}
          </p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-11 shrink-0 items-center justify-center gap-1 rounded-lg border border-slate-200 px-3 text-xs font-bold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
        @click="openCreate"
      >
        <span class="material-symbols-outlined text-sm">add</span>
        {{ t('endpointManager_add') }}
      </button>
    </div>

    <!-- 卡片级通知条:测试/拉模型等软结果(成功/探测失败)行内展示;保存/删除/
         加载失败走全局报错弹窗(showErrorDialog),与 AgentMCPRegistrySettings 分工一致 -->
    <div
      v-if="actionNotice"
      class="mt-4 rounded-lg border px-4 py-3 text-xs leading-5"
      :class="actionNoticeOk
        ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-300'
        : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-300'"
      role="status"
    >{{ actionNotice }}</div>

    <div v-if="loading && !hasLoaded" class="mt-5 flex min-h-24 items-center justify-center text-xs text-slate-500 dark:text-slate-400">
      <span class="material-symbols-outlined mr-2 text-base motion-safe:animate-spin">progress_activity</span>
      {{ t('endpointManager_loading') }}
    </div>

    <template v-else>
      <ul v-if="rows.length" class="mt-5 divide-y divide-slate-100 dark:divide-slate-800">
        <li v-for="row in rows" :key="row.id" class="py-3">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <span class="text-xs font-semibold text-slate-800 dark:text-slate-100">{{ row.name }}</span>
            <span
              v-if="row.builtin"
              class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary"
            >{{ t('endpointManager_builtinBadge') }}</span>
            <span
              v-for="proto in row.protocolBadges"
              :key="proto.label"
              class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-600 dark:bg-slate-700/40 dark:text-slate-300"
            >{{ proto.label }}</span>
            <span
              class="rounded px-1.5 py-0.5 text-[10px] font-bold"
              :class="row.autoGroup
                ? 'bg-primary/10 text-primary'
                : 'bg-slate-100 text-slate-500 dark:bg-slate-700/40 dark:text-slate-400'"
            >{{ t('endpointManager_autoGroup') }}</span>
            <span
              class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
              :class="endpointToneClass(row.health.tone)"
            >{{ t(row.health.key) }}</span>
            <span
              v-if="!row.enabled"
              class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-700/40 dark:text-slate-400"
            >{{ t('endpointManager_disabledBadge') }}</span>
            <span class="text-[10px] text-slate-400">
              {{ t('endpointManager_priority') }} {{ row.priority }}
              · {{ t('endpointManager_inflightLabel') }} {{ row.inflight }}/{{ row.maxConcurrency === 0 ? '∞' : row.maxConcurrency }}
            </span>
          </div>
          <p class="mt-1 break-all text-[11px] leading-4 text-slate-500 dark:text-slate-400">
            {{ row.baseUrl || t('endpointManager_noBaseUrl') }}
          </p>
          <p
            v-if="row.lastError"
            class="mt-0.5 break-words text-[10px] leading-4 text-amber-600 dark:text-amber-400"
          >
            {{ t('endpointManager_lastError') }}: {{ row.lastError }}
          </p>
          <div class="mt-2 flex flex-wrap items-center gap-1.5">
            <button
              type="button"
              class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              :disabled="actionPendingId === row.id"
              @click="runTest(row)"
            >{{ t('endpointManager_test') }}</button>
            <button
              type="button"
              class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              :disabled="actionPendingId === row.id || !row.baseUrl"
              @click="runFetchModels(row)"
            >{{ t('endpointManager_fetchModels') }}</button>
            <button
              type="button"
              class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              :disabled="actionPendingId === row.id"
              @click="runResetHealth(row)"
            >{{ t('endpointManager_resetHealth') }}</button>
            <button
              type="button"
              class="inline-flex h-9 items-center rounded-lg border border-slate-200 px-2.5 text-xs font-semibold text-slate-600 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10"
              @click="openEdit(row)"
            >{{ t('endpointManager_edit') }}</button>
            <button
              type="button"
              class="inline-flex h-9 items-center rounded-lg border border-rose-200 px-2.5 text-xs font-semibold text-rose-600 transition-colors hover:bg-rose-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-rose-300/40 disabled:cursor-not-allowed disabled:opacity-60 dark:border-rose-500/30 dark:text-rose-300 dark:hover:bg-rose-500/10"
              :disabled="row.builtin"
              :title="row.builtin ? t('endpointManager_deleteBuiltin') : ''"
              @click="removeRow(row)"
            >{{ t('endpointManager_delete') }}</button>
          </div>
        </li>
      </ul>
      <p v-else class="mt-5 text-xs text-slate-400">{{ t('endpointManager_empty') }}</p>
    </template>

    <!-- 组策略卡(auto 组;GET/PUT /api/endpoint-groups/auto/policy) -->
    <div class="mt-6 rounded-xl border border-slate-200 p-4 dark:border-slate-800" data-endpoint-policy>
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-base text-primary">tune</span>
        <div class="min-w-0">
          <h4 class="text-sm font-bold text-slate-900 dark:text-white">{{ t('endpointManager_policyTitle') }}</h4>
          <p class="mt-0.5 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('endpointManager_policyHint') }}</p>
        </div>
      </div>
      <label class="mt-3 flex min-h-9 cursor-pointer items-center gap-2 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300">
        <input
          v-model="policy.failureDisable"
          type="checkbox"
          data-field="policyFailureDisable"
          class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
        >
        {{ t('endpointManager_policyFailureDisable') }}
      </label>
      <p class="ml-6 text-[10px] leading-4 text-slate-400">{{ t('endpointManager_policyFailureDisableHint') }}</p>
      <div class="mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
          <span>{{ t('endpointManager_policyThreshold') }}</span>
          <input
            v-model="policy.failureThreshold"
            type="number"
            min="0"
            step="1"
            data-field="policyThreshold"
            class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          >
        </label>
        <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
          <span>{{ t('endpointManager_policyCooldownSec') }}</span>
          <input
            v-model="policy.cooldownSec"
            type="number"
            min="0"
            step="1"
            data-field="policyCooldownSec"
            class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          >
        </label>
        <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
          <span>{{ t('endpointManager_policyRateLimitCooldownSec') }}</span>
          <input
            v-model="policy.rateLimitCooldownSec"
            type="number"
            min="0"
            step="1"
            data-field="policyRateLimitCooldownSec"
            class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/10 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          >
        </label>
      </div>
      <p v-if="policyError" class="mt-2 text-xs text-red-600 dark:text-red-400" role="status">{{ policyError }}</p>
      <div class="mt-3 flex justify-end">
        <button
          type="button"
          class="inline-flex h-10 items-center rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
          :disabled="policySaving"
          @click="savePolicy"
        >{{ policySaving ? t('endpointManager_policySaving') : t('endpointManager_policySave') }}</button>
      </div>
    </div>

    <!-- 新增/编辑弹窗(AgentMCPRegistrySettings modal 形态) -->
    <div
      v-if="formOpen"
      class="fixed inset-0 z-[140] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('endpointManager_title')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeForm"></div>
      <div class="relative z-10 max-h-[88dvh] w-full max-w-md overflow-y-auto rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('endpointManager_title') }}</h4>

        <div class="mt-4 space-y-3">
          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_name') }}</span>
            <input
              v-model.trim="form.name"
              type="text"
              data-field="name"
              class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            >
          </label>

          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_baseUrl') }}</span>
            <input
              v-model.trim="form.baseUrl"
              type="text"
              data-field="baseUrl"
              :placeholder="editingBuiltin ? t('endpointManager_baseUrlBuiltinHint') : t('endpointManager_baseUrlPlaceholder')"
              :disabled="editingBuiltin"
              class="mt-1 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            >
          </label>

          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_apiKey') }}</span>
            <span
              v-if="editingId && form.apiKeyMasked"
              class="mt-0.5 block text-[10px] text-slate-400"
            >{{ t('endpointManager_apiKeyMaskedHint', { masked: form.apiKeyMasked }) }}</span>
            <span class="relative mt-1 block">
              <input
                v-model="form.apiKey"
                :type="showKey ? 'text' : 'password'"
                autocomplete="off"
                data-field="apiKey"
                :placeholder="t('endpointManager_apiKeyPlaceholder')"
                class="h-10 w-full rounded-lg border border-slate-200 bg-white px-3 pr-16 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              >
              <button
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-[11px] font-medium text-slate-400 hover:text-primary"
                :aria-pressed="showKey"
                @click="showKey = !showKey"
              >
                {{ showKey ? t('endpointManager_hideKey') : t('endpointManager_showKey') }}
              </button>
            </span>
          </label>

          <div class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_protocol') }}</span>
            <div class="mt-1 flex gap-4">
              <label class="flex min-h-9 cursor-pointer items-center gap-2 text-xs font-semibold text-slate-600 dark:text-slate-300">
                <input
                  v-model="form.protocolAnthropic"
                  type="checkbox"
                  data-field="protocolAnthropic"
                  class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
                >
                {{ t('endpointManager_protocolAnthropic') }}
              </label>
              <label class="flex min-h-9 cursor-pointer items-center gap-2 text-xs font-semibold text-slate-600 dark:text-slate-300">
                <input
                  v-model="form.protocolOpenAI"
                  type="checkbox"
                  data-field="protocolOpenAI"
                  class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
                >
                {{ t('endpointManager_protocolOpenAI') }}
              </label>
            </div>
          </div>

          <label class="flex min-h-9 cursor-pointer items-center gap-2 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300">
            <input
              v-model="form.autoGroup"
              type="checkbox"
              data-field="autoGroup"
              :disabled="editingBuiltin"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 disabled:opacity-60 dark:border-slate-600 dark:bg-slate-800"
            >
            {{ t('endpointManager_autoGroup') }}
          </label>

          <div class="grid grid-cols-2 gap-3">
            <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
              <span>{{ t('endpointManager_priority') }}</span>
              <input
                v-model="form.priority"
                type="number"
                min="0"
                step="1"
                data-field="priority"
                class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              >
            </label>
            <label class="min-w-0 text-[10px] font-bold uppercase text-slate-400">
              <span>{{ t('endpointManager_maxConcurrency') }}</span>
              <input
                v-model="form.maxConcurrency"
                type="number"
                min="0"
                step="1"
                data-field="maxConcurrency"
                :placeholder="t('endpointManager_maxConcurrencyPlaceholder')"
                class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
              >
            </label>
          </div>

          <label class="block">
            <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_models') }}</span>
            <textarea
              v-model="form.modelsText"
              rows="4"
              data-field="models"
              :placeholder="t('endpointManager_modelsPlaceholder')"
              class="mt-1 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            ></textarea>
            <span class="mt-1 block text-[10px] leading-4 text-slate-400">{{ t('endpointManager_modelsHint') }}</span>
          </label>

          <label class="flex min-h-9 cursor-pointer items-center gap-2 py-1 text-xs font-semibold text-slate-600 dark:text-slate-300">
            <input
              v-model="form.enabled"
              type="checkbox"
              data-field="enabled"
              class="h-4 w-4 rounded border-slate-300 text-primary focus:ring-primary/30 dark:border-slate-600 dark:bg-slate-800"
            >
            {{ t('endpointManager_enabled') }}
          </label>
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg border border-slate-200 px-4 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
            @click="closeForm"
          >{{ t('endpointManager_cancel') }}</button>
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
            :disabled="saving"
            @click="saveForm"
          >{{ saving ? t('endpointManager_saving') : t('endpointManager_save') }}</button>
        </div>
      </div>
    </div>

    <!-- 拉模型二级弹层:POST fetch-models 不自动写库(spec §10),展示列表让
         用户确认后回填编辑表单,保存仍走 PUT -->
    <div
      v-if="fetchModelsOpen"
      class="fixed inset-0 z-[150] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      :aria-label="t('endpointManager_fetchedTitle')"
    >
      <div class="absolute inset-0 bg-slate-900/45 backdrop-blur-sm" @click="closeFetchedModels"></div>
      <div class="relative z-10 max-h-[70dvh] w-full max-w-sm overflow-y-auto rounded-2xl border border-slate-200 bg-white p-5 shadow-2xl dark:border-slate-800 dark:bg-slate-900">
        <h4 class="text-base font-semibold text-slate-900 dark:text-white">{{ t('endpointManager_fetchedTitle') }}</h4>
        <p class="mt-1 text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('endpointManager_fetchedHint') }}</p>
        <p v-if="!fetchedModels.length" class="mt-3 text-xs text-slate-400">{{ t('endpointManager_fetchedEmpty') }}</p>
        <ul v-else class="mt-3 max-h-64 space-y-1 overflow-y-auto rounded-lg border border-slate-100 p-2 dark:border-slate-800">
          <li
            v-for="model in fetchedModels"
            :key="model"
            class="break-all font-mono text-[11px] text-slate-700 dark:text-slate-200"
          >{{ model }}</li>
        </ul>
        <div class="mt-5 flex justify-end gap-2">
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg border border-slate-200 px-4 text-xs font-bold text-slate-600 transition-colors hover:bg-slate-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
            @click="closeFetchedModels"
          >{{ t('endpointManager_cancel') }}</button>
          <button
            type="button"
            class="inline-flex h-10 items-center rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-60"
            :disabled="!fetchedModels.length"
            @click="confirmFetchedModels"
          >{{ t('endpointManager_fetchedConfirm') }}</button>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { showErrorDialog } from '../../composables/useErrorDialog';
import {
  createEndpoint,
  deleteEndpoint,
  fetchEndpointModels,
  getEndpointPolicy,
  getEndpoints,
  resetEndpointHealth,
  setEndpointPolicy,
  testEndpoint,
  updateEndpoint,
} from '../../services/agentApi';
import {
  endpointToPayload,
  formatModelsText,
  normalizeEndpointView,
  parseModelsText,
  validateEndpointConfig,
} from '../../utils/endpointSettings';

// 健康徽标色调 → 样式类(ListenerSettings TONE_CLASSES 同款配色词表):
// ok 绿 / cooldown 琥珀 / disabled 红 / 429 蓝灰(slate)。
const TONE_CLASSES = {
  ok: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
  warn: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
  error: 'bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-300',
  muted: 'bg-slate-100 text-slate-600 dark:bg-slate-700 dark:text-slate-300',
};

// 纯函数导出(ListenerSettings.listenerToneClass 同先例)。
export function endpointToneClass(tone) {
  return Object.prototype.hasOwnProperty.call(TONE_CLASSES, tone)
    ? TONE_CLASSES[tone]
    : TONE_CLASSES.muted;
}

// 健康徽标决策纯函数:health_status 是权威(ok/cooldown/disabled);429 冷却期
// 服务端不改 health_status(rate_limited_until 不出 REST 面),从 last_error
// 文案识别(429/rate limit),蓝灰表达「暂时限流、健康仍在」。
export function endpointHealthBadge(row) {
  const status = String(row?.healthStatus || 'ok');
  if (status === 'disabled') return { key: 'endpointManager_healthDisabled', tone: 'error' };
  if (status === 'cooldown') return { key: 'endpointManager_healthCooldown', tone: 'warn' };
  if (/\b429\b|rate[ _-]?limit/i.test(String(row?.lastError || ''))) {
    return { key: 'endpointManager_healthRateLimited', tone: 'muted' };
  }
  return { key: 'endpointManager_healthOk', tone: 'ok' };
}

function emptyForm() {
  return {
    name: '',
    baseUrl: '',
    apiKey: '',
    apiKeyMasked: '',
    protocolAnthropic: true,
    protocolOpenAI: false,
    autoGroup: true,
    priority: '0',
    maxConcurrency: '0',
    modelsText: '',
    enabled: true,
  };
}

function emptyPolicy() {
  return {
    failureDisable: true,
    failureThreshold: '3',
    cooldownSec: '300',
    rateLimitCooldownSec: '60',
  };
}

export default {
  name: 'EndpointManagerSettings',
  setup() {
    const { t } = useI18n();
    return { t, endpointToneClass };
  },
  data() {
    return {
      endpoints: [],
      loading: false,
      hasLoaded: false,
      pollTimer: null,
      actionPendingId: '',
      actionNotice: '',
      actionNoticeOk: false,
      formOpen: false,
      editingId: null,
      editingBuiltin: false,
      form: emptyForm(),
      showKey: false,
      saving: false,
      fetchModelsOpen: false,
      fetchedModels: [],
      fetchedTargetRow: null,
      policy: emptyPolicy(),
      policyError: '',
      policySaving: false,
    };
  },
  computed: {
    rows() {
      return this.endpoints.map((view) => ({
        ...view,
        health: endpointHealthBadge(view),
        protocolBadges: [
          ...(view.protocolAnthropic ? [{ label: 'A' }] : []),
          ...(view.protocolOpenAI ? [{ label: 'O' }] : []),
        ],
      }));
    },
  },
  mounted() {
    this.loadEndpoints();
    this.loadPolicy();
    // 5s 轮询(ListenerSettings 同模式):inflight/健康徽标/最近错误是运行态。
    this.pollTimer = window.setInterval(() => {
      if (document.visibilityState === 'visible') this.loadEndpoints();
    }, 5000);
  },
  beforeUnmount() {
    if (this.pollTimer) window.clearInterval(this.pollTimer);
  },
  methods: {
    async loadEndpoints() {
      if (this.loading) return;
      this.loading = true;
      try {
        // agentApi 已解包为数组(缺键/非数组兜 []);逐条过 normalizeEndpointView
        // 归一——服务端载荷是线格式(snake_case),不归一则徽标/编辑回填全空。
        const list = await getEndpoints();
        this.endpoints = (Array.isArray(list) ? list : [])
          .map(normalizeEndpointView)
          .filter(Boolean);
        this.hasLoaded = true;
      } catch (error) {
        showErrorDialog(this.t('endpointManager_loadFailed', { error: error?.message || '' }));
      } finally {
        this.loading = false;
      }
    },
    async loadPolicy() {
      try {
        const p = await getEndpointPolicy();
        const src = p && typeof p === 'object' ? p : {};
        this.policy = {
          failureDisable: src.failure_disable !== false,
          failureThreshold: String(src.failure_threshold ?? 3),
          cooldownSec: String(src.cooldown_sec ?? 300),
          rateLimitCooldownSec: String(src.rate_limit_cooldown_sec ?? 60),
        };
        this.policyError = '';
      } catch (error) {
        // 策略读取失败行内提示(不打断列表);保存时再走全局弹窗。
        this.policyError = this.t('endpointManager_policyLoadFailed', { error: error?.message || '' });
      }
    },
    async savePolicy() {
      // 与后端 SetPolicy 同源:三数值必须 ≥ 0(整数)。空串先显式拒绝——
      // Number('')===0 会让空输入静默写 0(失败阈值 0 = 首败即摘除)。
      const raw = {
        failure_threshold: String(this.policy.failureThreshold ?? '').trim(),
        cooldown_sec: String(this.policy.cooldownSec ?? '').trim(),
        rate_limit_cooldown_sec: String(this.policy.rateLimitCooldownSec ?? '').trim(),
      };
      if (Object.values(raw).some((v) => v === '')) {
        this.policyError = this.t('endpointManager_error_policyValues');
        return;
      }
      const numbers = {
        failure_threshold: Number(raw.failure_threshold),
        cooldown_sec: Number(raw.cooldown_sec),
        rate_limit_cooldown_sec: Number(raw.rate_limit_cooldown_sec),
      };
      for (const value of Object.values(numbers)) {
        if (!Number.isInteger(value) || value < 0) {
          this.policyError = this.t('endpointManager_error_policyValues');
          return;
        }
      }
      this.policySaving = true;
      this.policyError = '';
      try {
        await setEndpointPolicy({ failure_disable: this.policy.failureDisable === true, ...numbers });
        await this.loadPolicy();
        this.setNotice(this.t('endpointManager_policySaved'), true);
      } catch (error) {
        showErrorDialog(this.t('endpointManager_policySaveFailed', { error: error?.message || '' }));
      } finally {
        this.policySaving = false;
      }
    },
    setNotice(text, ok) {
      this.actionNotice = text;
      this.actionNoticeOk = ok === true;
    },
    openCreate() {
      this.editingId = null;
      this.editingBuiltin = false;
      this.form = emptyForm();
      this.showKey = false;
      this.formOpen = true;
    },
    openEdit(row) {
      this.editingId = row.id;
      this.editingBuiltin = row.builtin === true;
      this.form = {
        name: row.name,
        baseUrl: row.baseUrl,
        // api_key 永不回显完整值:表单留空 = 不改(§17 不变量 7),掩码只作提示。
        apiKey: '',
        apiKeyMasked: row.apiKeyMasked,
        protocolAnthropic: row.protocolAnthropic,
        protocolOpenAI: row.protocolOpenAI,
        autoGroup: row.builtin ? true : row.autoGroup,
        priority: String(row.priority ?? 0),
        maxConcurrency: String(row.maxConcurrency ?? 0),
        modelsText: formatModelsText(row.models),
        enabled: row.enabled !== false,
      };
      this.showKey = false;
      this.formOpen = true;
    },
    closeForm() {
      this.formOpen = false;
      this.editingId = null;
      this.editingBuiltin = false;
    },
    validateForm() {
      // 编辑态 api_key 留空 = 沿用存储值(§17 不变量 7):GET 只回掩码,空输入
      // 不代表「无 key」。apiKeyMasked 非空 ⟺ 存储行确有 key(掩码规则:空 key
      // 掩成 ''),据此给校验一个非空哨兵,让「非回环必须 key」按生效配置判定;
      // 新增态 apiKeyMasked 恒空,哨兵不生效。
      const hasStoredKey = this.editingId !== null && String(this.form.apiKeyMasked || '') !== '';
      const errorKey = validateEndpointConfig({
        name: this.form.name,
        baseUrl: this.form.baseUrl,
        apiKey: this.form.apiKey || (hasStoredKey ? 'stored-key-sentinel' : ''),
        protocolAnthropic: this.form.protocolAnthropic,
        protocolOpenAI: this.form.protocolOpenAI,
        priority: this.form.priority,
        maxConcurrency: this.form.maxConcurrency,
        models: parseModelsText(this.form.modelsText),
        builtin: this.editingBuiltin,
      });
      if (errorKey) {
        showErrorDialog(this.t(errorKey));
        return false;
      }
      return true;
    },
    async saveForm() {
      if (!this.validateForm()) return;
      this.saving = true;
      try {
        const payload = endpointToPayload(this.form, this.editingBuiltin);
        if (this.editingId) {
          await updateEndpoint(this.editingId, payload);
        } else {
          await createEndpoint(payload);
        }
        this.formOpen = false;
        this.editingId = null;
        this.editingBuiltin = false;
        await this.loadEndpoints();
      } catch (error) {
        showErrorDialog(this.t('endpointManager_saveFailed', { error: error?.message || '' }));
      } finally {
        this.saving = false;
      }
    },
    async runTest(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      try {
        const res = await testEndpoint(row.id);
        const ok = res?.ok === true;
        const count = Number(res?.model_count ?? 0);
        this.setNotice(
          ok
            ? this.t('endpointManager_testOk', { count })
            : this.t('endpointManager_testFailed', { error: res?.error || '' }),
          ok,
        );
        await this.loadEndpoints();
      } catch (error) {
        this.setNotice(this.t('endpointManager_testFailed', { error: error?.message || '' }), false);
      } finally {
        this.actionPendingId = '';
      }
    },
    async runFetchModels(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      try {
        const models = await fetchEndpointModels(row.id);
        this.fetchedModels = models.filter((m) => typeof m === 'string' && m.trim());
        this.fetchedTargetRow = row;
        this.fetchModelsOpen = true;
      } catch (error) {
        this.setNotice(this.t('endpointManager_fetchFailed', { error: error?.message || '' }), false);
      } finally {
        this.actionPendingId = '';
      }
    },
    closeFetchedModels() {
      this.fetchModelsOpen = false;
      this.fetchedModels = [];
      this.fetchedTargetRow = null;
    },
    // 确认后把 models 写进编辑表单(不自动 PUT);用户点保存才落库(spec §10)。
    confirmFetchedModels() {
      const row = this.fetchedTargetRow;
      const models = this.fetchedModels;
      if (!row || !models.length) {
        this.closeFetchedModels();
        return;
      }
      this.closeFetchedModels();
      this.openEdit(row);
      this.form.modelsText = models.join('\n');
    },
    async runResetHealth(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      try {
        await resetEndpointHealth(row.id);
        this.setNotice(this.t('endpointManager_resetHealthDone', { name: row.name }), true);
        await this.loadEndpoints();
      } catch (error) {
        this.setNotice(this.t('endpointManager_actionFailed', { error: error?.message || '' }), false);
      } finally {
        this.actionPendingId = '';
      }
    },
    async removeRow(row) {
      if (row.builtin) return; // 内置行禁删(按钮已禁用,此处兜一道;§17 不变量 8)
      if (!window.confirm(this.t('endpointManager_deleteConfirm', { name: row.name }))) return;
      this.actionPendingId = row.id;
      try {
        await deleteEndpoint(row.id);
        await this.loadEndpoints();
      } catch (error) {
        showErrorDialog(this.t('endpointManager_actionFailed', { error: error?.message || '' }));
      } finally {
        this.actionPendingId = '';
      }
    },
  },
};
</script>
