<template>
  <div class="settings-pane" data-pane="logs">
    <div class="mt-4 flex flex-col gap-4">
      <section class="space-y-4">
        <div class="rounded-3xl border border-slate-200/70 bg-white/88 px-5 py-5 shadow-[0_20px_60px_rgba(15,23,42,0.10)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/75">
          <div class="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
            <div class="min-w-0 space-y-3">
              <div
                class="inline-flex items-center gap-2 rounded-full border px-3 py-1 text-[11px] font-semibold uppercase tracking-[0.18em]"
                :class="aiStatusBadgeClass"
              >
                <span class="h-2 w-2 rounded-full" :class="aiStatusDotClass"></span>
                {{ t('status_liveSignal') }}
              </div>
              <div>
                <h2 class="text-2xl font-semibold tracking-tight text-slate-950 dark:text-white">
                  {{ activeSummaryTitle }}
                </h2>
                <p class="mt-2 text-sm leading-6 text-slate-600 dark:text-slate-300">
                  {{ activeSummaryText }}
                </p>
              </div>
              <div class="flex flex-wrap gap-2 text-xs text-slate-500 dark:text-slate-400">
                <span class="rounded-full border border-slate-200 bg-slate-50 px-3 py-1 dark:border-slate-800 dark:bg-slate-900/80">
                  {{ t('status_lastUpdatedCompact') }} {{ summaryUpdatedAgoText }}
                </span>
                <span class="rounded-full border border-slate-200 bg-slate-50 px-3 py-1 dark:border-slate-800 dark:bg-slate-900/80">
                  {{ t('status_trackingWindow', {
                    seconds: statusTTLSeconds,
                    minutes: Math.max(1, Math.round(statusVisibleWindowSeconds / 60))
                  }) }}
                </span>
              </div>
            </div>

            <div class="grid min-w-0 gap-3 sm:grid-cols-3 lg:min-w-[380px]">
              <article
                v-for="metric in compactMetrics"
                :key="metric.key"
                class="rounded-2xl border border-slate-200/80 bg-slate-50/90 p-4 dark:border-slate-800/80 dark:bg-slate-900/70"
              >
                <div class="text-[11px] font-semibold uppercase tracking-[0.16em] text-slate-500 dark:text-slate-400">
                  {{ metric.label }}
                </div>
                <div class="mt-2 text-xl font-semibold text-slate-950 dark:text-white">
                  {{ metric.value }}
                </div>
                <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">
                  {{ metric.caption }}
                </p>
              </article>
            </div>
          </div>
        </div>

        <div class="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
          <div class="grid gap-4 sm:grid-cols-2">
            <article
              v-for="card in trafficCards"
              :key="card.key"
              class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
            >
              <div class="flex items-start justify-between gap-3">
                <div>
                  <p class="text-sm font-medium text-slate-500 dark:text-slate-400">{{ card.label }}</p>
                  <h3 class="mt-2 text-2xl font-semibold tracking-tight text-slate-950 dark:text-white">
                    {{ card.value }}
                  </h3>
                </div>
                <div :class="card.badgeClass" class="rounded-full px-3 py-1 text-xs font-medium">
                  {{ card.badge }}
                </div>
              </div>
              <p class="mt-3 text-sm leading-6 text-slate-500 dark:text-slate-400">
                {{ card.description }}
              </p>
            </article>
          </div>

          <article
            class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="text-base font-semibold text-slate-950 dark:text-white">
                  {{ t('status_providerSectionTitle') }}
                </h3>
                <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">
                  {{ t('status_providerSectionDescription') }}
                </p>
              </div>
              <span class="rounded-full border border-slate-200 bg-slate-100 px-3 py-1 text-xs font-medium text-slate-600 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-300">
                {{ t('status_hitsBadge', { count: aiSummary.totalHits || 0 }) }}
              </span>
            </div>

            <div v-if="providerCards.length" class="mt-4 grid gap-3 sm:grid-cols-2">
              <div
                v-for="provider in providerCards"
                :key="provider.key"
                class="rounded-2xl border p-4 transition-colors"
                :class="provider.active
                  ? 'border-emerald-200 bg-emerald-50/80 dark:border-emerald-500/30 dark:bg-emerald-500/10'
                  : 'border-slate-200/70 bg-slate-50/90 dark:border-slate-800 dark:bg-slate-900/70'"
              >
                <div class="flex items-start justify-between gap-3">
                  <div>
                    <div class="text-sm font-semibold text-slate-900 dark:text-slate-100">{{ provider.label }}</div>
                    <div class="mt-1 text-xs text-slate-500 dark:text-slate-400">
                      {{ provider.caption }}
                    </div>
                  </div>
                  <span
                    class="rounded-full px-3 py-1 text-xs font-medium"
                    :class="provider.active
                      ? 'bg-emerald-600 text-white dark:bg-emerald-400 dark:text-slate-950'
                      : 'bg-slate-900 text-white dark:bg-slate-100 dark:text-slate-900'"
                  >
                    {{ provider.badge }}
                  </span>
                </div>
                <div class="mt-4 flex flex-col gap-0.5 text-xs text-slate-500 dark:text-slate-400 sm:flex-row sm:items-center sm:justify-between">
                  <span>{{ provider.lastSeenText }}</span>
                  <span>{{ provider.detail }}</span>
                </div>
              </div>
            </div>

            <div
              v-else
              class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400"
            >
              {{ t('status_noProviderData') }}
            </div>
          </article>
        </div>

        <div class="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
          <article
            class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
            data-pane="ingress-sessions"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="text-base font-semibold text-slate-950 dark:text-white">
                  {{ t('status_ingressTitle') }}
                </h3>
                <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">
                  {{ t('status_ingressDesc') }}
                </p>
              </div>
              <span
                v-if="ingressRunning"
                class="shrink-0 rounded-full px-3 py-1 text-xs font-medium"
                :class="ingressActiveRows.length
                  ? 'bg-emerald-600 text-white dark:bg-emerald-400 dark:text-slate-950'
                  : 'bg-slate-200 text-slate-600 dark:bg-slate-800 dark:text-slate-300'"
                data-ingress-online-badge
              >
                {{ t('status_ingressOnlineBadge', { count: ingressActiveRows.length }) }}
              </span>
            </div>

            <div
              v-if="!ingressRunning"
              class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400"
            >
              {{ t('status_ingressNotRunning') }}
            </div>
            <div v-else-if="ingressActiveRows.length" class="mt-4 grid gap-2" data-ingress-active-list>
              <div
                v-for="row in ingressActiveRows"
                :key="row.key"
                class="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5 text-sm"
                :class="row.form === 'mesh'
                  ? 'border-emerald-200 bg-emerald-50/70 dark:border-emerald-500/30 dark:bg-emerald-500/10'
                  : 'border-slate-200 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-900/60'"
              >
                <div class="flex min-w-0 items-center gap-2.5">
                  <span class="h-2 w-2 shrink-0 rounded-full bg-emerald-400" aria-hidden="true"></span>
                  <div class="min-w-0">
                    <div class="truncate font-medium text-slate-900 dark:text-slate-100">{{ row.title }}</div>
                    <div class="truncate text-xs text-slate-400">{{ row.subtitle }}</div>
                  </div>
                </div>
                <div class="flex shrink-0 items-center gap-2 text-xs">
                  <span class="rounded-full bg-white px-2 py-0.5 text-[11px] font-medium text-slate-500 shadow-sm ring-1 ring-slate-200 dark:bg-slate-950 dark:text-slate-400 dark:ring-slate-800">{{ row.formLabel }}</span>
                  <span class="text-slate-400">{{ row.duration }}</span>
                  <button
                    v-if="row.manageable"
                    type="button"
                    class="rounded-lg px-2 py-1 font-medium text-slate-500 transition hover:bg-slate-200/70 hover:text-slate-700 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                    :data-ingress-kick="row.title"
                    @click="kickIngressSession(row)"
                  >
                    {{ t('status_ingressKick') }}
                  </button>
                  <button
                    v-if="row.manageable && !row.blocked"
                    type="button"
                    class="rounded-lg px-2 py-1 font-medium text-red-500 transition hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-500/10 dark:hover:text-red-300"
                    :data-ingress-block="row.title"
                    @click="blockIngressRow(row)"
                  >
                    {{ t('status_ingressBlock') }}
                  </button>
                  <span
                    v-else-if="row.blocked"
                    class="rounded-full bg-red-100 px-2 py-0.5 text-[11px] font-medium text-red-600 dark:bg-red-500/15 dark:text-red-300"
                    data-ingress-blocked-badge
                  >
                    {{ t('status_ingressBlockedBadge') }}
                  </span>
                </div>
              </div>
            </div>
            <div
              v-else
              class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400"
            >
              {{ t('status_ingressEmpty') }}
            </div>

            <!-- 最近断开的接入（环形历史前几条）：排查「刚掉线谁」 -->
            <div v-if="ingressRunning && (ingressClosedRows.length || ingressBlockedRows.length)" class="mt-4 border-t border-slate-100 pt-3 dark:border-slate-800/60">
              <div v-if="ingressClosedRows.length" data-ingress-closed-list>
                <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-400">{{ t('status_ingressClosedTitle') }}</p>
                <div class="mt-2 grid gap-1.5">
                  <div
                    v-for="row in ingressClosedRows"
                    :key="row.key"
                    class="flex items-center justify-between gap-3 text-xs text-slate-500 dark:text-slate-400"
                  >
                    <span class="min-w-0 truncate">{{ row.title }} <span class="text-slate-400">· {{ row.formLabel }}</span><span v-if="row.kicked" class="ml-1 text-red-400">· {{ t('status_ingressKickedBadge') }}</span></span>
                    <span class="shrink-0 text-slate-400">{{ row.closedAgo }}</span>
                  </div>
                </div>
              </div>

              <!-- 拒绝名单（配置维度）：条目可解封 -->
              <div v-if="ingressBlockedRows.length" class="mt-3" data-ingress-blocked-list>
                <p class="text-[11px] font-semibold uppercase tracking-wide text-slate-400">{{ t('status_ingressBlockedTitle') }}</p>
                <div class="mt-2 grid gap-1.5">
                  <div
                    v-for="row in ingressBlockedRows"
                    :key="row.key"
                    class="flex items-center justify-between gap-3 text-xs"
                  >
                    <span class="min-w-0 truncate text-slate-500 dark:text-slate-400">
                      {{ row.title }}<span class="text-slate-400"> · {{ row.kindLabel }}</span>
                    </span>
                    <button
                      type="button"
                      class="shrink-0 rounded-lg px-2 py-0.5 font-medium text-slate-500 transition hover:bg-slate-200/70 hover:text-slate-700 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                      :data-ingress-unblock="row.title"
                      @click="unblockIngressRow(row)"
                    >
                      {{ t('status_ingressUnblock') }}
                    </button>
                  </div>
                </div>
              </div>
            </div>
          </article>
        </div>

        <div class="grid gap-4 xl:grid-cols-[1.15fr_0.85fr]">
          <article
            class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
            data-pane="connection-records"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <h3 class="text-base font-semibold text-slate-950 dark:text-white">
                  {{ t('status_connectionsTitle') }}
                </h3>
                <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">
                  {{ t('status_connectionsDesc') }}
                </p>
              </div>
              <div class="flex shrink-0 rounded-full border border-slate-200 bg-slate-100 p-0.5 text-xs font-medium dark:border-slate-800 dark:bg-slate-900">
                <button
                  type="button"
                  data-connections-tab="active"
                  class="rounded-full px-3 py-1 transition"
                  :class="connectionsTab === 'active'
                    ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-800 dark:text-slate-100'
                    : 'text-slate-500 hover:text-slate-700 dark:text-slate-400'"
                  @click="connectionsTab = 'active'"
                >
                  {{ t('status_connectionsActiveTab', { count: activeConnections.length }) }}
                </button>
                <button
                  type="button"
                  data-connections-tab="closed"
                  class="rounded-full px-3 py-1 transition"
                  :class="connectionsTab === 'closed'
                    ? 'bg-white text-slate-900 shadow-sm dark:bg-slate-800 dark:text-slate-100'
                    : 'text-slate-500 hover:text-slate-700 dark:text-slate-400'"
                  @click="connectionsTab = 'closed'"
                >
                  {{ t('status_connectionsClosedTab', { count: closedConnections.length }) }}
                </button>
              </div>
            </div>

            <div v-if="connectionRows.length" class="mt-4 max-h-72 overflow-y-auto" data-connections-table>
              <table class="w-full text-left text-xs">
                <thead>
                  <tr class="text-[11px] font-semibold uppercase tracking-wide text-slate-400">
                    <th class="py-1.5 pr-3">{{ t('status_connectionsColumnHost') }}</th>
                    <th class="py-1.5 pr-3">{{ t('status_connectionsColumnRoute') }}</th>
                    <th class="py-1.5 pr-3 text-right">{{ t('status_connectionsColumnUpload') }}</th>
                    <th class="py-1.5 pr-3 text-right">{{ t('status_connectionsColumnDownload') }}</th>
                    <th class="py-1.5 text-right">{{ connectionsTab === 'active' ? t('status_connectionsColumnDuration') : t('status_connectionsColumnClosedAt') }}</th>
                  </tr>
                </thead>
                <tbody>
                  <tr
                    v-for="row in connectionRows"
                    :key="row.id"
                    class="border-t border-slate-100 text-slate-600 dark:border-slate-800/60 dark:text-slate-300"
                  >
                    <td class="max-w-[220px] truncate py-1.5 pr-3" :title="row.hostTitle">{{ row.host }}</td>
                    <td class="py-1.5 pr-3">{{ row.routeLabel }}</td>
                    <td class="py-1.5 pr-3 text-right font-mono">{{ formatBytes(row.upload) }}</td>
                    <td class="py-1.5 pr-3 text-right font-mono">{{ formatBytes(row.download) }}</td>
                    <td class="py-1.5 text-right font-mono text-slate-400">{{ connectionsTab === 'active' ? formatDurationMillis(row.duration) : formatClosedAt(row.endUnix) }}</td>
                  </tr>
                </tbody>
              </table>
            </div>
            <div
              v-else
              class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400"
            >
              {{ t('status_connectionsEmpty') }}
            </div>
          </article>

          <article
            class="rounded-2xl border border-slate-200/70 bg-white/90 p-4 shadow-[0_18px_40px_rgba(15,23,42,0.07)] backdrop-blur dark:border-slate-800/80 dark:bg-slate-950/70"
            data-pane="ssh-chains"
          >
            <div>
              <h3 class="text-base font-semibold text-slate-950 dark:text-white">
                {{ t('status_sshTitle') }}
              </h3>
              <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">
                {{ t('status_sshDesc') }}
              </p>
            </div>

            <div v-if="!sshStatus.ok" class="mt-4 rounded-2xl border border-dashed border-slate-300 bg-slate-50 px-4 py-6 text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-400">
              {{ t('status_sshNotConfigured') }}
            </div>
            <div v-else class="mt-4 grid gap-2" data-ssh-chains>
              <div
                v-for="chain in sshChainCards"
                :key="chain.key"
                class="flex items-center justify-between gap-3 rounded-xl border px-3 py-2.5 text-sm"
                :class="chain.cardClass"
              >
                <div class="flex items-center gap-2.5">
                  <span class="h-2 w-2 shrink-0 rounded-full" :class="chain.dotClass"></span>
                  <span class="font-medium text-slate-900 dark:text-slate-100">{{ chain.label }}</span>
                </div>
                <div class="flex items-center gap-2 text-xs">
                  <span :class="chain.stateClass">{{ chain.stateText }}</span>
                  <span v-if="chain.activeText" class="text-slate-400">{{ chain.activeText }}</span>
                </div>
              </div>
            </div>
          </article>
        </div>
      </section>

      <div v-if="statusError" class="rounded-xl border border-rose-200 bg-rose-50 px-4 py-3 text-sm text-rose-700 dark:border-rose-900 dark:bg-rose-950/30 dark:text-rose-300">
        {{ statusError }}
      </div>

      <section class="overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-sm dark:border-slate-800 dark:bg-background-dark">
        <button
          type="button"
          class="flex w-full items-center justify-between gap-4 px-5 py-4 text-left transition hover:bg-slate-50 dark:hover:bg-slate-900/40 sm:px-6"
          :aria-expanded="logsExpanded ? 'true' : 'false'"
          @click="toggleLogsExpanded"
        >
          <div>
            <div class="flex flex-wrap items-center gap-2">
              <h3 class="text-lg font-bold text-slate-900 dark:text-white">{{ t('status_logsTitle') }}</h3>
              <span class="rounded-full bg-primary/10 px-2.5 py-1 text-[11px] font-semibold uppercase tracking-wide text-primary">{{ logsExpanded ? t('status_logsCollapse') : t('status_logsExpand') }}</span>
              <span class="text-xs text-slate-400">{{ t('logs_entries', { count: filteredEntries.length }) }}</span>
            </div>
            <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">{{ t('status_logsDesc') }}</p>
          </div>
          <span class="material-symbols-outlined text-2xl text-slate-400">{{ logsExpanded ? 'keyboard_arrow_up' : 'keyboard_arrow_down' }}</span>
        </button>

        <div v-if="logsExpanded" class="border-t border-slate-200 px-5 py-5 dark:border-slate-800 sm:px-6">
          <div class="flex flex-col gap-3 lg:flex-row lg:items-center lg:justify-between">
            <div class="flex items-center gap-2">
              <span class="rounded-full bg-primary/20 px-2 py-0.5 text-[10px] font-bold uppercase text-primary">
                {{ isLogsPolling ? t('logs_live') : t('logs_paused') }}
              </span>
            </div>
            <div class="flex flex-wrap items-center gap-2 rounded-lg border border-slate-200 bg-slate-50 p-2 dark:border-slate-700 dark:bg-slate-800/60">
              <span class="px-1 text-[11px] font-semibold uppercase tracking-wide text-slate-400">
                {{ t('logs_filterLevel') }}
              </span>
              <select
                id="logLevelSelect"
                v-model="selectedLevel"
                class="min-h-10 rounded-md border-0 bg-white px-2.5 py-1.5 text-xs font-medium text-slate-600 shadow-sm ring-1 ring-slate-200 focus:outline-none focus:ring-2 focus:ring-primary/40 dark:bg-slate-900 dark:text-slate-200 dark:ring-slate-600 md:min-h-0"
              >
                <option v-for="option in filterLevelOptions" :key="option.value" :value="option.value">
                  {{ option.label }}
                </option>
              </select>
              <label class="inline-flex min-h-10 items-center gap-2 rounded-md bg-white px-2.5 py-1.5 text-xs font-medium text-slate-600 shadow-sm ring-1 ring-slate-200 dark:bg-slate-900 dark:text-slate-200 dark:ring-slate-600">
                <input id="logAutoScroll" v-model="autoScroll" type="checkbox" class="rounded border-slate-300 text-primary focus:ring-primary/40" />
                {{ t('logs_autoScroll') }}
              </label>
              <button
                id="logsClearBtn"
                type="button"
                class="inline-flex min-h-10 items-center rounded-md px-2.5 py-1.5 text-xs font-medium text-slate-500 transition hover:bg-slate-200 hover:text-slate-700 dark:hover:bg-slate-700 dark:hover:text-slate-100"
                @click="clearLogs"
              >
                {{ t('logs_clear') }}
              </button>
              <button
                id="wsDisconnectBtn"
                type="button"
                class="flex min-h-10 min-w-10 items-center justify-center rounded-md p-1.5 text-slate-400 transition-all hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-500/10"
                @click.stop="toggleLogsPolling"
              >
                <span class="material-symbols-outlined text-lg">{{ isLogsPolling ? 'stop_circle' : 'play_circle' }}</span>
              </button>
              <button
                id="logsRefreshBtn"
                type="button"
                class="flex min-h-10 min-w-10 items-center justify-center rounded-md p-1.5 text-slate-400 transition-all hover:bg-primary/10 hover:text-primary"
                :disabled="isLogsLoading"
                @click.stop="loadLogs"
              >
                <span class="material-symbols-outlined text-lg">{{ isLogsLoading ? 'hourglass_top' : 'refresh' }}</span>
              </button>
            </div>
          </div>

          <div
            ref="logsContainer"
            class="mt-4 overflow-y-auto rounded-xl border border-slate-200 bg-slate-950 p-4 font-mono text-xs text-slate-300 dark:border-slate-800"
            style="height: 460px;"
          >
            <div v-if="logsError" class="mb-3 rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-200">
              {{ logsError }}
            </div>
            <div v-if="!filteredEntries.length" class="rounded-lg border border-dashed border-slate-700 bg-slate-900/60 px-4 py-6 text-sm text-slate-500">
              {{ t('logs_noData') }}
            </div>
            <div v-else id="logsOutput" class="space-y-1 whitespace-pre-wrap break-all">
              <div
                v-for="(entry, index) in filteredEntries"
                :key="`${entry.timestamp}-${entry.source}-${index}`"
                class="rounded px-2 py-1.5"
                :class="entryRowClass(entry.level)"
              >
                <span class="text-slate-500">[{{ entry.timestamp }}]</span>
                <span class="ml-2 font-semibold" :class="entryLevelClass(entry.level)">{{ entry.level }}</span>
                <span class="ml-2 text-slate-400">({{ entry.source }})</span>
                <span class="ml-2">{{ entry.message }}</span>
              </div>
            </div>
          </div>
        </div>
      </section>
    </div>

    <div class="compat-anchors" aria-hidden="true">
      <select id="logSourceSelect"><option value="all">All</option></select>
      <button id="logFilterBtn" type="button"></button>
      <button id="wsConnectBtn" type="button"></button>
      <button id="logsFullscreenBtn" type="button"><i class="bi bi-fullscreen"></i></button>
      <div id="logs-page"></div>
      <div id="wsConnectionStatus">{{ isLogsPolling ? 'connected' : 'paused' }}</div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import { blockIngressSession, getIngressSessions, getStatusConnections, getStatusSummary, kickIngressSession as kickSessionApi, unblockIngressSession } from '../../services/statusApi';

