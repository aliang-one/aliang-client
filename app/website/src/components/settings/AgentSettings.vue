<template>
  <div class="overflow-hidden rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-background-dark">
    <div class="p-5 sm:p-6">
      <div class="flex flex-col gap-5 sm:flex-row sm:items-start sm:justify-between">
        <div class="flex min-w-0 items-start gap-3">
          <div class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
            <span class="material-symbols-outlined">terminal</span>
          </div>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-2">
              <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('agent_title') }}</h2>
              <span class="rounded-full px-2 py-0.5 text-[10px] font-bold" :class="statusBadgeClass">
                {{ statusLabel }}
              </span>
            </div>
            <p class="mt-1 max-w-2xl text-xs leading-5 text-slate-500 dark:text-slate-400">
              {{ t('agent_desc') }}
            </p>
          </div>
        </div>

        <div class="flex shrink-0 flex-wrap gap-2 sm:justify-end">
          <button
            v-if="agentEnabled"
            type="button"
            class="inline-flex h-11 items-center justify-center gap-1.5 rounded-lg border border-slate-200 bg-white px-3 text-xs font-bold text-slate-700 transition-colors hover:border-primary/40 hover:bg-primary/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-200 dark:hover:bg-primary/10"
            @click="settingsModalOpen = true"
          >
            <span class="material-symbols-outlined text-base">tune</span>
            {{ t('agent_settings') }}
          </button>
          <button
            type="button"
            class="inline-flex h-11 items-center justify-center gap-1.5 rounded-lg px-3 text-xs font-bold transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:cursor-not-allowed disabled:opacity-60"
            :class="agentEnabled ? 'border border-red-200 bg-white text-red-600 hover:bg-red-50 dark:border-red-500/30 dark:bg-slate-900 dark:text-red-300 dark:hover:bg-red-500/10' : 'bg-primary text-white hover:bg-primary/90'"
            :disabled="loading || binding"
            @click="agentEnabled ? turnOffAgent() : turnOnAgent()"
          >
            <span class="material-symbols-outlined text-base">{{ agentEnabled ? 'power_settings_new' : 'play_arrow' }}</span>
            {{ agentEnabled ? t('agent_disable') : t('agent_enable') }}
          </button>
        </div>
      </div>

      <div class="mt-6 flex items-start gap-3 border-t border-slate-100 pt-5 dark:border-slate-800">
        <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full" :class="statusIconClass">
          <span class="material-symbols-outlined text-lg">{{ statusIcon }}</span>
        </div>
        <div class="min-w-0">
          <p class="text-sm font-semibold text-slate-900 dark:text-slate-100">{{ statusTitle }}</p>
          <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ statusMessage }}</p>
        </div>
      </div>
    </div>

    <div class="mt-5 flex flex-wrap items-center gap-x-4 gap-y-1.5 border-t border-slate-100 px-5 py-4 text-[11px] font-medium text-slate-600 dark:border-slate-800 dark:text-slate-300 sm:px-6">
      <span v-if="deviceSummary" class="flex min-w-0 items-center gap-1.5">
        <span class="material-symbols-outlined text-sm text-slate-400">devices</span>
        <span class="truncate">{{ deviceSummary }}</span>
      </span>
      <span class="flex items-center gap-1.5">
        <span class="material-symbols-outlined text-sm text-slate-400">build</span>
        {{ toolAvailabilitySummary }}
      </span>
      <span class="flex items-center gap-1.5" :title="syncTitle">
        <span class="material-symbols-outlined text-sm text-slate-400">sync</span>
        {{ syncLabel }}
      </span>
    </div>

    <!-- 注册 / 连接健康度：正常一行摘要，出错才展开 -->
    <div v-if="healthVisible" class="border-t border-slate-100 px-5 py-4 dark:border-slate-800 sm:px-6">
      <template v-if="healthHasProblem">
        <div class="space-y-1.5">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_registration_label') }}</span>
            <span v-if="registrationLabel" class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold" :class="registrationBadgeClass">{{ registrationLabel }}</span>
            <span v-if="status?.registration_message" class="min-w-0 flex-1 truncate text-[10px] text-rose-600 dark:text-rose-300" :title="status.registration_message">{{ status.registration_message }}</span>
          </div>
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-[10px] font-bold uppercase tracking-wide text-slate-400">{{ t('agent_connection_label') }}</span>
            <span v-if="connectionLabel" class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold" :class="connectionBadgeClass">{{ connectionLabel }}</span>
            <span v-if="status?.connection_message" class="min-w-0 flex-1 truncate text-[10px] text-rose-600 dark:text-rose-300" :title="status.connection_message">{{ status.connection_message }}</span>
          </div>
        </div>
        <button
          v-if="canRetry"
          type="button"
          class="mt-2 inline-flex min-h-11 items-center gap-1 rounded-lg px-2 text-[11px] font-semibold text-primary transition-colors hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/20 disabled:opacity-60"
          :disabled="retrying"
          @click="retryRegistration"
        >
          <span class="material-symbols-outlined text-sm">{{ retrying ? 'progress_activity' : 'sync' }}</span>
          {{ retrying ? t('agent_health_retrying') : t('agent_health_retry') }}
        </button>
      </template>
      <p v-else class="text-[11px] text-slate-500 dark:text-slate-400">
        {{ t('agent_registration_label') }} {{ registrationLabel }} · {{ t('agent_connection_label') }} {{ connectionLabel }}
      </p>
    </div>

    <div
      v-if="settingsModalOpen"
      class="fixed inset-0 z-[1090] flex items-center justify-center bg-slate-950/65 p-4 backdrop-blur-sm"
      role="dialog"
      aria-modal="true"
      :aria-label="t('agent_settingsTitle')"
      @click.self="settingsModalOpen = false"
    >
      <div class="flex max-h-[88vh] w-full max-w-3xl flex-col overflow-hidden rounded-2xl border border-slate-200 bg-white shadow-2xl dark:border-slate-700 dark:bg-slate-900">
        <div class="border-b border-slate-200 bg-slate-50/85 px-5 py-4 dark:border-slate-700 dark:bg-slate-800/65">
          <div class="flex items-start justify-between gap-4">
            <div>
              <p class="text-[11px] font-bold uppercase tracking-[0.22em] text-primary">{{ t('agent_title') }}</p>
              <h3 class="mt-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ t('agent_settingsTitle') }}</h3>
              <p class="mt-1 text-sm leading-6 text-slate-500 dark:text-slate-400">{{ t('agent_settingsDesc') }}</p>
            </div>
            <button
              type="button"
              class="flex h-11 w-11 items-center justify-center rounded-lg text-slate-500 transition-colors hover:bg-slate-100 hover:text-slate-700 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 dark:hover:bg-slate-800 dark:hover:text-slate-200"
              :aria-label="t('agent_closeSettings')"
              @click="settingsModalOpen = false"
            >
              <span class="material-symbols-outlined text-lg">close</span>
            </button>
          </div>
        </div>

        <div class="flex-1 overflow-y-auto p-5">
    <div v-if="status?.device" class="mb-4 rounded-lg border border-slate-200 bg-white px-3 py-3 dark:border-slate-700 dark:bg-slate-900/40">
      <div class="flex items-center justify-between gap-3">
        <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('agent_deviceInfo') }}</p>
        <span class="rounded-full px-2 py-0.5 text-[10px] font-bold" :class="deviceStatusPillClass">{{ deviceStatusLabel }}</span>
      </div>
      <div class="mt-3 grid grid-cols-1 gap-2 sm:grid-cols-2">
        <div class="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-[11px] dark:border-slate-700 dark:bg-slate-950/50">
          <span class="block font-bold uppercase tracking-[0.16em] text-slate-400">{{ t('agent_deviceId') }}</span>
          <code class="mt-1 block truncate text-slate-700 dark:text-slate-200" :title="deviceIdLabel">{{ deviceIdLabel }}</code>
        </div>
        <div class="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-[11px] dark:border-slate-700 dark:bg-slate-950/50">
          <span class="block font-bold uppercase tracking-[0.16em] text-slate-400">{{ t('agent_lastSeen') }}</span>
          <span class="mt-1 block text-slate-700 dark:text-slate-200">{{ lastSeenLabel }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-[11px] dark:border-slate-700 dark:bg-slate-950/50">
          <span class="block font-bold uppercase tracking-[0.16em] text-slate-400">{{ t('agent_remoteFeatures') }}</span>
          <span class="mt-1 block text-slate-700 dark:text-slate-200">{{ deviceFeatureSummary }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-[11px] dark:border-slate-700 dark:bg-slate-950/50">
          <span class="block font-bold uppercase tracking-[0.16em] text-slate-400">{{ t('agent_capabilities') }}</span>
          <span class="mt-1 block truncate text-slate-700 dark:text-slate-200" :title="capabilitiesLabel">{{ capabilitiesLabel }}</span>
        </div>
      </div>
    </div>

    <div v-if="failedPrerequisites.length" class="rounded-lg border border-amber-200 bg-amber-50/70 px-3 py-3 dark:border-amber-500/30 dark:bg-amber-500/10">
      <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-amber-700 dark:text-amber-300">{{ t('agent_prerequisites') }}</p>
      <div class="mt-3 space-y-2">
        <div
          v-for="item in failedPrerequisites"
          :key="item.key"
          class="flex items-start gap-2 text-[11px] text-amber-700 dark:text-amber-200"
        >
          <span class="material-symbols-outlined mt-0.5 text-sm">radio_button_unchecked</span>
          <span class="leading-5">{{ item.label }}</span>
        </div>
      </div>
    </div>

    <div class="mt-4 rounded-lg border border-slate-200 bg-slate-50 px-3 py-3 dark:border-slate-700 dark:bg-slate-900/50">
      <div class="flex items-center justify-between gap-3">
        <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('agent_history') }}</p>
        <span class="text-[10px] text-slate-400">{{ historySummary }}</span>
      </div>
      <p class="mt-1.5 text-[10px] leading-4 text-slate-400 dark:text-slate-500">{{ t('agent_historyMetaHint') }}</p>
      <div class="mt-3 space-y-2">
        <div
          v-for="item in historyRoots"
          :key="`${item.tool}-${item.path}`"
          class="rounded border border-slate-200 bg-white px-3 py-2 text-[11px] text-slate-600 dark:border-slate-700 dark:bg-slate-950/50 dark:text-slate-300"
        >
          <div class="flex items-center justify-between gap-3">
            <span class="font-bold text-slate-800 dark:text-slate-100">{{ item.tool }}</span>
            <span :class="item.exists ? 'text-emerald-600 dark:text-emerald-300' : 'text-slate-400'">
              {{ item.exists ? t('agent_historyFound') : t('agent_historyMissing') }}
            </span>
          </div>
          <div class="mt-1 truncate text-slate-400" :title="item.path">{{ item.path }}</div>
          <div v-if="item.exists" class="mt-1 text-slate-500 dark:text-slate-400">
            {{ t('agent_historyMeta', { files: item.file_count || 0, size: formatBytes(item.total_size || 0) }) }}
          </div>
        </div>
      </div>
    </div>

    <div class="mt-4 space-y-3">
      <div class="flex items-center justify-between gap-3">
        <p class="text-sm font-semibold">{{ t('agent_tools') }}</p>
        <button
          type="button"
          class="inline-flex min-h-9 items-center rounded border border-slate-200 px-2.5 py-1 text-[10px] font-bold text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800 md:min-h-0 md:px-2"
          :disabled="loading"
          @click="refreshStatus"
        >
          {{ t('agent_refresh') }}
        </button>
      </div>

      <div class="grid grid-cols-1 gap-2">
        <button
          v-for="tool in primaryTools"
          :key="tool.id"
          type="button"
          class="flex min-h-10 items-center justify-between gap-3 rounded-lg border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-50"
          :class="tool.available ? 'border-slate-200 hover:border-primary/40 hover:bg-primary/5 dark:border-slate-700 dark:hover:bg-primary/10' : 'border-slate-200 bg-slate-50 dark:border-slate-800 dark:bg-slate-900/60'"
          :disabled="!canLaunchCommands || !tool.available || launchLoading"
          @click="launchTool(tool.id)"
        >
          <span class="min-w-0">
            <span class="block text-xs font-bold text-slate-800 dark:text-slate-100">{{ tool.name }}</span>
            <span class="block truncate text-[10px] text-slate-500 dark:text-slate-400">
              {{ tool.available ? tool.path : t('agent_toolMissing') }}
            </span>
          </span>
          <span class="material-symbols-outlined text-base text-primary">play_arrow</span>
        </button>
      </div>

      <div class="rounded-lg border border-slate-200 bg-slate-50 p-3 dark:border-slate-700 dark:bg-slate-900/50">
        <label class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">
          {{ t('agent_workdir') }}
        </label>
        <input
          v-model.trim="cwd"
          type="text"
          class="mt-2 h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
          :placeholder="t('agent_workdirPlaceholder')"
          :disabled="!canLaunchCommands || launchLoading"
        />

        <label class="mt-3 block text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">
          {{ t('agent_command') }}
        </label>
        <div class="mt-2 flex gap-2">
          <input
            v-model.trim="commandLine"
            type="text"
            class="h-10 min-w-0 flex-1 rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
            :placeholder="t('agent_commandPlaceholder')"
            :disabled="!canLaunchCommands || launchLoading"
            @keydown.enter.prevent="launchCommand"
          />
          <button
            type="button"
            class="inline-flex h-10 items-center justify-center rounded-lg bg-slate-900 px-3 text-xs font-bold text-white transition hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-60 dark:bg-primary"
            :disabled="!canLaunchCommands || !commandLine || launchLoading"
            @click="launchCommand"
          >
            {{ t('agent_run') }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="lastLaunch" class="mt-4 rounded-lg border border-emerald-200 bg-emerald-50/80 px-3 py-3 text-[11px] text-emerald-800 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200">
      <p class="font-bold uppercase tracking-[0.18em]">{{ t('agent_lastLaunch') }}</p>
      <div class="mt-2 space-y-1">
        <div><span class="font-semibold">{{ t('agent_lastStatus') }}:</span> {{ lastLaunch.status || '-' }}</div>
        <div><span class="font-semibold">{{ t('agent_lastSession') }}:</span> <code class="break-all">{{ lastLaunch.session_id || '-' }}</code></div>
        <div><span class="font-semibold">{{ t('agent_lastCommand') }}:</span> <code class="break-all">{{ lastLaunch.command || '-' }}</code></div>
        <div v-if="lastLaunch.cwd"><span class="font-semibold">{{ t('agent_lastCwd') }}:</span> <code class="break-all">{{ lastLaunch.cwd }}</code></div>
      </div>
    </div>

        </div>
      </div>
    </div>

    <p v-if="feedback" class="mt-3 text-[11px]" :class="feedbackClass">{{ feedback }}</p>
  </div>
</template>

<script>
import {
  disableAgent,
  launchAgentTool,
  enableAgent,
} from '../../services/agentApi';
import { useI18n } from '../../i18n';
import { useAgentStatus } from '../../composables/useAgentStatus';

export default {
  name: 'AgentSettings',
  setup() {
    const { t } = useI18n();
    return { t, ...useAgentStatus() };
  },
  data() {
    return {
      binding: false,
      settingsModalOpen: false,
      launchLoading: false,
      commandLine: '',
      cwd: '',
      lastLaunch: null,
      feedback: '',
      feedbackType: 'info',
      retrying: false,
    };
  },
  computed: {
    statusTitle() {
      if (!this.runtimeOnline) return this.t('agent_offlineTitle');
      if (this.agentEnabled) return this.t('agent_readyTitle');
      return this.t('agent_disabledTitle');
    },
    statusMessage() {
      if (!this.runtimeOnline) return this.status?.message || this.t('agent_offlineDesc');
      return this.status?.message || this.t('agent_disabledDesc');
    },
    statusIcon() {
      if (!this.runtimeOnline) return 'cloud_off';
      return this.agentEnabled ? 'check_circle' : 'pause_circle';
    },
    statusIconClass() {
      if (!this.runtimeOnline) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
      if (this.agentEnabled) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
      return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-300';
    },
    deviceSummary() {
      const device = this.status?.device;
      if (!device) return '';
      return this.t('agent_deviceSummary', {
        name: device.name || '-',
        platform: device.platform || '-',
      });
    },
    deviceStatusLabel() {
      const status = this.status?.device?.status || (this.agentEnabled ? 'offline' : '');
      if (status === 'online') return this.t('agent_deviceOnline');
      if (status === 'offline') return this.t('agent_deviceOffline');
      return this.status?.device ? status : '-';
    },
    deviceStatusPillClass() {
      return this.status?.device?.status === 'online'
        ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
        : 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
    },
    deviceIdLabel() {
      return this.status?.device?.device_id || this.status?.device?.id || '-';
    },
    lastSeenLabel() {
      const value = this.status?.device?.last_seen_at || this.status?.device?.bound_at || this.status?.device?.paired_at;
      if (!value) return '-';
      const date = new Date(value);
      if (Number.isNaN(date.getTime())) return value;
      return date.toLocaleString();
    },
    capabilitiesLabel() {
      const capabilities = Array.isArray(this.status?.device?.capabilities) ? this.status.device.capabilities : [];
      return capabilities.length ? capabilities.join(' / ') : '-';
    },
    deviceFeatureSummary() {
      const device = this.status?.device;
      if (!device) return '-';
      const features = [];
      features.push(this.remoteTerminalFeatureEnabled ? this.t('agent_featureTerminal') : this.t('agent_featureTerminalOff'));
      features.push(this.aiControlFeatureEnabled ? this.t('agent_featureAI') : this.t('agent_featureAIOff'));
      return features.length ? features.join(' / ') : '-';
    },
    remoteTerminalFeatureEnabled() {
      const device = this.status?.device;
      if (!device) return true;
      return device.remote_terminal_enabled !== false;
    },
    aiControlFeatureEnabled() {
      const device = this.status?.device;
      if (!device) return true;
      return device.ai_control_enabled !== false;
    },
    canLaunchCommands() {
      return this.agentEnabled && this.remoteTerminalFeatureEnabled;
    },
    failedPrerequisites() {
      return [
        { key: 'runtime', ok: this.runtimeOnline, label: this.t('agent_preRuntimeMissing') },
        { key: 'registered', ok: Boolean(this.status?.registered), label: this.t('agent_preRegisteredMissing') },
        { key: 'bound', ok: this.agentEnabled, label: this.t('agent_preBoundMissing') },
        { key: 'tool', ok: this.primaryTools.some((tool) => tool.available), label: this.t('agent_preToolMissing') },
        { key: 'terminal', ok: this.remoteTerminalFeatureEnabled, label: this.t('agent_preTerminalDisabled') },
        { key: 'ai', ok: this.aiControlFeatureEnabled, label: this.t('agent_preAIDisabled') },
      ].filter((item) => !item.ok);
    },
    // —— 注册 / 连接健康度（自 AgentActivitySettings 迁入，唯一呈现处）——
    healthVisible() {
      return Boolean(this.status?.registration_state || this.status?.connection_state);
    },
    registrationLabel() {
      const map = {
        registered: 'agent_reg_registered',
        rejected: 'agent_reg_rejected',
        login_required: 'agent_reg_login_required',
        unregistered: 'agent_reg_unregistered',
        not_configured: 'agent_reg_not_configured',
      };
      const key = map[this.status?.registration_state];
      return key ? this.t(key) : '';
    },
    registrationBadgeClass() {
      switch (this.status?.registration_state) {
        case 'registered': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'rejected': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        case 'login_required': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    connectionLabel() {
      const map = {
        connected: 'agent_conn_connected',
        connecting: 'agent_conn_connecting',
        error: 'agent_conn_error',
        disconnected: 'agent_conn_disconnected',
      };
      const key = map[this.status?.connection_state];
      return key ? this.t(key) : '';
    },
    connectionBadgeClass() {
      switch (this.status?.connection_state) {
        case 'connected': return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
        case 'connecting': return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
        case 'error': return 'bg-rose-100 text-rose-700 dark:bg-rose-500/15 dark:text-rose-300';
        default: return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
      }
    },
    canRetry() {
      const r = this.status?.registration_state;
      const c = this.status?.connection_state;
      // login_required / not_configured 需要用户操作（登录/配置），不是重试能解决的
      if (r === 'login_required' || r === 'not_configured') return false;
      // connection_state 缺失时不据此判定可重试（契约漂移防护）
      return Boolean(c) ? (r !== 'registered' || c !== 'connected') : r !== 'registered';
    },
    healthHasProblem() {
      if (!this.healthVisible) return false;
      return this.canRetry
        || this.status?.registration_state !== 'registered'
        || this.status?.connection_state !== 'connected';
    },
    syncTitle() {
      return this.status?.sync_message || this.syncLabel;
    },
    historyRoots() {
      return Array.isArray(this.status?.history) ? this.status.history : [];
    },
    historySummary() {
      const found = this.historyRoots.filter(item => item.exists).length;
      return this.t('agent_historySummary', { found, total: this.historyRoots.length });
    },
    feedbackClass() {
      if (this.feedbackType === 'error') return 'text-red-500';
      if (this.feedbackType === 'success') return 'text-emerald-600 dark:text-emerald-400';
      return 'text-slate-500 dark:text-slate-400';
    },
  },
  mounted() {
    this.refreshStatus();
  },
  methods: {
    setFeedback(message, type = 'info') {
      this.feedback = message;
      this.feedbackType = type;
    },
    async refreshStatus() {
      const result = await this.refresh();
      if (!result) {
        this.setFeedback(this.error || this.t('agent_statusFailed'), 'error');
      }
    },
    async turnOnAgent() {
      this.binding = true;
      try {
        this.applyStatus(await enableAgent());
        this.setFeedback(this.t('agent_enableSuccess'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_enableFailed'), 'error');
      } finally {
        this.binding = false;
      }
    },
    async turnOffAgent() {
      this.binding = true;
      try {
        this.applyStatus(await disableAgent());
        this.setFeedback(this.t('agent_disabledFeedback'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_disableFailed'), 'error');
      } finally {
        this.binding = false;
      }
    },
    // 重试注册与「开启」同一接口（POST /api/agent/enable），仅出错态可见
    async retryRegistration() {
      this.retrying = true;
      try {
        this.applyStatus(await enableAgent());
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_enableFailed'), 'error');
      } finally {
        this.retrying = false;
      }
    },
    async launchTool(tool) {
      if (!this.canLaunchCommands) {
        this.setFeedback(this.t('agent_remoteTerminalDisabled'), 'error');
        return;
      }
      await this.launch({ tool });
    },
    async launchCommand() {
      if (!this.commandLine) return;
      if (!this.canLaunchCommands) {
        this.setFeedback(this.t('agent_remoteTerminalDisabled'), 'error');
        return;
      }
      await this.launch({ tool: 'command', command_line: this.commandLine });
    },
    async launch(payload) {
      this.launchLoading = true;
      try {
        const response = await launchAgentTool({
          mode: 'external_terminal',
          cwd: this.cwd,
          ...payload,
        });
        this.lastLaunch = response || null;
        this.setFeedback(response?.message || this.t('agent_launchSuccess'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_launchFailed'), 'error');
      } finally {
        this.launchLoading = false;
      }
    },
    formatBytes(value) {
      const size = Number(value || 0);
      if (size < 1024) return `${size} B`;
      if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
      return `${(size / 1024 / 1024).toFixed(1)} MB`;
    },
  },
};
</script>
