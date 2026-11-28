import { syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';
import { assistPrefix } from './assistPrefix';

function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: payload && typeof payload === 'object' ? payload.data : null
  };
}

async function request(path, options = {}) {
  const response = await fetch(assistPrefix() + path, {
    credentials: 'same-origin',
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {})
    },
    ...options
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope;
}

export async function getStatusSummary() {
  return request('/api/status/summary');
}

export async function getStatusConnections() {
  return request('/api/status/connections');
}

// 接入客户端台账（服务端视角「谁经 SSH 入口连进来」）：在线 + 关闭历史。
export async function getIngressSessions() {
  return request('/api/status/ingress');
}

// 踢出一条在线接入连接（按公钥指纹）。返回 data: { kicked, fingerprint }。
export async function kickIngressSession(fingerprint) {
  return request('/api/status/ingress/kick', { method: 'POST', body: JSON.stringify({ fingerprint }) });
}

// 加入拒绝名单（指纹或 mesh 标签；默认同时断开在线连接，noKick 只拉黑）。
export async function blockIngressSession({ fingerprint, deviceId, noKick } = {}) {
  return request('/api/status/ingress/block', {
    method: 'POST',
    body: JSON.stringify({ fingerprint: fingerprint || '', device_id: deviceId || '', no_kick: Boolean(noKick) })
  });
}

// 移出拒绝名单。
export async function unblockIngressSession({ fingerprint, deviceId } = {}) {
  return request('/api/status/ingress/unblock', {
    method: 'POST',
    body: JSON.stringify({ fingerprint: fingerprint || '', device_id: deviceId || '' })
  });
}

// SSH 出站「测试连接」：POST 当前表单凭据做一次现场探测（服务端不落盘）。
// 返回 data: { ok, stage, error, latency_millis, host_key_fingerprint }。
export async function testSshConnection(payload) {
  return request('/api/proxy/ssh/test', { method: 'POST', body: JSON.stringify(payload) });
}
