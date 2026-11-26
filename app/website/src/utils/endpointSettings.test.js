import { describe, expect, it } from 'vitest';
import {
  endpointToPayload,
  formatModelsText,
  maskAPIKey,
  normalizeEndpointView,
  parseModelsText,
  validateEndpointConfig,
} from './endpointSettings';

// 合法基线配置(BYO 远端):各用例在此基础上翻转单字段。
const validBase = () => ({
  name: 'prov-a',
  baseUrl: 'https://gw.example.com',
  apiKey: 'sk-x',
  protocolAnthropic: true,
  protocolOpenAI: false,
  autoGroup: true,
  priority: 10,
  maxConcurrency: 5,
  models: '[]',
  builtin: false,
});

describe('validateEndpointConfig 与 Go LLMEndpoint.Validate 同源', () => {
  it('合法配置全绿', () => {
    expect(validateEndpointConfig(validBase())).toBe('');
  });

  it('name 必填(trim 空白)', () => {
    expect(validateEndpointConfig({ ...validBase(), name: '' }))
      .toBe('endpointManager_error_nameRequired');
    expect(validateEndpointConfig({ ...validBase(), name: '   ' }))
      .toBe('endpointManager_error_nameRequired');
  });

  it('双协议全 0 拒(Go: at least one protocol)', () => {
    expect(validateEndpointConfig({ ...validBase(), protocolAnthropic: false, protocolOpenAI: false }))
      .toBe('endpointManager_error_protocolRequired');
  });

  it('anthropic 单开合法', () => {
    expect(validateEndpointConfig({ ...validBase(), protocolAnthropic: true, protocolOpenAI: false }))
      .toBe('');
  });

  it('openai 单开合法', () => {
    expect(validateEndpointConfig({ ...validBase(), protocolAnthropic: false, protocolOpenAI: true }))
      .toBe('');
  });

  it('双协议全开合法', () => {
    expect(validateEndpointConfig({ ...validBase(), protocolAnthropic: true, protocolOpenAI: true }))
      .toBe('');
  });

  it('base_url 畸形拒(非 URL/无 host)', () => {
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'not a url' }))
      .toBe('endpointManager_error_baseUrlInvalid');
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'http://' }))
      .toBe('endpointManager_error_baseUrlInvalid');
  });

  it('base_url 非 http/https scheme 拒', () => {
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'ftp://gw.example.com' }))
      .toBe('endpointManager_error_baseUrlInvalid');
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'file:///etc/passwd' }))
      .toBe('endpointManager_error_baseUrlInvalid');
  });

  it('非回环缺 key 拒(Go: 非回环必须 api_key)', () => {
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'https://gw.example.com', apiKey: '' }))
      .toBe('endpointManager_error_keyRequired');
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'https://gw.example.com', apiKey: '  ' }))
      .toBe('endpointManager_error_keyRequired');
  });

  it('回环无 key 合法(回环可走平台 relay 注入)', () => {
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'http://127.0.0.1:8082', apiKey: '' }))
      .toBe('');
    expect(validateEndpointConfig({ ...validBase(), baseUrl: 'http://localhost:8082', apiKey: '' }))
      .toBe('');
  });

  it('base_url 为空合法(builtin 走内置隧道)', () => {
    expect(validateEndpointConfig({ ...validBase(), baseUrl: '', apiKey: '' }))
      .toBe('');
  });

  it('priority/max_concurrency 负数拒(Go: 必须 >= 0)', () => {
    expect(validateEndpointConfig({ ...validBase(), priority: -1 }))
      .toBe('endpointManager_error_priority');
    expect(validateEndpointConfig({ ...validBase(), maxConcurrency: -3 }))
      .toBe('endpointManager_error_maxConcurrency');
  });

  it('priority/max_concurrency 非整数/非数字拒', () => {
    expect(validateEndpointConfig({ ...validBase(), priority: 'abc' }))
      .toBe('endpointManager_error_priority');
    expect(validateEndpointConfig({ ...validBase(), priority: 1.5 }))
      .toBe('endpointManager_error_priority');
    expect(validateEndpointConfig({ ...validBase(), maxConcurrency: 'x' }))
      .toBe('endpointManager_error_maxConcurrency');
  });

  it('priority/max_concurrency 0 合法(0 = 不限)', () => {
    expect(validateEndpointConfig({ ...validBase(), priority: 0, maxConcurrency: 0 }))
      .toBe('');
  });

  it('builtin base_url 非空拒(Go: builtin 行 base_url 必须为空)', () => {
    expect(validateEndpointConfig({ ...validBase(), builtin: true, baseUrl: 'https://x.example.com' }))
      .toBe('endpointManager_error_builtinBaseUrl');
  });

  it('builtin base_url 为空合法', () => {
    expect(validateEndpointConfig({
      ...validBase(), builtin: true, baseUrl: '', apiKey: 'sk-b',
      protocolAnthropic: true, protocolOpenAI: true, autoGroup: true,
    })).toBe('');
  });

  it('models 合法 JSON 数组通过', () => {
    expect(validateEndpointConfig({ ...validBase(), models: '["glm-4.6"]' })).toBe('');
    expect(validateEndpointConfig({ ...validBase(), models: '[]' })).toBe('');
  });

  it('models 非 JSON 数组拒(Go: 必须是 JSON 字符串数组)', () => {
    expect(validateEndpointConfig({ ...validBase(), models: 'not json' }))
      .toBe('endpointManager_error_models');
    expect(validateEndpointConfig({ ...validBase(), models: '{"a":1}' }))
      .toBe('endpointManager_error_models');
    expect(validateEndpointConfig({ ...validBase(), models: '[1,2]' }))
      .toBe('endpointManager_error_models');
  });

  it('models 空值合法([] = 不限制)', () => {
    expect(validateEndpointConfig({ ...validBase(), models: '' })).toBe('');
    expect(validateEndpointConfig({ ...validBase(), models: '  ' })).toBe('');
  });
});

