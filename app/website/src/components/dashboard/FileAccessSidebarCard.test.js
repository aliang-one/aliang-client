// 文件访问授权侧栏紧凑卡(2026-11-05 spec §4.2B)。挂载用 createApp + jsdom 手法
// (仓内无 @vue/test-utils);agentApi 模块级 mock——宿主卡与弹窗内承载的
// AgentFileAccessSettings 共用同一解析模块,mock 一处两头生效。
import { describe, expect, it, vi } from 'vitest';

const grantedSnapshot = {
  supported: true,
  attended: true,
  state: 'done',
  probeTimeoutSec: 25,
  categories: [
    { key: 'desktop', root: '/Users/mac/Desktop', state: 'granted' },
    { key: 'documents', root: '/Users/mac/Documents', state: 'granted' },
    { key: 'downloads', root: '/Users/mac/Downloads', state: 'granted' }
  ]
};

const getAgentFileAccess = vi.fn(async () => grantedSnapshot);

vi.mock('../../services/agentApi', () => ({
  getAgentFileAccess: (...args) => getAgentFileAccess(...args),
  requestAgentFileAccess: vi.fn(async () => ({ snapshot: grantedSnapshot })),
  openAgentFileAccessSettings: vi.fn(async () => undefined)
}));

vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h, nextTick } from 'vue';
import FileAccessSidebarCard from './FileAccessSidebarCard.vue';

async function mountComp() {
  const el = document.createElement('div');
  const app = createApp({ render: () => h(FileAccessSidebarCard) });
  app.mount(el);
  await Promise.resolve();
  await Promise.resolve();
  await nextTick();
  return el;
}

describe('FileAccessSidebarCard', () => {
  it('渲染标题与授权摘要(键名透出)', async () => {
    const el = await mountComp();
    expect(el.textContent).toContain('agent_fileAccess_title');
    expect(el.textContent).toContain('agent_fileAccess_summary');
    // 三类全授权,无琥珀警示点
    expect(el.querySelector('[data-testid="file-access-denied"]')).toBeNull();
  });

  it('点击打开弹窗并承载原卡(类别清单 3 项)', async () => {
    const el = await mountComp();
    el.querySelector('[data-testid="file-access-manage"]').click();
    await Promise.resolve();
    await Promise.resolve();
    await nextTick();
    expect(el.querySelector('[data-testid="file-access-modal"]')).toBeTruthy();
    const items = el.querySelectorAll('[data-testid="file-access-status"] li');
    expect(items.length).toBe(3);
  });

  it('关闭弹窗后触发一次授权状态刷新', async () => {
    const el = await mountComp();
    const before = getAgentFileAccess.mock.calls.length;
    el.querySelector('[data-testid="file-access-manage"]').click();
    await Promise.resolve();
    await nextTick();
    el.querySelector('[data-testid="file-access-modal-close"]').click();
    await Promise.resolve();
    await Promise.resolve();
    await nextTick();
    expect(getAgentFileAccess.mock.calls.length).toBeGreaterThan(before);
  });
});
