import { syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';

function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: payload && typeof payload === 'object' ? payload.data : null
  };
}

async function request(path) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json'
    }
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope;
}

export async function getAliangLinkStatus({ probe = false } = {}) {
  const query = new URLSearchParams();
  if (probe) {
    query.set('probe', '1');
  }

  const suffix = query.toString() ? `?${query.toString()}` : '';
  return request(`/api/run/aliang/status${suffix}`);
}
