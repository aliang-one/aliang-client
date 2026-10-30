// AliangCode 内核 LLM 设置(仪表盘)纯逻辑:校验与归一。零依赖,vitest 直接
// 覆盖;UI 装配在 settings/AgentCoreSettings.vue。校验规则与后端
// AgentCoreLLMConfig.Validate(processor/config/types.go)同源,两端不许分叉。
// 已知接受的分歧:WHATWG URL 会把 127.1/0x7f.0.0.1/2130706433 归一成
// 127.0.0.1(前端放行),Go url.Parse 则保持原样并拒绝——后端是权威闸
// (保存 400+读取防御),前端宽松只影响 UX。已接受的顺序分歧:后端先查
// scheme 后查 host,前端相反——两端都拒,仅提示文案不同。

function isLoopbackHost(hostname) {
  const host = String(hostname || '').trim().toLowerCase();
  if (!host) return false;
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  if (host === '::1' || /^\[::1\]$/.test(host)) return true;
  return /^127\.\d+\.\d+\.\d+$/.test(host);
}

// 返回 ''(合法)或 i18n 键。规则与后端一致:启用时 URL 必填可解析;
// 非回环 ⇒ key 必填。输入是**表单格式**(enable/baseUrl/apiKey);线格式键
// (enabled)传进来会被当作未启用——线格式载荷先过 normalizeAgentCoreLLMConfig。
export function validateAgentCoreLLMConfig({ enable, baseUrl, apiKey } = {}) {
  if (!enable) return '';
  const url = String(baseUrl || '').trim();
  if (!url) return 'agentCore_error_urlRequired';
  let parsed;
  try {
    parsed = new URL(url);
  } catch {
    return 'agentCore_error_urlInvalid';
  }
  if (!parsed.hostname) return 'agentCore_error_urlInvalid';
  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return 'agentCore_error_scheme';
  }
  if (!isLoopbackHost(parsed.hostname) && !String(apiKey || '').trim()) {
    return 'agentCore_error_keyRequired';
  }
  return '';
}

// 服务端载荷 → 归一配置。⚠️ 输出必须是线格式(`enabled`,Go tag
// json:"enabled"):本函数的输出会经 SettingsPage 的 normalizeCustomerConfig →
// buildCustomerConfigPatch 原样进 POST patch——布尔键在这里走样成 `enable` 的
// 话,后端 encoding/json 会静默丢弃它,层永远激活不了(且保存+回读后面板
// 显示会翻回关闭)。面板表单内部状态键用 enable,由组件自行映射;
// custom_env_vars 的 enable 惯例不可照搬(Go 侧有 UnmarshalJSON 别名,本结构没有)。
// 字符串原样保留不 trim——trim 发生在校验判空与后端读取(EffectiveAgentCoreLLM),
// 归一层不改值。
export function normalizeAgentCoreLLMConfig(source) {
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return { enabled: false, base_url: '', api_key: '' };
  }
  return {
    enabled: typeof source.enabled === 'boolean' ? source.enabled : false,
    base_url: typeof source.base_url === 'string' ? source.base_url : '',
    api_key: typeof source.api_key === 'string' ? source.api_key : '',
  };
}

// agentcore_llm 清空必须可达(spec §3.3):buildConfigPatch 对空串返回
// undefined(键不进 patch),后端 deepMergeJSONObjects 只增不减——不显式写
// 空值,旧 key 会永久留存。enabled 是布尔,走 Object.is 比较,天然可达。
// 返回 patch,存在清空项时写入 patch.agentcore_llm。
export function applyAgentCoreLLMClearing(patch, nextLLM, currentLLM) {
  const next = nextLLM || {};
  const current = currentLLM || {};
  for (const key of ['base_url', 'api_key']) {
    if (!String(next[key] ?? '').trim() && String(current[key] ?? '').trim()) {
      patch.agentcore_llm = patch.agentcore_llm && typeof patch.agentcore_llm === 'object'
        ? patch.agentcore_llm
        : {};
      patch.agentcore_llm[key] = '';
    }
  }
  return patch;
}

// ---- customer.agentcore(行为调优) ----
// 与 agentcore_llm(端点)同面板不同层。校验规则与后端
// AgentCoreTuningConfig.Validate(processor/config/types.go)同源,两端不许分叉:
// 0/未填 = 用内核默认,1..50 = 显式阈值,负数/非整数/越界拒绝。
// ⚠️ 与 agentcore_llm 的字符串不同,数字的「清空」天然可达:归一成 0 后,
// buildConfigPatch 的 Object.is 比较会把 0≠旧值写进 patch(0 显式落盘,
// 后端 omitempty 存储时抹掉 = 回到默认),所以这里不需要 applyXxxClearing。

// 服务端载荷 → 归一线格式。非对象/缺键输出 {max_identical_tool_turns: 0}
// (= 未配置,与 Go 侧 nil 层等价)。非法数字(负数/非整数)归一成 0——归一层
// 不报错,报错是 validate 的职责;这里只保证输出永远是合法线格式。
export function normalizeAgentCoreTuning(source) {
  let turns = 0;
  if (source && typeof source === 'object' && !Array.isArray(source)) {
    const raw = source.max_identical_tool_turns;
    if (typeof raw === 'number' && Number.isInteger(raw) && raw > 0) {
      turns = raw;
    }
  }
  return { max_identical_tool_turns: turns };
}

// 表单值('' 或数字字符串)→ 校验。返回 ''(合法)或 i18n 键。空串与 0 都是
// 「用默认」,合法;正整数上限 50 与后端 Validate 对齐。
export function validateAgentCoreTuning(maxTurns) {
  const raw = String(maxTurns ?? '').trim();
  if (!raw) return '';
  const n = Number(raw);
  if (!Number.isInteger(n) || n < 0) return 'agentCore_error_guardInvalid';
  if (n === 0) return '';
  if (n > 50) return 'agentCore_error_guardInvalid';
  return '';
}

// 表单值 → 线格式数字。空串/0 → 0(= 默认,后端 omitempty 存储时抹掉)。
// 非法输入不会被提交(validate 挡在 save 前),这里兜底归 0。
export function agentCoreTuningToLineFormat(maxTurns) {
  const raw = String(maxTurns ?? '').trim();
  if (!raw) return 0;
  const n = Number(raw);
  if (!Number.isInteger(n) || n <= 0) return 0;
  return n;
}
