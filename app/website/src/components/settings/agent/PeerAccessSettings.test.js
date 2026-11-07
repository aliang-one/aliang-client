// 设备互访子 tab 内容卡(2026-11-04 收编;2026-11-07 门禁占位转正为真开关)。
// 挂载用 createApp + jsdom 手法(仓内无 @vue/test-utils,vite.config 已配
// jsdom);i18n 与 agentApi 必须 mock:前者避免 locale 依赖,后者挡住
// PeerMCPSettings/PeerGateSettings mounted 的真实网络请求。
import { describe, expect, it, vi } from 'vitest';

const gatesNow = { device_id: 'device-b', found: true, peer_exec_enabled: false, peer_fetch_enabled: true };
const getPeerGates = vi.fn(async () => gatesNow);
const updatePeerGates = vi.fn(async (payload) => ({ ...gatesNow, ...payload }));

vi.mock('../../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
vi.mock('../../../services/agentApi', () => ({
  getPeerMCPStatus: vi.fn(async () => ({ targets: [] })),
  setPeerMCPRegistration: vi.fn(async () => ({ targets: [] })),
  getPeerGates: (...args) => getPeerGates(...args),
  updatePeerGates: (...args) => updatePeerGates(...args)
}));

import { createApp, h } from 'vue';
import PeerAccessSettings from './PeerAccessSettings.vue';

const baseProps = { config: { peer_mcp: { enable: true } } };

async function mountComp(props = {}, listeners = {}) {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(PeerAccessSettings, { ...baseProps, ...props, ...listeners }) });
  app.mount(el);
  await Promise.resolve();
  await Promise.resolve();
  return el;
}

describe('PeerAccessSettings 收编卡', () => {
  it('渲染 PeerMCPSettings 卡(标题经 i18n 键透出)', async () => {
    const el = await mountComp();
    expect(el.textContent).toContain('peerMcp_title');
  });

  it('门禁卡渲染两枚真开关(可点、初值来自服务端态)', async () => {
    const el = await mountComp();
    const exec = el.querySelector('input[data-testid="peer-gate-exec"]');
    const transfer = el.querySelector('input[data-testid="peer-gate-transfer"]');
    expect(exec).toBeTruthy();
    expect(transfer).toBeTruthy();
    expect(exec.disabled).toBe(false);
    expect(transfer.disabled).toBe(false);
    expect(exec.checked).toBe(false); // gatesNow.peer_exec_enabled = false
    expect(transfer.checked).toBe(true); // gatesNow.peer_fetch_enabled = true
    expect(el.querySelector('input[disabled]')).toBeNull();
  });

  it('拨动开关即写服务端(载荷=单键布尔),成功后取回读态', async () => {
    const el = await mountComp();
    const exec = el.querySelector('input[data-testid="peer-gate-exec"]');
    exec.checked = true;
    exec.dispatchEvent(new Event('change'));
    await Promise.resolve();
    await Promise.resolve();
    expect(updatePeerGates).toHaveBeenCalledWith({ peer_exec_enabled: true });
    expect(el.querySelector('input[data-testid="peer-gate-exec"]').checked).toBe(true);
  });

  it('save 事件两层透传:子卡 → 本卡 → 父页(payload 带 peer_mcp)', async () => {
    const onSave = vi.fn();
    const el = await mountComp({}, { onSave });
    // PeerMCPSettings 的保存按钮是子卡内最后一个 button(两段式保存的①段入口)
    const buttons = el.querySelectorAll('button');
    const saveBtn = buttons[buttons.length - 1];
    saveBtn.click();
    await Promise.resolve();
    await Promise.resolve();
    expect(onSave).toHaveBeenCalledTimes(1);
    // 2026-11-05 细分开关:save 总是显式产出四键(子开关初始=跟随默认开)
    expect(onSave.mock.calls[0][0]).toEqual({ peer_mcp: { enable: true, agentcore_enabled: true, claude_enabled: true, codex_enabled: true } });
  });
});