const { t } = useI18n();

const isProdBuild = import.meta.env.PROD || ['prod', 'production'].includes(String(import.meta.env.MODE || '').toLowerCase());
const frontEndLogsFetchLimit = isProdBuild ? 200 : 800;

const LOG_LEVEL_OPTIONS = [
  { value: 'trace', labelKey: 'logs_levelTrace' },
  { value: 'debug', labelKey: 'logs_levelDebug' },
  { value: 'info', labelKey: 'logs_levelInfo' },
  { value: 'warn', labelKey: 'logs_levelWarning' },
  { value: 'error', labelKey: 'logs_levelError' }
];

const entries = ref([]);
const statusSummary = ref({ ai: {}, traffic: {}, http: {} });
const connectionsData = ref({ active: [], closed: [] });
// 接入客户端台账（服务端视角：谁经 SSH 入口连进来）
const ingressData = ref({ ok: false, active: [], closed: [] });
const connectionsTab = ref('active');
const selectedLevel = ref('all');
const autoScroll = ref(true);
const isLogsLoading = ref(false);
const isStatusLoading = ref(false);
const isLogsPolling = ref(true);
const logsExpanded = ref(false);
const logsError = ref('');
const statusError = ref('');
const lastUpdatedAt = ref(0);
const logsContainer = ref(null);

