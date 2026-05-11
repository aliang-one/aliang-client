<template>
  <div
    v-if="currentPage === 'dashboard'"
    id="dashboard-page"
    class="page-container content-section active flex flex-row flex-1 min-w-0 h-full overflow-hidden"
  >
    <aside
      class="w-80 lg:w-96 bg-white dark:bg-slate-900 border-r border-slate-200 dark:border-slate-800 flex flex-col h-full overflow-y-auto custom-scrollbar"
    >
      <div
        class="p-8 border-b border-slate-100 dark:border-slate-800 bg-slate-50/50 dark:bg-slate-800/20 transition-colors"
        :class="!isAuthenticated ? 'cursor-pointer hover:bg-primary/5 dark:hover:bg-primary/10' : ''"
        :role="!isAuthenticated ? 'button' : undefined"
        :tabindex="!isAuthenticated ? 0 : undefined"
        @click="handleAccountCardClick"
        @keydown.enter.prevent="handleAccountCardClick"
        @keydown.space.prevent="handleAccountCardClick"
      >
        <div class="flex items-center gap-4">
          <div class="relative group">
            <div
              class="size-14 ring-2 ring-primary ring-offset-2 dark:ring-offset-slate-900 rounded-xl overflow-hidden shadow-sm transition-transform duration-300 group-hover:scale-105 flex items-center justify-center bg-gradient-to-br from-primary to-emerald-500 text-white"
            >
              <span class="text-lg font-bold uppercase tracking-[0.2em] ml-[0.2em]">{{ userAvatarText }}</span>
            </div>
            <div
              class="absolute -bottom-1 -right-1 size-4 bg-primary border-2 border-white dark:border-slate-900 rounded-full"
            ></div>
          </div>
          <div>
            <div class="flex items-center gap-1.5">
              <h2 class="font-bold text-slate-900 dark:text-white tracking-tight">{{ userDisplayName }}</h2>
            </div>
            <div class="flex items-center gap-2 mt-1">
              <span
                class="px-2 py-0.5 bg-primary/10 text-primary text-[10px] font-bold rounded uppercase tracking-wider"
              >
                {{ planLabel }}
              </span>
            </div>
            <p class="text-slate-400 text-[11px] mt-1.5 font-medium">{{ accountSubtitle }}</p>
          </div>
        </div>
        <div
          class="mt-4 rounded-lg border px-3 py-2 text-xs"
          :class="isAuthenticated ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-900 dark:bg-emerald-950/40 dark:text-emerald-300' : 'border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-900 dark:bg-amber-950/40 dark:text-amber-300'"
        >
          <div class="flex items-center justify-between gap-3">
            <span>{{ authNotice }}</span>
            <button
              v-if="!isAuthenticated"
              type="button"
              class="shrink-0 rounded-md bg-white/80 px-2.5 py-1 text-[11px] font-semibold text-amber-700 transition hover:bg-white dark:bg-slate-900/70 dark:text-amber-200 dark:hover:bg-slate-900"
              @click.stop="openLoginModal"
            >
              {{ t('dash_loginNow') }}
            </button>
          </div>
        </div>
      </div>
      <div class="p-8 flex flex-col items-center justify-center gap-6">
        <div class="relative">
          <div class="absolute -inset-4 rounded-full scale-110 transition-all" :class="powerButtonHaloClass"></div>
          <button
            type="button"
            :disabled="powerButtonDisabled"
            :title="powerButtonTitle"
            class="relative size-24 rounded-full border flex items-center justify-center transition-all disabled:cursor-not-allowed"
            :class="powerButtonClass"
            @click="toggleProxyPower"
          >
            <span
              v-if="runActionLoading"
              class="inline-block size-8 border-4 border-white/30 border-t-white rounded-full animate-spin"
            ></span>
            <span v-else class="material-symbols-outlined text-4xl font-bold">power_settings_new</span>
          </button>
        </div>
        <div class="text-center">
          <p class="text-sm font-semibold text-slate-600 dark:text-slate-400">{{ proxyStatusTitle }}</p>
          <p class="text-xs text-slate-400 mt-1">{{ runActionLoading ? powerButtonBusyText : proxyStatusSubtitle }}</p>
        </div>
      </div>
      <div class="mx-6 p-4 bg-slate-50 dark:bg-slate-800/50 rounded border border-slate-100 dark:border-slate-700">
        <div class="flex justify-between items-start mb-3">
          <p class="text-xs font-bold text-slate-500 uppercase">{{ t('dash_networkStatus') }}</p>
          <span
            v-if="certLoading"
            class="inline-block size-4 border-2 border-slate-200 border-t-slate-400 rounded-full animate-spin"
          ></span>
          <span
            v-else
            class="material-symbols-outlined text-sm"
            :class="networkStatusIconClass"
          >{{ networkStatusIcon }}</span>
        </div>
          <div class="space-y-2">
            <div class="flex justify-between text-sm">
              <span class="text-slate-500">{{ t('dash_mode') }}</span>
              <span class="font-medium text-slate-700 dark:text-slate-200">{{ runModeLabel }}</span>
            </div>
          <div class="flex justify-between text-sm">
            <span class="text-slate-500">{{ t('dash_protocol') }}</span>
            <span class="font-medium text-slate-700 dark:text-slate-200">SOCKS5</span>
          </div>
          <div class="flex justify-between text-sm">
            <span class="text-slate-500">{{ t('dash_certificate') }}</span>
            <span class="font-medium" :class="certBadgeClass">{{ certBadgeText }}</span>
          </div>
        </div>
        <div class="grid grid-cols-2 gap-2 mt-4">
          <button
            type="button"
            class="px-3 py-1.5 text-xs font-bold border border-slate-200 dark:border-slate-600 rounded hover:bg-white dark:hover:bg-slate-700 transition-colors"
            @click="openCertModal"
          >
            {{ t('dash_details') }}
          </button>
          <button
            type="button"
            class="px-3 py-1.5 text-xs font-bold bg-primary/10 text-primary rounded hover:bg-primary/20 transition-colors"
            @click="handleReinstall"
          >
            {{ t('dash_reinstall') }}
          </button>
        </div>
      </div>
      <div class="mt-8 px-6 space-y-3">
        <p class="text-[10px] font-bold text-slate-400 uppercase tracking-widest px-1">{{ t('dash_quickTools') }}</p>
        <div class="group relative">
          <button
            type="button"
            :disabled="!isAuthenticated"
            @click="openQuickSetup"
            class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
            :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 hover:border-slate-200 dark:hover:border-slate-700' : ''"
          >
            <span class="material-symbols-outlined text-slate-400 text-lg">bolt</span>
            {{ t('dash_quickSetup') }}
          </button>
        </div>
        <!-- Chat feature hidden temporarily
        <button
          type="button"
          :disabled="!isAuthenticated"
          @click="openQuickChat"
          class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
          :class="!isAuthenticated ? 'cursor-not-allowed opacity-60 hover:border-slate-200 dark:hover:border-slate-700' : ''"
        >
          <span class="material-symbols-outlined text-slate-400 text-lg">chat_bubble</span>
          {{ t('dash_quickChat') }}
        </button>
        -->
        <button
          type="button"
          class="w-full flex items-center gap-3 px-4 py-2.5 bg-white dark:bg-slate-900 border border-slate-200 dark:border-slate-700 rounded text-sm font-medium hover:border-primary transition-colors"
          @click="handleShowSettings"
        >
          <span class="material-symbols-outlined text-slate-400 text-lg">settings</span>
          {{ t('dash_moreSettings') }}
        </button>
      </div>
      <div class="mt-auto p-6 border-t border-slate-100 dark:border-slate-800 bg-slate-50/30 dark:bg-slate-800/10">
        <div class="flex justify-between items-center mb-3">
          <span class="text-xs font-bold text-slate-500 uppercase tracking-wider">{{ t('dash_accountBalance') }}</span>
          <span class="text-lg font-bold text-slate-900 dark:text-white">{{ accountBalanceText }}</span>
        </div>
        <button
          type="button"
          class="w-full py-2 bg-slate-900 dark:bg-primary text-white text-xs font-bold rounded hover:opacity-90 transition-opacity"
          @click="handleTopUp"
        >
          {{ t('dash_topUpFunds') }}
        </button>
        <p
          class="mt-2 text-center text-[10px] italic transition-colors"
          :class="accountBalanceHintClass"
        >
          {{ accountBalanceHint }}
        </p>
      </div>
    </aside>
    <main class="flex-1 min-w-0 flex flex-col h-full bg-background-light dark:bg-background-dark overflow-hidden">
      <header
        class="h-16 bg-white dark:bg-slate-900 border-b border-slate-200 dark:border-slate-800 flex items-center justify-between px-8 shrink-0"
      >
        <div class="flex items-center gap-4">
          <div class="size-8 bg-primary rounded-lg flex items-center justify-center text-white shadow-sm">
            <span class="material-symbols-outlined">api</span>
          </div>
          <h1 class="font-bold text-xl tracking-tight">
            ALiang
            <span class="text-primary font-medium">Gateway</span>
          </h1>
        </div>
        <div class="flex items-center gap-4">
          <div class="rounded-xl border border-slate-200 bg-slate-50/90 px-3 py-2 dark:border-slate-700 dark:bg-slate-800/70">
            <div class="flex items-center gap-3">
              <div class="flex size-10 items-center justify-center rounded-lg" :class="serverLinkIconWrapClass">
                <span class="material-symbols-outlined text-lg" :class="serverLinkIconClass">{{ serverLinkIcon }}</span>
              </div>
              <div class="min-w-[210px]">
                <div class="flex items-center gap-2">
                  <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-400">{{ t('dash_serverLink') }}</p>
                  <span class="rounded-full px-2 py-0.5 text-[10px] font-bold" :class="serverLinkBadgeClass">
                    {{ serverLinkBadgeText }}
                  </span>
                </div>
                <div class="mt-1 grid grid-cols-3 gap-2 text-[11px]">
                  <div>
                    <p class="text-slate-400">{{ t('dash_scoreLatency') }}</p>
                    <p class="font-semibold text-slate-700 dark:text-slate-100">{{ serverLatencyLabel }}</p>
                  </div>
                  <div>
                    <p class="text-slate-400">{{ t('dash_mode') }}</p>
                    <p class="font-semibold text-slate-700 dark:text-slate-100">{{ runModeLabel }}</p>
                  </div>
                  <div>
                    <p class="text-slate-400">{{ t('dash_state') }}</p>
                    <p class="font-semibold" :class="serverStateTextClass">{{ serverStateLabel }}</p>
                  </div>
                </div>
                <p class="mt-2 text-[10px] text-slate-400" :title="serverLinkDetailText">
                  {{ serverLinkDetailText }}
                </p>
              </div>
            </div>
          </div>
          <div class="h-8 w-px bg-slate-200 dark:bg-slate-800 mx-2"></div>
          <button
            type="button"
            class="px-4 py-1.5 bg-primary/10 text-primary text-xs font-bold rounded-lg hover:bg-primary/20 transition-colors"
            @click="refreshDashboardView"
          >
            {{ t('dash_refreshDashboard') }}
          </button>
        </div>
      </header>
      <div class="flex-1 overflow-y-auto p-8 custom-scrollbar">
        <div class="grid grid-cols-1 lg:grid-cols-2 gap-8 mb-8">
          <div
            class="bg-white dark:bg-slate-900 p-6 rounded border border-slate-200 dark:border-slate-800 shadow-sm"
          >
            <div class="flex justify-between items-center mb-6">
              <h3 class="font-bold text-slate-700 dark:text-slate-300">{{ t('dash_modelUsageDistribution') }}</h3>
              <button type="button" class="text-slate-400 hover:text-primary">
                <span class="material-symbols-outlined">more_horiz</span>
              </button>
            </div>
            <div v-if="dashboardError" class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">
              {{ dashboardError }}
            </div>
            <div v-else-if="dashboardLoading" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-10 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
              {{ t('dash_loadingModelUsage') }}
            </div>
            <div v-else-if="!modelDistribution.length" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-10 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
              {{ t('dash_noModelData') }}
            </div>
            <div v-else class="flex items-center gap-8">
              <div class="relative size-40 flex items-center justify-center">
                <svg class="size-full transform -rotate-90" viewBox="0 0 36 36">
                  <title>Model usage distribution donut chart</title>
                  <circle cx="18" cy="18" fill="transparent" r="15.9" stroke="#e2e8f0" stroke-width="3"></circle>
                  <circle
                    v-for="item in modelDistribution"
                    :key="item.label"
                    cx="18"
                    cy="18"
                    fill="transparent"
                    r="15.9"
                    :stroke="item.color"
                    :stroke-dasharray="item.dashArray"
                    :stroke-dashoffset="item.dashOffset"
                    stroke-width="3"
                  ></circle>
                </svg>
                <div class="absolute inset-0 flex flex-col items-center justify-center">
                  <span class="text-2xl font-bold">{{ formatCount(totalRequestCount) }}</span>
                  <span class="text-[10px] text-slate-400 uppercase">{{ t('dash_requests') }}</span>
                </div>
              </div>
              <div class="flex-1 space-y-2">
                <div
                  v-for="item in modelDistribution"
                  :key="`${item.label}-legend`"
                  class="flex items-center justify-between text-xs"
                >
                  <div class="flex items-center gap-2">
                    <span class="size-2 rounded-full" :style="{ backgroundColor: item.color }"></span>
                    <span class="truncate">{{ item.label }}</span>
                  </div>
                  <span class="font-bold">{{ item.percent.toFixed(0) }}%</span>
                </div>
              </div>
            </div>
          </div>
          <div
            class="bg-white dark:bg-slate-900 p-6 rounded border border-slate-200 dark:border-slate-800 shadow-sm"
          >
            <div class="flex justify-between items-center mb-6">
              <h3 class="font-bold text-slate-700 dark:text-slate-300">{{ t('dash_usageTrend') }}</h3>
              <div class="flex gap-2">
                <span class="px-2 py-0.5 bg-primary/10 text-primary text-[10px] font-bold rounded">LIVE</span>
                <span
                  class="px-2 py-0.5 bg-slate-100 dark:bg-slate-800 text-slate-500 text-[10px] font-bold rounded"
                >
                  {{ t('dash_requests') }}                </span>
              </div>
            </div>
            <div v-if="dashboardLoading" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-10 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
              {{ t('dash_loadingTrend') }}
            </div>
            <div v-else-if="!trendPoints.length" class="rounded-lg border border-dashed border-slate-300 bg-slate-50 px-4 py-10 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-400">
              {{ t('dash_noTrendData') }}
            </div>
            <div v-else class="h-40 w-full relative">
              <svg class="w-full h-full" preserveAspectRatio="none" viewBox="0 0 100 40">
                <title>Usage trend line chart</title>
                <path
                  :d="trendPath"
                  fill="none"
                  stroke="#21c45d"
                  stroke-width="1.5"
                ></path>
                <path
                  :d="trendAreaPath"
                  fill="url(#grad1)"
                  stroke="none"
                ></path>
                <defs>
                  <linearGradient id="grad1" x1="0%" x2="0%" y1="0%" y2="100%">
                    <stop offset="0%" style="stop-color: rgba(33, 196, 93, 0.2); stop-opacity: 1"></stop>
                    <stop offset="100%" style="stop-color: rgba(33, 196, 93, 0); stop-opacity: 1"></stop>
                  </linearGradient>
                </defs>
                <line
                  stroke="#e2e8f0"
                  stroke-dasharray="1"
                  stroke-width="0.1"
                  x1="0"
                  x2="100"
                  y1="35"
                  y2="35"
                ></line>
                <line
                  stroke="#e2e8f0"
                  stroke-dasharray="1"
                  stroke-width="0.1"
                  x1="0"
                  x2="100"
                  y1="25"
                  y2="25"
                ></line>
                <line
                  stroke="#e2e8f0"
                  stroke-dasharray="1"
                  stroke-width="0.1"
                  x1="0"
                  x2="100"
                  y1="15"
                  y2="15"
                ></line>
              </svg>
              <div class="absolute bottom-0 w-full flex justify-between text-[10px] text-slate-400 pt-2">
                <span v-for="point in trendPoints" :key="point.label">{{ point.label }}</span>
              </div>
              <div class="absolute left-0 top-0 rounded bg-white/80 px-2 py-1 text-[10px] font-semibold text-slate-500 dark:bg-slate-900/80 dark:text-slate-300">
                {{ trendSummaryText }}
              </div>
            </div>
          </div>
        </div>
        <div class="bg-white dark:bg-slate-900 rounded border border-slate-200 dark:border-slate-800 shadow-sm">
          <div class="p-6 border-b border-slate-100 dark:border-slate-800 flex justify-between items-center">
            <div class="flex items-center gap-3">
              <h3 class="font-bold text-slate-700 dark:text-slate-300">{{ t('dash_recentUsageRecords') }}</h3>
              <span class="bg-slate-100 dark:bg-slate-800 text-slate-500 px-2 py-0.5 rounded text-xs font-medium">
                {{ t('dash_paginationInfo') }}
              </span>
            </div>
            <div class="flex items-center gap-4">
              <button
                type="button"
                class="text-xs font-bold text-slate-500 flex items-center gap-1 hover:text-slate-700 dark:text-slate-300 dark:hover:text-slate-100 disabled:cursor-not-allowed disabled:opacity-50"
                :disabled="dashboardLoading"
                @click="refreshUsageRecords"
              >
                <span class="material-symbols-outlined text-sm">refresh</span>
                {{ t('dash_refresh') }}
              </button>
              <button type="button" class="text-xs font-bold text-primary flex items-center gap-1 hover:underline" @click="exportUsageRecords">
                <span class="material-symbols-outlined text-sm">download</span>
                {{ t('dash_exportData') }}
              </button>
            </div>
          </div>
          <div class="px-6 py-3 border-b border-slate-100 dark:border-slate-800 bg-slate-50/50 dark:bg-slate-800/30">
            <div class="flex flex-wrap items-center gap-2">
              <select
                v-model="requestFilter"
                class="h-9 rounded border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-3 text-xs text-slate-600 dark:text-slate-300"
                @change="applyUsageFilters"
              >
                <option value="all">{{ t('dash_filterAll') }}</option>
                <option value="chat">{{ t('dash_filterChat') }}</option>
                <option value="stream">{{ t('dash_filterStream') }}</option>
                <option value="image">{{ t('dash_filterImage') }}</option>
              </select>
              <input
                v-model="pathSearch"
                type="text"
                :placeholder="t('dash_searchPlaceholder')"
                class="h-9 min-w-[260px] flex-1 rounded border border-slate-200 dark:border-slate-700 bg-white dark:bg-slate-900 px-3 text-xs text-slate-700 dark:text-slate-300 placeholder:text-slate-400"
                @keydown.enter.prevent="applyUsageFilters"
              />
              <button
                type="button"
                class="h-9 rounded bg-slate-900 px-3 text-xs font-semibold text-white transition hover:bg-slate-700 dark:bg-slate-100 dark:text-slate-900 dark:hover:bg-slate-200"
                :disabled="dashboardLoading"
                @click="applyUsageFilters"
              >
                {{ t('dash_applyFilters') }}
              </button>
              <button
                type="button"
                class="h-9 rounded border border-slate-200 bg-white px-3 text-xs font-semibold text-slate-600 transition hover:border-slate-300 hover:text-slate-800 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:border-slate-600 dark:hover:text-slate-100"
                :disabled="dashboardLoading || (requestFilter === 'all' && !pathSearch.trim())"
                @click="resetUsageFilters"
              >
                {{ t('dash_reset') }}
              </button>
            </div>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full text-left">
              <thead
                class="bg-slate-50 dark:bg-slate-800/50 text-slate-400 text-[10px] font-bold uppercase tracking-wider"
              >
                <tr>
                  <th class="px-6 py-3">{{ t('dash_type') }}</th>
                  <th class="px-6 py-3">{{ t('dash_model') }}</th>
                  <th class="px-6 py-3">{{ t('dash_endpoint') }}</th>
                  <th class="px-6 py-3">{{ t('dash_apiKey') }}</th>
                  <th class="px-6 py-3">{{ t('dash_group') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_inputTokens') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_outputTokens') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_totalTokens') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_actualCost') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_duration') }}</th>
                  <th class="px-6 py-3 text-right">{{ t('dash_timestamp') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-slate-100 dark:divide-slate-800 text-sm">
                <tr
                  v-for="item in filteredRequestRows"
                  :key="`${item.id}-${item.createdAt}-${item.endpoint}`"
                  class="hover:bg-slate-50 dark:hover:bg-slate-800/30"
                >
                  <td class="px-6 py-4 font-bold text-slate-700 dark:text-slate-300">{{ item.requestType }}</td>
                  <td class="px-6 py-4 text-slate-500">{{ item.model }}</td>
                  <td class="px-6 py-4 text-slate-500"><code>{{ item.endpoint }}</code></td>
                  <td class="px-6 py-4 text-slate-500">{{ item.apiKeyName }}</td>
                  <td class="px-6 py-4 text-slate-500">{{ item.groupName }}</td>
                  <td class="px-6 py-4 text-right text-slate-500 tabular-nums">{{ formatCount(item.inputTokens) }}</td>
                  <td class="px-6 py-4 text-right text-slate-500 tabular-nums">{{ formatCount(item.outputTokens) }}</td>
                  <td class="px-6 py-4 text-right text-slate-500 tabular-nums">{{ formatCount(item.totalTokens) }}</td>
                  <td class="px-6 py-4 text-right text-slate-500 tabular-nums">{{ formatCurrency(item.actualCost) }}</td>
                  <td class="px-6 py-4 text-right text-slate-500 tabular-nums">{{ formatDuration(item.durationMs) }}</td>
                  <td class="px-6 py-4 text-right text-slate-400 tabular-nums">{{ formatDateTime(item.createdAt) }}</td>
                </tr>
                <tr v-if="filteredRequestRows.length === 0">
                  <td colspan="11" class="px-6 py-6 text-center text-xs text-slate-400">{{ t('dash_noMatchingRecords') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div
            class="p-4 bg-slate-50 dark:bg-slate-800/50 flex flex-col gap-3 border-t border-slate-100 dark:border-slate-800 sm:flex-row sm:items-center sm:justify-between"
          >
            <p class="text-xs text-slate-500 dark:text-slate-400">
              {{ t('dash_showingRecords', { shown: filteredRequestRows.length, total: formatCount(usageTotal), page: usagePage }) }}
            </p>
            <div class="flex items-center justify-end gap-2">
              <button
                type="button"
                class="h-9 rounded border border-slate-200 bg-white px-3 text-xs font-semibold text-slate-600 transition hover:border-slate-300 hover:text-slate-800 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:border-slate-600 dark:hover:text-slate-100"
                :disabled="dashboardLoading || usagePage <= 1"
                @click="changeUsagePage(usagePage - 1)"
              >
                {{ t('dash_prev') }}
              </button>
              <div class="min-w-[120px] text-center text-xs font-semibold text-slate-500 dark:text-slate-300">
                {{ usagePage }} / {{ usageTotalPages }}
              </div>
              <button
                type="button"
                class="h-9 rounded border border-slate-200 bg-white px-3 text-xs font-semibold text-slate-600 transition hover:border-slate-300 hover:text-slate-800 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:border-slate-600 dark:hover:text-slate-100"
                :disabled="dashboardLoading || usagePage >= usageTotalPages"
                @click="changeUsagePage(usagePage + 1)"
              >
                {{ t('dash_next') }}
              </button>
            </div>
          </div>
        </div>
      </div>
    </main>

    <div
      v-if="isLoginModalOpen"
      class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/60 p-4 backdrop-blur-sm"
      @click.self="closeLoginModal"
    >
      <div class="w-full max-w-md overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
        <div class="border-b border-slate-200 bg-slate-50/80 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/60">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-xs font-bold uppercase tracking-[0.2em] text-primary">{{ t('dash_accountAccess') }}</p>
              <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ t('dash_loginModalTitle') }}</h3>
              <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
                {{ t('dash_loginModalDesc') }}
              </p>
            </div>
            <button
              type="button"
              class="rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              :disabled="loginPending"
              @click="closeLoginModal"
            >
              <span class="material-symbols-outlined text-lg">close</span>
            </button>
          </div>
        </div>

        <form class="space-y-4 p-5" @submit.prevent="submitLogin">
          <div class="rounded-xl border border-dashed border-amber-200 bg-amber-50/70 p-4 text-xs text-amber-700 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">
            {{ t('dash_loginNotLoggedIn') }}
          </div>

          <div>
            <label class="mb-1 block text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('user_email') }}</label>
            <input
              v-model.trim="loginEmail"
              type="email"
              autocomplete="username"
              class="h-11 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
              placeholder="name@example.com"
              :disabled="loginPending"
            />
          </div>

          <div>
            <label class="mb-1 block text-xs font-semibold uppercase tracking-wide text-slate-500">{{ t('user_password') }}</label>
            <input
              v-model="loginPassword"
              type="password"
              autocomplete="current-password"
              class="h-11 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
              :placeholder="t('user_passwordPh')"
              :disabled="loginPending"
            />
          </div>

          <p v-if="loginError" class="text-xs text-rose-500">{{ loginError }}</p>

          <div class="flex gap-3 pt-1">
            <button
              type="button"
              class="inline-flex h-11 flex-1 items-center justify-center rounded-lg border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
              :disabled="loginPending"
              @click="closeLoginModal"
            >
              {{ t('dash_cancel') }}
            </button>
            <button
              type="submit"
              class="inline-flex h-11 flex-1 items-center justify-center rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="loginPending"
            >
              {{ loginPending ? t('dash_loggingIn') : t('dash_login') }}
            </button>
          </div>
        </form>
      </div>
    </div>

    <div
      v-if="tunStartModal.visible"
      class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/70 p-4 backdrop-blur-sm"
      @click.self="closeTunStartModal"
    >
      <div class="w-full max-w-3xl overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
        <div class="border-b border-slate-200 bg-slate-50/80 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/60">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-xs font-bold uppercase tracking-[0.2em] text-primary">Deep Mode Startup</p>
              <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ tunStartModal.title }}</h3>
              <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ tunStartModal.detail }}</p>
            </div>
            <button
              type="button"
              class="rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-60 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              @click="closeTunStartModal"
            >
              <span class="material-symbols-outlined text-lg">close</span>
            </button>
          </div>
        </div>

        <div class="space-y-5 p-5">
          <div class="rounded-2xl border px-4 py-4"
            :class="tunStartModal.status === 'error'
              ? 'border-red-200 bg-red-50 dark:border-red-500/30 dark:bg-red-500/10'
              : tunStartModal.status === 'success'
                ? 'border-emerald-200 bg-emerald-50 dark:border-emerald-500/30 dark:bg-emerald-500/10'
                : 'border-primary/20 bg-primary/5 dark:border-primary/30 dark:bg-primary/10'"
          >
            <div class="flex items-center gap-3">
              <span
                v-if="tunStartModal.status === 'starting'"
                class="inline-block size-5 border-2 border-primary/30 border-t-primary rounded-full animate-spin"
              ></span>
              <span
                v-else
                class="material-symbols-outlined text-lg"
                :class="tunStartModal.status === 'error' ? 'text-red-500' : 'text-emerald-500'"
              >
                {{ tunStartModal.status === 'error' ? 'error' : 'check_circle' }}
              </span>
              <div class="flex-1">
                <div class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ tunStartModal.statusLabel }}</div>
                <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">{{ tunStartModal.statusHint }}</div>
              </div>
            </div>

            <div class="mt-4 h-2 overflow-hidden rounded-full bg-white/70 dark:bg-slate-900/70">
              <div
                class="h-full rounded-full transition-all duration-300"
                :class="tunStartModal.status === 'error' ? 'bg-red-500' : 'bg-primary'"
                :style="{ width: `${tunStartupProgressPercent}%` }"
              ></div>
            </div>
          </div>

          <div class="grid gap-3 md:grid-cols-3">
            <div
              v-for="step in tunStartupSteps"
              :key="step.key"
              class="rounded-xl border px-4 py-3"
              :class="step.state === 'done'
                ? 'border-emerald-200 bg-emerald-50 dark:border-emerald-500/30 dark:bg-emerald-500/10'
                : step.state === 'active'
                  ? 'border-primary/30 bg-primary/5 dark:border-primary/40 dark:bg-primary/10'
                  : step.state === 'error'
                    ? 'border-red-200 bg-red-50 dark:border-red-500/30 dark:bg-red-500/10'
                    : 'border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/50'"
            >
              <div class="flex items-center gap-2">
                <span
                  class="material-symbols-outlined text-base"
                  :class="step.state === 'done'
                    ? 'text-emerald-500'
                    : step.state === 'active'
                      ? 'text-primary'
                      : step.state === 'error'
                        ? 'text-red-500'
                        : 'text-slate-400'"
                >{{ step.icon }}</span>
                <p class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ step.label }}</p>
              </div>
              <p class="mt-2 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ step.description }}</p>
            </div>
          </div>

          <div v-if="tunStartModal.errorMessage" class="rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200">
            {{ tunStartModal.errorMessage }}
          </div>

          <div v-if="tunStartModal.maxRetries > 0" class="rounded-xl border border-slate-200 bg-slate-50 px-4 py-3 text-sm text-slate-700 dark:border-slate-700 dark:bg-slate-800/50 dark:text-slate-200">
            {{ t('dash_retryProgress', { current: tunStartModal.retryCount || 0, max: tunStartModal.maxRetries }) }}
          </div>

          <div v-if="tunStartModal.errors.length > 0" class="rounded-2xl border border-amber-200 bg-amber-50 px-4 py-3 dark:border-amber-500/30 dark:bg-amber-500/10">
            <p class="text-sm font-semibold text-amber-800 dark:text-amber-200">{{ t('dash_capturedErrors') }}</p>
            <div class="mt-2 space-y-1">
              <p
                v-for="(item, index) in tunStartModal.errors"
                :key="`${index}-${item}`"
                class="text-xs leading-5 text-amber-700 dark:text-amber-100 break-all"
              >
                {{ item }}
              </p>
            </div>
          </div>

          <div class="overflow-hidden rounded-2xl border border-slate-200 dark:border-slate-700">
            <div class="flex items-center justify-between gap-3 border-b border-slate-200 bg-slate-50/80 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/60">
              <div>
                <p class="text-sm font-semibold text-slate-900 dark:text-slate-100">{{ t('dash_startupLogs') }}</p>
                <p class="text-xs text-slate-500 dark:text-slate-400">{{ t('dash_startupLogsHint') }}</p>
              </div>
              <span class="rounded-full bg-slate-900 px-2.5 py-1 text-[10px] font-bold uppercase tracking-wide text-white dark:bg-slate-100 dark:text-slate-900">
                {{ t('dash_lines', { count: tunStartModal.logs.length }) }}
              </span>
            </div>
            <div class="max-h-80 overflow-y-auto bg-slate-950 p-4 font-mono text-xs text-slate-200">
              <div v-if="tunStartModal.logs.length === 0" class="rounded-lg border border-dashed border-slate-700 bg-slate-900/60 px-4 py-6 text-sm text-slate-500">
                {{ t('dash_waitingForLogs') }}
              </div>
              <div v-else class="space-y-1 whitespace-pre-wrap break-all">
                <div
                  v-for="(entry, index) in tunStartModal.logs"
                  :key="`${entry.timestamp}-${entry.source}-${index}`"
                  class="rounded px-2 py-1.5"
                  :class="logEntryRowClass(entry.level)"
                >
                  <span class="text-slate-500">[{{ entry.timestamp }}]</span>
                  <span class="ml-2 font-semibold" :class="logEntryLevelClass(entry.level)">{{ entry.level }}</span>
                  <span class="ml-2 text-slate-400">({{ entry.source }})</span>
                  <span class="ml-2">{{ entry.message }}</span>
                </div>
              </div>
            </div>
          </div>

          <div class="flex justify-end gap-3">
            <button
              type="button"
              class="inline-flex h-11 items-center justify-center rounded-lg border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
              @click="closeTunStartModal"
            >
              {{ tunStartModal.status === 'success' ? 'Close' : 'Done' }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, ref, onMounted, onUnmounted, watch } from 'vue';
