import { describe, expect, it } from 'vitest';
import {
  applyAgentCoreLLMClearing,
  normalizeAgentCoreLLMConfig,
  validateAgentCoreLLMConfig,
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
