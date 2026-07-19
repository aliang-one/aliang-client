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
  return rawRequest('/api/quick-setup/render', {
    method: 'POST',
    body: JSON.stringify({
      software,
      key_ids: Array.isArray(keyIds) ? keyIds : [],
      ...(options && typeof options === 'object' ? options : {}),
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
