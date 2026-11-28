// LLM 端点管理(56432 聚合路由)纯逻辑:校验/归一/掩码/models 文本域↔JSON 数组。
// 零依赖,vitest 直接覆盖;UI 装配在 components/settings/EndpointManagerSettings.vue。
// 校验规则与后端 LLMEndpoint.Validate(processor/config/endpoint.go)同源,两端不许
// 分叉。已知接受的分歧与 agentCoreSettings.js 相同:WHATWG URL 会把 127.1/
// 0x7f.0.0.1/2130706433 归一成 127.0.0.1(前端放行),Go url.Parse 保持原样并拒
// ——后端是权威闸,前端宽松只影响 UX。掩码/PUT 空值不改/builtin 禁改等不变量
// 见 spec §17 不变量 7/8。

function isLoopbackHost(hostname) {
  const host = String(hostname || '').trim().toLowerCase();
  if (!host) return false;
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  if (host === '::1' || /^\[::1\]$/.test(host)) return true;
  return /^127\.\d+\.\d+\.\d+$/.test(host);
}

// 掩码显示:前 4 + *** + 后 4;长度 ≤ 8 全掩(短 key 泄露比例太高)。与 Go
// handler maskAPIKey(endpoint_handler.go)同源。⚠️ 前端只负责「展示」掩码——
// GET 响应里 api_key 已被服务端掩码,前端永不接触完整值(§17 不变量 7)。
export function maskAPIKey(key) {
  const k = String(key || '');
  if (!k) return '';
  if (k.length <= 8) return '********';
  return k.slice(0, 4) + '***' + k.slice(k.length - 4);
}

// 输入是**表单格式**(camelCase:baseUrl/apiKey/protocolAnthropic/...)。
// 返回 ''(合法)或 i18n 键。规则与后端 LLMEndpoint.Validate 同源:
//   name 必填;≥1 协议 toggle;base_url 非空时须 http/https 且含 host;
//   非回环 host 必须 api_key(回环可走平台 relay 注入);priority/max_concurrency
//   ≥ 0;builtin base_url 必须空;models 是合法 JSON 字符串数组。
export function validateEndpointConfig({
  name = '',
  baseUrl = '',
  apiKey = '',
  protocolAnthropic = false,
  protocolOpenAI = false,
  priority = 0,
  maxConcurrency = 0,
  models = '',
  builtin = false,
} = {}) {
  if (!String(name || '').trim()) return 'endpointManager_error_nameRequired';

  if (!protocolAnthropic && !protocolOpenAI) {
    return 'endpointManager_error_protocolRequired';
  }

  const url = String(baseUrl || '').trim();
  if (url) {
    let parsed;
    try {
      parsed = new URL(url);
    } catch {
      return 'endpointManager_error_baseUrlInvalid';
    }
    if (!parsed.hostname) return 'endpointManager_error_baseUrlInvalid';
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
      return 'endpointManager_error_baseUrlInvalid';
    }
    if (!isLoopbackHost(parsed.hostname) && !String(apiKey || '').trim()) {
      return 'endpointManager_error_keyRequired';
    }
  }

  const p = typeof priority === 'number' ? priority : Number(String(priority ?? '').trim());
  if (!Number.isInteger(p) || p < 0) return 'endpointManager_error_priority';

  const m = typeof maxConcurrency === 'number'
    ? maxConcurrency
    : Number(String(maxConcurrency ?? '').trim());
  if (!Number.isInteger(m) || m < 0) return 'endpointManager_error_maxConcurrency';

  if (builtin && url) return 'endpointManager_error_builtinBaseUrl';

  // 与 Go 同一检查覆盖全部形状:合法 JSON ∧ 是数组 ∧ 元素全是字符串。
  const modelsText = String(models || '').trim();
  if (modelsText) {
    let arr;
    try {
      arr = JSON.parse(modelsText);
    } catch {
      return 'endpointManager_error_models';
    }
    if (!Array.isArray(arr) || !arr.every((item) => typeof item === 'string')) {
      return 'endpointManager_error_models';
    }
  }
  return '';
}

