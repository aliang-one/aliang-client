import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// UserInfoSettings.vue 依赖面较大,逐模块 mock(SshIngressSettings.test.js 同范式:
// i18n t 原样回键,断言不硬编码文案)。auth store 与 userCenterApi 注入纯对象。
// 本文件钉死员工中心「复制」链路:仪表盘以 http 局域网直连(56431 无 TLS)打开时
// navigator.clipboard 不存在,裸调 writeText 曾把
// "Cannot read properties of undefined (reading 'writeText')" 原文透传到卡片反馈。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

// vi.mock 工厂随 import 提升到文件头,工厂内引用的顶层对象必须经 vi.hoisted 同步提升
const { apiMocks } = vi.hoisted(() => ({
  apiMocks: {
    getUserCenterProfile: vi.fn(),
    getUserCenterUsageSummary: vi.fn(),
    getUserCenterUsageProgress: vi.fn(),
    getUserCenterAPIKeys: vi.fn(),
    redeemUserCenterCode: vi.fn(),
    updateUserCenterProfile: vi.fn()
  }
}));

// auth store 用真 Vue ref:组件内 watch(isAuthenticated) 对假 ref({value})
// 会报 Invalid watch source 且永不触发。工厂内 await import('vue') 取 ref。
vi.mock('../../stores/auth', async () => {
  const { ref } = await import('vue');
  return {
    useAuthStore: () => ({
      user: ref(null),
      isAuthenticated: ref(true),
      loginPending: ref(false),
      logoutPending: ref(false),
      loginError: ref(''),
      authNotice: ref(''),
      loginWithPassword: vi.fn(async () => true),
      logoutUser: vi.fn(async () => {}),
      restoreAuthSession: vi.fn(async () => {}),
      mergeAuthUser: vi.fn()
    })
  };
});

vi.mock('../../services/userCenterApi', () => apiMocks);

vi.mock('./ScanLoginPanel.vue', () => ({
  default: { name: 'ScanLoginPanel', render: () => null }
}));

import { createApp, h } from 'vue';
import UserInfoSettings from './UserInfoSettings.vue';

const flush = async (rounds = 10) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

const OK_ENVELOPE = (data) => ({ status: 'success', message: '', data });

const profilePayload = () => ({
  id: 1,
  email: 'staff@example.com',
  username: 'staff',
  role: 'user',
  balance: 12.5,
  concurrency: 3,
  status: 'active',
  allowed_groups: [7],
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-10T00:00:00Z'
});

const apiKeyItem = () => ({ id: 7, key: 'sk-testkey1234567890', provider: 'glm', group: 'default', name: 'main' });

function stubBackend() {
  apiMocks.getUserCenterProfile.mockResolvedValue(OK_ENVELOPE(profilePayload()));
  apiMocks.getUserCenterUsageSummary.mockResolvedValue(OK_ENVELOPE({ active_count: 1, total_used_usd: 0.5, subscriptions: [] }));
  apiMocks.getUserCenterUsageProgress.mockResolvedValue(OK_ENVELOPE({ items: [] }));
  apiMocks.getUserCenterAPIKeys.mockResolvedValue(OK_ENVELOPE({ items: [apiKeyItem()] }));
}

function mountPane() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(UserInfoSettings) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
}

function stubClipboard(value) {
  Object.defineProperty(navigator, 'clipboard', { value, configurable: true });
}

function stubExecCommand(impl) {
  Object.defineProperty(document, 'execCommand', { value: impl, configurable: true });
}

const copyButtons = (el) => el.querySelectorAll('button[aria-label="user_apiKeyCopyAction"]');
const copyFeedback = (el) => Array.from(el.querySelectorAll('p')).find((p) => p.textContent === 'user_apiKeyCopied' || p.textContent === 'user_apiKeyCopyFailed' || p.textContent.includes('writeText'));

describe('UserInfoSettings 复制链路(http 无 clipboard 回归)', () => {
  beforeEach(() => {
    stubBackend();
    try { delete navigator.clipboard; } catch (_) { /* 已不存在 */ }
    try { delete document.execCommand; } catch (_) { /* 已不存在 */ }
  });

  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
    try { delete navigator.clipboard; } catch (_) { /* 已不存在 */ }
    try { delete document.execCommand; } catch (_) { /* 已不存在 */ }
  });

  it('回归:http 场景(navigator.clipboard=undefined)点复制不再抛'
    + '"Cannot read properties of undefined (reading \'writeText\')",走 execCommand 兜底并报成功', async () => {
    stubClipboard(undefined);
    const execCommand = vi.fn(() => true);
    stubExecCommand(execCommand);
    const el = mountPane();
    await flush();
    const btn = copyButtons(el)[0];
    expect(btn).toBeTruthy();
    btn.click();
    await flush();
    // 反馈必须是成功文案,不得出现原始 TypeError 文本
    expect(copyFeedback(el)?.textContent).toBe('user_apiKeyCopied');
    expect(execCommand).toHaveBeenCalledWith('copy');
  });

  it('安全上下文:clipboard.writeText 存在时走 API 并报成功', async () => {
    const writeText = vi.fn().mockResolvedValue();
    stubClipboard({ writeText });
    const el = mountPane();
    await flush();
    copyButtons(el)[0].click();
    await flush();
    expect(writeText).toHaveBeenCalledWith('sk-testkey1234567890');
    expect(copyFeedback(el)?.textContent).toBe('user_apiKeyCopied');
  });

  it('兜底也被拒(execCommand=false)时报复制失败文案,不抛原文', async () => {
    stubClipboard(undefined);
    stubExecCommand(() => false);
    const el = mountPane();
    await flush();
    copyButtons(el)[0].click();
    await flush();
    expect(copyFeedback(el)?.textContent).toBe('user_apiKeyCopyFailed');
  });
});
