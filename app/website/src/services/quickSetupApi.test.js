import { beforeEach, describe, expect, it, vi } from 'vitest';

// auth store 是带 localStorage 副作用的单例；本文件只关心失败路由，
// 直接以 mock 替身验证 quickSetupApi 选了哪个恢复动作。
vi.mock('../stores/auth', () => ({
  syncUnauthenticatedAuthState: vi.fn(),
  markDashboardSessionRequired: vi.fn()
}));

import { markDashboardSessionRequired, syncUnauthenticatedAuthState } from '../stores/auth';
import { fetchConfigState } from './quickSetupApi';

function stubFetch(status, body) {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body
  }));
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe('quick setup API auth failure routing', () => {
  it('marks the dashboard session required for the dashboard-session 401 signature', async () => {
    stubFetch(401, {
      code: 101,
      msg: 'Authenticated dashboard session is required',
      data: { error_code: 'unauthorized', error_msg: 'Authenticated dashboard session is required' }
    });

    await expect(fetchConfigState('claude')).rejects.toThrow('Authenticated dashboard session is required');
    expect(markDashboardSessionRequired).toHaveBeenCalledTimes(1);
    expect(markDashboardSessionRequired).toHaveBeenCalledWith('Authenticated dashboard session is required');
    expect(syncUnauthenticatedAuthState).not.toHaveBeenCalled();
  });

  it('marks the dashboard session required for a nested code 101 envelope', async () => {
    stubFetch(401, {
      code: 0,
      msg: 'Authenticated dashboard session is required',
      data: { code: 101, msg: 'nested' }
    });

    await expect(fetchConfigState('claude')).rejects.toThrow('Authenticated dashboard session is required');
    expect(markDashboardSessionRequired).toHaveBeenCalledWith('Authenticated dashboard session is required');
    expect(syncUnauthenticatedAuthState).not.toHaveBeenCalled();
  });

  it('keeps generic reconciliation for an upstream-style 401', async () => {
    stubFetch(401, { code: 1001, msg: 'Token has expired' });

    await expect(fetchConfigState('claude')).rejects.toThrow('Token has expired');
    expect(markDashboardSessionRequired).not.toHaveBeenCalled();
    expect(syncUnauthenticatedAuthState).toHaveBeenCalledTimes(1);
    expect(syncUnauthenticatedAuthState).toHaveBeenCalledWith('Token has expired');
  });
});