// 服务端列表项(GET /api/endpoints,LLMEndpoint JSON 视图 + 掩码 api_key +
// inflight + 二期运行态六字段)→ 组件展示/表单形态。⚠️ api_key 永不回显完整
// 值:完整值只存在于 apiKeyMasked(掩码串)供展示,表单输入 apiKey 恒空——
// 编辑时留空 = 不改(§17 不变量 7)。缺键安全回落,builtin/enabled 缺省与 POST
// 契约对齐。二期(spec 2026-11-27 §8)直透字段:egress_kernel(出口指示/徽标)、
// last_used_at + total_requests + failed_requests(行尾计数)、cooldown_until +
// rate_limited_until(三词徽标的「恢复于/限流至 HH:MM」tooltip)。
export function normalizeEndpointView(source) {
  if (!source || typeof source !== 'object' || Array.isArray(source)) return null;
  const num = (v, fallback = 0) =>
    typeof v === 'number' && Number.isFinite(v) ? v : fallback;
  return {
    id: typeof source.id === 'string' ? source.id : '',
    name: typeof source.name === 'string' ? source.name : '',
    baseUrl: typeof source.base_url === 'string' ? source.base_url : '',
    apiKeyMasked: typeof source.api_key === 'string' ? source.api_key : '',
    apiKey: '', // 表单输入位:服务端只回掩码,编辑留空 = 不改
    protocolAnthropic: source.protocol_anthropic === true,
    protocolOpenAI: source.protocol_openai === true,
    autoGroup: source.auto_group === true,
    priority: num(source.priority),
    maxConcurrency: num(source.max_concurrency),
    models: typeof source.models === 'string' ? source.models : '[]',
    enabled: source.enabled !== false, // POST 契约:enabled 缺省 true
    builtin: source.builtin === true,
    healthStatus: typeof source.health_status === 'string' ? source.health_status : 'ok',
    lastError: typeof source.last_error === 'string' ? source.last_error : '',
    inflight: num(source.inflight),
    egressKernel: source.egress_kernel === true,
    lastUsedAt: num(source.last_used_at),
    totalRequests: num(source.total_requests),
    failedRequests: num(source.failed_requests),
    cooldownUntil: num(source.cooldown_until),
    rateLimitedUntil: num(source.rate_limited_until),
  };
}

// 表单 → POST/PUT 线格式载荷。isBuiltin:builtin 行 base_url 恒空、auto_group
// 恒 true、priority 恒 0(UI 禁改,这里兜一道防表单态残留;§17 不变量 8——
// store 层对 builtin 行 priority≠0 直接拒写,兜底防的是残留值静默 400)。
// api_key 空值不进载荷——PUT 语义「空 = 不改」要求键缺省而非空串;POST 侧缺
// 键 = 服务端空值,与「新端点无凭据」合法形态一致(非回环缺 key 已被
// validate 挡住)。二期:enabled 不再进载荷——表单勾选已移除(行上开关取代),
// 激活迁移走行开关的独立快速路径 PUT {enabled};POST 缺键服务端默认 true。
export function endpointToPayload(form, isBuiltin = false) {
  const f = form || {};
  const apiKey = String(f.apiKey || '').trim();
  // 表单字段是 modelsText(文本域原文);组件 saveForm 直接传 form,这里必须
  // 读 modelsText——读错键会恒发 '[]'(静默丢模型列表)。
  const modelsText = String(f.modelsText ?? '').trim();
  const models = modelsText
    ? JSON.stringify(modelsText.split(/[,\n]+/).map((s) => s.trim()).filter(Boolean))
    : '[]';
  const payload = {
    name: String(f.name || '').trim(),
    base_url: isBuiltin ? '' : String(f.baseUrl || '').trim(),
    protocol_anthropic: f.protocolAnthropic === true,
    protocol_openai: f.protocolOpenAI === true,
    auto_group: isBuiltin ? true : f.autoGroup === true,
    priority: isBuiltin ? 0 : (Number(String(f.priority ?? '0').trim()) || 0),
    max_concurrency: Number(String(f.maxConcurrency ?? '0').trim()) || 0,
    models,
  };
  if (apiKey) payload.api_key = apiKey;
  return payload;
}

