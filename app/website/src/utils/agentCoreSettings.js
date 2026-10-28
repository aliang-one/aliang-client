// AliangCode 内核 LLM 设置(仪表盘)纯逻辑:校验与归一。零依赖,vitest 直接
// 覆盖;UI 装配在 settings/AgentCoreSettings.vue。校验规则与后端
// AgentCoreLLMConfig.Validate(processor/config/types.go)同源,两端不许分叉。

function isLoopbackHost(hostname) {
  const host = String(hostname || '').trim().toLowerCase();
  if (!host) return false;
  if (host === 'localhost' || host.endsWith('.localhost')) return true;
  if (host === '::1' || /^\[::1\]$/.test(host)) return true;
  return /^127\.\d+\.\d+\.\d+$/.test(host);
}

// 返回 ''(合法)或 i18n 键。规则与后端一致:启用时 URL 必填可解析;
// 非回环 ⇒ key 必填。
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
