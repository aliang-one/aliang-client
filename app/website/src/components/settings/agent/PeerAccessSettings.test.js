// 设备互访子 tab 内容卡(2026-11-04 收编;2026-11-07 门禁开关接真读写)。
// 挂载用 createApp + jsdom 手法(仓内无 @vue/test-utils,vite.config 已配
// jsdom);i18n 与 agentApi 必须 mock:前者避免 locale 依赖,后者阻止
// PeerMCPSettings.mounted 与门禁卡 mounted 的真实网络请求。
// 断言走 i18n 键名(mock t 原样回键),不硬编码文案。
import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('../../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import PeerAccessSettings from './PeerAccessSettings.vue';
import { getPeerDeviceGates, setPeerDeviceGates } from '../../../services/agentApi';

vi.mock('../../../services/agentApi', () => ({
  getPeerMCPStatus: vi.fn(async () => ({ targets: [] })),
  setPeerMCPRegistration: vi.fn(async () => ({ targets: [] })),
  getPeerDeviceGates: vi.fn(),
  setPeerDeviceGates: vi.fn()
}));

const baseProps = { config: { peer_mcp: { enable: true } } };

// 服务端 /api/agent/peer/gates 的本机现值形状(handler 回显 publicDevice 子集)。
const gatesData = () => ({
  device_id: 'dev-1',
  name: 'SelfMachine',
  status: 'online',
  peer_exec_enabled: true,
  peer_fetch_enabled: false
});

// 微任务冲刷:mounted → loadGates 是 await 链,多轮 Promise.resolve 兜够
// (AgentMCPRegistrySettings.test.js 同范式)。
async function flush(rounds = 8) {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
}

function mountComp(props = {}, listeners = {}) {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(PeerAccessSettings, { ...baseProps, ...props, ...listeners }) });
  app.mount(el);
  return el;
}

function toggleByTestid(el, testid) {
  return el.querySelector(`input[data-testid="${testid}"]`);
}

// jsdom 的 checkbox .click() 会翻 checked 但不派发 change(探针实测),显式派发
// 才进组件 handler——测的是组件行为,不是环境激活语义。
function toggleTo(el, testid, checked) {
  const box = toggleByTestid(el, testid);
  box.checked = checked;
  box.dispatchEvent(new Event('change'));
}

beforeEach(() => {
  getPeerDeviceGates.mockReset();
  setPeerDeviceGates.mockReset();
});

describe('PeerAccessSettings 收编卡', () => {
  it('渲染 PeerMCPSettings 卡(标题经 i18n 键透出)', () => {
    getPeerDeviceGates.mockResolvedValue(gatesData());
    const el = mountComp();
    expect(el.textContent).toContain('peerMcp_title');
  });

  it('门禁卡是真开关:mount 拉本机现值,占位三件套清零', async () => {
    getPeerDeviceGates.mockResolvedValue(gatesData());
    const el = mountComp();
    await flush();
    expect(getPeerDeviceGates).toHaveBeenCalledTimes(1);
    // 不再有 disabled 占位与「即将上线」徽章
    expect(el.querySelector('input[type="checkbox"][disabled]')).toBeNull();
    expect(el.textContent).not.toContain('settings_comingSoon');
    // 现值如实回显(mock:exec=true fetch=false)
    expect(toggleByTestid(el, 'peer-gate-exec').checked).toBe(true);
    expect(toggleByTestid(el, 'peer-gate-fetch').checked).toBe(false);
    // rebind 回落提示常显
    expect(el.textContent).toContain('peerGates_rebindHint');
  });

  it('翻 fetch 开关发起 POST 且以服务端回显为准', async () => {
    getPeerDeviceGates.mockResolvedValue(gatesData());
    setPeerDeviceGates.mockResolvedValue({ ...gatesData(), peer_fetch_enabled: true });
    const el = mountComp();
    await flush();
    toggleTo(el, 'peer-gate-fetch', true);
    await flush();
    // 部分更新契约:只发被翻的那枚键
    expect(setPeerDeviceGates).toHaveBeenCalledWith({ peer_fetch_enabled: true });
    // 终值=服务端回显,不是本地乐观值
    expect(toggleByTestid(el, 'peer-gate-fetch').checked).toBe(true);
    expect(el.querySelector('[data-testid="peer-gates-error"]')).toBeNull();
  });

  it('写失败回显错误并把开关回滚到读值', async () => {
    getPeerDeviceGates.mockResolvedValue(gatesData());
    setPeerDeviceGates.mockRejectedValue(new Error('peer api status 403: peer_fetch_not_allowed'));
    const el = mountComp();
    await flush();
    toggleTo(el, 'peer-gate-fetch', true);
    await flush();
    expect(el.querySelector('[data-testid="peer-gates-error"]').textContent).toContain('peerGates_saveFailed');
    expect(toggleByTestid(el, 'peer-gate-fetch').checked).toBe(false);
  });

  it('读失败显示错误且开关置灰(不拿零值假装读到关)', async () => {
    getPeerDeviceGates.mockRejectedValue(new Error('self device not found in account device list'));
    const el = mountComp();
    await flush();
    expect(el.querySelector('[data-testid="peer-gates-error"]').textContent).toContain('peerGates_loadFailed');
    expect(toggleByTestid(el, 'peer-gate-exec').disabled).toBe(true);
    expect(toggleByTestid(el, 'peer-gate-fetch').disabled).toBe(true);
  });

  it('save 事件两层透传:子卡 → 本卡 → 父页(payload 带 peer_mcp)', async () => {
    getPeerDeviceGates.mockResolvedValue(gatesData());
    const onSave = vi.fn();
    const el = mountComp({}, { onSave });
    await flush();
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