let logsPollTimer = null;
let statusPollTimer = null;

const runtimeLevelOptions = computed(() => {
  return LOG_LEVEL_OPTIONS
    .filter((option) => !isProdBuild || option.value === 'info' || option.value === 'warn' || option.value === 'error')
    .map((option) => ({
      value: option.value,
      label: t(option.labelKey)
    }));
});

const filterLevelOptions = computed(() => [
  { value: 'all', label: t('logs_levelAll') },
  ...runtimeLevelOptions.value
]);

const aiSummary = computed(() => statusSummary.value?.ai || {});
const trafficStats = computed(() => statusSummary.value?.traffic || {});
const httpStats = computed(() => statusSummary.value?.http || {});
const sshStatus = computed(() => statusSummary.value?.ssh || { ok: false, chains: [] });
const activeConnections = computed(() => Array.isArray(connectionsData.value?.active) ? connectionsData.value.active : []);
const closedConnections = computed(() => Array.isArray(connectionsData.value?.closed) ? connectionsData.value.closed : []);

// —— 接入客户端台账（服务端「谁连进来了」）——
const ingressRunning = computed(() => Boolean(ingressData.value?.ok));
const ingressActiveRaw = computed(() => Array.isArray(ingressData.value?.active) ? ingressData.value.active : []);
const ingressClosedRaw = computed(() => Array.isArray(ingressData.value?.closed) ? ingressData.value.closed : []);
const ingressBlocks = computed(() => {
  const blocks = ingressData.value?.blocks;
  return {
    fingerprints: Array.isArray(blocks?.fingerprints) ? blocks.fingerprints : [],
    deviceIds: Array.isArray(blocks?.device_ids) ? blocks.device_ids : []
  };
});
const formLabel = (form) => form === 'mesh' ? t('status_ingressFormMesh') : t('status_ingressFormBare');
const sessionTitle = (sess) => sess.form === 'mesh' && sess.device_id ? sess.device_id : (sess.source_ip || '--');
const ingressActiveRows = computed(() => ingressActiveRaw.value.map((sess, i) => ({
  key: `${sess.source_ip}-${sess.auth_at_unix}-${i}`,
  title: sessionTitle(sess),
  subtitle: sess.form === 'mesh' && sess.device_id
    ? t('status_ingressMeshSubtitle', { ip: sess.source_ip || '--' })
    : (sess.user ? t('status_ingressUserSubtitle', { user: sess.user }) : ''),
  formLabel: formLabel(sess.form),
  duration: formatDurationMillis(sess.duration_millis),
  fingerprint: sess.fingerprint || '',
  deviceId: sess.form === 'mesh' ? (sess.device_id || '') : '',
  blocked: Boolean(sess.fingerprint && ingressBlocks.value.fingerprints.includes(sess.fingerprint))
    || Boolean(sess.device_id && ingressBlocks.value.deviceIds.includes(sess.device_id)),
  manageable: Boolean(sess.fingerprint)
})));
const ingressClosedRows = computed(() => ingressClosedRaw.value.slice(0, 5).map((sess, i) => ({
  key: `${sess.source_ip}-${sess.closed_at_unix}-${i}`,
  title: sessionTitle(sess),
  formLabel: formLabel(sess.form),
  kicked: Boolean(sess.kicked),
  closedAgo: formatClosedAt(sess.closed_at_unix)
})));
// 拒绝名单行（配置维度）：指纹条目 + device_id 条目，各自可解封。
const ingressBlockedRows = computed(() => [
  ...ingressBlocks.value.fingerprints.map((fp, i) => ({
    key: `fp-${fp}-${i}`,
    title: fp,
    kindLabel: t('status_ingressBlockedFpSuffix'),
    fingerprint: fp,
    deviceId: ''
  })),
  ...ingressBlocks.value.deviceIds.map((id, i) => ({
    key: `id-${id}-${i}`,
    title: id,
    kindLabel: formLabel('mesh'),
    fingerprint: '',
    deviceId: id
  }))
]);
const connectionRows = computed(() => connectionsTab.value === 'active' ? activeConnectionRows.value : closedConnectionRows.value);
const activeConnectionRows = computed(() => activeConnections.value.map((conn) => ({
  id: conn.id,
  host: conn.host || conn.destination,
  hostTitle: `${conn.host || ''} (${conn.destination})`,
  routeLabel: routeLabel(conn.route),
  upload: Number(conn.uploadTotal || 0),
  download: Number(conn.downloadTotal || 0),
  duration: Number(conn.durationMillis || 0)
})));
const closedConnectionRows = computed(() => closedConnections.value.map((conn) => ({
  id: conn.id,
  host: conn.host || conn.destination,
  hostTitle: `${conn.host || ''} (${conn.destination})`,
  routeLabel: routeLabel(conn.route),
  upload: Number(conn.uploadTotal || 0),
  download: Number(conn.downloadTotal || 0),
  endUnix: Number(conn.endUnix || 0)
})));
const sshChainCards = computed(() => (Array.isArray(sshStatus.value?.chains) ? sshStatus.value.chains : []).map((chain) => {
  const index = Number(chain.index || 0) + 1;
  const alive = Boolean(chain.alive);
  const backoff = Boolean(chain.backoff);
  const remaining = Math.max(1, Math.round(Number(chain.backoffRemainingMs || 0) / 1000));
  const activeCount = Number(chain.activeChannels || 0);
  let stateText;
  let cardClass;
  let dotClass;
  let stateClass;
  if (backoff) {
    stateText = t('status_sshChainBackoffIn', { seconds: remaining });
    cardClass = 'border-amber-200 bg-amber-50/70 dark:border-amber-500/30 dark:bg-amber-500/10';
    dotClass = 'bg-amber-400';
    stateClass = 'text-amber-600 dark:text-amber-300';
  } else if (alive) {
    stateText = t('status_sshChainAlive');
    cardClass = 'border-emerald-200 bg-emerald-50/70 dark:border-emerald-500/30 dark:bg-emerald-500/10';
    dotClass = 'bg-emerald-400';
    stateClass = 'text-emerald-600 dark:text-emerald-300';
  } else {
    stateText = t('status_sshChainIdle');
    cardClass = 'border-slate-200 bg-slate-50/80 dark:border-slate-800 dark:bg-slate-900/60';
    dotClass = 'bg-slate-300 dark:bg-slate-600';
    stateClass = 'text-slate-500 dark:text-slate-400';
  }
  return {
    key: chain.index ?? index,
    label: t('status_sshChainLabel', { index }),
    stateText,
    activeText: activeCount > 0 ? t('status_sshChainActive', { count: activeCount }) : '',
    cardClass,
    dotClass,
    stateClass
  };
}));

