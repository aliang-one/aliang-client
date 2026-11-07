<template>
  <div class="rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="space-y-5">
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
            {{ t('peerGate_execTitle') }}
          </p>
          <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
            {{ t('peerGate_execDesc') }}
          </p>
        </div>
        <input
          type="checkbox"
          :checked="execEnabled"
          :disabled="!ready || savingKey === 'exec'"
          :aria-label="t('peerGate_execTitle')"
          data-testid="peer-gate-exec"
          class="h-4 w-4 shrink-0 cursor-pointer rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          @change="toggle('exec', $event)"
        />
      </div>
      <div class="flex items-center justify-between gap-3">
        <div class="min-w-0">
          <p class="flex flex-wrap items-center gap-2 text-sm font-semibold text-slate-900 dark:text-white">
            {{ t('peerGate_transferTitle') }}
            <span
              v-if="savingKey === 'transfer'"
              class="rounded bg-slate-100 px-1.5 py-0.5 text-[10px] font-bold text-slate-500 dark:bg-slate-800 dark:text-slate-300"
            >
              {{ t('peerGate_saving') }}
            </span>
          </p>
          <p class="mt-0.5 text-[10px] leading-4 text-slate-500 dark:text-slate-400">
            {{ t('peerGate_transferDesc') }}
          </p>
        </div>
        <input
          type="checkbox"
          :checked="transferEnabled"
          :disabled="!ready || savingKey === 'transfer'"
          :aria-label="t('peerGate_transferTitle')"
          data-testid="peer-gate-transfer"
          class="h-4 w-4 shrink-0 cursor-pointer rounded border-slate-300 text-primary dark:border-slate-600 dark:bg-slate-900"
          @change="toggle('transfer', $event)"
        />
      </div>
      <p class="text-[10px] text-slate-400 dark:text-slate-500">{{ t('settings_scopeDevice') }}</p>
    </div>

    <!-- 状态行:加载失败可重试 / 本机未同步 / 保存失败已回滚 -->
    <button
      v-if="loadError"
      type="button"
      data-testid="peer-gate-retry"
      class="mt-3 text-left text-[11px] font-semibold text-red-500 hover:text-red-600"
      @click="load"
    >
      {{ t('peerGate_loadFailed') }}
    </button>
    <p v-else-if="gates && gates.found === false" class="mt-3 text-[11px] text-amber-600 dark:text-amber-300">
      {{ t('peerGate_unsynced') }}
    </p>
    <p v-if="saveError" class="mt-3 text-[11px] text-red-500">{{ saveError }}</p>
  </div>
</template>

<script>
// 设备门禁真开关（2026-11-07 占位转正）。真值源=服务端设备注册表:
// GET /api/agent/peer/gates 读,PATCH（经内核透传）写后回读为准。
// 交互=拨动即生效（乐观更新,失败回滚+红字）,无保存按钮——与手机端
// device.settings 同语义,区别于上方 PeerMCPSettings 的两段式保存链。
import { getPeerGates, updatePeerGates } from '../../../services/agentApi';
import { useI18n } from '../../../i18n';

const FLAG_KEYS = { exec: 'peer_exec_enabled', transfer: 'peer_fetch_enabled' };

export default {
  name: 'PeerGateSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  data() {
    return {
      gates: null,
      loading: false,
      loadError: '',
      savingKey: '',
      saveError: ''
    };
  },
  computed: {
    ready() {
      return Boolean(this.gates && this.gates.found && !this.loading);
    },
    execEnabled() {
      return Boolean(this.gates && this.gates.peer_exec_enabled);
    },
    transferEnabled() {
      return Boolean(this.gates && this.gates.peer_fetch_enabled);
    }
  },
  mounted() {
    this.load();
  },
  methods: {
    async load() {
      this.loading = true;
      this.loadError = '';
      try {
        const data = await getPeerGates();
        this.gates = data && typeof data === 'object' ? data : { found: false };
      } catch (err) {
        this.loadError = err instanceof Error ? err.message : '';
      } finally {
        this.loading = false;
      }
    },
    async toggle(kind, event) {
      if (!this.ready || this.savingKey) {
        // 未就绪/串行中:把勾选框拨回当前真值(受控语义,拒绝脏输入)。
        event.target.checked = kind === 'exec' ? this.execEnabled : this.transferEnabled;
        return;
      }
      const value = Boolean(event.target.checked);
      const flagKey = FLAG_KEYS[kind];
      this.savingKey = kind;
      this.saveError = '';
      try {
        const data = await updatePeerGates({ [flagKey]: value });
        if (data && typeof data === 'object') {
          this.gates = data;
        }
      } catch (err) {
        // 回滚到拨动前的真值,红字示失败(真值源仍在服务端)。
        this.gates = { ...(this.gates || {}), [flagKey]: !value };
        this.saveError = err instanceof Error ? err.message : this.t('peerGate_saveFailed');
      } finally {
        this.savingKey = '';
      }
    }
  }
};
</script>
