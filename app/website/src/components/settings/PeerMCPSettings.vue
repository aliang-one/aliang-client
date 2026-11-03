<template>
  <!-- 设备互访(aliang-peer 跨设备工具面,2026-11-03)。平台托管卡:开关
       走 customer.peer_mcp(core 落盘/DB),应用走 /api/agent/peer/mcp(runtime
       写内核配置)。与上面两张总闸卡同层:那张管 AliangCode 内核,这张管
       claude/codex 的跨设备工具入口。
       保存按钮不设 dirty 门(对照 AgentMCPGlobalSettings 的 !dirty 禁用)是
       有意的:两段式里持久化与应用可能漂移(runtime 自愈失败/配置被手改),
       允许随时保存 = 允许强制重新对齐 runtime 与持久化配置的偏差。 -->
  <div class="flex h-full flex-col rounded-xl border border-slate-200 bg-white p-5 dark:border-slate-800 dark:bg-background-dark">
    <div class="flex items-start gap-3">
      <div class="min-w-0 flex-1">
        <div class="flex items-center gap-2">
          <h2 class="text-base font-bold text-slate-900 dark:text-white">{{ t('peerMcp_title') }}</h2>
          <span class="rounded bg-primary/10 px-1.5 py-0.5 text-[10px] font-bold text-primary">{{ t('peerMcp_managed') }}</span>
        </div>
        <p class="mt-1 text-xs leading-5 text-slate-500 dark:text-slate-400">{{ t('peerMcp_description') }}</p>
      </div>
    </div>

    <label class="mt-5 flex min-h-11 cursor-pointer items-center justify-between gap-3 border-y border-slate-100 py-3 text-sm text-slate-700 dark:border-slate-800 dark:text-slate-200">
      <span>
        <span class="block text-xs font-semibold">{{ t('peerMcp_enable') }}</span>
        <span class="mt-0.5 block text-[11px] leading-4 text-slate-500 dark:text-slate-400">{{ t('peerMcp_hint') }}</span>
      </span>
      <input
        type="checkbox"
        data-testid="peer-mcp-enable"
        class="h-4 w-4 shrink-0 rounded border-slate-300 text-primary focus:ring-primary/20 dark:border-slate-600 dark:bg-slate-900"
        :checked="form.enabled"
        :disabled="loading || saving"
        @change="form.enabled = $event.target.checked"
      />
    </label>

    <ul class="mt-3 flex flex-wrap gap-1.5">
      <li
        v-for="tool in tools"
        :key="tool"
        class="rounded bg-slate-100 px-1.5 py-0.5 font-mono text-[10px] text-slate-600 dark:bg-slate-800 dark:text-slate-300"
      >{{ tool }}</li>
    </ul>

    <div class="mt-3 flex flex-wrap items-center gap-2 text-[11px]" data-testid="peer-mcp-registration" aria-live="polite">
      <template v-if="statusAvailable">
        <span
          v-for="row in registration"
          :key="row.kernel"
          class="rounded px-1.5 py-0.5 font-bold"
          :class="stateClass(row.state)"
        >{{ row.kernel }}:{{ stateLabel(row.state) }}</span>
        <!-- spec §7 诚实回显:逐内核失败原因短文案,不做静默假绿 -->
        <span
          v-for="row in failedRows"
          :key="'err-' + row.kernel"
          class="w-full break-words text-red-600 dark:text-red-400"
        >{{ row.kernel }}:{{ row.error }}</span>
      </template>
      <template v-else>
        <span class="text-slate-400">{{ t('peerMcp_statusUnavailable') }}</span>
        <!-- 真实原因透出:优先后端 status_error(如 "agent home unavailable"),
             传输/HTTP 层失败时是 request 抛出的 message——不猜「agent 未响应」。 -->
        <span v-if="statusError" class="w-full break-words text-amber-600 dark:text-amber-400">{{ statusError }}</span>
      </template>
    </div>

    <!-- apply 诊断行(在父页 error/success 之前,与其可区分):POST 应用注册
         失败意味着 runtime 侧工具面可能仍是旧状态——即使父页持久化成功
         (绿色 success)也不能被盖掉,必须单独红显(spec §7 诚实回显)。 -->
    <div v-if="applyError" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status" data-testid="peer-mcp-apply-error">
      {{ t('peerMcp_applyFailed') }}:{{ applyError }}
    </div>

    <div v-if="error" class="mt-3 text-xs text-red-600 dark:text-red-400" role="status">{{ error }}</div>
    <div v-else-if="successMessage" class="mt-3 text-xs text-emerald-600 dark:text-emerald-400" role="status">{{ successMessage }}</div>

    <button
      type="button"
      class="mt-auto inline-flex h-11 self-end items-center gap-1.5 rounded-lg bg-primary px-4 text-xs font-bold text-white transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/30 disabled:opacity-50"
      :disabled="loading || saving"
      @click="save"
    >
      {{ saving ? t('peerMcp_saving') : t('peerMcp_save') }}
    </button>
  </div>