function routeLabel(route) {
  if (route === 'RouteToALiang') {
    return t('status_routeToALiang');
  }
  if (route === 'RouteToSocks') {
    return t('status_routeToSocks');
  }
  return route || '--';
}
const activeProviders = computed(() => Array.isArray(aiSummary.value?.activeDetections) ? aiSummary.value.activeDetections : []);
const visibleProviders = computed(() => {
  if (Array.isArray(aiSummary.value?.recentProviderTraffic)) {
    return aiSummary.value.recentProviderTraffic;
  }
  return activeProviders.value;
});
const totalTrafficBytes = computed(() => Number(httpStats.value?.totalTrafficBytes || 0));
const statusTTLSeconds = computed(() => Number(aiSummary.value?.ttlSeconds || 15));
const statusVisibleWindowSeconds = computed(() => Number(aiSummary.value?.visibleWindowSeconds || 600));
const totalActiveConnections = computed(() => activeProviders.value.reduce((total, provider) => total + Number(provider?.activeConnectionCount || 0), 0));
const hasRecentAIActivity = computed(() => Boolean(aiSummary.value?.active));
const aiStatusDotClass = computed(() => hasRecentAIActivity.value ? 'bg-emerald-400 shadow-[0_0_16px_rgba(52,211,153,0.85)]' : 'bg-slate-400');
const aiStatusBadgeClass = computed(() => hasRecentAIActivity.value
  ? 'border-emerald-200 bg-emerald-50 text-emerald-700 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200'
  : 'border-slate-200 bg-slate-50 text-slate-500 dark:border-slate-700 dark:bg-slate-900/80 dark:text-slate-300');
