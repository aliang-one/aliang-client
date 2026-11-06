// 系统状态侧栏紧凑卡(2026-11-05 spec §4.2A)。挂载用 createApp + jsdom 手法
// (仓内无 @vue/test-utils,同 PeerAccessSettings.test.js);服务状态/证书状态
// 全局 fetch 打桩:紧凑卡 mount 即取服务状态,证书走 useCertStatus 单例轮询。
import { describe, expect, it, vi } from 'vitest';

const serviceStatus = {
  supported: true,
  installed: true,
  running: true,
  status: 'active',
  pid: 1,
  platform: 'darwin',
  service_kind: 'launchd'
};
vi.stubGlobal(
  'fetch',
  vi.fn(async (url) => {
    if (String(url).includes('/api/system/service/status')) {
      return { ok: true, json: async () => ({ code: 0, data: serviceStatus }) };
    }
    if (String(url).includes('/api/cert/status')) {
      return {
        ok: true,
        json: async () => ({
          code: 0,
          data: {
            is_installed: true,
            is_trusted: true,
            is_exported: true,
            trust_status: 'trusted',
            fingerprint: 'f',
            source: 's',
            activated_at: '',
            can_rollback: false
          }
        })
      };
    }
    return { ok: true, json: async () => ({ code: 0, data: {} }) };
  })
);

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h, nextTick } from 'vue';
import SystemStatusSidebarCard from './SystemStatusSidebarCard.vue';

async function mountComp() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(SystemStatusSidebarCard) });
  app.mount(el);
  await Promise.resolve();
  await Promise.resolve();
  await nextTick();
  return el;
}

function statusCallCount() {
  return vi.mocked(fetch).mock.calls.filter(([u]) => String(u).includes('/api/system/service/status')).length;
}

describe('SystemStatusSidebarCard', () => {
  it('渲染标题与服务状态摘要(键名透出)', async () => {
    const el = await mountComp();
    expect(el.textContent).toContain('dash_systemStatusTitle');
    expect(el.textContent).toContain('sys_running');
  });

  it('点击管理按钮打开弹窗并承载完整卡', async () => {
    const el = await mountComp();
    el.querySelector('[data-testid="system-status-manage"]').click();
    await Promise.resolve();
    await nextTick();
    expect(el.querySelector('[data-testid="system-status-modal"]')).toBeTruthy();
    // 弹窗内承载 SystemStatusSettings(软件状态区块键名可见)
    expect(el.textContent).toContain('sys_softwareStatus');
  });

  it('关闭弹窗后触发一次服务状态刷新', async () => {
    const el = await mountComp();
    const before = statusCallCount();
    el.querySelector('[data-testid="system-status-manage"]').click();
    await Promise.resolve();
    await nextTick();
    el.querySelector('[data-testid="system-status-modal-close"]').click();
    await Promise.resolve();
    await Promise.resolve();
    await nextTick();
    expect(statusCallCount()).toBeGreaterThan(before);
  });
});
