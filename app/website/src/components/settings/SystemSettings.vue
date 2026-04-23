<template>
  <div class="settings-pane" data-pane="system">
    <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
      <h3 class="mb-4 flex items-center gap-2 font-bold">
        <span class="material-symbols-outlined text-primary">settings</span>
        {{ t('sys_systemSettings') }}
      </h3>

      <div class="space-y-6">
        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-semibold">{{ t('sys_runMode') }}</p>
            <p class="text-[10px] text-slate-500">{{ t('sys_runModeDesc') }}</p>
          </div>
          <div class="flex rounded bg-slate-100 p-1 dark:bg-slate-800">
            <button
              type="button"
              :disabled="loadingMode || switchingMode || wintunDependency.installing"
              :class="[
                'rounded px-3 py-1 text-[10px] font-bold transition',
                selectedMode === 'tun'
                  ? 'bg-primary text-white shadow-sm'
                  : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70'
              ]"
              @click="selectMode('tun')"
            >
              {{ t('sys_modeDeep') }}
            </button>
            <button
              type="button"
              :disabled="loadingMode || switchingMode || wintunDependency.installing"
              :class="[
                'rounded px-3 py-1 text-[10px] font-bold transition',
                selectedMode === 'http'
                  ? 'bg-primary text-white shadow-sm'
                  : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70'
              ]"
              @click="selectMode('http')"
            >
              {{ t('sys_modeRegular') }}
            </button>
          </div>
        </div>

        <div class="flex items-center justify-between">
          <div>
            <p class="text-sm font-semibold">{{ t('sys_language') }}</p>
            <p class="text-[10px] text-slate-500">{{ t('sys_languageDesc') }}</p>
          </div>
          <div class="flex rounded bg-slate-100 p-1 dark:bg-slate-800">
            <button
              v-for="lang in languages"
              :key="lang.value"
              type="button"
              :class="[
                'rounded px-3 py-1 text-[10px] font-bold transition',
                locale === lang.value
                  ? 'bg-primary text-white shadow-sm'
                  : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70'
              ]"
              @click="setLocale(lang.value)"
            >
              {{ lang.label }}
            </button>
          </div>
        </div>

        <div class="flex items-center justify-between gap-4">
          <div>
            <p class="text-sm font-semibold">{{ t('sys_theme') }}</p>
            <p class="text-[10px] text-slate-500">{{ t('sys_themeDesc') }}</p>
            <p class="mt-1 text-[10px] text-slate-400 dark:text-slate-500">{{ themeStatusText }}</p>
          </div>
          <div class="flex rounded bg-slate-100 p-1 dark:bg-slate-800">
            <button
              v-for="option in themeOptions"
              :key="option.value"
              type="button"
              :class="[
                'rounded px-3 py-1 text-[10px] font-bold transition',
                themeButtonClass(option.value)
              ]"
              @click="applyThemeMode(option.value)"
            >
              {{ t(option.labelKey) }}
            </button>
          </div>
        </div>

        <div class="space-y-3 rounded-lg border border-slate-200 bg-slate-50 p-3 dark:border-slate-700 dark:bg-slate-900/50">
          <div class="flex flex-wrap items-center gap-2 text-[11px] text-slate-600 dark:text-slate-300">
            <span class="rounded bg-slate-200 px-2 py-0.5 font-semibold dark:bg-slate-700">{{ t('sys_backend') }}: {{ backendModeLabel }}</span>
            <span class="rounded bg-slate-200 px-2 py-0.5 font-semibold dark:bg-slate-700">{{ t('sys_selected') }}: {{ selectedModeLabel }}</span>
            <span v-if="isRunning !== null" class="rounded bg-slate-200 px-2 py-0.5 font-semibold dark:bg-slate-700">
              {{ isRunning ? t('sys_running') : t('sys_stopped') }}
            </span>
          </div>
          <p v-if="modeStatus" class="text-[11px] text-slate-500 dark:text-slate-400">{{ modeStatus }}</p>
          <div
            v-if="showMissingWintunBanner"
            class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-[11px] text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
          >
            {{ wintunDependency.message || t('sys_wintunMissing') }}
          </div>
          <div
            v-if="showInstallingWintunBanner"
            class="rounded-lg border border-sky-200 bg-sky-50 px-3 py-2 text-[11px] text-sky-700 dark:border-sky-500/30 dark:bg-sky-500/10 dark:text-sky-200"
          >
            {{ wintunDependency.message || t('sys_wintunInstalling') }}
          </div>
          <p v-if="modeError" class="text-[11px] text-red-500">{{ modeError }}</p>
          <p v-if="modeSuccess" class="text-[11px] text-emerald-600 dark:text-emerald-400">{{ modeSuccess }}</p>

          <div class="grid grid-cols-1 gap-2">
            <button
              type="button"
              class="rounded bg-slate-900 px-3 py-2 text-[11px] font-bold text-white transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-primary"
              :disabled="loadingMode || switchingMode || wintunDependency.installing"
              @click="refreshModeState"
            >
              {{ loadingMode ? t('sys_refreshing') : t('sys_refreshState') }}
            </button>
          </div>
        </div>

        <div
          v-if="showTunSwitchConfirm"
          class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/60 p-4 backdrop-blur-sm"
          @click.self="cancelTunSwitchConfirm"
        >
          <div class="w-full max-w-md overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
            <div class="border-b border-slate-200 bg-slate-50/80 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/60">
              <div class="flex items-start justify-between gap-4">
                <div>
                  <p class="text-xs font-bold uppercase tracking-[0.2em] text-amber-500">{{ t('sys_switchToTun') }}</p>
                  <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ tunSwitchDialogTitle }}</h3>
                  <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
                    {{ tunSwitchDialogDescription }}
                  </p>
                </div>
                <button
                  type="button"
                  class="rounded-lg p-1.5 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                  @click="cancelTunSwitchConfirm"
                >
                  <span class="material-symbols-outlined text-lg">close</span>
                </button>
              </div>
            </div>

            <div class="space-y-4 p-5">
              <div class="rounded-xl border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
                {{ tunSwitchDialogWarning }}
              </div>

              <div
                v-if="tunConflictScan.interfaces.length > 0"
                class="rounded-xl border border-slate-200 bg-slate-50/90 px-4 py-3 dark:border-slate-700 dark:bg-slate-800/60"
              >
                <div class="flex flex-wrap items-center justify-between gap-2">
                  <p class="text-[11px] font-bold uppercase tracking-[0.2em] text-slate-500 dark:text-slate-400">{{ t('sys_tunConflictListTitle') }}</p>
                  <span class="text-[11px] text-slate-500 dark:text-slate-400">
                    {{ tunConflictPaginationSummary }}
                  </span>
                </div>
                <div class="mt-3 space-y-2">
                  <div
                    v-for="item in paginatedTunConflictInterfaces"
                    :key="`${item.name}-${item.description}`"
                    class="rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 dark:border-slate-700 dark:bg-slate-900/60 dark:text-slate-200"
                  >
                    <div class="font-semibold">{{ item.name }}</div>
                    <div v-if="item.description" class="mt-1 text-xs text-slate-500 dark:text-slate-400">{{ item.description }}</div>
                    <div class="mt-1 text-[11px] text-slate-500 dark:text-slate-400">
                      {{ item.match_reason }}<span v-if="item.status"> · {{ item.status }}</span>
                    </div>
                  </div>
                </div>
                <div
                  v-if="tunConflictTotalPages > 1"
                  class="mt-4 flex flex-wrap items-center justify-between gap-3 border-t border-slate-200 pt-3 dark:border-slate-700"
                >
                  <span class="text-[11px] text-slate-500 dark:text-slate-400">
                    {{ currentTunConflictPage }}/{{ tunConflictTotalPages }}
                  </span>
                  <div class="flex items-center gap-2">
                    <button
                      type="button"
                      class="inline-flex min-h-11 items-center justify-center rounded-lg border border-slate-200 px-3 text-sm font-semibold text-slate-700 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
                      :disabled="currentTunConflictPage === 1"
                      @click="goToTunConflictPage(currentTunConflictPage - 1)"
                    >
                      上一页
                    </button>
                    <button
                      type="button"
                      class="inline-flex min-h-11 items-center justify-center rounded-lg border border-slate-200 px-3 text-sm font-semibold text-slate-700 transition hover:bg-slate-100 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
                      :disabled="currentTunConflictPage === tunConflictTotalPages"
                      @click="goToTunConflictPage(currentTunConflictPage + 1)"
                    >
                      下一页
                    </button>
                  </div>
                </div>
              </div>

              <p class="text-xs leading-6 text-slate-500 dark:text-slate-400">
                {{ t('sys_tunConflictHint') }}
              </p>

              <div class="flex gap-3">
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-lg border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
                  @click="cancelTunSwitchConfirm"
                >
                  {{ t('sys_cancel') }}
                </button>
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90"
                  @click="confirmTunSwitch"
                >
                  {{ t('sys_continue') }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div
          v-if="showSystemServiceConfirm"
          class="fixed inset-0 z-[1000] flex items-center justify-center bg-slate-950/65 p-4 backdrop-blur-sm"
          role="dialog"
          aria-modal="true"
          @click.self="closeSystemServiceConfirm"
        >
          <div class="w-full max-w-lg overflow-hidden rounded-3xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
            <div class="border-b border-slate-200 bg-slate-50/85 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/65">
              <div class="flex items-start justify-between gap-4">
                <div class="flex items-start gap-3">
                  <div
                    class="mt-0.5 flex h-11 w-11 items-center justify-center rounded-2xl border text-sm shadow-sm"
                    :class="systemServiceConfirmAction === 'uninstall'
                      ? 'border-red-200 bg-red-50 text-red-600 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200'
                      : 'border-primary/20 bg-primary/10 text-primary dark:border-primary/30 dark:bg-primary/15'"
                  >
                    <span class="material-symbols-outlined text-[20px]">
                      {{ systemServiceConfirmAction === 'uninstall' ? 'delete' : 'deployed_code' }}
                    </span>
                  </div>
                  <div>
                    <p
                      class="text-[11px] font-bold uppercase tracking-[0.22em]"
                      :class="systemServiceConfirmAction === 'uninstall'
                        ? 'text-red-500 dark:text-red-300'
                        : 'text-primary'"
                    >
                      {{ systemServiceConfirmAction === 'uninstall' ? t('sys_removeService') : t('sys_registerService') }}
                    </p>
                    <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">
                      {{ systemServiceConfirmTitle }}
                    </h3>
                    <p class="mt-1 text-sm text-slate-500 dark:text-slate-400">
                      {{ systemServiceConfirmDescription }}
                    </p>
                  </div>
                </div>
                <button
                  type="button"
                  class="rounded-xl p-2 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 disabled:cursor-not-allowed disabled:opacity-50 dark:hover:bg-slate-800 dark:hover:text-slate-200"
                  :disabled="serviceActionLoading"
                  @click="closeSystemServiceConfirm"
                >
                  <span class="material-symbols-outlined text-lg">close</span>
                </button>
              </div>
            </div>

            <div class="space-y-4 p-5">
              <div
                class="rounded-2xl border px-4 py-3 text-sm"
                :class="systemServiceConfirmAction === 'uninstall'
                  ? 'border-red-200 bg-red-50 text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200'
                  : 'border-primary/20 bg-primary/5 text-slate-700 dark:border-primary/30 dark:bg-primary/10 dark:text-slate-100'"
              >
                {{ systemServiceConfirmHighlight }}
              </div>

              <div class="rounded-2xl border border-slate-200 bg-slate-50/80 p-4 dark:border-slate-700 dark:bg-slate-800/45">
                <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('sys_operationSummary') }}</p>
                <div class="mt-3 space-y-2">
                  <div
                    v-for="item in systemServiceConfirmChecklist"
                    :key="item"
                    class="flex items-start gap-2 text-sm text-slate-700 dark:text-slate-200"
                  >
                    <span class="material-symbols-outlined mt-0.5 text-[16px] text-primary">check_circle</span>
                    <span>{{ item }}</span>
                  </div>
                </div>
              </div>

              <div class="flex gap-3">
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-xl border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
                  :disabled="serviceActionLoading"
                  @click="closeSystemServiceConfirm"
                >
                  {{ t('sys_cancel') }}
                </button>
                <button
                  type="button"
                  class="inline-flex h-11 flex-1 items-center justify-center rounded-xl px-4 text-sm font-semibold text-white transition disabled:cursor-not-allowed disabled:opacity-60"
                  :class="systemServiceConfirmAction === 'uninstall'
                    ? 'bg-red-600 hover:bg-red-500 dark:bg-red-500 dark:hover:bg-red-400'
                    : 'bg-primary hover:bg-primary/90'"
                  :disabled="serviceActionLoading"
                  @click="confirmSystemServiceAction"
                >
                  {{ systemServiceConfirmButtonLabel }}
                </button>
              </div>
            </div>
          </div>
        </div>

        <div class="space-y-2">
          <p class="text-sm font-semibold">{{ t('sys_softwareStatus') }}</p>
          <div class="rounded border-l-4 p-3" :class="serviceCardClass">
            <div class="flex items-start justify-between gap-3">
              <div>
                <p class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ systemServiceTitle }}</p>
                <p class="mt-1 text-[10px] leading-relaxed text-slate-600 dark:text-slate-400">
                  {{ systemServiceDescription }}
                </p>
                <p
                  v-if="showServicePrivilegeHint"
                  class="mt-2 text-[10px] font-semibold text-amber-700 dark:text-amber-300"
                >
                  {{ t('sys_adminRequired') }}
                </p>
              </div>
              <span class="rounded-full px-2 py-0.5 text-[10px] font-bold" :class="serviceBadgeClass">
                {{ systemServiceBadge }}
              </span>
            </div>

            <div v-if="systemServiceInfo.warning" class="mt-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-[10px] text-amber-800 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200">
              {{ systemServiceInfo.warning }}
            </div>

            <div v-if="systemServiceInfo.supported && systemServiceInfo.installed" class="mt-3 grid grid-cols-2 gap-2 rounded border border-slate-200 bg-white/70 p-3 text-[10px] text-slate-600 dark:border-slate-700 dark:bg-slate-900/40 dark:text-slate-300">
              <div>
                <p class="text-slate-400">{{ t('sys_status') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ systemServiceInfo.status || 'unknown' }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_pid') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ systemServiceInfo.pid || '-' }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_platform') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ friendlyPlatformLabel }}</p>
              </div>
              <div>
                <p class="text-slate-400">{{ t('sys_kind') }}</p>
                <p class="mt-1 font-semibold text-slate-700 dark:text-slate-100">{{ friendlyServiceLabel }}</p>
              </div>
            </div>

            <p v-if="systemServiceError" class="mt-3 text-[11px] text-red-500">{{ systemServiceError }}</p>
            <p v-if="systemServiceMessage" class="mt-3 text-[11px] text-emerald-600 dark:text-emerald-400">{{ systemServiceMessage }}</p>

            <div class="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
              <button
                type="button"
                class="rounded bg-slate-900 py-1.5 text-[11px] font-bold text-white transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-primary"
                :disabled="serviceActionLoading || !systemServiceInfo.supported || systemServiceInfo.installed || showServicePrivilegeHint"
                @click="openSystemServiceConfirm('install')"
              >
                {{ serviceActionLoading && serviceAction === 'install' ? t('sys_registering') : t('sys_registerServiceBtn') }}
              </button>
              <button
                type="button"
                class="rounded border border-red-200 py-1.5 text-[11px] font-bold text-red-600 transition hover:bg-red-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-red-500/30 dark:text-red-300 dark:hover:bg-red-500/10"
                :disabled="serviceActionLoading || !systemServiceInfo.supported || !systemServiceInfo.installed"
                @click="openSystemServiceConfirm('uninstall')"
              >
                {{ serviceActionLoading && serviceAction === 'uninstall' ? t('sys_uninstalling') : t('sys_uninstallServiceBtn') }}
              </button>
            </div>

            <button
              type="button"
              class="mt-2 w-full rounded border border-slate-200 py-1.5 text-[11px] font-bold text-slate-700 transition hover:bg-white disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-900/50"
              :disabled="serviceActionLoading"
              @click="refreshSystemServiceStatus"
            >
              {{ t('sys_refreshServiceStatus') }}
            </button>
          </div>
        </div>

        <div class="space-y-3">
          <div class="flex items-center justify-between">
            <p class="text-sm font-semibold">{{ t('sys_certCa') }}</p>
            <span class="flex items-center gap-1 text-[10px] font-bold text-primary">
              <span class="material-symbols-outlined text-xs">verified_user</span>
              {{ t('sys_trusted') }}
            </span>
          </div>
          <div class="space-y-1 rounded border border-slate-100 p-2 font-mono text-[10px] text-slate-500 dark:border-slate-800">
            <div><span class="text-slate-400">{{ t('sys_subject') }}</span> Opencode Local CA</div>
            <div><span class="text-slate-400">{{ t('sys_validity') }}</span> 2026-2031 (Valid)</div>
            <div class="truncate"><span class="text-slate-400">{{ t('sys_finger') }}</span> 7A:9C:B5:E1:02...</div>
          </div>
          <div class="grid grid-cols-2 gap-2">
            <button type="button" class="flex items-center justify-center gap-1 rounded border border-slate-200 py-1.5 text-[10px] font-bold hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800">
              <span class="material-symbols-outlined text-sm">download</span>
              {{ t('sys_export') }}
            </button>
            <button type="button" class="flex items-center justify-center gap-1 rounded border border-primary py-1.5 text-[10px] font-bold text-primary hover:bg-primary/5">
              <span class="material-symbols-outlined text-sm">install_desktop</span>
              {{ t('sys_install') }}
            </button>
          </div>
          <button type="button" class="flex w-full items-center justify-center gap-1 text-[10px] font-medium text-red-500">
            <span class="material-symbols-outlined text-sm">delete</span>
            {{ t('sys_removeRootCert') }}
          </button>
        </div>

        <div class="compat-anchors" aria-hidden="true">
          <input type="radio" name="runMode" id="runModeTun" value="tun" checked />
          <input type="radio" name="runMode" id="runModeHttp" value="http" />
          <button type="button" id="runStartBtn"></button>
          <button type="button" id="runStopBtn"></button>
          <button type="button" id="runModeBtn"></button>
          <div id="runCurrentMode"></div>
          <div id="runServiceStatus"></div>
          <div id="runAvailableModes"></div>
          <div id="runStatusInfo"></div>
          <button type="button" id="openCertManagementModalBtn"></button>
          <button type="button" id="btn-install-cert"></button>
          <button type="button" id="btn-export-cert"></button>
          <button type="button" id="btn-remove-cert"></button>
          <button type="button" id="btn-download-cert"></button>
          <button type="button" id="btn-check-cert"></button>
          <span id="cert-status"></span>
          <span id="cert-source"></span>
          <span id="cert-validity"></span>
          <span id="cert-fingerprint"></span>
          <span id="cert-subject"></span>
          <span id="cert-issuer"></span>
          <span id="cert-san"></span>
          <span id="cert-not-before"></span>
          <span id="cert-not-after"></span>
          <span id="cert-last-check"></span>
          <span id="cert-status-badge"></span>
          <div id="cert-raw-detail"></div>
          <span id="cert-trust"></span>
          <span id="cert-device"></span>
          <span id="cert-error"></span>
        </div>
      </div>
    </div>
  </div>
