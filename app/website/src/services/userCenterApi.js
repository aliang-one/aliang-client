import { clearChatIdentityProfileCache, persistChatIdentityProfile } from '../utils/chatIdentityCache';
import { syncUnauthenticatedAuthState } from '../stores/auth';

function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  const data = payload?.data && typeof payload.data === 'object' ? payload.data : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    status: typeof data.status === 'string' ? data.status : '',
    error: typeof data.error === 'string' ? data.error : '',
    message: typeof data.msg === 'string' ? data.msg : '',
    data: data && typeof data.data !== 'undefined' ? data.data : null
  };
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {})
    }
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.message || envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  if (envelope.status === 'unauthenticated' || envelope.error === 'session_expired') {
    syncUnauthenticatedAuthState(envelope.message || envelope.msg);
  }

  return envelope;
}

function syncChatIdentityCache(envelope) {
  if (!envelope || typeof envelope !== 'object') {
    return;
  }

  if (envelope.status === 'success' && envelope.data && typeof envelope.data === 'object') {
    persistChatIdentityProfile(envelope.data);
    return;
  }

  if (envelope.status === 'unauthenticated') {
    clearChatIdentityProfileCache();
  }
}

export async function getUserCenterProfile() {
  const envelope = await request('/api/user-center/profile', {
    method: 'GET'
  });
  syncChatIdentityCache(envelope);
  return envelope;
}

export async function updateUserCenterProfile(username) {
  const envelope = await request('/api/user-center/profile', {
    method: 'PUT',
    body: JSON.stringify({ username })
  });
  syncChatIdentityCache(envelope);
  return envelope;
}

export async function getUserCenterUsageSummary() {
  return request('/api/user-center/usage/summary', {
    method: 'GET'
  });
}

export async function getUserCenterUsageProgress() {
  return request('/api/user-center/usage/progress', {
    method: 'GET'
  });
}

export async function getUserCenterAPIKeys() {
  return request('/api/user-center/api-keys', {
    method: 'GET'
  });
}

export async function redeemUserCenterCode(code) {
  return request('/api/user-center/redeem', {
    method: 'POST',
    body: JSON.stringify({ code })
  });
}
