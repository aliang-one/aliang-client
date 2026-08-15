import { syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';

function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  const data = payload?.data && typeof payload.data === 'object' ? payload.data : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    status: typeof data.status === 'string' ? data.status : '',
    error: typeof data.error === 'string' ? data.error : '',
    message: typeof data.msg === 'string' ? data.msg : '',
    data: typeof data.data !== 'undefined' ? data.data : null
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
    throw new Error(envelope.message || envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope;
}

export async function getDashboardStats() {
  return request('/api/dashboard/stats');
}

export async function getDashboardTrend() {
  const query = new URLSearchParams({
    granularity: 'day'
  });
  return request(`/api/dashboard/trend?${query.toString()}`);
}

export async function getDashboardModels() {
  return request('/api/dashboard/models');
}

export async function getDashboardUsageRecords({
  page = 1,
  perPage = 10,
  requestType = 'all'
} = {}) {
  const query = new URLSearchParams();
  query.set('page', String(page));
  query.set('per_page', String(perPage));

  if (requestType === 'chat' || requestType === 'image') {
    query.set('request_type', requestType);
  }

  if (requestType === 'stream') {
    query.set('stream', 'true');
  }

  return request(`/api/dashboard/usage?${query.toString()}`);
}

export async function getDashboardHealth() {
  return request('/api/health');
}
