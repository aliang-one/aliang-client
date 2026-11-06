// MCP 注册表管理卡（Task 10）。挂载用 createApp + jsdom 手法（仓内无
// @vue/test-utils，PeerAccessSettings.test.js 同范式）；i18n 与 agentApi 必须
// mock：前者避免 locale 依赖，后者阻止 mounted 的真实网络请求。
// 断言走 i18n 键名（mock t 原样回键），不硬编码文案。
import { beforeEach, describe, expect, it, vi } from 'vitest';

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
import AgentMCPRegistrySettings from './AgentMCPRegistrySettings.vue';
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
