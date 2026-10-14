import { computed, ref } from 'vue';
import { getAgentStatus } from '../services/agentApi';
import { useI18n } from '../i18n';

// 模块级共享状态：Agent 标签页所有组件共用一次 /api/agent/status 拉取，
// 状态判定逻辑只在这里写一遍（此前 AgentSettings 与 AgentActivitySettings 各有一份逐行重复）。
const status = ref(null);
const loading = ref(false);
const error = ref('');
let loaded = false;
let inFlight = null;
let requestSeq = 0;

async function ensureLoaded() {
  if (loaded && status.value) return status.value;
  if (inFlight) return inFlight;
  inFlight = (async () => {
    const seq = ++requestSeq;
    loading.value = true;
    error.value = '';
    try {
      const data = await getAgentStatus();
      if (seq !== requestSeq) return status.value; // 已有更新的写入（applyStatus/refresh），丢弃过期快照
      status.value = data;
      loaded = true;
      return status.value;
    } catch (err) {
      error.value = err instanceof Error ? err.message : String(err);
      return null;
    } finally {
      loading.value = false;
      inFlight = null;
    }
  })();
  return inFlight;
}

async function refresh() {
  loaded = false;
  return ensureLoaded();
}

// enable/disable 等变更接口返回同一 AgentStatusResponse，直接写入共享状态
function applyStatus(next) {
  if (!next) return;
  requestSeq += 1; // 使在途请求作废
  status.value = next;
  loaded = true;
}

function resetForTests() {
  status.value = null;
  loading.value = false;
  error.value = '';
  loaded = false;
  inFlight = null;
  requestSeq = 0;
}

export function useAgentStatus() {
  const { t } = useI18n();

  const runtimeOnline = computed(() => (!status.value?.runtime ? true : Boolean(status.value.runtime.online)));
  const agentEnabled = computed(() => runtimeOnline.value && Boolean(status.value?.enabled && status.value?.bound));
  const statusLabel = computed(() => {
    if (!runtimeOnline.value) return t('agent_runtimeOffline');
    if (agentEnabled.value) return t('agent_enabled');
    return t('agent_disabled');
  });
  const statusBadgeClass = computed(() => {
    if (!runtimeOnline.value) return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300';
    if (agentEnabled.value) return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300';
    return 'bg-slate-200 text-slate-600 dark:bg-slate-700 dark:text-slate-300';
  });
  const primaryTools = computed(() => {
    const tools = Array.isArray(status.value?.tools) ? status.value.tools : [];
    return tools.filter((tool) => ['codex', 'claude', 'claudecode'].includes(tool.id));
  });
  const toolAvailabilitySummary = computed(() => t('agent_toolsSummary', {
    available: primaryTools.value.filter((tool) => tool.available).length,
    total: primaryTools.value.length,
  }));
  const syncLabel = computed(() => status.value?.sync_status
    || (status.value?.registered ? t('agent_syncRegistered') : t('agent_syncNotRegistered')));

  // 共享单例：写状态一律走 applyStatus/refresh，禁止组件侧直接赋值（Options API 的 this.status = x 会静默改写全局状态）
  return {
    status, loading, error,
    runtimeOnline, agentEnabled, statusLabel, statusBadgeClass,
    primaryTools, toolAvailabilitySummary, syncLabel,
    ensureLoaded, refresh, applyStatus,
  };
}

export { resetForTests };
