import { ref } from 'vue';
import { handleAuthenticationFailure } from '../services/authFailure';
import { syncAuthFromStartupStatus, syncUnauthenticatedAuthState } from '../stores/auth';

const runMode = ref('unknown');
const runIsRunning = ref(false);
const runStatus = ref('');
const runDescription = ref('');
const runSyncError = ref('');
const runWintunDependency = ref({
  supported: false,
  required: false,
  available: true,
  installing: false,
  state: 'not_applicable',
  message: ''
});
const startupStatus = ref('UNKNOWN');
const loading = ref(false);

const POLL_INTERVAL = 5000;

let pollTimer = null;
let refCount = 0;
let hasLoadedOnce = false;

function extractApiErrorMessage(payload, status, fallback) {
  return (
    payload?.msg ||
    payload?.message ||
    payload?.data?.error_msg ||
    payload?.data?.message ||
    payload?.data?.details?.error ||
    payload?.data?.details?.error_msg ||
    `${fallback}: HTTP ${status}`
  );
}

function normalizeMode(mode) {
  const value = String(mode || '').toLowerCase();
  if (value === 'http') {
    return 'http';
  }
  if (value === 'tun') {
    return 'tun';
  }
  return 'unknown';
}

export async function syncRunStatus() {
  try {
    const response = await fetch('/api/run/status');
    const payload = await response.json().catch(() => ({}));
    handleAuthenticationFailure(response.status, payload, syncUnauthenticatedAuthState);
    if (!response.ok) {
      throw new Error(extractApiErrorMessage(payload, response.status, 'Failed to sync run status'));
    }
    if (payload?.code !== 0) {
      throw new Error(payload?.msg || 'Failed to sync run status');
    }

    const data = payload?.data || {};
    runMode.value = normalizeMode(data?.current_mode);
    runIsRunning.value = Boolean(data?.is_running);
    runStatus.value = typeof data?.status === 'string' ? data.status : '';
    runDescription.value = typeof data?.description === 'string' ? data.description : '';
    runWintunDependency.value = {
      supported: Boolean(data?.wintun_dependency?.supported),
      required: Boolean(data?.wintun_dependency?.required),
      available: data?.wintun_dependency?.available !== false,
      installing: Boolean(data?.wintun_dependency?.installing),
      state: typeof data?.wintun_dependency?.state === 'string' ? data.wintun_dependency.state : 'unknown',
      message: typeof data?.wintun_dependency?.message === 'string' ? data.wintun_dependency.message : '',
      error: typeof data?.wintun_dependency?.error === 'string' ? data.wintun_dependency.error : '',
      installPath: typeof data?.wintun_dependency?.install_path === 'string' ? data.wintun_dependency.install_path : '',
      targetPath: typeof data?.wintun_dependency?.target_path === 'string' ? data.wintun_dependency.target_path : '',
      architecture: typeof data?.wintun_dependency?.architecture === 'string' ? data.wintun_dependency.architecture : '',
      downloadURL: typeof data?.wintun_dependency?.download_url === 'string' ? data.wintun_dependency.download_url : '',
      lastChecked: Number(data?.wintun_dependency?.last_checked || 0),
      updatedAt: Number(data?.wintun_dependency?.updated_at || 0)
    };
    runSyncError.value = '';
    return data;
  } catch (error) {
    runSyncError.value = error instanceof Error ? error.message : 'Unknown error';
    throw error;
  }
}

export async function syncStartupStatus() {
  try {
    const response = await fetch('/api/startup/status');
    const payload = await response.json().catch(() => ({}));
    handleAuthenticationFailure(response.status, payload, syncUnauthenticatedAuthState);
    if (!response.ok) {
      throw new Error(extractApiErrorMessage(payload, response.status, 'Failed to sync startup status'));
    }
    if (payload?.code !== 0) {
      throw new Error(payload?.msg || 'Failed to sync startup status');
    }

    const data = payload?.data || {};
    startupStatus.value = typeof data?.status === 'string' ? data.status : 'UNKNOWN';
    syncAuthFromStartupStatus(data);
    return data;
  } catch {
    startupStatus.value = 'UNKNOWN';
    return null;
  }
}

export async function refreshRunState() {
  const isFirstLoad = !hasLoadedOnce;
  if (isFirstLoad) {
    loading.value = true;
  }

  const [runResult] = await Promise.allSettled([
    syncRunStatus(),
    syncStartupStatus()
  ]);

  hasLoadedOnce = true;
  if (isFirstLoad) {
    loading.value = false;
  }

  if (runResult.status === 'rejected') {
    throw runResult.reason;
  }

  return {
    mode: runMode.value,
    isRunning: runIsRunning.value,
    status: runStatus.value,
    description: runDescription.value,
    wintunDependency: runWintunDependency.value,
    startupStatus: startupStatus.value
  };
}

export function startPolling() {
  refCount += 1;
  if (refCount === 1) {
    refreshRunState().catch(() => {});
    pollTimer = window.setInterval(() => {
      refreshRunState().catch(() => {});
    }, POLL_INTERVAL);
  }
}

export function stopPolling() {
  refCount -= 1;
  if (refCount <= 0) {
    refCount = 0;
    if (pollTimer !== null) {
      window.clearInterval(pollTimer);
      pollTimer = null;
    }
  }
}

export function useRunStatus() {
  return {
    runMode,
    runIsRunning,
    runStatus,
    runDescription,
    runWintunDependency,
    runSyncError,
    startupStatus,
    loading,
    syncRunStatus,
    syncStartupStatus,
    refreshRunState,
    startPolling,
    stopPolling
  };
}
