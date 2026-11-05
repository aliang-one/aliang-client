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

// MCP 与技能只读报告：设备侧 MCP server / skill 清单 + 每项对 AliangCode
// 内核的生效状态（injected / skipped_headers / not_collected）。
export async function getAgentIntegrations() {
  return request('/api/agent/integrations', { method: 'GET' });
}

export async function getAgentSessionDetail(sessionId, { limit, before } = {}) {
  const params = new URLSearchParams();
  if (limit) params.set('limit', String(limit));
  if (before) params.set('before', before);
  const query = params.toString();
  return request(
    `/api/agent/session?id=${encodeURIComponent(sessionId)}${query ? `&${query}` : ''}`,
    { method: 'GET' }
  );
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

// aliang-peer 跨设备 MCP:GET 注册状态 / POST 应用注册(runtime 执行)。
export async function getPeerMCPStatus() {
  return request('/api/agent/peer/mcp', { method: 'GET' });
}

// 载荷=完整 peer_mcp 配置对象(载荷即真相):runtime 内存配置是启动快照,
// 显式载荷保证两段式保存的立即性;nil 子键=跟随总开关(由归一层三态契约保证)。
export async function setPeerMCPRegistration(peerMcp) {
  return request('/api/agent/peer/mcp', {
    method: 'POST',
    body: JSON.stringify({ peer_mcp: peerMcp }),
  });
}

// --- 文件访问授权（一次性预先授权 macOS 隐私目录） ---

export async function getAgentFileAccess() {
  return request('/api/agent/file-access', { method: 'GET' });
}

export async function requestAgentFileAccess() {
  return request('/api/agent/file-access/request', { method: 'POST' });
}

// pane: 'files'（文件与文件夹）| 'allfiles'（完全磁盘访问权限）
export async function openAgentFileAccessSettings(pane = 'files') {
  return request('/api/agent/file-access/settings', {
    method: 'POST',
    body: JSON.stringify({ pane }),
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