</template>

<script>
import { useI18n } from '../../i18n';
import { getPeerMCPStatus, setPeerMCPRegistration } from '../../services/agentApi';

// 工具名按 stdio 面展示(本卡是 claude/codex 的入口,以它们实际可见的名字为准)。
const PEER_TOOLS = ['peer_exec', 'peer_fetch_file', 'device_list', 'device_info', 'device_note'];

export default {
  name: 'PeerMCPSettings',
  setup() {
    const { t } = useI18n();
    return { t };
  },
  props: {
    config: { type: Object, default: () => ({}) },
    loading: { type: Boolean, default: false },
    saving: { type: Boolean, default: false },
    error: { type: String, default: '' },
    successMessage: { type: String, default: '' }
  },
  emits: ['save'],
  computed: {
    failedRows() {
      return this.registration.filter((row) => row.error);
    }
  },
  data() {
    return {
      form: { enabled: this.formFromConfig(this.config) },
      tools: PEER_TOOLS,
      registration: [],
      statusAvailable: false,
      // 「状态不可用」的真实原因:后端 status_error 或请求层 message。
      statusError: '',
      // 与父页 error prop 可区分的本地态:POST 应用注册失败的具体原因。
      applyError: ''
    };
  },
  watch: {
    config: {
      deep: true,
      handler(nextConfig) {
        this.form.enabled = this.formFromConfig(nextConfig);
      }
    }
  },
  mounted() {
    this.loadRegistration();
  },
  methods: {
    formFromConfig(config) {
      const raw = config?.peer_mcp?.enable;
      return typeof raw === 'boolean' ? raw : true;
    },
    // 后端读取失败时 registration 为 null 且带 status_error——此时按「状态
    // 不可用」回显,不拿空数组假装读到了零内核(spec §7 诚实回显)。
    // status_error 是后端给出的精确原因,原样透出不猜。
    absorbStatus(data) {
      this.registration = Array.isArray(data?.registration) ? data.registration : [];
      this.statusAvailable = Array.isArray(data?.registration);
      const raw = data?.status_error;
      this.statusError = typeof raw === 'string' && raw ? raw : '';
    },
    errorMessage(e) {
      return (e instanceof Error && e.message) || String(e || '');
    },
    async loadRegistration() {
      try {
        this.absorbStatus(await getPeerMCPStatus());
      } catch (e) {
        this.statusAvailable = false;
        this.statusError = this.errorMessage(e);
      }
    },
    stateLabel(state) {
      const key = `peerMcp_state_${state}`;
      const label = this.t(key);
      // 缺键回落键名本身(i18n 行为),此时展示原始值更可读
      return label === key ? String(state) : label;
    },
    stateClass(state) {
      if (state === 'registered') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300';
      if (state === 'stale') return 'bg-amber-50 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300';
      if (state === 'missing') return 'bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400';
      return 'bg-red-50 text-red-600 dark:bg-red-900/30 dark:text-red-300';
    },
    async save() {
      const enable = !!this.form.enabled;
      this.applyError = '';
      // 两段式:①emit 让父页持久化 customer.peer_mcp(core 落盘/DB);
      // ②本端点让 runtime 立即应用注册(写内核配置 + 内存同步)。
      this.$emit('save', { ...(this.config || {}), peer_mcp: { enable } });
      try {
        this.absorbStatus(await setPeerMCPRegistration(enable));
      } catch (e) {
        // ②失败与①结果互不掩盖:即使父页持久化成功(绿 success),apply
        // 失败也要红显——runtime 工具面可能仍是旧状态(spec §7 诚实回显)。
        const msg = this.errorMessage(e);
        this.statusAvailable = false;
        this.statusError = msg;
        this.applyError = msg || this.t('peerMcp_applyFailed');
      }
    }
  }
};
</script>