const activeSummaryTitle = computed(() => {
  if (hasRecentAIActivity.value) {
    return t('status_activeTitle', { count: aiSummary.value.activeCount || activeProviders.value.length || 1 });
  }
  return t('status_idleTitle');
});
const activeSummaryText = computed(() => {
  if (hasRecentAIActivity.value) {
    const latestLabel = aiSummary.value?.lastLabel || aiSummary.value?.lastProvider || '';
    if (latestLabel && totalActiveConnections.value > 0) {
      return t('status_activeConnectionDescriptionProvider', {
        provider: latestLabel,
        count: totalActiveConnections.value
      });
    }
    return latestLabel
      ? t('status_activeDescriptionProvider', {
          provider: latestLabel,
          ago: formatRelativeFromSeconds(aiSummary.value?.lastSeenAt)
        })
      : t('status_activeDescription', { ago: formatRelativeFromSeconds(aiSummary.value?.lastSeenAt) });
  }
  return t('status_idleDescription', {
    seconds: statusTTLSeconds.value,
    minutes: Math.max(1, Math.round(statusVisibleWindowSeconds.value / 60))
  });
});
const summaryUpdatedAgoText = computed(() => {
  if (!lastUpdatedAt.value) {
    return t('status_updatedJustNow');
  }
  return formatRelativeFromMilliseconds(lastUpdatedAt.value);
});
const compactMetrics = computed(() => [
  {
    key: 'providers',
    label: t('status_heroActiveProviders'),
    value: String(aiSummary.value.activeCount || activeProviders.value.length || 0),
    caption: hasRecentAIActivity.value ? t('status_heroActiveProvidersHint') : t('status_heroIdleHint')
  },
  {
    key: 'rate',
    label: t('status_heroTrafficRate'),
    value: formatRate(Number(trafficStats.value.upload_rate || 0) + Number(trafficStats.value.download_rate || 0)),
    caption: t('status_heroTrafficRateHint')
  },
  {
    key: 'last',
    label: t('status_heroLastSeen'),
    value: aiSummary.value.lastSeenAt ? formatRelativeFromSeconds(aiSummary.value.lastSeenAt) : t('status_heroWaiting'),
    caption: aiSummary.value.lastLabel || t('status_heroLastSeenHint')
  }
]);
const trafficCards = computed(() => [
  {
    key: 'connections',
    label: t('status_cardConnections'),
    value: formatInteger(trafficStats.value.active_connections),
    badge: t('status_cardConnectionsBadge'),
    badgeClass: 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300',
    description: t('status_cardConnectionsDesc')
  },
  {
    key: 'traffic',
    label: t('status_cardTotalTraffic'),
    value: formatBytes(totalTrafficBytes.value),
    badge: formatInteger(httpStats.value.totalRequests),
    badgeClass: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
    description: t('status_cardTotalTrafficDesc')
  },
  {
    key: 'upload',
    label: t('status_cardUploadRate'),
    value: formatRate(trafficStats.value.upload_rate),
    badge: t('status_cardUploadBadge'),
    badgeClass: 'bg-violet-100 text-violet-700 dark:bg-violet-500/15 dark:text-violet-300',
    description: t('status_cardUploadRateDesc')
  },
  {
    key: 'download',
    label: t('status_cardDownloadRate'),
    value: formatRate(trafficStats.value.download_rate),
    badge: t('status_cardDownloadBadge'),
    badgeClass: 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
    description: t('status_cardDownloadRateDesc')
  }
]);
const providerCards = computed(() => {
  const latestSeenAt = Number(aiSummary.value.lastSeenAt || 0);
  const hiddenAfterMinutes = Math.max(1, Math.round(statusVisibleWindowSeconds.value / 60));
  return visibleProviders.value.slice(0, 6).map((provider) => {
    const lastSeenAt = Number(provider.lastSeenAt || 0);
    const label = provider.providerLabel || provider.providerKey || t('status_unknownProvider');
    const active = Boolean(provider.active);
    const activeConnectionCount = Number(provider.activeConnectionCount || 0);
    const lastConnectionDurationSeconds = Number(provider.lastConnectionDurationSeconds || 0);
    return {
      key: provider.providerKey || label,
      label,
      active,
      badge: active ? t('status_providerActiveBadge') : t('status_providerInactiveBadge'),
      caption: active
        ? (activeConnectionCount > 0
          ? t('status_providerConnectionActiveCaption', { count: activeConnectionCount })
          : t('status_providerActiveCaption', { seconds: statusTTLSeconds.value }))
        : t('status_providerInactiveCaption', {
            seconds: statusTTLSeconds.value,
            minutes: hiddenAfterMinutes
          }),
      lastSeenText: t('status_providerLastSeen', { value: formatRelativeFromSeconds(lastSeenAt) }),
      detail: active
        ? (activeConnectionCount > 0
          ? t('status_providerConnectionActiveDetail', { count: activeConnectionCount })
          : (latestSeenAt === lastSeenAt ? t('status_providerLatest') : t('status_providerRecent')))
        : (lastConnectionDurationSeconds > 0
          ? t('status_providerInactiveDetailWithDuration', {
              duration: formatDurationSeconds(lastConnectionDurationSeconds),
              minutes: hiddenAfterMinutes
            })
          : t('status_providerInactiveDetail', { minutes: hiddenAfterMinutes }))
    };
  });
});