// ---- 二期行上徽标/出口指示纯函数(spec 2026-11-27 §7.1/§7.2)----------------

// unix 秒 → 本地 HH:MM(补零)。0/负/非数回落空串(调用方以空串判「无 tooltip」)。
export function formatClockTime(unixSec) {
  const sec = Number(unixSec);
  if (!Number.isFinite(sec) || sec <= 0) return '';
  const d = new Date(sec * 1000);
  const hh = String(d.getHours()).padStart(2, '0');
  const mm = String(d.getMinutes()).padStart(2, '0');
  return `${hh}:${mm}`;
}

// last_used_at(unix 秒)→ i18n 相对时间档位。0/缺键 = 从未调用;未来时间戳
// (时钟偏差)按「刚刚」兜底,不画负数。返回 {key, params} 由调用方 t() 渲染,
// 与组件解耦便于纯测。
export function formatLastUsedAt(unixSec, now = Date.now()) {
  const nowMs = Number(now);
  const nowSafe = Number.isFinite(nowMs) && nowMs > 0 ? nowMs : Date.now();
  const sec = Number(unixSec);
  if (!Number.isFinite(sec) || sec <= 0) {
    return { key: 'endpointManager_lastUsedNever', params: {} };
  }
  const diffMs = nowSafe - sec * 1000;
  if (diffMs < 60000) return { key: 'endpointManager_lastUsedJustNow', params: {} };
  const minutes = Math.floor(diffMs / 60000);
  if (minutes < 60) return { key: 'endpointManager_lastUsedMinAgo', params: { n: minutes } };
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return { key: 'endpointManager_lastUsedHourAgo', params: { n: hours } };
  return { key: 'endpointManager_lastUsedDayAgo', params: { n: Math.floor(hours / 24) } };
}

// 出口指示三态(spec 二期 §7.2,聊天页抽屉吃它):输入 GET /api/endpoints
// 原始行数组(线格式,内部归一),输出 {mode, name}:
//   active —— egress_kernel=1 且 enabled:出口生效;
//   paused —— egress_kernel=1 但 enabled=0:出口暂停保留,auto 调度中
//             (spec §4:enabled=0 与 egress_kernel=1 可共存,解绑不发生);
//   auto   —— 无 egress 行:未指定,auto 调度。
// 服务端事务保证全表至多一行 egress_kernel=1;多行(异常形态)取第一行兜底。
export function egressIndicator(rows) {
  const list = Array.isArray(rows) ? rows : [];
  const egress = list
    .map((r) => (r && typeof r === 'object' && !Array.isArray(r) ? normalizeEndpointView(r) : null))
    .filter(Boolean)
    .find((v) => v.egressKernel === true);
  if (!egress) return { mode: 'auto', name: '' };
  return egress.enabled === false
    ? { mode: 'paused', name: egress.name }
    : { mode: 'active', name: egress.name };
}

// models 文本域(换行/逗号分隔,可混用)→ JSON 数组字符串。空段过滤;
// 全空 → '[]'(= 不限制模型)。
export function parseModelsText(text) {
  const items = String(text || '')
    .split(/[,\n]+/)
    .map((s) => s.trim())
    .filter(Boolean);
  return JSON.stringify(items);
}

// JSON 数组字符串 → 文本域(每行一个)。畸形 JSON/非数组安全回落空串。
export function formatModelsText(modelsJson) {
  const raw = String(modelsJson || '').trim();
  if (!raw) return '';
  let arr;
  try {
    arr = JSON.parse(raw);
  } catch {
    return '';
  }
  if (!Array.isArray(arr)) return '';
  return arr.filter((m) => typeof m === 'string').join('\n');
}
