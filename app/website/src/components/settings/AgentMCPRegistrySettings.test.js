// MCP 注册表管理卡（Task 10）。挂载用 createApp + jsdom 手法（仓内无
// @vue/test-utils，PeerAccessSettings.test.js 同范式）；i18n 与 agentApi 必须
// mock：前者避免 locale 依赖，后者阻止 mounted 的真实网络请求。
// 断言走 i18n 键名（mock t 原样回键），不硬编码文案。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../i18n', () => ({
  // 键原样回吐，参数值尾随拼接：让 delete_confirm 这类插值文案的
  // 参数（如行名）也能被断言到。
  useI18n: () => ({ t: (k, params = {}) => (params && Object.keys(params).length ? `${k} ${Object.values(params).join(' ')}` : k) }),
}));
vi.mock('../../services/agentApi', () => ({
  getAliangMCPRegistry: vi.fn(),
  putAliangMCPServer: vi.fn(),
  deleteAliangMCPServer: vi.fn(),
  getAgentIntegrations: vi.fn(),
}));

import { createApp, h } from 'vue';
import AgentMCPRegistrySettings, {
  formatKeyValueLines,
  parseKeyValueLines,
} from './AgentMCPRegistrySettings.vue';
import {
  deleteAliangMCPServer,
  getAgentIntegrations,
  getAliangMCPRegistry,
  putAliangMCPServer,
} from '../../services/agentApi';

const registryData = () => ({
  version: 1,
  servers: {
    demo: { type: 'stdio', command: '/usr/bin/demo', args: ['--fast'], enabled: true },
  },
});

// 微任务冲刷：mounted → loadAll 是多层 await 链，多轮 Promise.resolve 兜够。
async function flush(rounds = 8) {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
}

function mountComp() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(AgentMCPRegistrySettings) });
  app.mount(el);
  return el;
}

// 新增按钮带 material 图标 span，textContent 是「图标名 + 文案」——按归一化
// 空白后的包含关系匹配（各按钮文案键互不为子串，无歧义）。
function buttonByText(el, text) {
  return [...el.querySelectorAll('button')].find((btn) =>
    btn.textContent.replace(/\s+/g, ' ').trim().includes(text));
}

function setInput(el, field, value) {
  const input = el.querySelector(`[data-field="${field}"]`);
  input.value = value;
  input.dispatchEvent(new Event('input'));
}