import { useI18n } from '../i18n';
import { useCertStatus } from '../composables/useCertStatus';
import { useNavigation } from '../composables/useNavigation';
import { useRunStatus } from '../composables/useRunStatus';
import { useSoftwareUpdate } from '../composables/useSoftwareUpdate';
import { useAuthStore } from '../stores/auth';
import { getUserCenterProfile } from '../services/userCenterApi';
import { extractUsagePagination, extractUsageRecords } from '../utils/dashboardData';
import {
  getDashboardHealth,
  getDashboardModels,
  getDashboardStats,
  getDashboardTrend,
  getDashboardUsageRecords
} from '../services/dashboardApi';
import { getAliangLinkStatus } from '../services/runApi';

const { t } = useI18n();
const isProdBuild = import.meta.env.PROD || ['prod', 'production'].includes(String(import.meta.env.MODE || '').toLowerCase());
const frontEndLogsFetchLimit = isProdBuild ? 200 : 800;

const { certStatus, loading: certLoading, startPolling: startCertPolling, stopPolling: stopCertPolling } = useCertStatus();
const { currentPage, showSettings } = useNavigation();
const { isAuthenticated, user, userDisplayName, planLabel, authNotice, loginPending, loginError, loginWithPassword } = useAuthStore();
const {
  runMode,
  runIsRunning,
  runStatus,
  runDescription,
  runSyncError,
  startupStatus,
  refreshRunState,
  syncRunStatus,
  syncStartupStatus,
  startPolling: startRunStatusPolling,
  stopPolling: stopRunStatusPolling
} = useRunStatus();
const {
  softwareUpdateStatus,
  blockingSoftwareUpdate,
  applySoftwareUpdateStatus,
  refreshSoftwareUpdateStatus,
  openSoftwareUpdateModal
} = useSoftwareUpdate();