const filteredEntries = computed(() => {
  if (selectedLevel.value === 'all') {
    return entries.value;
  }
  const expectedLevel = selectedLevel.value.toUpperCase();
  return entries.value.filter((entry) => String(entry.level || '').toUpperCase() === expectedLevel);
});

function levelRank(level) {
  switch (String(level || '').toLowerCase()) {
    case 'trace':
      return 0;
    case 'debug':
      return 1;
    case 'info':
      return 2;
    case 'warn':
      return 3;
    case 'error':
      return 4;
    default:
      return 2;
  }
}

function formatInteger(value) {
  return new Intl.NumberFormat('en-US').format(Number(value || 0));
}

function formatBytes(value) {
  const bytes = Number(value || 0);
  if (!Number.isFinite(bytes) || bytes <= 0) {
    return '0 B';
  }

  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let current = bytes;
  let unitIndex = 0;
  while (current >= 1024 && unitIndex < units.length - 1) {
    current /= 1024;
    unitIndex += 1;
  }
  return `${current >= 10 || unitIndex === 0 ? current.toFixed(0) : current.toFixed(1)} ${units[unitIndex]}`;
}

function formatRate(value) {
  return `${formatBytes(value)}/s`;
}

function formatDurationMillis(millis) {
  const value = Number(millis || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '--';
  }
  return formatDurationSeconds(Math.round(value / 1000));
}

