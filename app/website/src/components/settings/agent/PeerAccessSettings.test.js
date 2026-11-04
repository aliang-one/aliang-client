// 设备互访子 tab 内容卡(2026-11-04 收编)。挂载用 createApp + jsdom 手法
// (仓内无 @vue/test-utils,vite.config 已配 jsdom);i18n 与 agentApi 必须 mock:
// 前者避免 locale 依赖,后者阻止 PeerMCPSettings.mounted 的真实网络请求。
import { describe, expect, it, vi } from 'vitest';

vi.mock('../../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));
vi.mock('../../../services/agentApi', () => ({
  getPeerMCPStatus: vi.fn(async () => ({ registration: [] })),
  setPeerMCPRegistration: vi.fn(async () => ({}))
}));

import { createApp, h } from 'vue';
import PeerAccessSettings from './PeerAccessSettings.vue';

const baseProps = { config: { peer_mcp: { enable: true } } };

function mountComp(props = {}, listeners = {}) {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(PeerAccessSettings, { ...baseProps, ...props, ...listeners }) });
  app.mount(el);
  return el;
}

describe('PeerAccessSettings 收编卡', () => {
  it('渲染 PeerMCPSettings 卡(标题经 i18n 键透出)', () => {
    const el = mountComp();
    expect(el.textContent).toContain('peerMcp_title');
  });

  it('渲染门禁占位卡:两行开关均 disabled + comingSoon 徽章', () => {
    const el = mountComp();
    const boxes = el.querySelectorAll('input[type="checkbox"][disabled]');
    expect(boxes.length).toBeGreaterThanOrEqual(2);
    expect(el.querySelector('input[aria-label="settings_peerExecPendingTitle"]')).toBeTruthy();
    expect(el.querySelector('input[aria-label="settings_peerTransferPendingTitle"]')).toBeTruthy();
    expect(el.textContent.match(/settings_comingSoon/g).length).toBe(2);
  });

  it('save 事件两层透传:子卡 → 本卡 → 父页(payload 带 peer_mcp)', async () => {
    const onSave = vi.fn();
    const el = mountComp({}, { onSave });
    // PeerMCPSettings 的保存按钮是子卡内最后一个 button(两段式保存的①段入口)
    const buttons = el.querySelectorAll('button');
    const saveBtn = buttons[buttons.length - 1];
    saveBtn.click();
    await Promise.resolve();
    await Promise.resolve();
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toEqual({ peer_mcp: { enable: true } });
  });
});