const emit = defineEmits(['openQuickSetup', 'openCertModal', 'startCertReinstall']);

const requestFilter = ref('all');
const pathSearch = ref('');
const isLoginModalOpen = ref(false);
const loginEmail = ref('');
const loginPassword = ref('');
const runActionLoading = ref(false);
const runActionIntent = ref('');   // 'start' | 'stop' | ''
const runActionMessage = ref('');

// External state changes (e.g. tray quit, auto-exit) update runIsRunning via polling.
// Clear stale action message so the subtitle reflects the real state immediately.
watch(runIsRunning, () => {
  if (!runActionLoading.value) {
    runActionMessage.value = '';
  }
});
const accountBalance = ref(null);
const tunStartModal = ref(createTunStartModalState());
const dashboardLoading = ref(false);
const dashboardError = ref('');
const dashboardStats = ref({});
const dashboardModels = ref([]);
const dashboardTrend = ref([]);
const dashboardUsageRecords = ref([]);
const usagePage = ref(1);
const usagePageSize = ref(10);
const usageTotal = ref(0);
const usageTotalPages = ref(1);
const usageMaxPages = 20;
const appliedRequestFilter = ref('all');
const serverLinkState = ref('unknown');
const serverLinkPending = ref(false);
const serverLinkLatencyMs = ref(null);
const serverLinkTCPConnectMs = ref(null);
const serverLinkTLSHandshakeMs = ref(null);
const serverLinkProbeTotalMs = ref(null);
const serverLinkLastCheckedAt = ref(0);
const serverLinkLastConnectedAt = ref(0);
const serverLinkHighLatencyThresholdMs = ref(null);
const serverLinkConsecutiveFailures = ref(0);
const serverLinkError = ref('');
const serverHealthScore = ref(null);
let tunStartLogTimer = null;
let tunStartStatusTimer = null;
let tunStartBackendTimer = null;
let tunStartAutoCloseTimer = null;
let serverLinkTimer = null;

