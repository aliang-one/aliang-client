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

// OpenViking 跨设备记忆卡(spec Phase2 §7.5/§7.7):拨测读点 + 显式 apply。
export async function getOpenVikingStatus() {
  return request('/api/agent/openviking/status', { method: 'GET' });
}

export async function applyOpenVikingNow() {
  return request('/api/agent/openviking/apply', { method: 'POST' });
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

// —— 路由 canonical(apply store)读写:唯一事实源为 GET/POST /api/config/routing。
// 消费方=EgressTargetSettings 卡(规则分流实际出口);载荷必须是 GET 回的整份
// canonical 原样修改,后端 DisallowUnknownFields 多键即 400,绝不可与 customer
// 配置对象混入。── (2026-11-16 深度模式 toSocks 孤儿开关事故的 UI 救生通道)
export async function getRoutingConfig() {
  return request('/api/config/routing', { method: 'GET' });
}

export async function saveRoutingConfig(payload) {
  return request('/api/config/routing', {
    method: 'POST',
    body: JSON.stringify(payload),
  });
}

// —— 事件监听器(specs/005-event-listener):agent 本地 REST 代理 /api/listeners,
// C1 评审定案=代理信封化转发——2xx 走 common.Success 重裹 {code:0,msg,data}
// (data=云端对象,201/204 拍平 200),非 2xx 保留云端状态码且 msg 携带云端
// error/detail 文案(I1:业务错误在浏览器是可读的 thrown Error),离线=502
// {code:"listener_cloud_offline"}(msg 带哨兵前缀)。request() 抛出的 Error
// 只带 envelope.msg(不带 code)——离线判定用 utils/listenerFormat 的
// isListenerCloudOffline,勿在此做错误映射。返回值在此处解包到消费面直取
// 的形状(list/listener/events),调用方不再碰信封。──
export { LISTENER_CLOUD_OFFLINE } from '../utils/listenerFormat';

// → listeners 数组(云端 {listeners:[…]} 经代理落进 data;FR-11 归一后
//   缺键/非数组在此兜 [])。
export async function getListeners() {
  const data = await request('/api/listeners', { method: 'GET' });
  return Array.isArray(data?.listeners) ? data.listeners : [];
}

// → 云端回传的 listener 对象(缺省 null)。
export async function listenerCreate(payload) {
  const data = await request('/api/listeners', {
    method: 'POST',
    body: JSON.stringify(payload || {}),
  });
  return data?.listener ?? null;
}

// → 云端回传的 listener 对象(缺省 null;复活撞 LWW 时这里 throw 可读错误)。
export async function listenerPatch(id, payload) {
  const data = await request(`/api/listeners/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(payload || {}),
  });
  return data?.listener ?? null;
}

// → 直通 data(云端 DELETE 回空对象,无消费字段)。
export async function listenerDelete(id) {
  return request(`/api/listeners/${encodeURIComponent(id)}`, { method: 'DELETE' });
}

// → events 数组(最近 20 条由面板再截;缺键/非数组兜 [])。
export async function getListenerEvents(id) {
  const data = await request(`/api/listeners/${encodeURIComponent(id)}/events`, { method: 'GET' });
  return Array.isArray(data?.events) ? data.events : [];
}

// ——— 网页终端(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md)———

export async function getWebTerminalState() {
  return request('/api/agent/web-terminal', { method: 'GET' });
}

// token 由调用方(useWebTerminal)注入 header——plain HTTP proxy 全量转发。
// 业务失败不走 throw:{ok:false, reason,...} 结构化返回(前端要读锁定秒数)。
export async function webTerminalCommand(payload, token = '') {
  const headers = {};
  if (token) headers['X-Web-Terminal-Token'] = token;
  return request('/api/agent/web-terminal', {
    method: 'POST',
    body: JSON.stringify(payload),
    headers,
  });
}

// ——— 网页终端文件浏览(spec §6.3;阶段3 四端点)———
// 四端点同门同治:runtime 腿主开关+令牌头(X-Web-Terminal-Token);业务拒绝
// 走 HTTP 错误码(403 越界/关停/令牌无效、404 不存在、400 坏文件名)。list/
// read 经 request() 即抛 envelope.msg;download/upload 是 binary/multipart,
// 绕开 request() 的 JSON Content-Type 假设,信封/鉴权失败处理与
// uploadChatAttachment 同构(extractEnvelope + handleAuthenticationFailure)。

export async function fsList(path, token) {
  return request(`/api/agent/fs/list?path=${encodeURIComponent(path)}`, {
    method: 'GET',
    headers: { 'X-Web-Terminal-Token': token },
  });
}

export async function fsRead(path, token) {
  return request(`/api/agent/fs/read?path=${encodeURIComponent(path)}`, {
    method: 'GET',
    headers: { 'X-Web-Terminal-Token': token },
  });
}

// 下载:binary 流(ServeContent),不走 request()(它假定 JSON envelope)。
// 失败体仍是 JSON 信封(403/404/400),读出 msg 让 UI 文案可读。
export async function fsDownload(path, token) {
  const response = await fetch(`/api/agent/fs/download?path=${encodeURIComponent(path)}`, {
    method: 'GET',
    credentials: 'same-origin',
    headers: { 'X-Web-Terminal-Token': token },
  });
  if (!response.ok) {
    const json = await response.json().catch(() => ({}));
    const envelope = extractEnvelope(json);
    handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);
    throw new Error(envelope.msg || `Download failed: HTTP ${response.status}`);
  }
  return await response.blob();
}

// 上传:multipart FormData(Content-Type 由浏览器带 boundary,不能手设)。
// data 形状 {path(冲突加时间戳后缀的终名), bytes}。
export async function fsUpload(dir, file, token) {
  const form = new FormData();
  form.append('dir', dir);
  form.append('file', file);
  const response = await fetch('/api/agent/fs/upload', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'X-Web-Terminal-Token': token },
    body: form,
  });
  const json = await response.json().catch(() => ({}));
  const envelope = extractEnvelope(json);
  handleAuthenticationFailure(response.status, envelope, syncUnauthenticatedAuthState);
  if (!response.ok || envelope.code !== 0) {
    throw new Error(envelope.msg || `Upload failed: HTTP ${response.status}`);
  }
  return envelope.data;
}

// ——— 文件管理批(Task C:mkdir/rename/delete 写操作三端点)———
// 与 list/read 同门同治(X-Web-Terminal-Token 令牌头);小 JSON POST 走
// request() 即抛 envelope.msg——服务端错误文案用户可读直显(104 重名/100 非
// 法名/103 不存在/102 权限或越根)。dir 缺省 "." 由服务端兜,前端始终传规范
// currentDir(空串时传 '.');rename 载荷按计划契约 snake_case(old_name/new_name,
// handler 双拼宽容);delete 的 recursive 仅供审计溯源(服务端目录恒 RemoveAll)。
export async function fsMkdir(dir, name, token) {
  return request('/api/agent/fs/mkdir', {
    method: 'POST',
    body: JSON.stringify({ dir, name }),
    headers: { 'X-Web-Terminal-Token': token },
  });
}

export async function fsRename(dir, oldName, newName, token) {
  return request('/api/agent/fs/rename', {
    method: 'POST',
    body: JSON.stringify({ dir, old_name: oldName, new_name: newName }),
    headers: { 'X-Web-Terminal-Token': token },
  });
}

export async function fsDelete(path, recursive, token) {
  return request('/api/agent/fs/delete', {
    method: 'POST',
    body: JSON.stringify({ path, recursive }),
    headers: { 'X-Web-Terminal-Token': token },
  });
}
