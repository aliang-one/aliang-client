import { describe, expect, it } from 'vitest';
import {
  agentCoreTuningToLineFormat,
  applyAgentCoreLLMClearing,
  normalizeAgentCoreLLMConfig,
  normalizeAgentCoreTuning,
  normalizePeerMCPConfig,
  validateAgentCoreLLMConfig,
  validateAgentCoreTuning,
} from './agentCoreSettings';

describe('validateAgentCoreLLMConfig', () => {
  it('未启用不校验', () => {
    expect(validateAgentCoreLLMConfig({ enable: false, baseUrl: '', apiKey: '' })).toBe('');
  });

  it('启用+URL 空', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: '  ', apiKey: '' }))
      .toBe('agentCore_error_urlRequired');
  });

  it('启用+URL 畸形', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'not a url', apiKey: 'k' }))
      .toBe('agentCore_error_urlInvalid');
  });

  it('启用+回环+无 key 合法', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'http://127.0.0.1:8082', apiKey: '' }))
      .toBe('');
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'http://localhost:8082', apiKey: '' }))
      .toBe('');
  });

  it('启用+远端+无 key 拒', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'https://gw.example.com', apiKey: '' }))
      .toBe('agentCore_error_keyRequired');
  });

  it('启用+远端+有 key 合法', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'https://gw.example.com', apiKey: 'sk-x' }))
      .toBe('');
  });

  it('启用+非 http/https 协议拒', () => {
    expect(validateAgentCoreLLMConfig({ enable: true, baseUrl: 'ftp://gw.example.com', apiKey: 'k' }))
      .toBe('agentCore_error_scheme');
  });
});

describe('normalizeAgentCoreLLMConfig', () => {
  it('脏输入安全回落默认', () => {
    expect(normalizeAgentCoreLLMConfig(undefined)).toEqual({ enabled: false, base_url: '', api_key: '' });
    expect(normalizeAgentCoreLLMConfig('x')).toEqual({ enabled: false, base_url: '', api_key: '' });
  });

  it('读服务端线格式 enabled,输出保持线格式(可直进 patch)', () => {
    expect(normalizeAgentCoreLLMConfig({ enabled: true, base_url: ' https://a.b ', api_key: 'sk=1 2' }))
      .toEqual({ enabled: true, base_url: ' https://a.b ', api_key: 'sk=1 2' });
  });

  it('线格式守卫:输出键必须是 enabled,绝不含 enable', () => {
    const out = normalizeAgentCoreLLMConfig({ enable: true });
    expect(out.enabled).toBe(false);
    expect(Object.keys(out)).not.toContain('enable');
  });
});

describe('applyAgentCoreLLMClearing', () => {
  it('next 空且 current 非空 → 显式写空值', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: '', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ base_url: '', api_key: '' });
  });

  it('next 有值 → 只清空空的那维', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: 'https://c.d', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ api_key: '' });
  });

  it('双方都空 → 不产生键', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: '', api_key: '' }, { base_url: '', api_key: '' });
    expect(patch.agentcore_llm).toBeUndefined();
  });

  it('与既有 patch.agentcore_llm 合并而非覆盖', () => {
    const patch = { agentcore_llm: { enabled: true } };
    applyAgentCoreLLMClearing(patch, { base_url: '', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ enabled: true, base_url: '', api_key: '' });
  });

  it('纯空白 next 值视为空(trim 语义)', () => {
    const patch = {};
    applyAgentCoreLLMClearing(patch, { base_url: '   ', api_key: '' }, { base_url: 'https://a.b', api_key: 'sk-x' });
    expect(patch.agentcore_llm).toEqual({ base_url: '', api_key: '' });
  });
});

// ---- customer.agentcore(行为调优) ----

describe('normalizeAgentCoreTuning', () => {
  it('非对象/缺键 → 0(= 未配置,与 Go nil 层等价)', () => {
    expect(normalizeAgentCoreTuning(undefined)).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning(null)).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning('oops')).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({})).toEqual({ max_identical_tool_turns: 0 });
  });

  it('合法数字原样,非法数字归一成 0(报错是 validate 的职责)', () => {
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 7 })).toEqual({ max_identical_tool_turns: 7 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 0 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: -3 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 1.5 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: '7' })).toEqual({ max_identical_tool_turns: 0 });
  });
});

