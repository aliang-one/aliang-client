import { syncUnauthenticatedAuthState } from '../stores/auth';
import { handleAuthenticationFailure } from './authFailure';

function extractEnvelope(json) {
  const payload = json && typeof json === 'object' ? json : {};
  return {
    code: typeof payload.code === 'number' ? payload.code : 0,
    msg: typeof payload.msg === 'string' ? payload.msg : '',
    data: typeof payload.data !== 'undefined' ? payload.data : null,
  };
}

async function request(path, options = {}) {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...(options.headers || {}),
    },
  });

  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);

  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);

  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Request failed with HTTP ${response.status}`);
  }

  return envelope.data;
}

export async function getAgentStatus() {
  return request('/api/agent/status', { method: 'GET' });
}

export async function enableAgent() {
  return request('/api/agent/enable', { method: 'POST' });
}

export async function disableAgent(reason = 'manual') {
  return request('/api/agent/disable', {
    method: 'POST',
    body: JSON.stringify({ reason }),
  });
}

export async function launchAgentTool(payload) {
  return request('/api/agent/tools/launch', {
    method: 'POST',
    body: JSON.stringify(payload || {}),
  });
}

export async function getAgentSessions() {
  return request('/api/agent/sessions', { method: 'GET' });
}

export async function getAgentRuntimeSessions() {
  return request('/api/agent/runtime-sessions', { method: 'GET' });
}

export async function getAgentSessionDetail(sessionId, { limit, before, signal } = {}) {
  const params = new URLSearchParams();
  if (limit) params.set('limit', String(limit));
  if (before) params.set('before', before);
  const query = params.toString();
  return request(
    `/api/agent/session?id=${encodeURIComponent(sessionId)}${query ? `&${query}` : ''}`,
    { method: 'GET', signal }
  );
}

// 转录冷路径（首请求可能触发设备侧冷扫描/读盘，>15s 是常态）：AbortController
// 硬顶 40s。超时抛 'transcript-timeout' 标记错误；重试=再次调用（UI 重试按钮）。
export const AGENT_TRANSCRIPT_TIMEOUT_MS = 40000;

export async function loadAgentTranscript(sessionId, { limit = 50, timeoutMs = AGENT_TRANSCRIPT_TIMEOUT_MS } = {}) {
  const controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
  const timer = setTimeout(() => controller && controller.abort(), timeoutMs);
  try {
    return await getAgentSessionDetail(sessionId, { limit, signal: controller ? controller.signal : undefined });
  } catch (err) {
    if (err && err.name === 'AbortError') {
      throw new Error('transcript-timeout');
    }
    throw err;
  } finally {
    clearTimeout(timer);
  }
}

export async function getAgentScanDirectories() {
  return request('/api/agent/scan-directories', { method: 'GET' });
}

export async function setAgentScanDirectories({ enabled, directories }) {
  return request('/api/agent/scan-directories', {
    method: 'POST',
    body: JSON.stringify({ enabled, directories }),
  });
}

export async function getAgentTerminalScrollback(sessionId, { limit } = {}) {
  const params = new URLSearchParams();
  if (limit) params.set('limit', String(limit));
  const query = params.toString();
  return request(
    `/api/agent/terminal/${encodeURIComponent(sessionId)}/scrollback${query ? `?${query}` : ''}`,
    { method: 'GET' }
  );
}
