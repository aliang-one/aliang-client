<template>
  <!-- LLM 端点管理卡(spec 2026-11-25 §10 + 二期 2026-11-27 §7.1):56432 多
       endpoint 聚合路由的上游入口注册表。自管 REST 面(/api/endpoints),不进
       customer 保存链;写路径走 utils/endpointSettings.js 的 endpointToPayload
       线格式。二期:行上激活开关(PUT enabled 快速路径)+ 出口指定按钮
       (set-egress-kernel)+ 三词健康徽标(停用/熔断/限流,不变量 16)+ 行尾
       调用计数与相对时间。列表 5s 轮询(ListenerSettings 同模式);弹窗形态
       对照 AgentMCPRegistrySettings;api_key 全程只显示服务端掩码(§17 不变量 7)。 -->
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
        <li v-for="row in rows" :key="row.id" class="py-3" :class="{ 'opacity-60': !row.enabled }">
          <div class="flex flex-wrap items-center gap-x-2 gap-y-1.5">
            <span class="text-xs font-semibold text-slate-800 dark:text-slate-100">{{ row.name }}</span>
            <span
              v-if="row.builtin"
              class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary"
            >{{ t('endpointManager_builtinBadge') }}</span>
            <span
              v-if="row.egressKernel"
              class="rounded bg-primary px-1.5 py-0.5 text-[10px] font-bold text-white"
              data-testid="endpoint-egress-badge"
            >{{ t('endpointManager_egressBadge') }}</span>
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
            <!-- 健康徽标三词分明(spec 二期不变量 16):停用(灰,enabled=0 最高
                 优先)/熔断(红,tooltip 恢复于 HH:MM;锁存无冷却不画时间)/限流
                 (蓝灰,tooltip 限流至 HH:MM)/正常(绿)。 -->
            <span
              class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold"
              :class="endpointToneClass(row.health.tone)"
              data-testid="endpoint-health-badge"
              :title="row.health.tooltipKey
                ? t(row.health.tooltipKey, { time: row.health.tooltipTime })
                : ''"
            >{{ t(row.health.key) }}</span>
            <span class="text-[10px] text-slate-400">
              {{ t('endpointManager_priority') }} {{ row.priority }}
              · {{ t('endpointManager_inflightLabel') }} {{ row.inflight }}/{{ row.maxConcurrency === 0 ? '∞' : row.maxConcurrency }}
            </span>
            <!-- 行尾运行态:调用/失败计数 + last_used_at 相对时间(二期 §7.1) -->
            <span class="text-[10px] text-slate-400" data-testid="endpoint-usage-tail">
              {{ t('endpointManager_totalRequests') }} {{ row.totalRequests }}
              · {{ t('endpointManager_failedRequests') }} {{ row.failedRequests }}
              · {{ t(row.lastUsedKey, row.lastUsedParams) }}
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
            <!-- 行上激活开关(升格为一等公民):点击 = PUT enabled 快速路径,
                 不进编辑弹窗;builtin 行开关照常可用(停 builtin=只停兜底行)。 -->
            <button
              type="button"
              role="switch"
              data-field="rowEnabledToggle"
              :aria-checked="row.enabled ? 'true' : 'false'"
              :aria-label="t('endpointManager_enabled')"
              class="relative inline-flex h-6 w-11 shrink-0 items-center rounded-full transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-60"
              :class="row.enabled ? 'bg-primary' : 'bg-slate-300 dark:bg-slate-600'"
              :disabled="actionPendingId === row.id"
              @click="toggleEnabled(row)"
            >
              <span
                class="inline-block h-4 w-4 transform rounded-full bg-white shadow transition-transform"
                :class="row.enabled ? 'translate-x-6' : 'translate-x-1'"
              ></span>
            </button>
            <!-- 出口指定(spec 二期 §7.1):anthropic 行显示;当前出口行变
                 「取消出口」;换绑即事务生效。非 anthropic 行不显示。 -->
            <button
              v-if="row.protocolAnthropic"
              type="button"
              class="inline-flex h-9 items-center rounded-lg border px-2.5 text-xs font-semibold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:cursor-not-allowed disabled:opacity-60"
              :class="row.egressKernel
                ? 'border-primary/40 bg-primary/10 font-bold text-primary hover:bg-primary/20'
                : 'border-slate-200 text-slate-600 hover:border-primary/40 hover:bg-primary/5 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-primary/10'"
              :disabled="actionPendingId === row.id"
              @click="toggleEgress(row)"
            >{{ row.egressKernel ? t('endpointManager_unsetEgress') : t('endpointManager_setEgress') }}</button>
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
                :disabled="editingBuiltin"
                class="mt-1.5 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 font-mono text-xs font-normal text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
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
            <span class="flex items-center justify-between">
              <span class="text-xs font-semibold text-slate-600 dark:text-slate-300">{{ t('endpointManager_models') }}</span>
              <button
                type="button"
                class="inline-flex h-7 items-center rounded-md border border-primary/30 px-2.5 text-[11px] font-bold text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
                :disabled="form.busy || formBusy || probingModels"
                :title="t('endpointManager_modelsAutoHint')"
                @click="runAutoFetchModels"
              >{{ probingModels ? t('endpointManager_modelsAutoRunning') : t('endpointManager_modelsAuto') }}</button>
            </span>
            <textarea
              v-model="form.modelsText"
              rows="4"
              data-field="models"
              :placeholder="t('endpointManager_modelsPlaceholder')"
              class="mt-1.5 w-full rounded-lg border border-slate-200 bg-white px-3 py-2 font-mono text-xs text-slate-800 focus:border-primary/50 focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
            ></textarea>
            <span class="mt-1 block text-[10px] leading-4 text-slate-400">{{ t('endpointManager_modelsHint') }}</span>
          </label>

          <!-- enabled 勾选已移除(二期 §7.1):行上开关取代,编辑弹窗不再
               承担激活语义;PUT 不带 enabled 键 = 不改。 -->
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
  probeEndpointModels,
  resetEndpointHealth,
  setEndpointEgressKernel,
  setEndpointPolicy,
  testEndpoint,
  updateEndpoint,
} from '../../services/agentApi';
import {
  endpointToPayload,
  formatClockTime,
  formatLastUsedAt,
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

// 健康徽标决策纯函数(二期三词分明,spec 不变量 16):停用/熔断/限流/正常,
// 四态互斥按优先级短路——
//   1. enabled=0 → 停用(灰):用户停用是最高优先级(不变量 11),健康列如何
//      都不再展示,避免「停用但显示正常」的混词;
//   2. health_status 非 ok → 熔断(红):含阈值摘除(cooldown_until>now,tooltip
//      「恢复于 HH:MM」)与 401/402 锁存(cooldown_until=0,无冷却,不画时间);
//      legacy 'cooldown' 值(一期历史行)按非 ok 同路处理;
//   3. rate_limited_until>now → 限流(蓝灰,tooltip「限流至 HH:MM」):429 不改
//      健康态,一期靠 last_error 文案猜,二期起 Task 6 已把该字段出 REST 面,
//      按时间窗权威判定(窗过即回正常,不看陈旧错误文案);
//   4. 其余 → 正常(绿)。
// now 可注入(毫秒,纯测);cooldown_until/rate_limited_until 是 Go 侧 unix 秒
// (time.Now().Unix()),比较时 *1000 对齐毫秒。tooltipTime 是 formatClockTime
// 折出的本地 HH:MM。
export function endpointHealthBadge(row, now = Date.now()) {
  const nowMs = typeof now === 'number' && Number.isFinite(now) && now > 0 ? now : Date.now();
  if (row && row.enabled === false) {
    return { key: 'endpointManager_disabledBadge', tone: 'muted' };
  }
  const status = String(row?.healthStatus || 'ok');
  const cooldownUntil = Number(row?.cooldownUntil) || 0; // unix 秒,0 = 无冷却(锁存)
  const rateLimitedUntil = Number(row?.rateLimitedUntil) || 0;
  if (status !== 'ok') {
    return cooldownUntil * 1000 > nowMs
      ? {
        key: 'endpointManager_healthDisabled',
        tone: 'error',
        tooltipKey: 'endpointManager_tooltipRecoverAt',
        tooltipTime: formatClockTime(cooldownUntil),
      }
      : { key: 'endpointManager_healthDisabled', tone: 'error' };
  }
  if (rateLimitedUntil * 1000 > nowMs) {
    return {
      key: 'endpointManager_healthRateLimited',
      tone: 'muted',
      tooltipKey: 'endpointManager_tooltipLimitedUntil',
      tooltipTime: formatClockTime(rateLimitedUntil),
    };
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
  };
}

// 编辑态的 key:表单留空 = 沿用存储值;自动获取探测仍要带真 key 上行,
// 所以优先显式输入,否则回退编辑对象的存储 key(行上带的是掩码,不可用;
// 此时探测不带 key,上游 401 由摘要提示用户先填 key)。
function effectiveApiKeyForProbe(form) {
  const typed = String(form.apiKey || '').trim();
  if (typed) return typed;
  return '';
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
      probingModels: false,
      policy: emptyPolicy(),
      policyError: '',
      policySaving: false,
    };
  },
  computed: {
    rows() {
      return this.endpoints.map((view) => {
        const lastUsed = formatLastUsedAt(view.lastUsedAt);
        return {
          ...view,
          health: endpointHealthBadge(view),
          lastUsedKey: lastUsed.key,
          lastUsedParams: lastUsed.params,
          protocolBadges: [
            ...(view.protocolAnthropic ? [{ label: 'A' }] : []),
            ...(view.protocolOpenAI ? [{ label: 'O' }] : []),
          ],
        };
      });
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
        // enabled 不进表单(二期 §7.1):行上开关取代,PUT 载荷不带该键 = 不改。
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
    // 表单内「自动获取」:按当前填写的 base_url+key 裸探测(行未落库场景)。
    // 结果直接回填 modelsText(值可见可改),不自动保存——落库仍走表单「保存」。
    async runAutoFetchModels() {
      if (this.probingModels) return;
      const base = String(this.form.baseUrl || '').trim();
      if (!base) {
        this.setNotice(this.t('endpointManager_modelsAutoNeedUrl'), false);
        return;
      }
      this.probingModels = true;
      try {
        const models = await probeEndpointModels(base, effectiveApiKeyForProbe(this.form));
        const ids = models.filter((m) => typeof m === 'string' && m.trim());
        if (!ids.length) {
          this.setNotice(this.t('endpointManager_modelsAutoEmpty'), false);
          return;
        }
        this.form.modelsText = ids.join('\n');
        this.setNotice(this.t('endpointManager_modelsAutoDone', { count: ids.length }), true);
      } catch (error) {
        this.setNotice(this.t('endpointManager_fetchFailed', { error: error?.message || '' }), false);
      } finally {
        this.probingModels = false;
      }
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
    // 行上激活开关快速路径(二期 §7.1):PUT 只带 enabled 一键(服务端全字段
    // 可选合并,零值=不改),激活清零健康逻辑在服务层此路径触发;完成后刷新
    // 列表拿新 enabled/健康态。不进编辑弹窗。
    async toggleEnabled(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      try {
        await updateEndpoint(row.id, { enabled: !row.enabled });
        await this.loadEndpoints();
      } catch (error) {
        showErrorDialog(this.t('endpointManager_actionFailed', { error: error?.message || '' }));
      } finally {
        this.actionPendingId = '';
      }
    },
    // 出口指定换绑(二期 §7.1):anthropic 行设为 AliangCode 出口 / 出口行取消。
    // set 语义取「当前态取反」,POST set-egress-kernel 事务换绑,刷新列表拿
    // egress_kernel 新分布(徽标与按钮文案随行数据走)。
    async toggleEgress(row) {
      if (this.actionPendingId) return;
      this.actionPendingId = row.id;
      const setting = row.egressKernel !== true;
      try {
        await setEndpointEgressKernel(row.id, setting);
        this.setNotice(
          setting
            ? this.t('endpointManager_egressSet', { name: row.name })
            : this.t('endpointManager_egressUnset', { name: row.name }),
          true,
        );
        await this.loadEndpoints();
      } catch (error) {
        showErrorDialog(this.t('endpointManager_actionFailed', { error: error?.message || '' }));
      } finally {
        this.actionPendingId = '';
      }
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
