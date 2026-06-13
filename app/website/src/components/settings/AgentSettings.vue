<template>
  <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <h3 class="flex items-center gap-2 font-bold">
          <span class="material-symbols-outlined text-primary">terminal</span>
          {{ t('agent_title') }}
        </h3>
        <p class="mt-1 text-[11px] leading-5 text-slate-500 dark:text-slate-400">
          {{ t('agent_desc') }}
        </p>
      </div>
      <span class="shrink-0 rounded-full px-2 py-0.5 text-[10px] font-bold" :class="statusBadgeClass">
        {{ statusLabel }}
      </span>
    </div>

    <div class="mt-4 rounded-lg border px-3 py-3" :class="statusPanelClass">
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="text-sm font-semibold text-slate-800 dark:text-slate-100">{{ statusTitle }}</p>
          <p class="mt-1 text-[11px] leading-5 text-slate-600 dark:text-slate-400">{{ statusMessage }}</p>
        </div>
        <div class="flex shrink-0 flex-col gap-2 sm:flex-row">
          <button
            v-if="agentEnabled"
            type="button"
            class="inline-flex h-9 items-center justify-center gap-1 rounded-lg bg-slate-900 px-3 text-[11px] font-bold text-white transition hover:opacity-90 dark:bg-primary"
            @click="settingsModalOpen = true"
          >
            <span class="material-symbols-outlined text-sm">tune</span>
            {{ t('agent_settings') }}
          </button>
          <button
            type="button"
            class="inline-flex h-9 items-center justify-center rounded-lg px-3 text-[11px] font-bold transition disabled:cursor-not-allowed disabled:opacity-60"
            :class="agentEnabled ? 'border border-red-200 text-red-600 hover:bg-red-50 dark:border-red-500/30 dark:text-red-300 dark:hover:bg-red-500/10' : 'bg-primary text-white hover:bg-primary/90'"
            :disabled="loading || binding"
            @click="agentEnabled ? turnOffAgent() : turnOnAgent()"
          >
            {{ agentEnabled ? t('agent_disable') : t('agent_enable') }}
          </button>
        </div>
      </div>
      <div v-if="deviceSummary" class="mt-3 rounded border border-slate-200 bg-white/80 px-3 py-2 text-[11px] text-slate-600 dark:border-slate-700 dark:bg-slate-900/50 dark:text-slate-300">
        {{ deviceSummary }}
      </div>
      <div class="mt-3 grid grid-cols-2 gap-2 text-[10px] text-slate-500 dark:text-slate-400">
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_runtime') }}</span>
          <span class="mt-1 block font-semibold" :class="runtimeOnline ? 'text-emerald-600 dark:text-emerald-300' : 'text-amber-600 dark:text-amber-300'" :title="runtimeTitle">
            {{ runtimeLabel }}
          </span>
        </div>
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_platform') }}</span>
          <span class="mt-1 block text-slate-700 dark:text-slate-200">{{ platformLabel }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_deviceStatus') }}</span>
          <span class="mt-1 block font-semibold" :class="deviceStatusClass">{{ deviceStatusLabel }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_toolsFound') }}</span>
          <span class="mt-1 block text-slate-700 dark:text-slate-200">{{ toolAvailabilitySummary }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_server') }}</span>
          <span class="mt-1 block truncate text-slate-700 dark:text-slate-200" :title="agentServerLabel">{{ agentServerLabel }}</span>
        </div>
        <div class="rounded border border-slate-200 bg-white/80 px-2 py-2 dark:border-slate-700 dark:bg-slate-900/50">
          <span class="block font-bold uppercase tracking-[0.16em]">{{ t('agent_sync') }}</span>
          <span class="mt-1 block truncate text-slate-700 dark:text-slate-200" :title="syncTitle">{{ syncLabel }}</span>
        </div>
      </div>
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
              class="rounded-xl p-2 text-slate-500 transition hover:bg-slate-100 hover:text-slate-700 dark:hover:bg-slate-800 dark:hover:text-slate-200"
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

    <div class="rounded-lg border border-slate-200 bg-white px-3 py-3 dark:border-slate-700 dark:bg-slate-900/40">
      <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('agent_prerequisites') }}</p>
      <div class="mt-3 space-y-2">
        <div
          v-for="item in prerequisites"
          :key="item.key"
          class="flex items-start gap-2 text-[11px]"
          :class="item.ok ? 'text-emerald-700 dark:text-emerald-300' : 'text-slate-500 dark:text-slate-400'"
        >
          <span class="material-symbols-outlined mt-0.5 text-sm">{{ item.ok ? 'check_circle' : 'radio_button_unchecked' }}</span>
          <span class="leading-5">{{ item.label }}</span>
        </div>
      </div>
    </div>

    <div class="mt-4 rounded-lg border border-slate-200 bg-slate-50 px-3 py-3 dark:border-slate-700 dark:bg-slate-900/50">
      <div class="flex items-center justify-between gap-3">
        <p class="text-[11px] font-bold uppercase tracking-[0.18em] text-slate-500 dark:text-slate-400">{{ t('agent_history') }}</p>
        <span class="text-[10px] text-slate-400">{{ historySummary }}</span>
      </div>
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
          class="rounded border border-slate-200 px-2 py-1 text-[10px] font-bold text-slate-600 transition hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
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
  getAgentStatus,
  launchAgentTool,
  enableAgent,
} from '../../services/agentApi';
import { useI18n } from '../../i18n';

