import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// auth store 是带 localStorage 副作用的单例;本文件只关心 URL 前缀拼接,
// 直接以 mock 替身验证请求落在哪个路径上。
vi.mock('../stores/auth', () => ({
  syncUnauthenticatedAuthState: vi.fn(),
  syncAuthFromStartupStatus: vi.fn(),
  markDashboardSessionRequired: vi.fn()
}));

import { getAgentStatus } from './agentApi';
import { assistPrefix } from './assistPrefix';
import { syncRunStatus } from '../composables/useRunStatus';

function stubFetch(body = { code: 0, data: {} }) {
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => body
    })
  );
}

// jsdom 下改 location.pathname 的唯一安全通道:history.pushState。
function mountAt(pathname) {
  window.history.pushState(null, '', pathname);
}

beforeEach(() => {
  vi.clearAllMocks();
  mountAt('/');
});

afterEach(() => {
  mountAt('/');
  vi.unstubAllGlobals();
});

describe('assistPrefix', () => {
  it('根路径挂载返回空串(不加前缀)', () => {
    mountAt('/');
    expect(assistPrefix()).toBe('');
  });

  it('镜像前缀路径返回 /assist/service(无尾斜杠,调用方自行拼接)', () => {
    mountAt('/assist/service/');
    expect(assistPrefix()).toBe('/assist/service');
  });

  it('前缀子树内任意子路径同样返回前缀', () => {
    mountAt('/assist/service/settings/logs');
    expect(assistPrefix()).toBe('/assist/service');
  });

  it('相似但不同的前缀不误判', () => {
    mountAt('/assist/other/service/');
    expect(assistPrefix()).toBe('');
    mountAt('/assist/serviceX/');
    expect(assistPrefix()).toBe('');
  });
});

describe('agentApi 前缀拼接', () => {
  it('根挂载时请求落在 /api/agent/status', async () => {
    stubFetch();
    await getAgentStatus();
    expect(fetch).toHaveBeenCalledWith('/api/agent/status', expect.any(Object));
  });

  it('镜像前缀挂载时请求落在 /assist/service/api/agent/status', async () => {
    mountAt('/assist/service/');
    stubFetch();
    await getAgentStatus();
    expect(fetch).toHaveBeenCalledWith('/assist/service/api/agent/status', expect.any(Object));
  });
});

describe('组件内裸 fetch 清扫(抽样:useRunStatus)', () => {
  it('镜像前缀挂载时 syncRunStatus 落在 /assist/service/api/run/status', async () => {
    mountAt('/assist/service/');
    stubFetch();
    await syncRunStatus();
    expect(fetch).toHaveBeenCalledWith('/assist/service/api/run/status');
  });

  it('根挂载时 syncRunStatus 仍落在 /api/run/status', async () => {
    stubFetch();
    await syncRunStatus();
    expect(fetch).toHaveBeenCalledWith('/api/run/status');
  });
});
