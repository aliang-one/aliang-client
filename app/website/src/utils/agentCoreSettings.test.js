import { describe, expect, it } from 'vitest';
import {
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
