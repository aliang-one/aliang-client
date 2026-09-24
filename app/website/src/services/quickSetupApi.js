import { syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';

function extractOuterEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: typeof payload.data !== 'undefined' ? payload.data : null,
  };
}

async function rawRequest(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractOuterEnvelope(json);

  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope.data;
}

export async function getQuickSetupCatalog() {
  const payload = await rawRequest('/api/quick-setup/catalog', {
    method: 'GET',
  });
  const wrapper = payload && typeof payload === 'object' ? payload : {};
  return {
    status: typeof wrapper.status === 'string' ? wrapper.status : '',
    error: typeof wrapper.error === 'string' ? wrapper.error : '',
    message: typeof wrapper.msg === 'string' ? wrapper.msg : '',
    data: wrapper.data && typeof wrapper.data === 'object' ? wrapper.data : null,
  };
}

export async function renderQuickSetup(software, keyIds = [], options = {}) {
  const opts = options && typeof options === 'object' ? options : {};
  return rawRequest('/api/quick-setup/render', {
    method: 'POST',
    body: JSON.stringify({
      ...opts,
      software,
      key_ids: Array.isArray(keyIds) ? keyIds : [],
      // 接入模式只接受 local/public，其余一律归一为 public（与后端缺省一致）。
      mode: opts.mode === 'local' ? 'local' : 'public',
    }),
  });
}

export async function getQuickSetupModels(keyId) {
  return rawRequest('/api/quick-setup/models', {
    method: 'POST',
    body: JSON.stringify({
      key_id: Number(keyId) || 0,
    }),
  });
}

export async function applyQuickSetup(software, files) {
  return rawRequest('/api/quick-setup/apply', {
    method: 'POST',
    body: JSON.stringify({
      software,
      files: Array.isArray(files) ? files : [],
    }),
  });
}

export async function fetchConfigState(software) {
  return rawRequest(`/api/quick-setup/config-state?software=${encodeURIComponent(software)}`, {
    method: 'GET',
  });
}

export async function restoreConfig(software) {
  return rawRequest('/api/quick-setup/restore', {
    method: 'POST',
    body: JSON.stringify({ software }),
  });
}

export async function createCombo(payload) {
  return rawRequest('/api/quick-setup/combos', {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

export async function updateCombo(id, payload) {
  return rawRequest(`/api/quick-setup/combos/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(payload),
  });
}

export async function deleteCombo(id) {
  return rawRequest(`/api/quick-setup/combos/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

export async function setComboDefault(id) {
  return rawRequest(`/api/quick-setup/combos/${encodeURIComponent(id)}/default`, {
    method: 'POST',
  });
}
