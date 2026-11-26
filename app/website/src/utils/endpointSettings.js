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
// inflight)→ 组件展示/表单形态。⚠️ api_key 永不回显完整值:完整值只存在于
// apiKeyMasked(掩码串)供展示,表单输入 apiKey 恒空——编辑时留空 = 不改
// (§17 不变量 7)。缺键安全回落,builtin/enabled 缺省与 POST 契约对齐。
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
  };
}

// 表单 → POST/PUT 线格式载荷。isBuiltin:builtin 行 base_url 恒空、auto_group
// 恒 true(UI 禁改,这里兜一道防表单态残留;§17 不变量 8)。api_key 空值不进
// 载荷——PUT 语义「空 = 不改」要求键缺省而非空串;POST 侧缺键 = 服务端空值,
// 与「新端点无凭据」合法形态一致(非回环缺 key 已被 validate 挡住)。
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
    priority: Number(String(f.priority ?? '0').trim()) || 0,
    max_concurrency: Number(String(f.maxConcurrency ?? '0').trim()) || 0,
    models,
    enabled: f.enabled !== false,
  };
  if (apiKey) payload.api_key = apiKey;
  return payload;
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