function formatClosedAt(endUnix) {
  return formatRelativeFromSeconds(endUnix);
}

function formatRelativeFromSeconds(seconds) {
  const timestamp = Number(seconds || 0) * 1000;
  if (!Number.isFinite(timestamp) || timestamp <= 0) {
    return '--';
  }
  return formatRelativeFromMilliseconds(timestamp);
}

function formatRelativeFromMilliseconds(timestamp) {
  const value = Number(timestamp || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '--';
  }
  const diff = Date.now() - value;
  if (diff < 5000) {
    return t('status_updatedJustNow');
  }
  const diffSeconds = Math.round(diff / 1000);
  if (diffSeconds < 60) {
    return t('status_relativeSeconds', { count: diffSeconds });
  }
  const diffMinutes = Math.round(diffSeconds / 60);
  if (diffMinutes < 60) {
    return t('status_relativeMinutes', { count: diffMinutes });
  }
  const diffHours = Math.round(diffMinutes / 60);
  return t('status_relativeHours', { count: diffHours });
}

function formatDurationSeconds(seconds) {
  const value = Number(seconds || 0);
  if (!Number.isFinite(value) || value <= 0) {
    return '--';
  }
  if (value < 60) {
    return t('status_durationSeconds', { count: Math.round(value) });
  }
  const diffMinutes = Math.round(value / 60);
  if (diffMinutes < 60) {
    return t('status_durationMinutes', { count: diffMinutes });
  }
  const diffHours = Math.round(diffMinutes / 60);
  return t('status_durationHours', { count: diffHours });
}