const serverLinkOnline = computed(() => ['connected', 'degraded'].includes(serverLinkState.value));

const accountSubtitle = computed(() => {
  if (!isAuthenticated.value) {
    return t('dash_accountSubtitleGuest');
  }
  if (user.value?.status) {
    return t('dash_accountStatus', { status: user.value.status });
  }
  if (user.value?.createdAt) {
    return t('dash_memberSince', { date: user.value.createdAt });
  }
  return t('dash_sessionActive');
});

const userAvatarText = computed(() => {
  const emailPrefix = typeof user.value?.email === 'string' ? user.value.email.trim().slice(0, 2) : '';
  if (emailPrefix) {
    return emailPrefix;
  }

  const usernamePrefix = typeof user.value?.username === 'string' ? user.value.username.trim().slice(0, 2) : '';
  if (usernamePrefix) {
    return usernamePrefix;
  }

  return 'GU';
});

const accountBalanceText = computed(() => {
  const value = Number(accountBalance.value);
  if (!Number.isFinite(value)) {
    return isAuthenticated.value ? '--' : '$0.00';
  }
  return `$${value.toFixed(2)}`;
});

const accountBalanceHint = computed(() => {
  const currentVersion = softwareUpdateStatus.value.current_version || '--';
  const latestVersion = softwareUpdateStatus.value.latest_version || '';

  if (softwareUpdateStatus.value.needs_update) {
    if (softwareUpdateStatus.value.force_update) {
      return latestVersion
        ? t('dash_versionUpdateRequired', { current: currentVersion, latest: latestVersion })
        : t('dash_versionForceUpdate', { current: currentVersion });
    }
    return latestVersion
      ? t('dash_versionUpdateAvailable', { current: currentVersion, latest: latestVersion })
      : t('dash_versionUpdateGeneric', { current: currentVersion });
  }

  if (!softwareUpdateStatus.value.current_version) {
    return t('dash_versionChecking');
  }

  return t('dash_versionCurrent', { current: currentVersion });
});

const accountBalanceHintClass = computed(() => {
  if (softwareUpdateStatus.value.force_update) {
    return 'text-red-500';
  }
  if (softwareUpdateStatus.value.needs_update) {
    return 'text-amber-500';
  }
  return 'text-slate-400';
});

const totalRequestCount = computed(() => Number(dashboardStats.value?.total_requests || 0));

const modelDistribution = computed(() => {
  const items = Array.isArray(dashboardModels.value) ? dashboardModels.value : [];
  const total = items.reduce((sum, item) => sum + Number(item?.requests || 0), 0);
  if (total <= 0) {
    return [];
  }

  const palette = ['#21c45d', '#10b981', '#34d399', '#6ee7b7', '#a7f3d0'];
  const topItems = items
    .slice()
    .sort((left, right) => Number(right?.requests || 0) - Number(left?.requests || 0))
    .slice(0, 5)
    .map((item, index) => ({
      label: item?.model || `Model ${index + 1}`,
      requests: Number(item?.requests || 0),
      percent: (Number(item?.requests || 0) / total) * 100,
      color: palette[index] || palette[palette.length - 1]
    }));

  let cumulative = 0;
  return topItems.map((item) => {
    const segment = {
      ...item,
      dashArray: `${item.percent} 100`,
      dashOffset: -cumulative
    };
    cumulative += item.percent;
    return segment;
  });
});

const trendPoints = computed(() => {
  const items = Array.isArray(dashboardTrend.value) ? dashboardTrend.value : [];
  if (!items.length) {
    return [];
  }
  const maxRequests = Math.max(...items.map((item) => Number(item?.requests || 0)), 1);
  const step = items.length > 1 ? 100 / (items.length - 1) : 100;
  return items.map((item, index) => ({
    x: Number((index * step).toFixed(2)),
    y: Number((35 - ((Number(item?.requests || 0) / maxRequests) * 25)).toFixed(2)),
    label: formatTrendLabel(item?.date),
    requests: Number(item?.requests || 0),
    actualCost: Number(item?.actual_cost || item?.cost || 0)
  }));
});

const trendPath = computed(() => {
  if (!trendPoints.value.length) {
    return '';
  }
  return trendPoints.value
    .map((point, index) => `${index === 0 ? 'M' : 'L'}${point.x} ${point.y}`)
    .join(' ');
});

const trendAreaPath = computed(() => {
  if (!trendPoints.value.length) {
    return '';
  }
  const linePath = trendPath.value;
  return `${linePath} L 100 40 L 0 40 Z`;
});

const trendSummaryText = computed(() => {
  if (!trendPoints.value.length) {
    return t('dash_noRecentTrend');
  }
  const latestPoint = trendPoints.value[trendPoints.value.length - 1];
  return t('dash_requestsInBucket', { count: latestPoint.requests });
});

const serverLinkBadgeText = computed(() => {
  if (serverLinkPending.value) {
    return t('dash_serverLinkChecking');
  }
  if (serverLinkState.value === 'degraded') {
    return t('dash_serverLinkHighLatency');
  }
  if (serverLinkState.value === 'connected') {
    return t('dash_serverLinkOnline');
  }
  if (serverLinkState.value === 'disconnected') {
    return t('dash_serverLinkOffline');
  }
  return t('dash_unknown');
});

const serverLinkBadgeClass = computed(() => {
  if (serverLinkPending.value) {
    return 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300';
  }
  if (serverLinkState.value === 'degraded') {
    return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
  }
  if (serverLinkState.value === 'connected') {
    return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
  }
  if (serverLinkState.value === 'disconnected') {
    return 'bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-300';
  }
  return 'bg-slate-100 text-slate-600 dark:bg-slate-700/40 dark:text-slate-300';
});

const serverLinkIcon = computed(() => {
  if (serverLinkPending.value) {
    return 'network_ping';
  }
  if (serverLinkState.value === 'degraded') {
    return 'warning';
  }
  if (serverLinkState.value === 'connected') {
    return 'cloud_done';
  }
  if (serverLinkState.value === 'disconnected') {
    return 'cloud_off';
  }
  return 'help';
});

const serverLinkIconClass = computed(() => {
  if (serverLinkPending.value) {
    return 'text-sky-600 dark:text-sky-300';
  }
  if (serverLinkState.value === 'degraded') {
    return 'text-amber-600 dark:text-amber-300';
  }
  if (serverLinkState.value === 'connected') {
    return 'text-emerald-600 dark:text-emerald-300';
  }
  if (serverLinkState.value === 'disconnected') {
    return 'text-red-600 dark:text-red-300';
  }
  return 'text-slate-500 dark:text-slate-300';
});

const serverLinkIconWrapClass = computed(() => {
  if (serverLinkPending.value) {
    return 'bg-sky-100 dark:bg-sky-500/10';
  }
  if (serverLinkState.value === 'degraded') {
    return 'bg-amber-100 dark:bg-amber-500/10';
  }
  if (serverLinkState.value === 'connected') {
    return 'bg-emerald-100 dark:bg-emerald-500/10';
  }
  if (serverLinkState.value === 'disconnected') {
    return 'bg-red-100 dark:bg-red-500/10';
  }
  return 'bg-slate-100 dark:bg-slate-700/40';
});