describe('maskAPIKey 与 Go handler 同源', () => {
  it('空值返回空串', () => {
    expect(maskAPIKey('')).toBe('');
    expect(maskAPIKey(null)).toBe('');
    expect(maskAPIKey(undefined)).toBe('');
  });

  it('长度 ≤ 8 全掩(短 key 泄露比例太高)', () => {
    expect(maskAPIKey('short')).toBe('********');
    expect(maskAPIKey('12345678')).toBe('********');
    expect(maskAPIKey('a')).toBe('********');
  });

  it('前 4 + *** + 后 4', () => {
    expect(maskAPIKey('sk-abcdefghij1234')).toBe('sk-a***1234');
    expect(maskAPIKey('1234567890')).toBe('1234***7890');
  });
});

describe('parseModelsText 文本域 → JSON 数组', () => {
  it('换行分隔', () => {
    expect(parseModelsText('glm-4.6\nclaude-3')).toBe('["glm-4.6","claude-3"]');
  });

  it('逗号分隔', () => {
    expect(parseModelsText('glm-4.6,claude-3')).toBe('["glm-4.6","claude-3"]');
  });

  it('混合换行+逗号+空白段', () => {
    expect(parseModelsText('glm-4.6,\n claude-3,,\n  '))
      .toBe('["glm-4.6","claude-3"]');
  });

  it('空输入 → [](不限制)', () => {
    expect(parseModelsText('')).toBe('[]');
    expect(parseModelsText('   ')).toBe('[]');
    expect(parseModelsText(undefined)).toBe('[]');
  });

  it('空段过滤后为空 → []', () => {
    expect(parseModelsText(',,,')).toBe('[]');
  });
});

describe('formatModelsText JSON 数组 → 文本域', () => {
  it('数组 → 每行一个', () => {
    expect(formatModelsText('["glm-4.6","claude-3"]')).toBe('glm-4.6\nclaude-3');
  });

  it('空数组 → 空串', () => {
    expect(formatModelsText('[]')).toBe('');
  });

  it('空值/畸形 JSON 安全回落空串', () => {
    expect(formatModelsText('')).toBe('');
    expect(formatModelsText(null)).toBe('');
    expect(formatModelsText('not json')).toBe('');
    expect(formatModelsText('{"a":1}')).toBe('');
  });
});