export default {
  name: 'AgentSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      status: null,
      loading: false,
      binding: false,
      settingsModalOpen: false,
      launchLoading: false,
      commandLine: '',
      cwd: '',
      lastLaunch: null,
      feedback: '',
      feedbackType: 'info',
    };
  },
  computed: {
    runtimeOnline() {
      if (!this.status?.runtime) return true;
      return Boolean(this.status.runtime.online);
    },
    agentEnabled() {
      return this.runtimeOnline && Boolean(this.status?.enabled && this.status?.bound);
    },
    statusLabel() {
      if (!this.runtimeOnline) return this.t('agent_runtimeOffline');
      if (this.agentEnabled) return this.t('agent_enabled');
      return this.t('agent_disabled');
    },
    statusTitle() {
      if (!this.runtimeOnline) return this.t('agent_offlineTitle');
      if (this.agentEnabled) return this.t('agent_readyTitle');
      return this.t('agent_disabledTitle');
    },
    statusMessage() {
      if (!this.runtimeOnline) return this.status?.message || this.t('agent_offlineDesc');
      return this.status?.message || this.t('agent_disabledDesc');
    },
    statusBadgeClass() {
      if (!this.runtimeOnline) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
      if (this.agentEnabled) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
      return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
    },
    statusPanelClass() {
      if (!this.runtimeOnline) return 'border-amber-200 bg-amber-50/70 dark:border-amber-500/30 dark:bg-amber-500/10';
      if (this.agentEnabled) return 'border-emerald-200 bg-emerald-50/70 dark:border-emerald-500/30 dark:bg-emerald-500/10';
      return 'border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-900/50';
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
    deviceStatusClass() {
      return this.status?.device?.status === 'online' ? 'text-emerald-600 dark:text-emerald-300' : 'text-slate-600 dark:text-slate-300';
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
    primaryTools() {
      const tools = Array.isArray(this.status?.tools) ? this.status.tools : [];
      return tools.filter(tool => ['codex', 'claude', 'claudecode'].includes(tool.id));
    },
    availablePrimaryTools() {
      return this.primaryTools.filter(tool => tool.available);
    },
    platformLabel() {
      return this.status?.platform || '-';
    },
    runtimeLabel() {
      if (!this.status?.runtime) return '-';
      return this.runtimeOnline ? this.t('agent_runtimeOnline') : this.t('agent_runtimeOffline');
    },
    runtimeTitle() {
      const runtime = this.status?.runtime;
      if (!runtime) return '';
      const parts = [runtime.kind, runtime.url, runtime.pid ? `pid ${runtime.pid}` : ''].filter(Boolean);
      return parts.join(' · ');
    },
    toolAvailabilitySummary() {
      return this.t('agent_toolsSummary', {
        available: this.availablePrimaryTools.length,
        total: this.primaryTools.length,
      });
    },
    prerequisites() {
      return [
        {
          key: 'runtime',
          ok: this.runtimeOnline,
          label: this.runtimeOnline ? this.t('agent_preRuntimeOk') : this.t('agent_preRuntimeMissing'),
        },
        {
          key: 'registered',
          ok: Boolean(this.status?.registered),
          label: this.status?.registered ? this.t('agent_preRegisteredOk') : this.t('agent_preRegisteredMissing'),
        },
        {
          key: 'bound',
          ok: this.agentEnabled,
          label: this.agentEnabled ? this.t('agent_preBoundOk') : this.t('agent_preBoundMissing'),
        },
        {
          key: 'tool',
          ok: this.availablePrimaryTools.length > 0,
          label: this.availablePrimaryTools.length > 0 ? this.t('agent_preToolOk') : this.t('agent_preToolMissing'),
        },
        {
          key: 'terminal',
          ok: this.remoteTerminalFeatureEnabled,
          label: this.remoteTerminalFeatureEnabled ? this.t('agent_preTerminalOk') : this.t('agent_preTerminalDisabled'),
        },
        {
          key: 'ai',
          ok: this.aiControlFeatureEnabled,
          label: this.aiControlFeatureEnabled ? this.t('agent_preAIOk') : this.t('agent_preAIDisabled'),
        },
      ];
    },
    agentServerLabel() {
      return this.status?.agent_server || '-';
    },
    syncLabel() {
      return this.status?.sync_status || (this.status?.registered ? this.t('agent_syncRegistered') : this.t('agent_syncNotRegistered'));
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
      this.loading = true;
      try {
        this.status = await getAgentStatus();
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_statusFailed'), 'error');
      } finally {
        this.loading = false;
      }
    },
    async turnOnAgent() {
      this.binding = true;
      try {
        this.status = await enableAgent();
        this.setFeedback(this.t('agent_enableSuccess'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_enableFailed'), 'error');
      } finally {
        this.binding = false;
      }
    },
    async turnOffAgent() {
      this.loading = true;
      try {
        this.status = await disableAgent();
        this.setFeedback(this.t('agent_disabledFeedback'), 'success');
      } catch (err) {
        this.setFeedback(err instanceof Error ? err.message : this.t('agent_disableFailed'), 'error');
      } finally {
        this.loading = false;
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