const serverLatencyLabel = computed(() => {
  if (serverLinkPending.value) {
    return '...';
  }
  if (typeof serverLinkLatencyMs.value === 'number') {
    const latency = `${Math.round(serverLinkLatencyMs.value)} ms`;
    if (typeof serverHealthScore.value === 'number') {
      return `${serverHealthScore.value}/${latency}`;
    }
    return latency;
  }
  if (typeof serverHealthScore.value === 'number') {
    return `${serverHealthScore.value}/--`;
  }
  return '--';
});

const serverStateLabel = computed(() => {
  if (serverLinkPending.value || serverLinkState.value === 'connecting') {
    return t('dash_serverProbing');
  }
  if (serverLinkState.value === 'degraded') {
    return t('dash_serverHighLatency');
  }
  if (runIsRunning.value && serverLinkOnline.value) {
    return t('dash_serverServing');
  }
  if (serverLinkState.value === 'connected') {
    return t('dash_serverLinked');
  }
  if (serverLinkState.value === 'disconnected') {
    return t('dash_serverNoLink');
  }
  if (startupStatus.value === 'READY' || startupStatus.value === 'CONFIGURED') {
    return t('dash_serverReady');
  }
  return startupStatus.value === 'UNKNOWN' ? t('dash_serverLinkChecking') : startupStatus.value;
});

const serverStateTextClass = computed(() => {
  if (serverLinkPending.value || serverLinkState.value === 'connecting') {
    return 'text-sky-600 dark:text-sky-300';
  }
  if (serverLinkState.value === 'degraded') {
    return 'text-amber-600 dark:text-amber-300';
  }
  if (runIsRunning.value && serverLinkOnline.value) {
    return 'text-emerald-600 dark:text-emerald-300';
  }
  if (serverLinkState.value === 'connected') {
    return 'text-emerald-600 dark:text-emerald-300';
  }
  if (serverLinkState.value === 'disconnected') {
    return 'text-red-600 dark:text-red-300';
  }
  return 'text-slate-700 dark:text-slate-100';
});

async function sampleServerLink() {
  serverLinkPending.value = true;

  try {
    const [linkEnvelope, healthEnvelope] = await Promise.allSettled([
      getAliangLinkStatus({ probe: true }),
      getDashboardHealth()
    ]);

    if (linkEnvelope.status === 'fulfilled') {
      const linkData = linkEnvelope.value?.data || {};
      serverLinkState.value = typeof linkData.state === 'string' ? linkData.state : 'unknown';
      serverLinkLatencyMs.value = typeof linkData.latency_ms === 'number' ? linkData.latency_ms : null;
      serverLinkTCPConnectMs.value = typeof linkData.tcp_connect_ms === 'number' ? linkData.tcp_connect_ms : null;
      serverLinkTLSHandshakeMs.value = typeof linkData.tls_handshake_ms === 'number' ? linkData.tls_handshake_ms : null;
      serverLinkProbeTotalMs.value = typeof linkData.probe_total_ms === 'number' ? linkData.probe_total_ms : null;
      serverLinkLastCheckedAt.value = Number(linkData.last_checked_at || 0);
      serverLinkLastConnectedAt.value = Number(linkData.last_connected_at || 0);
      serverLinkConsecutiveFailures.value = Number(linkData.consecutive_failures || 0);
      serverLinkHighLatencyThresholdMs.value = Number(linkData.high_latency_threshold || 0) || null;
      serverLinkError.value = typeof linkData.last_error === 'string' ? linkData.last_error : '';
    } else {
      serverLinkState.value = 'disconnected';
      serverLinkLatencyMs.value = null;
      serverLinkTCPConnectMs.value = null;
      serverLinkTLSHandshakeMs.value = null;
      serverLinkProbeTotalMs.value = null;
      serverLinkLastCheckedAt.value = Date.now();
      serverLinkLastConnectedAt.value = 0;
      serverLinkConsecutiveFailures.value = 0;
      serverLinkHighLatencyThresholdMs.value = null;
      serverLinkError.value = linkEnvelope.reason instanceof Error ? linkEnvelope.reason.message : 'mTLS link probe failed';
    }

    if (healthEnvelope.status === 'fulfilled') {
      serverHealthScore.value = healthEnvelope.value?.data?.health_score ?? null;
    } else {
      serverHealthScore.value = null;
    }
  } catch {
    serverLinkState.value = 'disconnected';
    serverLinkLatencyMs.value = null;
    serverLinkTCPConnectMs.value = null;
    serverLinkTLSHandshakeMs.value = null;
    serverLinkProbeTotalMs.value = null;
    serverLinkError.value = 'mTLS link probe failed';
    serverHealthScore.value = null;
    serverLinkLastCheckedAt.value = Date.now();
  } finally {
    serverLinkPending.value = false;
  }
}

async function refreshDashboardView() {
  await Promise.allSettled([
    sampleServerLink(),
    loadDashboardUsageData(),
    syncAccountBalance(),
    refreshRunState()
  ]);
}

const serverLinkDetailText = computed(() => {
  if (serverLinkPending.value) {
    return t('dash_serverLinkProbing');
  }
  if (serverLinkError.value) {
    return t('dash_serverLinkError', { error: serverLinkError.value });
  }
  if (serverLinkState.value === 'degraded' && typeof serverLinkHighLatencyThresholdMs.value === 'number') {
    return `Probe total time is above ${serverLinkHighLatencyThresholdMs.value} ms.`;
  }
  if (serverLinkState.value === 'connected' || serverLinkState.value === 'degraded') {
    const parts = [];
    if (typeof serverLinkTCPConnectMs.value === 'number') {
      parts.push(`TCP ${Math.round(serverLinkTCPConnectMs.value)} ms`);
    }
    if (typeof serverLinkTLSHandshakeMs.value === 'number') {
      parts.push(`TLS ${Math.round(serverLinkTLSHandshakeMs.value)} ms`);
    }
    if (typeof serverLinkProbeTotalMs.value === 'number') {
      parts.push(`total ${Math.round(serverLinkProbeTotalMs.value)} ms`);
    }
    if (parts.length > 0) {
      return parts.join(' · ');
    }
    return 'Latest handshake completed successfully.';
  }
  if (serverLinkLastConnectedAt.value > 0) {
    return t('dash_serverLinkOffline', { time: formatRelativeTimestamp(serverLinkLastConnectedAt.value) });
  }
  return t('dash_serverLinkNoHandshake');
});

function formatRelativeTimestamp(timestamp) {
  const value = Number(timestamp);
  if (!Number.isFinite(value) || value <= 0) {
    return 'recently';
  }

  const diff = Date.now() - value;
  const seconds = Math.max(0, Math.round(diff / 1000));
  if (seconds < 5) {
    return 'just now';
  }
  if (seconds < 60) {
    return `${seconds}s ago`;
  }

  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return `${minutes}m ago`;
  }

  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return `${hours}h ago`;
  }

  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

async function syncAccountBalance() {
  if (!isAuthenticated.value) {
    accountBalance.value = null;
    return;
  }

  try {
    const envelope = await getUserCenterProfile();
    if (envelope?.status === 'success') {
      const nextBalance = Number(envelope?.data?.balance);
      accountBalance.value = Number.isFinite(nextBalance) ? nextBalance : 0;
      return;
    }
    accountBalance.value = null;
  } catch {
    accountBalance.value = null;
  }
}

function handleTopUp() {
  window.open('https://www.aliang.one', '_blank', 'noopener,noreferrer');
}

async function loadDashboardUsageData() {
  if (!isAuthenticated.value) {
    dashboardStats.value = {};
    dashboardModels.value = [];
    dashboardTrend.value = [];
    dashboardUsageRecords.value = [];
    usagePage.value = 1;
    usageTotal.value = 0;
    usageTotalPages.value = 1;
    dashboardError.value = '';
    return;
  }

  dashboardLoading.value = true;
  dashboardError.value = '';

  try {
    const [statsEnvelope, trendEnvelope, modelsEnvelope, usageEnvelope] = await Promise.all([
      getDashboardStats(),
      getDashboardTrend(),
      getDashboardModels(),
      getDashboardUsageRecords({
        page: usagePage.value,
        perPage: usagePageSize.value,
        requestType: appliedRequestFilter.value
      })
    ]);

    dashboardStats.value = asObject(statsEnvelope?.data);
    dashboardTrend.value = Array.isArray(trendEnvelope?.data?.trend) ? trendEnvelope.data.trend : [];
    dashboardModels.value = Array.isArray(modelsEnvelope?.data?.models) ? modelsEnvelope.data.models : [];
    dashboardUsageRecords.value = extractUsageRecords(usageEnvelope?.data)
      .slice(0, usagePageSize.value)
      .map(normalizeUsageRecord);
    const pagination = extractUsagePagination(usageEnvelope?.data, usagePage.value, usagePageSize.value);
    usagePage.value = pagination.page;
    usageTotal.value = pagination.total;
    usageTotalPages.value = Math.min(Math.max(pagination.totalPages, 1), usageMaxPages);
  } catch (error) {
    dashboardError.value = error instanceof Error ? error.message : 'Failed to load dashboard usage data.';
    dashboardStats.value = {};
    dashboardModels.value = [];
    dashboardTrend.value = [];
    dashboardUsageRecords.value = [];
    usageTotal.value = 0;
    usageTotalPages.value = 1;
  } finally {
    dashboardLoading.value = false;
  }
}

function refreshUsageRecords() {
  loadDashboardUsageData();
}

function applyUsageFilters() {
  usagePage.value = 1;
  appliedRequestFilter.value = requestFilter.value;
  loadDashboardUsageData();
}

function resetUsageFilters() {
  requestFilter.value = 'all';
  pathSearch.value = '';
  appliedRequestFilter.value = 'all';
  usagePage.value = 1;
  loadDashboardUsageData();
}

function changeUsagePage(nextPage) {
  const targetPage = Number(nextPage || 1);
  if (!Number.isFinite(targetPage)) {
    return;
  }
  const boundedPage = Math.min(Math.max(1, targetPage), Math.min(Math.max(usageTotalPages.value, 1), usageMaxPages));
  if (boundedPage === usagePage.value) {
    return;
  }
  usagePage.value = boundedPage;
  loadDashboardUsageData();
}