describe('normalizeEndpointView 服务端载荷 → 组件展示形态', () => {
  it('完整载荷逐字段映射(线格式 → camelCase)', () => {
    const view = normalizeEndpointView({
      id: 'e1',
      name: 'prov',
      base_url: 'https://a.b',
      api_key: 'sk-a***7890',
      protocol_anthropic: true,
      protocol_openai: false,
      auto_group: true,
      priority: 10,
      max_concurrency: 5,
      models: '["glm-4.6"]',
      enabled: true,
      builtin: false,
      health_status: 'ok',
      last_error: '',
      inflight: 3,
      created_at: 1,
      updated_at: 2,
    });
    expect(view).toEqual({
      id: 'e1',
      name: 'prov',
      baseUrl: 'https://a.b',
      apiKeyMasked: 'sk-a***7890',
      apiKey: '',
      protocolAnthropic: true,
      protocolOpenAI: false,
      autoGroup: true,
      priority: 10,
      maxConcurrency: 5,
      models: '["glm-4.6"]',
      enabled: true,
      builtin: false,
      healthStatus: 'ok',
      lastError: '',
      inflight: 3,
    });
  });

  it('apiKey 永不回显完整值(掩码走 apiKeyMasked,表单 apiKey 恒空)', () => {
    const view = normalizeEndpointView({ id: 'x', api_key: 'sk-real-secret-value' });
    expect(view.apiKeyMasked).toBe('sk-real-secret-value');
    expect(view.apiKey).toBe('');
  });

  it('缺键/脏输入安全回落', () => {
    expect(normalizeEndpointView(undefined)).toBeNull();
    expect(normalizeEndpointView('x')).toBeNull();
    const view = normalizeEndpointView({});
    expect(view.enabled).toBe(true);
    expect(view.builtin).toBe(false);
    expect(view.healthStatus).toBe('ok');
    expect(view.inflight).toBe(0);
    expect(view.priority).toBe(0);
    expect(view.maxConcurrency).toBe(0);
  });
});

describe('endpointToPayload 表单 → 服务端线格式', () => {
  it('BYO 行逐字段映射(布尔键不发字符串)', () => {
    const payload = endpointToPayload({
      name: 'prov',
      baseUrl: 'https://a.b',
      apiKey: 'sk-x',
      protocolAnthropic: true,
      protocolOpenAI: true,
      autoGroup: true,
      priority: '10',
      maxConcurrency: '5',
      modelsText: 'glm-4.6\nclaude-3',
      enabled: true,
    }, false);
    expect(payload).toEqual({
      name: 'prov',
      base_url: 'https://a.b',
      api_key: 'sk-x',
      protocol_anthropic: true,
      protocol_openai: true,
      auto_group: true,
      priority: 10,
      max_concurrency: 5,
      models: '["glm-4.6","claude-3"]',
      enabled: true,
    });
  });

  it('builtin 行 base_url 恒空、auto_group 恒 true(禁改),api_key 可带', () => {
    const payload = endpointToPayload({
      name: 'builtin',
      baseUrl: 'https://should-be-ignored.example.com',
      apiKey: 'sk-login',
      protocolAnthropic: true,
      protocolOpenAI: true,
      autoGroup: false,
      priority: 0,
      maxConcurrency: 0,
      modelsText: '',
      enabled: true,
    }, true);
    expect(payload.base_url).toBe('');
    expect(payload.auto_group).toBe(true);
    expect(payload.api_key).toBe('sk-login');
  });

  it('api_key 空值不进 payload(PUT 语义:空=不改,§17 不变量 7)', () => {
    const payload = endpointToPayload({
      name: 'prov',
      baseUrl: 'https://a.b',
      apiKey: '',
      protocolAnthropic: true,
      protocolOpenAI: false,
      autoGroup: true,
      priority: 0,
      maxConcurrency: 0,
      modelsText: '',
      enabled: true,
    }, false);
    expect('api_key' in payload).toBe(false);
  });

  it('models 空文本 → [](不限制),非空文本 → JSON 数组', () => {
    const empty = endpointToPayload({
      name: 'p', baseUrl: '', apiKey: 'k', protocolAnthropic: true, protocolOpenAI: true,
      autoGroup: true, priority: 0, maxConcurrency: 0, modelsText: '', enabled: true,
    }, false);
    expect(empty.models).toBe('[]');

    const filled = endpointToPayload({
      name: 'p', baseUrl: '', apiKey: 'k', protocolAnthropic: true, protocolOpenAI: true,
      autoGroup: true, priority: 0, maxConcurrency: 0, modelsText: 'm1, m2', enabled: true,
    }, false);
    expect(filled.models).toBe('["m1","m2"]');
  });

  it('enabled 缺省为 true(POST 契约:enabled 缺省 true)', () => {
    const payload = endpointToPayload({
      name: 'p', baseUrl: '', apiKey: 'k', protocolAnthropic: true, protocolOpenAI: true,
      autoGroup: true, priority: 0, maxConcurrency: 0, modelsText: '',
    }, false);
    expect(payload.enabled).toBe(true);
  });
});