async function loadStatus() {
  isStatusLoading.value = true;
  statusError.value = '';

  try {
    const envelope = await getStatusSummary();
    statusSummary.value = envelope?.data || { ai: {}, traffic: {}, http: {} };
    lastUpdatedAt.value = Date.now();
  } catch (error) {
    statusError.value = error instanceof Error ? error.message : t('status_summaryLoadFailed');
  } finally {
    isStatusLoading.value = false;
  }
}

async function loadConnections() {
  try {
    const envelope = await getStatusConnections();
    connectionsData.value = envelope?.data || { active: [], closed: [] };
  } catch {
    // 连接明细是辅助面板,失败不打扰主状态展示,下个轮询周期自愈。
  }
}

async function loadIngressSessions() {
  try {
    const envelope = await getIngressSessions();
    ingressData.value = envelope?.data || { ok: false, active: [], closed: [] };
  } catch {
    // 辅助面板同款静默降级，下个轮询周期自愈
  }
}

// —— 接入客户端管理动作（踢出 / 拉黑 / 解封）——
// 破坏性动作走 window.confirm（仓库既有惯例）；结果反馈用同款轻量 alert。
// 操作成功后立即刷新台账——被踢/被拉黑行在下一个界面帧就消失或带上标记。

async function kickIngressSession(row) {
  if (!row?.fingerprint) {
    window.alert(t('status_ingressManageErrNoIdentity'));
    return;
  }
  if (!window.confirm(t('status_ingressKickConfirm', { name: row.title }))) return;
  try {
    await kickSessionApi(row.fingerprint);
    await loadIngressSessions();
  } catch (error) {
    window.alert(t('status_ingressActionFailed', { reason: error?.message || error }));
  }
}

async function blockIngressRow(row) {
  if (!row?.fingerprint && !row?.deviceId) {
    window.alert(t('status_ingressManageErrNoIdentity'));
    return;
  }
  if (!window.confirm(t('status_ingressBlockConfirm', { name: row.title }))) return;
  try {
    await blockIngressSession({ fingerprint: row.fingerprint, deviceId: row.deviceId });
    window.alert(t('status_ingressBlockDone', { name: row.title }));
    await loadIngressSessions();
  } catch (error) {
    window.alert(t('status_ingressActionFailed', { reason: error?.message || error }));
  }
}

async function unblockIngressRow(row) {
  if (!row?.fingerprint && !row?.deviceId) return;
  if (!window.confirm(t('status_ingressUnblockConfirm', { name: row.title }))) return;
  try {
    await unblockIngressSession({ fingerprint: row.fingerprint, deviceId: row.deviceId });
    window.alert(t('status_ingressUnblockDone', { name: row.title }));
    await loadIngressSessions();
  } catch (error) {
    window.alert(t('status_ingressActionFailed', { reason: error?.message || error }));
  }
}

async function loadLogs() {
  if (!logsExpanded.value) {
    return;
  }

  isLogsLoading.value = true;
  logsError.value = '';

  try {
    const levelQuery = selectedLevel.value !== 'all' ? `&level=${encodeURIComponent(selectedLevel.value.toUpperCase())}` : '';
    const response = await fetch(`/api/logs?limit=${frontEndLogsFetchLimit}${levelQuery}`);
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }

    const nextEntries = Array.isArray(payload?.data?.entries) ? payload.data.entries : [];
    entries.value = nextEntries.map(normalizeLogEntry);
    await scrollToBottomIfNeeded();
  } catch (error) {
    logsError.value = error instanceof Error ? error.message : t('logs_loadFailed');
  } finally {
    isLogsLoading.value = false;
  }
}

async function clearLogs() {
  logsError.value = '';
  try {
    const response = await fetch('/api/logs/clear', { method: 'POST' });
    const payload = await response.json().catch(() => ({}));
    if (!response.ok || payload?.code !== 0) {
      throw new Error(payload?.msg || payload?.message || `Request failed (${response.status})`);
    }
    entries.value = [];
  } catch (error) {
    logsError.value = error instanceof Error ? error.message : t('logs_clearFailed');
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

function entryLevelClass(level) {
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

function entryRowClass(level) {
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

async function scrollToBottomIfNeeded() {
  if (!autoScroll.value) {
    return;
  }
  await nextTick();
  const element = logsContainer.value;
  if (element instanceof HTMLElement) {
    element.scrollTop = element.scrollHeight;
  }
}

function startStatusPolling() {
  stopStatusPolling();
  statusPollTimer = window.setInterval(() => {
    loadStatus();
    loadConnections();
    loadIngressSessions();
  }, 5000);
}

function stopStatusPolling() {
  if (statusPollTimer) {
    window.clearInterval(statusPollTimer);
    statusPollTimer = null;
  }
}

function startLogsPolling() {
  stopLogsPolling();
  logsPollTimer = window.setInterval(() => {
    loadLogs();
  }, 5000);
}

function stopLogsPolling() {
  if (logsPollTimer) {
    window.clearInterval(logsPollTimer);
    logsPollTimer = null;
  }
}

function toggleLogsExpanded() {
  logsExpanded.value = !logsExpanded.value;
}

function toggleLogsPolling() {
  isLogsPolling.value = !isLogsPolling.value;
  if (isLogsPolling.value && logsExpanded.value) {
    startLogsPolling();
    loadLogs();
    return;
  }
  stopLogsPolling();
}

watch(selectedLevel, (nextLevel) => {
  if (logsExpanded.value) {
    loadLogs();
  }
});

watch(logsExpanded, (expanded) => {
  if (expanded) {
    loadLogs();
    if (isLogsPolling.value) {
      startLogsPolling();
    }
    return;
  }

  stopLogsPolling();
});

onMounted(async () => {
  await Promise.all([loadStatus(), loadConnections(), loadIngressSessions()]);
  startStatusPolling();
});

onBeforeUnmount(() => {
  stopLogsPolling();
  stopStatusPolling();
});
</script>