describe('AgentMCPRegistrySettings MCP 注册表管理卡', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    getAliangMCPRegistry.mockResolvedValue(registryData());
    getAgentIntegrations.mockResolvedValue({ mcp_servers: [] });
    putAliangMCPServer.mockResolvedValue({ codex_sync: { ok: true } });
    deleteAliangMCPServer.mockResolvedValue({ codex_sync: { ok: true } });
  });

  // confirm spy 等 spyOn 统一还原，防跨用例泄漏（beforeEach 会重设 agentApi 实现）。
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('挂载后渲染注册表行（名称/类型/入口）', async () => {
    const el = mountComp();
    await flush();
    expect(el.textContent).toContain('demo');
    expect(el.textContent).toContain('stdio');
    // 入口 = command + args 拼接（stdio 形态）
    expect(el.textContent).toContain('/usr/bin/demo --fast');
    // 空态文案不应出现（有条目）
    expect(el.textContent).not.toContain('agent_mcp_registry_empty');
  });

  it('编辑弹窗 type 切 stdio→http 时字段组联动（url 出现、command 消失）', async () => {
    const el = mountComp();
    await flush();
    buttonByText(el, 'agent_mcp_registry_edit').click();
    await flush();
    expect(el.querySelector('[data-field="command"]')).toBeTruthy();
    expect(el.querySelector('[data-field="url"]')).toBeFalsy();
    const select = el.querySelector('[data-field="type"]');
    select.value = 'http';
    select.dispatchEvent(new Event('change'));
    await flush();
    expect(el.querySelector('[data-field="url"]')).toBeTruthy();
    expect(el.querySelector('[data-field="command"]')).toBeFalsy();
  });

  it('客户端校验：非法名提交不发 PUT 且显示 name_invalid', async () => {
    const el = mountComp();
    await flush();
    buttonByText(el, 'agent_mcp_registry_add').click();
    await flush();
    setInput(el, 'name', 'aliang-shadow');
    buttonByText(el, 'agent_mcp_registry_save').click();
    await flush();
    expect(putAliangMCPServer).not.toHaveBeenCalled();
    expect(el.textContent).toContain('agent_mcp_registry_name_invalid');
    // 弹窗保持打开，等用户修正
    expect(el.querySelector('[data-field="name"]')).toBeTruthy();
  });

  it('删除确认后调用 DELETE 并刷新列表', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const el = mountComp();
    await flush();
    expect(getAliangMCPRegistry).toHaveBeenCalledTimes(1);
    buttonByText(el, 'agent_mcp_registry_delete').click();
    await flush();
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy.mock.calls[0][0]).toContain('demo');
    expect(deleteAliangMCPServer).toHaveBeenCalledWith('demo');
    // 刷新：注册表与集成清单都重拉
    expect(getAliangMCPRegistry.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(getAgentIntegrations.mock.calls.length).toBeGreaterThanOrEqual(2);
    confirmSpy.mockRestore();
  });

  it('删除 404（条目已不存在）按良性处理：无错误文案且照常重拉', async () => {
    // request 封装对 404 抛泛型 Error（无 status、消息形如 "Request failed with HTTP 404"）
    deleteAliangMCPServer.mockRejectedValue(new Error('Request failed with HTTP 404'));
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    const el = mountComp();
    await flush();
    const regBefore = getAliangMCPRegistry.mock.calls.length;
    const integBefore = getAgentIntegrations.mock.calls.length;
    buttonByText(el, 'agent_mcp_registry_delete').click();
    await flush();
    expect(deleteAliangMCPServer).toHaveBeenCalledWith('demo');
    // 良性：不出现保存失败/加载失败文案
    expect(el.textContent).not.toContain('agent_mcp_registry_save_failed');
    expect(el.textContent).not.toContain('agent_integrations_loadFailed');
    // 仍按「条目已不存在」刷新：两 API 调用数都增加
    expect(getAliangMCPRegistry.mock.calls.length).toBeGreaterThan(regBefore);
    expect(getAgentIntegrations.mock.calls.length).toBeGreaterThan(integBefore);
  });

  it('codex_sync.ok=false 响应显示同步失败文案', async () => {
    putAliangMCPServer.mockResolvedValue({ codex_sync: { ok: false, error: 'config.toml header broken' } });
    const el = mountComp();
    await flush();
    buttonByText(el, 'agent_mcp_registry_add').click();
    await flush();
    setInput(el, 'name', 'remote');
    const select = el.querySelector('[data-field="type"]');
    select.value = 'http';
    select.dispatchEvent(new Event('change'));
    await flush();
    setInput(el, 'url', 'https://mcp.example.com/sse');
    buttonByText(el, 'agent_mcp_registry_save').click();
    await flush();
    expect(putAliangMCPServer).toHaveBeenCalledWith('remote', expect.objectContaining({
      type: 'http',
      url: 'https://mcp.example.com/sse',
    }));
    expect(el.textContent).toContain('agent_mcp_registry_codex_sync_failed');
  });

  it('保存成功后同时重拉注册表与集成清单（两个 API 各 ≥2 次）', async () => {
    const el = mountComp();
    await flush();
    buttonByText(el, 'agent_mcp_registry_add').click();
    await flush();
    setInput(el, 'name', 'fresh');
    setInput(el, 'command', '/usr/local/bin/fresh');
    buttonByText(el, 'agent_mcp_registry_save').click();
    await flush();
    expect(putAliangMCPServer).toHaveBeenCalledWith('fresh', expect.objectContaining({
      type: 'stdio',
      command: '/usr/local/bin/fresh',
      enabled: true,
    }));
    expect(getAliangMCPRegistry.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(getAgentIntegrations.mock.calls.length).toBeGreaterThanOrEqual(2);
  });
});

// 导出纯函数直测（对照 AgentIntegrationsSettings.vue 的 paginateItems 先例）。
describe('parseKeyValueLines / formatKeyValueLines 多行键值解析', () => {
  it('按首个分隔符拆分，值含分隔符不误伤（headers 冒号 URL 场景）', () => {
    expect(parseKeyValueLines('X-Endpoint: https://a.com', ':')).toEqual({ 'X-Endpoint': 'https://a.com' });
    expect(parseKeyValueLines('URL=https://a.com?x=1', '=')).toEqual({ URL: 'https://a.com?x=1' });
  });

  it('重复键后写优先', () => {
    expect(parseKeyValueLines('A=1\nA=2', '=')).toEqual({ A: '2' });
    expect(parseKeyValueLines('K: v1\nK: v2', ':')).toEqual({ K: 'v2' });
  });

  it('无分隔符行跳过', () => {
    expect(parseKeyValueLines('NOSEP\nA=1', '=')).toEqual({ A: '1' });
    expect(parseKeyValueLines('NOSEP', ':')).toEqual({});
  });

  it('空键行跳过', () => {
    expect(parseKeyValueLines('=value', '=')).toEqual({});
    expect(parseKeyValueLines('  : value', ':')).toEqual({});
  });

  it('空输入/非对象输入安全返回空', () => {
    expect(parseKeyValueLines('', '=')).toEqual({});
    expect(parseKeyValueLines(null, '=')).toEqual({});
    expect(formatKeyValueLines(null, '=')).toBe('');
    expect(formatKeyValueLines(undefined, ': ')).toBe('');
  });

  it('往返一致：parse → format 还原规范形文本', () => {
    const envText = 'A=1\nB=https://x.example/p?q=2';
    expect(formatKeyValueLines(parseKeyValueLines(envText, '='), '=')).toBe(envText);
    const headerText = 'X-Endpoint: https://a.com\nAuthorization: Bearer t0k3n';
    expect(formatKeyValueLines(parseKeyValueLines(headerText, ':'), ': ')).toBe(headerText);
  });
});