</template>

<script>
import { nextTick } from 'vue';
import { useNavigation } from '../../composables/useNavigation';
import { useRunStatus } from '../../composables/useRunStatus';
import { useTheme } from '../../composables/useTheme';
import { useI18n } from '../../i18n';

export default {
  name: 'SystemSettings',
  setup() {
    const { locale, t, setLocale } = useI18n();
    const { themeMode, resolvedTheme, setThemeMode } = useTheme();
    const { showDashboard } = useNavigation();
    const {
      runMode,
      runIsRunning,
      runStatus,
      runWintunDependency,
      runSyncError,
      refreshRunState,
      startPolling,
      stopPolling
    } = useRunStatus();

    return {
      locale,
      t,
      setLocale,
      themeMode,
      resolvedTheme,
      setThemeMode,
      languages: [
        { value: 'en', label: 'English' },
        { value: 'zh', label: '\u4E2D\u6587' },
      ],
      themeOptions: [
        { value: 'system', labelKey: 'sys_themeSystem' },
        { value: 'light', labelKey: 'sys_themeLight' },
        { value: 'dark', labelKey: 'sys_themeDark' },
      ],
      showDashboard,
      sharedRunMode: runMode,
      sharedRunIsRunning: runIsRunning,
      sharedRunStatus: runStatus,
      sharedRunWintunDependency: runWintunDependency,
      sharedRunSyncError: runSyncError,
      refreshSharedRunState: refreshRunState,
      startRunStatePolling: startPolling,
      stopRunStatePolling: stopPolling
    };
  },
  data() {
    return {
      selectedMode: 'tun',
      modeError: '',
      modeSuccess: '',
      loadingMode: false,
      pendingTunSwitchAfterInstall: false,
      tunConflictPageSize: 5,
      currentTunConflictPage: 1,
      tunConflictScan: {
        should_prompt: false,
        first_time_prompt: false,
        has_conflict: false,
        interfaces: []
      },
      authoritativeWintunDependency: null,
      systemServiceInfo: {
        supported: false,
        installed: false,
        running: false,
        status: 'unknown',
        pid: 0,
        platform: '',
        service_kind: '',
        display_name: '',
        message: '',
        warning: '',
        requires_admin: false,
        has_privileges: false
      },
      serviceActionLoading: false,
      serviceAction: '',
      systemServiceError: '',
      systemServiceMessage: '',
      wintunInstallPollTimer: null,
      wintunInstallPollInFlight: false
    };
  },
  computed: {
    backendMode() {
      if (this.sharedRunMode === 'http') {
        return 'http';
      }
      if (this.sharedRunMode === 'tun') {
        return 'tun';
      }
      return this.selectedMode;
    },
    isRunning() {
      if (this.sharedRunMode === 'unknown' && !this.sharedRunStatus && !this.sharedRunSyncError) {
        return null;
      }
      return this.sharedRunIsRunning;
    },
    modeStatus() {
      if (this.sharedRunSyncError) {
        return this.t('sys_syncFailed', { error: this.sharedRunSyncError });
      }
      return typeof this.sharedRunStatus === 'string' ? this.sharedRunStatus : '';
    },
    backendModeLabel() {
      return this.modeLabel(this.backendMode);
    },
    selectedModeLabel() {
      return this.modeLabel(this.selectedMode);
    },
    tunSwitchDialogTitle() {
      return this.tunConflictScan.first_time_prompt
        ? this.t('sys_tunFirstTimeTitle')
        : this.t('sys_continueTunSwitch');
    },
    tunSwitchDialogDescription() {
      return this.tunConflictScan.first_time_prompt
        ? this.t('sys_tunFirstTimeDesc')
        : this.t('sys_tunSwitchDesc');
    },
    tunSwitchDialogWarning() {
      return this.tunConflictScan.first_time_prompt
        ? this.t('sys_tunFirstTimeWarning')
        : this.t('sys_tunPermissionWarning');
    },
    tunConflictTotalPages() {
      return Math.max(1, Math.ceil(this.tunConflictScan.interfaces.length / this.tunConflictPageSize));
    },
    paginatedTunConflictInterfaces() {
      const start = (this.currentTunConflictPage - 1) * this.tunConflictPageSize;
      return this.tunConflictScan.interfaces.slice(start, start + this.tunConflictPageSize);
    },
    tunConflictPaginationSummary() {
      const total = this.tunConflictScan.interfaces.length;
      if (!total) {
        return '';
      }
      const start = (this.currentTunConflictPage - 1) * this.tunConflictPageSize + 1;
      const end = Math.min(total, start + this.paginatedTunConflictInterfaces.length - 1);
      return `${start}-${end} / ${total}`;
    },
    wintunDependency() {
      const dependency = this.authoritativeWintunDependency;
      if (dependency && typeof dependency === 'object') {
        return dependency;
      }
      const shared = this.sharedRunWintunDependency;
      if (shared && typeof shared === 'object') {
        return shared;
      }
      return {
        supported: false,
        required: false,
        available: true,
        installing: false,
        state: 'not_applicable',
        progress_percent: 100,
        message: '',
        error: ''
      };
    },
    showMissingWintunBanner() {
      return this.wintunDependency.required && !this.wintunDependency.available && !this.wintunDependency.installing;
    },
    showInstallingWintunBanner() {
      return this.wintunDependency.installing;
    },
    showServicePrivilegeHint() {
      return this.systemServiceInfo.supported && this.systemServiceInfo.requires_admin && !this.systemServiceInfo.has_privileges;
    },
    systemServiceTitle() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_svcNotSupported');
      }
      return this.systemServiceInfo.display_name || this.t('sys_svcAliangDefault');
    },
    systemServiceDescription() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_svcDescNotSupported');
      }
      if (!this.systemServiceInfo.installed) {
        return this.t('sys_svcDescNotInstalled', { label: this.friendlyServiceLabel });
      }
      return this.systemServiceInfo.message || this.t('sys_svcDescInstalled');
    },
    systemServiceBadge() {
      if (!this.systemServiceInfo.supported) {
        return this.t('sys_badgeNotSupported');
      }
      if (!this.systemServiceInfo.installed) {
        return this.t('sys_badgeNotRegistered');
      }
      return this.systemServiceInfo.running ? this.t('sys_badgeRunning') : this.t('sys_badgeRegistered');
    },
    friendlyPlatformLabel() {
      return this.systemServiceInfo.platform_label || this.systemServiceInfo.platform || '-';
    },
    friendlyServiceLabel() {
      return this.systemServiceInfo.service_label || this.systemServiceInfo.service_kind || this.t('sys_serviceLabelFallback');
    },
    resolvedThemeLabel() {
      return this.resolvedTheme === 'dark'
        ? this.t('sys_themeDark')
        : this.t('sys_themeLight');
    },
    themeStatusText() {
      if (this.themeMode === 'system') {
        return this.t('sys_themeFollowingSystemState', { mode: this.resolvedThemeLabel });
      }
      return this.t('sys_themeManualState', { mode: this.resolvedThemeLabel });
    },
    serviceBadgeClass() {
      if (!this.systemServiceInfo.supported) {
        return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
      if (!this.systemServiceInfo.installed) {
        return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
      }
      return this.systemServiceInfo.running
        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
        : 'bg-sky-100 text-sky-700 dark:bg-sky-500/15 dark:text-sky-300';
    },
    serviceCardClass() {
      if (!this.systemServiceInfo.supported) {
        return 'border-slate-300 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/50';
      }
      if (!this.systemServiceInfo.installed) {
        return 'border-primary bg-slate-50 dark:bg-slate-800/50';
      }
      return this.systemServiceInfo.running
        ? 'border-emerald-400 bg-emerald-50/70 dark:border-emerald-500/40 dark:bg-emerald-500/10'
        : 'border-sky-400 bg-sky-50/70 dark:border-sky-500/40 dark:bg-sky-500/10';
    },
    systemServiceConfirmTitle() {
      return this.systemServiceConfirmAction === 'uninstall'
        ? this.t('sys_confirmUninstallTitle')
        : this.t('sys_confirmRegisterTitle', { label: this.friendlyServiceLabel });
    },
    systemServiceConfirmDescription() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.t('sys_confirmUninstallDesc');
      }
      return this.t('sys_confirmRegisterDesc', { label: this.friendlyServiceLabel });
    },
    systemServiceConfirmHighlight() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.t('sys_confirmUninstallHighlight');
      }
      return this.t('sys_confirmRegisterHighlight');
    },
    systemServiceConfirmChecklist() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return [
          this.t('sys_confirmUninstallCheck1', { label: this.friendlyServiceLabel }),
          this.t('sys_confirmUninstallCheck2'),
          this.t('sys_confirmUninstallCheck3')
        ];
      }
      return [
        this.t('sys_confirmRegisterCheck1', { label: this.friendlyServiceLabel }),
        this.t('sys_confirmRegisterCheck2'),
        this.t('sys_confirmRegisterCheck3')
      ];
    },
    systemServiceConfirmButtonLabel() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        return this.serviceActionLoading ? this.t('sys_uninstallingBtn') : this.t('sys_confirmUninstallBtn');
      }
      return this.serviceActionLoading ? this.t('sys_registeringBtn') : this.t('sys_confirmRegisterBtn');
    }
  },
  mounted() {
    this.startRunStatePolling();
    this.refreshModeState().finally(() => {
      if (this.wintunDependency.installing) {
        this.ensureWintunInstallProgressModal();
        this.startWintunInstallPolling();
      }
    });
    this.refreshSystemServiceStatus();
  },
  beforeUnmount() {
    this.stopRunStatePolling();
    this.stopWintunInstallPolling();
  },
  watch: {
    backendMode(nextMode) {
      if (!this.switchingMode && !this.pendingTunSwitchAfterInstall && nextMode) {
        this.selectedMode = nextMode;
      }
    },
    'tunConflictScan.interfaces'(nextInterfaces) {
      const totalPages = Math.max(1, Math.ceil((Array.isArray(nextInterfaces) ? nextInterfaces.length : 0) / this.tunConflictPageSize));
      if (this.currentTunConflictPage > totalPages) {
        this.currentTunConflictPage = totalPages;
      }
      if (this.currentTunConflictPage < 1) {
        this.currentTunConflictPage = 1;
      }
    },
    showTunSwitchConfirm(nextValue) {
      if (nextValue) {
        this.currentTunConflictPage = 1;
      }
    }
  },
  methods: {
    clearMessages() {
      this.modeError = '';
      this.modeSuccess = '';
    },
    goToTunConflictPage(page) {
      const normalizedPage = Number(page);
      if (!Number.isFinite(normalizedPage)) {
        return;
      }
      this.currentTunConflictPage = Math.min(
        this.tunConflictTotalPages,
        Math.max(1, Math.trunc(normalizedPage))
      );
    },
    themeButtonClass(mode) {
      return this.themeMode === mode
        ? 'bg-primary text-white shadow-sm'
        : 'text-slate-500 hover:bg-slate-200 dark:text-slate-300 dark:hover:bg-slate-700/70';
    },
    modeLabel(mode) {
      if (mode === 'http') {
        return this.t('sys_modeRegular');
      }
      if (mode === 'tun') {
        return this.t('sys_modeDeep');
      }
      return String(mode || '').toUpperCase() || '--';
    },
    formatWintunInstallError(dependency, fallbackMessage) {
      const code = dependency && typeof dependency.error_code === 'string' ? dependency.error_code : '';
      if (code === 'uac_cancelled') {
        return this.t('sys_wintunUacCancelled');
      }
      if (code === 'verification_failed') {
        return this.t('sys_wintunVerifyFailed');
      }
      return fallbackMessage;
    },
    async dispatchTunProgressEvent(name, detail = {}) {
      this.showDashboard();
      await nextTick();
      window.dispatchEvent(new CustomEvent(name, { detail }));
    },
    applyThemeMode(mode) {
      this.setThemeMode(mode);
    },
    async fetchWintunDependencyStatus() {
      const result = await this.requestJSON('/api/run/wintun/status', {
        method: 'GET'
      });
      this.authoritativeWintunDependency = result && typeof result === 'object' ? result : null;
      return this.authoritativeWintunDependency;
    },
    async fetchTunConflictScan() {
      const result = await this.requestJSON('/api/run/tun/conflicts', {
        method: 'GET'
      });
      this.tunConflictScan = result && typeof result === 'object'
        ? {
            should_prompt: Boolean(result.should_prompt),
            first_time_prompt: Boolean(result.first_time_prompt),
            has_conflict: Boolean(result.has_conflict),
            interfaces: Array.isArray(result.interfaces) ? result.interfaces : []
          }
        : {
            should_prompt: false,
            first_time_prompt: false,
            has_conflict: false,
            interfaces: []
          };
      this.currentTunConflictPage = 1;
      return this.tunConflictScan;
    },
    async refreshSystemServiceStatus() {
      try {
        const result = await this.requestJSON('/api/system/service/status', {
          method: 'GET'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcStatusFailed');
      }
    },
    async ensureWintunInstallProgressModal() {
      await this.dispatchTunProgressEvent('aliang:tun-progress-open', {
        phase: 'installing_dependency',
        installState: this.wintunDependency.state || 'queued',
        title: this.t('sys_wintunInstallTitle'),
        detail: this.t('sys_wintunInstallDetail'),
        statusLabel: this.t('sys_wintunInstallStatusLabel'),
        statusHint: this.wintunDependency.message || this.t('sys_wintunInstallStatusHint')
      });
    },
    async updateWintunInstallProgressModal() {
      await this.dispatchTunProgressEvent('aliang:tun-progress-update', {
        phase: 'installing_dependency',
        installState: this.wintunDependency.state || 'installing',
        title: this.t('sys_wintunInstallTitle'),
        detail: this.t('sys_wintunInstallDetail'),
        statusLabel: this.t('sys_wintunInstallStatusLabelProgress', { percent: Number(this.wintunDependency.progress_percent || 0) }),
        statusHint: this.wintunDependency.message || this.t('sys_wintunInstallStatusHint')
      });
    },
    async selectMode(mode) {
      const normalizedMode = this.normalizeMode(mode);
      if (this.loadingMode || this.switchingMode || this.wintunDependency.installing || this.selectedMode === normalizedMode) {
        return;
      }
      if (normalizedMode === 'tun') {
        try {
          const scan = await this.fetchTunConflictScan();
          if (scan.should_prompt) {
            this.selectedMode = normalizedMode;
            this.showTunSwitchConfirm = true;
            return;
          }
        } catch {
          this.modeError = this.t('sys_tunConflictScanFailed');
          this.selectedMode = normalizedMode;
          this.tunConflictScan = {
            should_prompt: true,
            first_time_prompt: true,
            has_conflict: false,
            interfaces: []
          };
          this.showTunSwitchConfirm = true;
          return;
        }
      }
      if (normalizedMode === 'tun' && this.wintunDependency.required && !this.wintunDependency.available) {
        this.selectedMode = normalizedMode;
        await this.installWintunDependency({ continueAfterInstall: true });
        return;
      }
      this.selectedMode = normalizedMode;
      await this.switchMode();
    },
    cancelTunSwitchConfirm() {
      this.showTunSwitchConfirm = false;
      this.selectedMode = this.backendMode;
      this.currentTunConflictPage = 1;
      this.tunConflictScan = {
        should_prompt: false,
        first_time_prompt: false,
        has_conflict: false,
        interfaces: []
      };
    },
    async confirmTunSwitch() {
      this.showTunSwitchConfirm = false;
      this.selectedMode = 'tun';
      if (this.wintunDependency.required && !this.wintunDependency.available) {
        await this.installWintunDependency({ continueAfterInstall: true });
        return;
      }
      await this.continueTunSwitchAfterDependencyReady();
    },
    async continueTunSwitchAfterDependencyReady() {
      await this.switchMode();
    },
    async requestJSON(url, options = {}) {
      const res = await fetch(url, options);
      const payload = await res.json().catch(() => ({}));
      if (!res.ok || payload?.code !== 0) {
        const msg = payload?.msg || payload?.message || `Request failed (${res.status})`;
        throw new Error(msg);
      }
      return payload?.data ?? payload;
    },
    normalizeMode(mode) {
      return mode === 'http' ? 'http' : 'tun';
    },
    async refreshModeState(options = {}) {
      this.loadingMode = true;
      if (!options.preserveMessages) {
        this.clearMessages();
      }
      try {
        await this.refreshSharedRunState();
        this.authoritativeWintunDependency = this.sharedRunWintunDependency && typeof this.sharedRunWintunDependency === 'object'
          ? { ...this.sharedRunWintunDependency }
          : null;
        this.selectedMode = this.pendingTunSwitchAfterInstall ? 'tun' : this.backendMode;
      } catch (err) {
        this.modeError = err instanceof Error ? err.message : this.t('sys_refreshModeFailed');
      } finally {
        this.loadingMode = false;
      }
    },
    clearSystemServiceMessages() {
      this.systemServiceError = '';
      this.systemServiceMessage = '';
    },
    openSystemServiceConfirm(action) {
      if (this.serviceActionLoading) {
        return;
      }
      this.systemServiceConfirmAction = action === 'uninstall' ? 'uninstall' : 'install';
      this.showSystemServiceConfirm = true;
    },
    closeSystemServiceConfirm() {
      if (this.serviceActionLoading) {
        return;
      }
      this.showSystemServiceConfirm = false;
      this.systemServiceConfirmAction = '';
    },
    async confirmSystemServiceAction() {
      if (this.systemServiceConfirmAction === 'uninstall') {
        await this.uninstallSystemService();
        return;
      }
      await this.registerSystemService();
    },
    async registerSystemService() {
      this.clearSystemServiceMessages();
      this.serviceActionLoading = true;
      this.serviceAction = 'install';
      try {
        const result = await this.requestJSON('/api/system/service/install', {
          method: 'POST'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
        this.systemServiceMessage = this.systemServiceInfo.message || this.t('sys_svcRegistered');
        this.showSystemServiceConfirm = false;
        this.systemServiceConfirmAction = '';
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcRegisterFailed');
      } finally {
        this.serviceActionLoading = false;
        this.serviceAction = '';
      }
    },
    async uninstallSystemService() {
      this.clearSystemServiceMessages();
      this.serviceActionLoading = true;
      this.serviceAction = 'uninstall';
      try {
        const result = await this.requestJSON('/api/system/service/uninstall', {
          method: 'POST'
        });
        this.systemServiceInfo = result && typeof result === 'object' ? result : this.systemServiceInfo;
        this.systemServiceMessage = this.systemServiceInfo.message || this.t('sys_svcUninstalled');
        this.showSystemServiceConfirm = false;
        this.systemServiceConfirmAction = '';
      } catch (err) {
        this.systemServiceError = err instanceof Error ? err.message : this.t('sys_svcUninstallFailed');
      } finally {
        this.serviceActionLoading = false;
        this.serviceAction = '';
      }
    },
    startWintunInstallPolling() {
      if (this.wintunInstallPollTimer !== null) {
        return;
      }
      this.wintunInstallPollTimer = window.setInterval(() => {
        this.pollWintunInstallState();
      }, 2000);
    },
    stopWintunInstallPolling() {
      if (this.wintunInstallPollTimer !== null) {
        window.clearInterval(this.wintunInstallPollTimer);
        this.wintunInstallPollTimer = null;
      }
    },
    async pollWintunInstallState() {
      if (this.wintunInstallPollInFlight) {
        return;
      }
      this.wintunInstallPollInFlight = true;
      try {
        await this.fetchWintunDependencyStatus();
        if (this.wintunDependency.installing) {
          await this.updateWintunInstallProgressModal();
          return;
        }

        this.stopWintunInstallPolling();
        await this.refreshSharedRunState();
        if (this.wintunDependency.available) {
          const shouldContinue = this.pendingTunSwitchAfterInstall;
          this.pendingTunSwitchAfterInstall = false;
          this.modeSuccess = this.wintunDependency.message || this.t('sys_wintunDependencyReady');
          if (shouldContinue) {
            await this.continueTunSwitchAfterDependencyReady();
          } else {
            await this.dispatchTunProgressEvent('aliang:tun-progress-success', {
              title: this.t('sys_wintunReady'),
              detail: this.t('sys_wintunReadyDetail'),
              statusLabel: this.t('sys_wintunReadyLabel'),
              statusHint: this.modeSuccess,
              message: this.modeSuccess
            });
          }
          return;
        }

        this.pendingTunSwitchAfterInstall = false;
        this.modeError = this.formatWintunInstallError(
          this.wintunDependency,
          this.wintunDependency.error || this.wintunDependency.message || this.t('sys_wintunDependencyFailed')
        );
        await this.dispatchTunProgressEvent('aliang:tun-progress-error', {
          title: this.t('sys_wintunInstallFailed'),
          detail: this.t('sys_wintunInstallFailedDetail'),
          statusLabel: this.t('sys_wintunInstallFailedLabel'),
          statusHint: this.t('sys_wintunInstallFailedHint'),
          message: this.modeError
        });
      } catch (err) {
        this.modeError = this.formatWintunInstallError(
          null,
          err instanceof Error ? err.message : this.t('sys_refreshWintunFailed')
        );
        await this.dispatchTunProgressEvent('aliang:tun-progress-error', {
          title: this.t('sys_wintunInstallFailed'),
          detail: this.t('sys_wintunInstallFailedDetail'),
          statusLabel: this.t('sys_wintunInstallFailedLabel'),
          statusHint: this.t('sys_wintunInstallFailedHint'),
          message: this.modeError
        });
      } finally {
        this.wintunInstallPollInFlight = false;
      }
    },
    async installWintunDependency(options = {}) {
      this.clearMessages();
      this.pendingTunSwitchAfterInstall = Boolean(options.continueAfterInstall);

      try {
        const result = await this.requestJSON('/api/run/wintun/install', {
          method: 'POST'
        });
        this.authoritativeWintunDependency = result && typeof result === 'object' ? result : null;

        if (result?.available) {
          this.modeSuccess = typeof result?.message === 'string' && result.message
            ? result.message
            : this.t('sys_wintunAlreadyInstalled');
          if (this.pendingTunSwitchAfterInstall) {
            this.pendingTunSwitchAfterInstall = false;
            await this.continueTunSwitchAfterDependencyReady();
          } else {
            await this.dispatchTunProgressEvent('aliang:tun-progress-success', {
              title: this.t('sys_wintunReady'),
              detail: this.t('sys_wintunReadyDetail'),
              statusLabel: this.t('sys_wintunReadyLabel'),
              statusHint: this.modeSuccess,
              message: this.modeSuccess
            });
          }
          return;
        }

        await this.ensureWintunInstallProgressModal();
        await this.updateWintunInstallProgressModal();
        this.startWintunInstallPolling();
        await this.pollWintunInstallState();
      } catch (err) {
        this.pendingTunSwitchAfterInstall = false;
        this.modeError = this.formatWintunInstallError(
          null,
          err instanceof Error ? err.message : this.t('sys_wintunStartFailed')
        );
        await this.dispatchTunProgressEvent('aliang:tun-progress-error', {
          title: this.t('sys_wintunInstallFailed'),
          detail: this.t('sys_wintunInstallFailedDetail'),
          statusLabel: this.t('sys_wintunInstallFailedLabel'),
          statusHint: this.t('sys_wintunInstallFailedHint'),
          message: this.modeError
        });
      }
    },
    async switchMode() {
      this.switchingMode = true;
      this.clearMessages();
      try {
        const result = await this.requestJSON('/api/run/swift', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ mode: this.selectedMode })
        });
        const status = typeof result?.status === 'string' ? result.status : '';
        if (status === 'failed') {
          throw new Error(result?.msg || this.t('sys_modeSwitchFailed'));
        }
        this.modeSuccess = this.t('sys_modeSwitchSuccess', { mode: this.modeLabel(this.selectedMode) });
        await this.refreshModeState({ preserveMessages: true });
      } catch (err) {
        this.modeError = err instanceof Error ? err.message : this.t('sys_modeSwitchFailed');
      } finally {
        this.switchingMode = false;
      }
    }
  }
}
</script>
