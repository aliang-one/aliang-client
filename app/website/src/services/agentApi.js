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

// 统一 MCP 注册表管理（第一方资产，三内核消费）。
export async function getAliangMCPRegistry() {
  return request('/api/agent/mcp', { method: 'GET' });
}

export async function putAliangMCPServer(name, entry) {
  return request(`/api/agent/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'PUT',
    body: JSON.stringify(entry),
  });
}

export async function deleteAliangMCPServer(name) {
  return request(`/api/agent/mcp/servers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  });
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

// 设备门禁开关(2026-11-07):GET 读本机两枚 peer 门禁现值(真值源=服务端设备
// 注册表,经 agent 过滤本机),POST 部分更新落库(至少一个显式布尔键)。
export async function getPeerDeviceGates() {
  return request('/api/agent/peer/gates', { method: 'GET' });
}

export async function setPeerDeviceGates(payload) {
  return request('/api/agent/peer/gates', {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

// --- mesh 节点转发网（仪表盘组网卡只读数据源；design §9）---
// 配置写不走这两个端点——mesh 配置经既有 /api/config/customer 的
// customer.mesh 段提交（Task 14 后端决策，避免 peer_mcp 两段式复杂度）。

// GET /api/mesh/status：本机 device_id、开关现值、LAN 监听口、已配 peers
// 与 beacon 候选的只读聚合。拉取失败由调用方降级处理（组网卡状态条降级态）。
export async function getMeshStatus() {
  return request('/api/mesh/status', { method: 'GET' });
}

// GET /api/mesh/lan_peers：LAN beacon 候选（离线可用），供兜底卡 Addr 点选。
export async function getMeshLanPeers() {
  return request('/api/mesh/lan_peers', { method: 'GET' });
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

// 聊天图片上传（spec 2026-11-05-chat-image-pipeline §3.4）：multipart 直传
// 本机 agent（不能用 request()——它强制 JSON Content-Type，会破坏 multipart
// boundary）。信封/鉴权处理与 request() 同构；data.attachment 形状
// {ref,path,name,mime,bytes}，ref 供渲染 URL，path 供 ai.message 载荷。
export async function uploadChatAttachment(sessionId, file) {
  const form = new FormData();
  form.append('session_id', sessionId);
  form.append('file', file);
  const response = await fetch('/api/agent/ai/attachment/upload', {
    method: 'POST',
    credentials: 'same-origin',
    body: form,
  });
  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);
  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);
  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Upload failed with HTTP ${response.status}`);
  }
  const attachment = envelope.data && envelope.data.attachment;
  if (!attachment || !attachment.ref || !attachment.path) {
    throw new Error('Malformed upload response');
  }
  return attachment;
}