function normalizeUsageRecord(item) {
  const raw = item && typeof item === 'object' ? item : {};
  return {
    id: raw.id || '',
    model: typeof raw.model === 'string' ? raw.model : '-',
    endpoint: typeof raw.inbound_endpoint === 'string' ? raw.inbound_endpoint : '-',
    apiKeyName: typeof raw?.api_key?.name === 'string' ? raw.api_key.name : '-',
    groupName: typeof raw?.group?.name === 'string' ? raw.group.name : '-',
    requestType: typeof raw.request_type === 'string' ? raw.request_type : '-',
    stream: Boolean(raw.stream),
    inputTokens: Number(raw.input_tokens || 0),
    outputTokens: Number(raw.output_tokens || 0),
    totalTokens: Number(raw.input_tokens || 0) + Number(raw.output_tokens || 0) + Number(raw.cache_creation_tokens || 0) + Number(raw.cache_read_tokens || 0),
    actualCost: Number(raw.actual_cost || 0),
    durationMs: Number(raw.duration_ms || 0),
    firstTokenMs: Number(raw.first_token_ms || 0),
    createdAt: typeof raw.created_at === 'string' ? raw.created_at : ''
  };
}

function asObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : {};
}

function formatTrendLabel(value) {
  if (typeof value !== 'string' || !value) {
    return '';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

function formatCount(value) {
  const numeric = Number(value || 0);
  if (!Number.isFinite(numeric)) {
    return '0';
  }
  return numeric.toLocaleString();
}

function formatCurrency(value) {
  const numeric = Number(value || 0);
  if (!Number.isFinite(numeric)) {
    return '$0.00';
  }
  return `$${numeric.toFixed(2)}`;
}

function formatDateTime(value) {
  if (typeof value !== 'string' || !value) {
    return '-';
  }
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return date.toLocaleString();
}

function exportUsageRecords() {
  const payload = JSON.stringify(dashboardUsageRecords.value, null, 2);
  const blob = new Blob([payload], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = 'dashboard-usage-records.json';
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function openQuickSetup() {
  if (!isAuthenticated.value) {
    return;
  }
  emit('openQuickSetup');
}

function handleAccountCardClick() {
  if (isAuthenticated.value) {
    return;
  }
  openLoginModal();
}

function openLoginModal() {
  if (isAuthenticated.value) {
    return;
  }
  isLoginModalOpen.value = true;
}

function closeLoginModal() {
  if (loginPending.value) {
    return;
  }
  isLoginModalOpen.value = false;
  loginPassword.value = '';
}

async function submitLogin() {
  const success = await loginWithPassword({
    email: loginEmail.value,
    password: loginPassword.value
  });

  if (success) {
    closeLoginModal();
  }
}

function handleDashboardKeydown(event) {
  if (!(event instanceof KeyboardEvent) || event.key !== 'Escape') {
    return;
  }

  if (isLoginModalOpen.value && !loginPending.value) {
    closeLoginModal();
    return;
  }

  if (tunStartModal.value.visible) {
    closeTunStartModal();
  }
}

function openCertModal() {
  emit('openCertModal');
}

function handleReinstall() {
  if (!confirm(t('dash_confirmReinstall'))) {
    return;
  }
  emit('openCertModal');
  setTimeout(() => emit('startCertReinstall'), 300);
}

function handleShowSettings() {
  showSettings();
}

const runModeLabel = computed(() => {
  if (runMode.value === 'tun') return t('dash_tunMode');
  if (runMode.value === 'http') return t('dash_httpMode');
  return t('dash_runModeUnknown');
});

const proxyStatusTitle = computed(() => {
  if (runSyncError.value) return t('dash_proxyStatusUnknown');
  if (runIsRunning.value) return t('dash_proxyActive');
  if (!canStartProxy.value) return t('dash_proxyNotReady');
  return t('dash_proxyInactive');
});

const proxyStatusSubtitle = computed(() => {
  if (runActionMessage.value) return runActionMessage.value;
  if (runSyncError.value) return t('dash_syncFailed', { error: runSyncError.value });
  if (blockingSoftwareUpdate.value) {
    return t('dash_disabledMandatoryUpdate', { version: softwareUpdateStatus.value.latest_version || 'latest version' });
  }
  if (!isAuthenticated.value) {
    return t('dash_disabledLoginRequired');
  }
  if (!canStartProxy.value) {
    switch (startupStatus.value) {
      case 'UNCONFIGURED':
        return t('dash_disabledUnconfigured');
      case 'CONFIGURING':
        return t('dash_disabledConfiguring');
      default:
        return t('dash_disabledStatusBlocked', { status: startupStatus.value });
    }
  }
  if (runDescription.value) return runDescription.value;
  if (runStatus.value) return runStatus.value;
  return runIsRunning.value ? t('dash_serviceRunning') : t('dash_serviceStopped');
});

const powerButtonBusyText = computed(() => {
  if (runActionIntent.value === 'stop') return t('dash_stoppingProxy');
  if (runActionIntent.value === 'start') return t('dash_startingProxy');
  return runIsRunning.value ? t('dash_stoppingProxy') : t('dash_startingProxy');
});

const canStartProxy = computed(() => {
  if (!isAuthenticated.value) {
    return false;
  }
  if (blockingSoftwareUpdate.value) {
    return false;
  }
  return startupStatus.value === 'READY' || startupStatus.value === 'CONFIGURED';
});

const powerButtonTitle = computed(() => {
  if (runActionLoading.value) return powerButtonBusyText.value;
  if (powerButtonDisabled.value && !runIsRunning.value) return proxyStatusSubtitle.value;
  return runIsRunning.value ? t('dash_stopProxy') : t('dash_startProxy');
});

const powerButtonHaloClass = computed(() => {
  if (powerButtonDisabled.value && !runIsRunning.value) {
    return 'bg-slate-300/50 dark:bg-slate-700/50';
  }
  if (runIsRunning.value) {
    return 'bg-rose-500/20';
  }
  return 'bg-primary/20';
});

const powerButtonClass = computed(() => {
  if (powerButtonDisabled.value && !runIsRunning.value) {
    return 'border-slate-400 bg-slate-300 text-slate-500 shadow-none dark:border-slate-600 dark:bg-slate-700 dark:text-slate-400';
  }
  if (runIsRunning.value) {
    return 'border-rose-400 bg-rose-500 text-white shadow-lg shadow-rose-500/40 hover:bg-rose-500/90';
  }
  return 'border-primary/70 bg-primary text-white shadow-lg shadow-primary/40 hover:bg-primary/90';
});

const powerButtonDisabled = computed(() => {
  if (runActionLoading.value) return true;
  if (runIsRunning.value) return false;
  return !canStartProxy.value;
});

const tunStartupPhase = computed(() => {
  if (!tunStartModal.value.visible) {
    return 'idle';
  }
  if (tunStartModal.value.status === 'success') {
    return 'running';
  }
  if (tunStartModal.value.status === 'error') {
    return 'error';
  }
  if (tunStartModal.value.phase === 'installing_dependency') {
    return 'installing_dependency';
  }
  if (tunStartModal.value.phase === 'switching_mode') {
    return 'switching_mode';
  }
  if ([
    'monitoring_device',
    'creating_tun',
    'resolving_gateway',
    'configuring_interface',
    'waiting_device_ready',
    'requesting_permission',
  ].includes(tunStartModal.value.phase)) {
    return 'creating_tun';
  }
  if (tunStartModal.value.phase === 'configuring_routes') {
    return 'finalizing_startup';
  }
  return 'requested';
});

const tunStartupProgressPercent = computed(() => {
  if (typeof tunStartModal.value.progressPercent === 'number' && Number.isFinite(tunStartModal.value.progressPercent)) {
    return Math.max(0, Math.min(100, tunStartModal.value.progressPercent));
  }
  switch (tunStartupPhase.value) {
    case 'installing_dependency':
      switch (tunStartModal.value.installState) {
        case 'queued':
          return 16;
        case 'downloading':
          return 28;
        case 'extracting':
          return 42;
        case 'installing':
          return 58;
        case 'installed':
          return 72;
        default:
          return 20;
      }
    case 'switching_mode':
      return 82;
    case 'requested':
      return 18;
    case 'creating_tun':
      return 56;
    case 'finalizing_startup':
      return 88;
    case 'running':
      return 100;
    case 'error':
      return 100;
    default:
      return 8;
  }
});

function describeTunBackendPhase(phase, options = {}) {
  const retryCount = Number(options.retryCount || 0);
  const maxRetries = Number(options.maxRetries || 0);
  const progressPercent = Number(options.progressPercent || 0);
  const baseMessage = typeof options.message === 'string' ? options.message : '';

  switch (phase) {
    case 'requested':
      return {
        statusLabel: 'Start request accepted',
        statusHint: 'The backend received the TUN startup request and is preparing the sequence.'
      };
    case 'monitoring_device':
      return {
        statusLabel: 'Preparing device monitor',
        statusHint: 'The backend is attaching the TUN device observer before the adapter comes up.'
      };
    case 'creating_tun':
      return {
        statusLabel: maxRetries > 0
          ? `Creating TUN device (${Math.max(retryCount, 1)}/${maxRetries})`
          : 'Creating TUN device',
        statusHint: baseMessage || 'The backend is asking the WireGuard/Wintun layer to create the virtual adapter.'
      };
    case 'resolving_gateway':
      return {
        statusLabel: 'Resolving default gateway',
        statusHint: 'The backend is reading the current system gateway before rewriting TUN routes.'
      };
    case 'requesting_permission':
      return {
        statusLabel: 'Waiting for administrator approval',
        statusHint: 'Windows is showing a UAC prompt. Approve it to continue creating and configuring the virtual adapter.'
      };
    case 'configuring_interface':
      return {
        statusLabel: 'Configuring virtual adapter',
        statusHint: 'The backend is assigning the TUN interface address, metric, and adapter state.'
      };
    case 'waiting_device_ready':
      return {
        statusLabel: 'Waiting for virtual adapter readiness',
        statusHint: 'The virtual adapter exists, and the backend is waiting for Windows to report it as ready.'
      };
    case 'configuring_routes':
      return {
        statusLabel: 'Configuring TUN routes',
        statusHint: 'The backend is rewriting system routes so traffic starts flowing through the new TUN adapter.'
      };
    case 'running':
      return {
        statusLabel: 'Deep Mode startup complete',
        statusHint: baseMessage || 'The TUN engine is running and ready to proxy traffic.'
      };
    case 'failed':
      return {
        statusLabel: maxRetries > 0 ? `Deep Mode startup failed after ${retryCount}/${maxRetries} attempts` : 'Deep Mode startup failed',
        statusHint: baseMessage || 'Review the captured errors and logs below for the exact failure point.'
      };
    default:
      return {
        statusLabel: progressPercent > 0 ? `Deep Mode startup ${progressPercent}%` : 'Starting Deep Mode...',
        statusHint: baseMessage || 'We are following backend startup progress and collecting fresh logs for this attempt.'
      };
  }
}

const tunStartupSteps = computed(() => {
  const phase = tunStartupPhase.value;
  const isError = tunStartModal.value.status === 'error';
  const createStepDescription = tunStartModal.value.phase === 'requesting_permission'
    ? 'Windows is asking for administrator approval so the backend can finish creating and configuring the virtual adapter.'
    : tunStartModal.value.phase === 'waiting_device_ready'
      ? 'The backend already created the virtual adapter and is waiting for Windows to report it as ready.'
      : tunStartModal.value.phase === 'configuring_interface'
        ? 'The backend is assigning interface addresses and metrics on the freshly created virtual adapter.'
        : tunStartModal.value.phase === 'resolving_gateway'
          ? 'The backend is resolving the current default gateway before it can safely switch traffic into the TUN path.'
          : 'The backend is creating the TUN interface and asking the OS for network privileges.';
  const finalizeDescription = tunStartModal.value.phase === 'configuring_routes'
    ? 'The backend is applying route changes so packets begin traversing the TUN interface.'
    : 'The proxy waits for the engine to come up and confirms the service is ready to route traffic.';
  if (phase === 'installing_dependency' || phase === 'switching_mode') {
    const installDone = phase !== 'installing_dependency';
    const switchActive = phase === 'switching_mode';
    return [
      {
        key: 'dependency',
        icon: isError && phase === 'installing_dependency' ? 'error' : 'download',
        label: 'Install Wintun Dependency',
        description: 'Download and install the Wintun driver package required by Windows before TUN mode can start.',
        state: isError && phase === 'installing_dependency' ? 'error' : installDone ? 'done' : 'active'
      },
      {
        key: 'switch',
        icon: isError && phase === 'switching_mode' ? 'error' : 'sync_alt',
        label: 'Apply TUN Mode',
        description: 'Switch the backend from HTTP mode to TUN mode after the Wintun dependency becomes available.',
        state: isError && phase === 'switching_mode' ? 'error' : switchActive ? 'active' : installDone ? 'pending' : 'pending'
      },
      {
        key: 'ready',
        icon: isError ? 'error' : 'verified',
        label: 'Finalize Startup',
        description: 'Wait for the TUN engine to confirm that packet routing is ready for real traffic.',
        state: isError && phase !== 'installing_dependency' && phase !== 'switching_mode' ? 'error' : phase === 'running' ? 'done' : 'pending'
      }
    ];
  }
  return [
      {
        key: 'request',
        icon: 'play_circle',
        label: 'Start Requested',
        description: 'The dashboard has sent a TUN startup request to the backend.',
        state: phase === 'requested' ? 'active' : (phase === 'creating_tun' || phase === 'finalizing_startup' || phase === 'running' || isError ? 'done' : 'pending')
      },
      {
        key: 'create',
        icon: 'router',
        label: 'Create TUN Device',
        description: createStepDescription,
        state: phase === 'creating_tun'
          ? (isError ? 'error' : 'active')
          : (phase === 'finalizing_startup' || phase === 'running' ? 'done' : (isError ? 'error' : 'pending'))
      },
      {
        key: 'ready',
        icon: 'verified',
        label: 'Finalize Startup',
        description: finalizeDescription,
        state: phase === 'finalizing_startup'
          ? (isError ? 'error' : 'active')
          : (phase === 'running' ? 'done' : (isError && tunStartModal.value.phase === 'configuring_routes' ? 'error' : 'pending'))
      }
    ];
});

async function toggleProxyPower() {
  if (runActionLoading.value) return;
  runActionLoading.value = true;
  runActionIntent.value = runIsRunning.value ? 'stop' : 'start';
  runActionMessage.value = '';
  runSyncError.value = '';
  try {
    await syncStartupStatus();
    await syncRunStatus();

    const wasRunning = runIsRunning.value;
    const currentMode = runMode.value;

    if (!wasRunning && !canStartProxy.value) {
      throw new Error(proxyStatusSubtitle.value);
    }

    const startingTun = !wasRunning && currentMode === 'tun';
    if (startingTun) {
      openTunStartModal();
    }

    const endpoint = wasRunning ? '/api/run/stop' : '/api/run/start';
    const actionText = wasRunning ? 'stop' : 'start';
    const response = await fetch(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' }
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok) {
      const detail = payload?.msg || payload?.message || payload?.data?.status || `HTTP ${response.status}`;
      throw new Error(`Failed to ${actionText} proxy: ${detail}`);
    }
    if (payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Failed to ${actionText} proxy`);
    }
    const data = payload?.data || {};
    if (data?.status === 'failed') {
      if (data?.error === 'force_update_required' || data?.update_status?.blocking_proxy_start) {
        applySoftwareUpdateStatus(data?.update_status, { openModalIfNeeded: true });
        openSoftwareUpdateModal();
      }
      throw new Error(data?.msg || `Failed to ${actionText} proxy`);
    }

    runActionMessage.value = wasRunning ? t('dash_proxyStoppedSuccessfully') : t('dash_proxyStartSent');
    await syncStartupStatus();
    await syncRunStatus();
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Proxy action failed';
    runSyncError.value = message;
    if (tunStartModal.value.visible) {
      markTunStartModalError(message);
    }
  } finally {
    runActionLoading.value = false;
    runActionIntent.value = '';
    if (runActionMessage.value) {
      window.setTimeout(() => {
        runActionMessage.value = '';
      }, 2500);
    }
  }
}

const certOverallState = computed(() => {
  const s = certStatus.value;
  if (!s) return 'unknown';
  if (s.is_trusted) return 'trusted';
  if (s.is_installed) return 'installed';
  if (s.is_exported) return 'exported';
  return 'not_found';
});

const networkStatusIcon = computed(() => {
  switch (certOverallState.value) {
    case 'trusted': return 'verified';
    case 'installed': return 'shield';
    case 'exported': return 'upload_file';
    case 'not_found': return 'error';
    default: return 'help';
  }
});

const networkStatusIconClass = computed(() => {
  switch (certOverallState.value) {
    case 'trusted': return 'text-emerald-500';
    case 'installed': return 'text-blue-500';
    case 'exported': return 'text-amber-500';
    case 'not_found': return 'text-red-500';
    default: return 'text-slate-400';
  }
});

const certBadgeText = computed(() => {
  switch (certOverallState.value) {
    case 'trusted': return t('dash_certTrusted');
    case 'installed': return t('dash_certInstalled');
    case 'exported': return t('dash_certGenerated');
    case 'not_found': return t('dash_certNotFound');
    default: return t('dash_loading');
  }
});

const certBadgeClass = computed(() => {
  switch (certOverallState.value) {
    case 'trusted': return 'text-emerald-500';
    case 'installed': return 'text-blue-500';
    case 'exported': return 'text-amber-500';
    case 'not_found': return 'text-red-500';
    default: return 'text-slate-400';
  }
});

onMounted(() => {
  startCertPolling();
  startRunStatusPolling();
  refreshSoftwareUpdateStatus().catch(() => {});
  sampleServerLink();
  serverLinkTimer = window.setInterval(sampleServerLink, 15000);
  syncAccountBalance();
  loadDashboardUsageData();
  window.addEventListener('keydown', handleDashboardKeydown);
  window.addEventListener('aliang:tun-progress-open', handleExternalTunProgressOpen);
  window.addEventListener('aliang:tun-progress-update', handleExternalTunProgressUpdate);
  window.addEventListener('aliang:tun-progress-success', handleExternalTunProgressSuccess);
  window.addEventListener('aliang:tun-progress-error', handleExternalTunProgressError);
});

onUnmounted(() => {
  stopCertPolling();
  stopRunStatusPolling();
  stopTunStartObservers();
  if (serverLinkTimer !== null) {
    window.clearInterval(serverLinkTimer);
    serverLinkTimer = null;
  }
  window.removeEventListener('keydown', handleDashboardKeydown);
  window.removeEventListener('aliang:tun-progress-open', handleExternalTunProgressOpen);
  window.removeEventListener('aliang:tun-progress-update', handleExternalTunProgressUpdate);
  window.removeEventListener('aliang:tun-progress-success', handleExternalTunProgressSuccess);
  window.removeEventListener('aliang:tun-progress-error', handleExternalTunProgressError);
});

watch(isAuthenticated, async (authenticated) => {
  if (authenticated) {
    isLoginModalOpen.value = false;
    loginPassword.value = '';
    await Promise.allSettled([
      sampleServerLink(),
      syncAccountBalance(),
      loadDashboardUsageData(),
      refreshRunState()
    ]);
    return;
  }
  accountBalance.value = null;
  await Promise.allSettled([
    sampleServerLink(),
    loadDashboardUsageData(),
    refreshRunState()
  ]);
});

function createTunStartModalState() {
  return {
    visible: false,
    status: 'idle',
    phase: 'startup',
    installState: 'idle',
    title: 'Starting TUN Proxy',
    detail: 'Preparing the TUN engine and waiting for backend startup logs.',
    statusLabel: 'Waiting to start',
    statusHint: 'Press Start to begin a TUN startup attempt.',
    errorMessage: '',
    logs: [],
    startedAtMs: 0,
    progressPercent: null,
    permissionRequired: false,
    errors: [],
    retryCount: 0,
    maxRetries: 0
  };
}

function openTunStartModal(overrides = {}) {
  tunStartModal.value = {
    ...createTunStartModalState(),
    ...overrides,
    visible: true,
    status: 'starting',
    phase: overrides.phase || 'startup',
    installState: overrides.installState || 'idle',
    statusLabel: overrides.statusLabel || 'Starting TUN proxy...',
    statusHint: overrides.statusHint || 'We are following backend startup progress and collecting fresh logs for this attempt.',
    startedAtMs: Date.now(),
    errors: Array.isArray(overrides.errors) ? overrides.errors : [],
    retryCount: Number(overrides.retryCount || 0),
    maxRetries: Number(overrides.maxRetries || 0)
  }
  loadTunStartLogs();
  startTunStartObservers();
}

function updateTunStartModal(overrides = {}) {
  tunStartModal.value = {
    ...tunStartModal.value,
    ...overrides
  }
}

function closeTunStartModal() {
  stopTunStartObservers();
  tunStartModal.value = createTunStartModalState();
}

function markTunStartModalSuccess(message, overrides = {}) {
  tunStartModal.value = {
    ...tunStartModal.value,
    ...overrides,
    status: 'success',
    title: overrides.title || 'TUN Proxy Started',
    detail: overrides.detail || 'The TUN engine reported a successful startup.',
    statusLabel: overrides.statusLabel || 'Startup complete',
    statusHint: typeof message === 'string' && message.trim()
      ? message
      : (overrides.statusHint || 'The service is running and ready to proxy traffic.'),
    errorMessage: ''
  };
  stopTunStartObservers();
  tunStartAutoCloseTimer = window.setTimeout(() => {
    tunStartAutoCloseTimer = null;
    if (tunStartModal.value.status === 'success') {
      closeTunStartModal();
    }
  }, 1000);
}

function markTunStartModalError(message, overrides = {}) {
  tunStartModal.value = {
    ...tunStartModal.value,
    ...overrides,
    status: 'error',
    title: overrides.title || 'TUN Startup Failed',
    detail: overrides.detail || 'The backend could not finish creating the TUN engine.',
    statusLabel: overrides.statusLabel || 'Startup failed',
    statusHint: overrides.statusHint || 'Review the log lines below for the exact failure point.',
    errorMessage: typeof message === 'string' && message.trim() ? message : 'TUN startup failed.'
  };
  stopTunStartObservers();
}

function startTunStartObservers() {
  stopTunStartObservers();
  loadTunStartupBackendStatus();
  tunStartLogTimer = window.setInterval(loadTunStartLogs, 1200);
  tunStartBackendTimer = window.setInterval(loadTunStartupBackendStatus, 250);
  tunStartStatusTimer = window.setInterval(async () => {
    try {
      await syncRunStatus();
    } catch {
      // Keep polling logs so the modal can still show backend progress or failure details.
    }
  }, 1500);
}

function stopTunStartObservers() {
  if (tunStartLogTimer !== null) {
    window.clearInterval(tunStartLogTimer);
    tunStartLogTimer = null;
  }
  if (tunStartStatusTimer !== null) {
    window.clearInterval(tunStartStatusTimer);
    tunStartStatusTimer = null;
  }
  if (tunStartBackendTimer !== null) {
    window.clearInterval(tunStartBackendTimer);
    tunStartBackendTimer = null;
  }
  if (tunStartAutoCloseTimer !== null) {
    window.clearTimeout(tunStartAutoCloseTimer);
    tunStartAutoCloseTimer = null;
  }
}

async function loadTunStartLogs() {
  if (!tunStartModal.value.visible) {
    return;
  }

  try {
    const response = await fetch(`/api/logs?limit=${frontEndLogsFetchLimit}`);
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }

    const nextEntries = Array.isArray(payload?.data?.entries) ? payload.data.entries : [];
    const normalizedEntries = nextEntries
      .map(normalizeLogEntry)
      .filter((entry) => isTunStartupLogEntry(entry, tunStartModal.value.startedAtMs))
      .slice(-120);

    tunStartModal.value = {
      ...tunStartModal.value,
      logs: normalizedEntries
    };
  } catch (error) {
    if (tunStartModal.value.status === 'starting') {
      tunStartModal.value = {
        ...tunStartModal.value,
        errorMessage: error instanceof Error ? error.message : 'Failed to load startup logs.'
      };
    }
  }
}

async function loadTunStartupBackendStatus() {
  if (!tunStartModal.value.visible) {
    return;
  }

  try {
    const response = await fetch('/api/run/tun/status', {
      method: 'GET',
      cache: 'no-store'
    });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }

    const data = payload?.data || {};
    const phaseCopy = describeTunBackendPhase(typeof data.phase === 'string' ? data.phase : tunStartModal.value.phase, {
      retryCount: Number(data.retry_count || 0),
      maxRetries: Number(data.max_retries || 0),
      progressPercent: Number(data.progress_percent || 0),
      message: typeof data.message === 'string' ? data.message : ''
    });
    updateTunStartModal({
      phase: typeof data.phase === 'string' && data.phase ? data.phase : tunStartModal.value.phase,
      progressPercent: Number.isFinite(Number(data.progress_percent)) ? Number(data.progress_percent) : tunStartModal.value.progressPercent,
      permissionRequired: Boolean(data.permission_required),
      errors: Array.isArray(data.errors) ? data.errors : tunStartModal.value.errors,
      retryCount: Number(data.retry_count || 0),
      maxRetries: Number(data.max_retries || 0),
      statusLabel: phaseCopy.statusLabel,
      statusHint: typeof data.error === 'string' && data.error
        ? data.error
        : (Boolean(data.permission_required)
          ? 'Windows is requesting administrator permission to continue TUN startup.'
          : phaseCopy.statusHint)
    });

    if (data.status === 'success') {
      markTunStartModalSuccess(typeof data.message === 'string' ? data.message : 'TUN service started successfully.');
      return;
    }

    if (data.status === 'failed') {
      markTunStartModalError(typeof data.error === 'string' && data.error ? data.error : 'TUN startup failed.', {
        errors: Array.isArray(data.errors) ? data.errors : tunStartModal.value.errors,
        retryCount: Number(data.retry_count || tunStartModal.value.retryCount || 0),
        maxRetries: Number(data.max_retries || tunStartModal.value.maxRetries || 0),
        statusHint: Boolean(data.permission_required)
          ? 'Administrator permission is required or was denied while creating the TUN adapter.'
          : 'Review the log lines below for the exact failure point.'
      });
    }
  } catch (error) {
    if (tunStartModal.value.status === 'starting') {
      updateTunStartModal({
        errorMessage: error instanceof Error ? error.message : 'Failed to sync backend TUN startup state.'
      });
    }
  }
}

function normalizeLogEntry(entry) {
  const raw = entry && typeof entry === 'object' ? entry : {};
  return {
    level: typeof raw.level === 'string' ? raw.level.toUpperCase() : 'INFO',
    timestamp: typeof raw.timestamp === 'string' ? raw.timestamp : '',
    message: typeof raw.message === 'string' ? raw.message : '',
    source: typeof raw.source === 'string' ? raw.source : 'main'
  };
}

function isTunStartupLogEntry(entry, startedAtMs) {
  const message = String(entry.message || '').toLowerCase();
  const timestampMs = parseLogTimestamp(entry.timestamp);
  if (Number.isFinite(startedAtMs) && Number.isFinite(timestampMs) && timestampMs + 1500 < startedAtMs) {
    return false;
  }
  return message.includes('tun') ||
    message.includes('engine') ||
    message.includes('create tun') ||
    message.includes('operation not permitted') ||
    message.includes('启动') ||
    message.includes('回滚');
}

function parseLogTimestamp(value) {
  if (typeof value !== 'string' || !value) {
    return Number.NaN;
  }
  const normalized = value.includes('T') ? value : value.replace(' ', 'T');
  const date = new Date(normalized);
  return Number.isNaN(date.getTime()) ? Number.NaN : date.getTime();
}

function logEntryLevelClass(level) {
  switch (String(level || '').toUpperCase()) {
    case 'ERROR':
    case 'FATAL':
    case 'PANIC':
      return 'text-red-300';
    case 'WARN':
      return 'text-amber-300';
    case 'DEBUG':
      return 'text-sky-300';
    case 'TRACE':
      return 'text-violet-300';
    default:
      return 'text-emerald-300';
  }
}

function logEntryRowClass(level) {
  switch (String(level || '').toUpperCase()) {
    case 'ERROR':
    case 'FATAL':
    case 'PANIC':
      return 'bg-red-500/5';
    case 'WARN':
      return 'bg-amber-500/5';
    case 'DEBUG':
      return 'bg-sky-500/5';
    case 'TRACE':
      return 'bg-violet-500/5';
    default:
      return 'bg-emerald-500/5';
  }
}

function handleExternalTunProgressOpen(event) {
  const detail = event instanceof CustomEvent ? (event.detail || {}) : {};
  openTunStartModal({
    phase: typeof detail.phase === 'string' && detail.phase ? detail.phase : 'startup',
    installState: typeof detail.installState === 'string' && detail.installState ? detail.installState : 'idle',
    title: typeof detail.title === 'string' && detail.title ? detail.title : 'Starting TUN Proxy',
    detail: typeof detail.detail === 'string' && detail.detail ? detail.detail : 'Preparing the TUN engine and waiting for backend startup logs.',
    statusLabel: typeof detail.statusLabel === 'string' && detail.statusLabel ? detail.statusLabel : 'Starting TUN proxy...',
    statusHint: typeof detail.statusHint === 'string' && detail.statusHint ? detail.statusHint : 'We are following backend startup progress and collecting fresh logs for this attempt.'
  });
}

function handleExternalTunProgressUpdate(event) {
  const detail = event instanceof CustomEvent ? (event.detail || {}) : {};
  if (!tunStartModal.value.visible) {
    handleExternalTunProgressOpen(event);
    return;
  }
  updateTunStartModal({
    phase: typeof detail.phase === 'string' && detail.phase ? detail.phase : tunStartModal.value.phase,
    installState: typeof detail.installState === 'string' && detail.installState ? detail.installState : tunStartModal.value.installState,
    title: typeof detail.title === 'string' && detail.title ? detail.title : tunStartModal.value.title,
    detail: typeof detail.detail === 'string' && detail.detail ? detail.detail : tunStartModal.value.detail,
    statusLabel: typeof detail.statusLabel === 'string' && detail.statusLabel ? detail.statusLabel : tunStartModal.value.statusLabel,
    statusHint: typeof detail.statusHint === 'string' && detail.statusHint ? detail.statusHint : tunStartModal.value.statusHint
  });
}

function handleExternalTunProgressSuccess(event) {
  const detail = event instanceof CustomEvent ? (event.detail || {}) : {};
  markTunStartModalSuccess(typeof detail.message === 'string' ? detail.message : '', {
    title: typeof detail.title === 'string' && detail.title ? detail.title : '',
    detail: typeof detail.detail === 'string' && detail.detail ? detail.detail : '',
    statusLabel: typeof detail.statusLabel === 'string' && detail.statusLabel ? detail.statusLabel : '',
    statusHint: typeof detail.statusHint === 'string' && detail.statusHint ? detail.statusHint : ''
  });
}

function handleExternalTunProgressError(event) {
  const detail = event instanceof CustomEvent ? (event.detail || {}) : {};
  markTunStartModalError(typeof detail.message === 'string' ? detail.message : 'TUN startup failed.', {
    title: typeof detail.title === 'string' && detail.title ? detail.title : '',
    detail: typeof detail.detail === 'string' && detail.detail ? detail.detail : '',
    statusLabel: typeof detail.statusLabel === 'string' && detail.statusLabel ? detail.statusLabel : '',
    statusHint: typeof detail.statusHint === 'string' && detail.statusHint ? detail.statusHint : ''
  });
}

const filteredRequestRows = computed(() => {
  const pathKeyword = pathSearch.value.trim().toLowerCase();
  return dashboardUsageRecords.value.filter(item => {
    if (appliedRequestFilter.value === 'chat' && item.requestType !== 'chat') {
      return false;
    }
    if (appliedRequestFilter.value === 'stream' && !item.stream) {
      return false;
    }
    if (appliedRequestFilter.value === 'image' && item.requestType !== 'image') {
      return false;
    }
    if (!pathKeyword) {
      return true;
    }
    return [
      item.endpoint,
      item.model,
      item.apiKeyName,
      item.groupName
    ].some((value) => String(value || '').toLowerCase().includes(pathKeyword));
  });
});

function formatBytes(bytes) {
  const value = Number(bytes || 0);
  if (value < 1024) {
    return `${value} B`;
  }
  if (value < 1024 * 1024) {
    return `${(value / 1024).toFixed(2)} KB`;
  }
  return `${(value / (1024 * 1024)).toFixed(2)} MB`;
}

function formatDuration(ms) {
  return `${Number(ms || 0)} ms`;
}
</script>
