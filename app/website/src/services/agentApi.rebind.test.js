import { afterEach, describe, expect, it, vi } from 'vitest';

// NET-01 验收②:换绑到不同账号须经本机属主确认。这里锁住前端调用的路径选择
// ——confirm 走 confirm-rebind,reject 走 reject-rebind,两者不能串。
vi.mock('../stores/auth', () => ({ syncUnauthenticatedAuthState: vi.fn() }));
vi.mock('./authFailure', () => ({ handleAuthenticationFailure: vi.fn() }));
vi.mock('./assistPrefix', () => ({ assistPrefix: () => '' }));

import { resolveAgentRebind } from './agentApi';

const fetchMock = vi.fn(async () => ({
  ok: true,
  status: 200,
  json: async () => ({ code: 0, data: {} }),
}));

describe('resolveAgentRebind (NET-01 ②)', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    fetchMock.mockClear();
  });

  it('confirm posts to /api/agent/confirm-rebind', async () => {
    vi.stubGlobal('fetch', fetchMock);
    await resolveAgentRebind('confirm');
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/agent/confirm-rebind');
    expect(init.method).toBe('POST');
  });

  it('reject posts to /api/agent/reject-rebind', async () => {
    vi.stubGlobal('fetch', fetchMock);
    await resolveAgentRebind('reject');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agent/reject-rebind');
  });

  it('未知 action 落到 reject(保守:宁可保持原绑定,也不误放行换绑)', async () => {
    vi.stubGlobal('fetch', fetchMock);
    await resolveAgentRebind('nonsense');
    expect(fetchMock.mock.calls[0][0]).toBe('/api/agent/reject-rebind');
  });
});
