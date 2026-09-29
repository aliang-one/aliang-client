import { markDashboardSessionRequired, syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';

function extractOuterEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: typeof payload.data !== 'undefined' ? payload.data : null,
  };
}

// HTTP 401 + {code:101, data:{error_code:'unauthorized'}} 是 dashboard 会话
// 中间件的签名（区别于上游 token 过期的 401）：此时上游快照可能仍 active，
// 通用恢复会被翻回 true 而让登录界面永远无法出现，必须强制停留在登录视图。
function isDashboardSessionFailure(responseStatus, envelope) {
  if (Number(responseStatus) !== 401) return false;
  const data = envelope && typeof envelope.data === 'object' && envelope.data ? envelope.data : null;
  return data?.error_code === 'unauthorized'
    || Number(data?.code) === 101
    || Number(envelope?.code) === 101;
}

function handleSessionFailure(responseStatus, envelope) {
  handleAuthenticationFailure(responseStatus, envelope, (message) => {
    if (isDashboardSessionFailure(responseStatus, envelope)) {
      markDashboardSessionRequired(message);
      return;
    }
    syncUnauthenticatedAuthState(message);
  });
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

  handleSessionFailure(response.status, envelope);

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

// apply：{ software, files }——逐文件写入 { path, content, format, kind }
export async function applyQuickSetup(payload) {
  const body = payload && typeof payload === 'object' ? payload : {};
  return rawRequest('/api/quick-setup/apply', {
    method: 'POST',
    body: JSON.stringify({
      software: body.software,
      files: Array.isArray(body.files) ? body.files : [],
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