describe('validateAgentCoreTuning', () => {
  it('空串/纯空白 = 用默认,合法', () => {
    expect(validateAgentCoreTuning('')).toBe('');
    expect(validateAgentCoreTuning('   ')).toBe('');
    expect(validateAgentCoreTuning(undefined)).toBe('');
  });

  it('0 = 用默认,合法;1..50 合法', () => {
    expect(validateAgentCoreTuning('0')).toBe('');
    expect(validateAgentCoreTuning('1')).toBe('');
    expect(validateAgentCoreTuning('5')).toBe('');
    expect(validateAgentCoreTuning('50')).toBe('');
  });

  it('负数/非整数/越界/非数字 拒绝', () => {
    expect(validateAgentCoreTuning('-1')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('2.5')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('51')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('abc')).toBe('agentCore_error_guardInvalid');
  });
});

describe('agentCoreTuningToLineFormat', () => {
  it('空串/0 → 0(后端 omitempty 抹掉 = 回默认)', () => {
    expect(agentCoreTuningToLineFormat('')).toBe(0);
    expect(agentCoreTuningToLineFormat('0')).toBe(0);
    expect(agentCoreTuningToLineFormat(undefined)).toBe(0);
  });

  it('正整数原样,非法兜底归 0(提交前已被 validate 挡住)', () => {
    expect(agentCoreTuningToLineFormat('5')).toBe(5);
    expect(agentCoreTuningToLineFormat('50')).toBe(50);
    expect(agentCoreTuningToLineFormat('abc')).toBe(0);
  });
});

// 全局总闸键透传（2026-10-31 验收缺陷①）：normalizeAgentCoreTuning 的输出
// 经 normalizeCustomerConfig → buildCustomerConfigPatch 原样进 POST patch 与
// GET 回显——在这里剥掉的话，两张全局开关卡「保存 200 但永不落盘、刷新恒
// 显默认开」。本组用例锁死：布尔键透传、非布尔/缺键不产出键。
describe('normalizeAgentCoreTuning 全局总闸透传', () => {
  it('布尔键显式透传', () => {
    expect(normalizeAgentCoreTuning({ mcp_enabled: false }))
      .toEqual({ max_identical_tool_turns: 0, mcp_enabled: false });
    expect(normalizeAgentCoreTuning({ skills_enabled: true }))
      .toEqual({ max_identical_tool_turns: 0, skills_enabled: true });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 3, mcp_enabled: true, skills_enabled: false }))
      .toEqual({ max_identical_tool_turns: 3, mcp_enabled: true, skills_enabled: false });
  });

  it('非布尔值不透传', () => {
    expect(normalizeAgentCoreTuning({ mcp_enabled: 'false' }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ skills_enabled: null }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ mcp_enabled: 1 }))
      .toEqual({ max_identical_tool_turns: 0 });
  });

  it('缺键不产出键(=未配置,后端 nil 视为开启)', () => {
    const next = normalizeAgentCoreTuning({ max_identical_tool_turns: 2 });
    expect('mcp_enabled' in next).toBe(false);
    expect('skills_enabled' in next).toBe(false);
    const empty = normalizeAgentCoreTuning(undefined);
    expect('mcp_enabled' in empty).toBe(false);
    expect('skills_enabled' in empty).toBe(false);
  });
});

// peer_mcp 归一:布尔透传为线格式 enable;缺键/非对象/非布尔 → undefined
// (不进 patch 与回显,后端 nil 视为开启——三态与 mcp_enabled 同族)。
// ⚠️ 还必须在 SettingsPage.normalizeCustomerConfig 挂键,否则「保存 HTTP 200
// 但永不落盘、刷新恒显默认开」(2026-10-31 验收缺陷①同族)。
describe('normalizePeerMCPConfig', () => {
  it('布尔透传为线格式 enable', () => {
    expect(normalizePeerMCPConfig({ enable: false })).toEqual({ enable: false });
    expect(normalizePeerMCPConfig({ enable: true })).toEqual({ enable: true });
  });

  it('缺键/非对象/非布尔 → undefined', () => {
    expect(normalizePeerMCPConfig(undefined)).toBeUndefined();
    expect(normalizePeerMCPConfig('x')).toBeUndefined();
    expect(normalizePeerMCPConfig({})).toBeUndefined();
    expect(normalizePeerMCPConfig({ enable: 'yes' })).toBeUndefined();
  });
});
