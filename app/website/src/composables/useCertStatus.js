import { ref } from 'vue';
import { handleAuthenticationFailure } from '../services/authFailure';
import { syncUnauthenticatedAuthState } from '../stores/auth';

const API_BASE = '/api';
const CERT_TYPE = 'mitm-ca';

const certStatus = ref(null);
const loading = ref(false);
const error = ref(null);

let pollTimer = null;
let refCount = 0;

const POLL_INTERVAL = 3000;

function statusKey(data) {
  return JSON.stringify([
    data.is_exported,
    data.is_installed,
    data.is_trusted,
    data.trust_status,
    data.fingerprint,
    data.source,
    data.activated_at,
    data.can_rollback
  ]);
}

let lastKey = '';

function isCertTrustedAndInstalled(data) {
  return Boolean(data?.is_installed) && Boolean(data?.is_trusted);
}

function ensurePollingState() {
  const shouldPoll = refCount > 0 && !isCertTrustedAndInstalled(certStatus.value);
  if (shouldPoll) {
    if (pollTimer === null) {
      pollTimer = window.setInterval(fetchStatus, POLL_INTERVAL);
    }
    return;
  }

  if (pollTimer !== null) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
}

async function fetchStatus() {
  const isFirstLoad = !certStatus.value;
  if (isFirstLoad) loading.value = true;

  try {
    const res = await fetch(`${API_BASE}/cert/status?cert_type=${encodeURIComponent(CERT_TYPE)}`);
    const payload = await res.json().catch(() => ({}));
    handleAuthenticationFailure(res.status, payload, syncUnauthenticatedAuthState);
    if (!res.ok) {
      throw new Error(payload?.data?.error_msg || payload?.msg || payload?.message || `HTTP ${res.status}`);
    }
    const data = payload.data || {};
    const key = statusKey(data);
    if (key !== lastKey) {
      lastKey = key;
      certStatus.value = data;
    }
    if (error.value) error.value = null;
  } catch (err) {
    if (!certStatus.value) {
      const fallback = { is_exported: false, is_installed: false, is_trusted: false, trust_status: 'not_found' };
      lastKey = statusKey(fallback);
      certStatus.value = fallback;
    }
    if (!error.value) error.value = err.message;
  } finally {
    ensurePollingState();
    if (isFirstLoad) loading.value = false;
  }
}

function startPolling() {
  refCount++;
  if (refCount === 1) {
    fetchStatus();
    ensurePollingState();
  }
}

function stopPolling() {
  refCount--;
  if (refCount <= 0) {
    refCount = 0;
  }
  ensurePollingState();
}

function invalidateCache() {
  lastKey = '';
}

export function useCertStatus() {
  return {
    certStatus,
    loading,
    error,
    fetchStatus,
    startPolling,
    stopPolling,
    invalidateCache
  };
}
