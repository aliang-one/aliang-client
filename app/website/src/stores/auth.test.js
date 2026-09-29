import { beforeAll, describe, expect, it, vi } from 'vitest';
import {
  createSessionViewState,
  reduceSessionReadFailure,
  reduceSessionSnapshot
} from './sessionSnapshot';

function snapshot(instanceId, revision, state, user) {
  return {
    type: 'session_snapshot',
    instance_id: instanceId,
    revision,
    state,
    ...(user ? { user } : {})
  };
}

describe('session snapshot reducer', () => {
  it('accepts a newer active snapshot and rejects stale revisions', () => {
    const initial = createSessionViewState();
    const active = reduceSessionSnapshot(initial, snapshot('backend-a', 4, 'active', {
      id: 7,
      username: 'alice'
    }));
    expect(active.accepted).toBe(true);
    expect(active.state.isAuthenticated).toBe(true);
    expect(active.state.user.username).toBe('alice');

    const stale = reduceSessionSnapshot(active.state, snapshot('backend-a', 3, 'unauthenticated'));
    expect(stale.accepted).toBe(false);
    expect(stale.state.user.username).toBe('alice');
  });

  it('allows a backend restart once and rejects delayed snapshots from the retired instance', () => {
    const first = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 9, 'active', { username: 'alice' })
    ).state;
    const restarted = reduceSessionSnapshot(first, snapshot('backend-b', 1, 'restoring'));
    expect(restarted.accepted).toBe(true);
    expect(restarted.state.user.username).toBe('alice');

    const delayed = reduceSessionSnapshot(
      restarted.state,
      snapshot('backend-a', 10, 'unauthenticated')
    );
    expect(delayed.accepted).toBe(false);
    expect(delayed.state.user.username).toBe('alice');
  });

  it('clears identity only for an authoritative terminal snapshot', () => {
    const active = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 1, 'active', { username: 'alice' })
    ).state;
    const unavailable = reduceSessionReadFailure(active, 'transport_unavailable');
    expect(unavailable.isAuthenticated).toBe(true);
    expect(unavailable.user.username).toBe('alice');

    const recovering = reduceSessionSnapshot(
      unavailable,
      snapshot('backend-a', 2, 'soft_expired', { username: 'alice' })
    ).state;
    expect(recovering.isAuthenticated).toBe(true);

    const terminal = reduceSessionSnapshot(
      recovering,
      snapshot('backend-a', 3, 'hard_invalid')
    ).state;
    expect(terminal.isAuthenticated).toBe(false);
    expect(terminal.user).toBeNull();
  });

  it('rejects malformed active snapshots without a user', () => {
    const result = reduceSessionSnapshot(
      createSessionViewState(),
      snapshot('backend-a', 1, 'active')
    );
    expect(result.accepted).toBe(false);
  });
});

// 远程访问重启死锁回归：dashboard 会话（服务端内存）随重启失效，但上游会话
// （持久化）的 active 快照会把 isAuthenticated 翻回 true，登录界面永远无法
// 出现。markDashboardSessionRequired 必须把 UI 钉在登录视图，直到一次真正
// 成功的登录（reconcileAfterAuthCommand）重新签发 dashboard cookie。
describe('auth store dashboard-session deadlock guard', () => {
  let auth;

  beforeAll(async () => {
    // i18n 在模块作用域读 localStorage；node 环境需先打桩再动态导入。
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {},
      removeItem: () => {}
    });
    auth = await import('./auth');
  });

  const instanceId = `backend-deadlock-${Math.random().toString(36).slice(2, 8)}`;
  let revision = 0;

  function jsonResponse(status, body) {
    return { ok: status >= 200 && status < 300, status, json: async () => body };
  }

  function activeSnapshot() {
    revision += 1;
    return {
      type: 'session_snapshot',
      instance_id: instanceId,
      revision,
      state: 'active',
      user: { id: 7, username: 'alice', email: 'alice@example.com' }
    };
  }

  function stubFetch(handler) {
    vi.stubGlobal('fetch', vi.fn((url) => Promise.resolve(handler(String(url)))));
  }

  it('pins the login view while the dashboard session is required and releases it after login', async () => {
    stubFetch(() => jsonResponse(200, { code: 0, msg: 'ok', data: activeSnapshot() }));
    await auth.restoreAuthSession({ force: true });
    const store = auth.useAuthStore();
    expect(store.isAuthenticated.value).toBe(true);

    auth.markDashboardSessionRequired('Authenticated dashboard session is required');
    expect(store.dashboardSessionRequired.value).toBe(true);
    expect(store.isAuthenticated.value).toBe(false);
    expect(store.restoreError.value).toBe('Authenticated dashboard session is required');

    // 上游仍 active：更新的 active 快照不得把 isAuthenticated 翻回 true。
    await auth.restoreAuthSession({ force: true });
    expect(store.isAuthenticated.value).toBe(false);
    expect(store.dashboardSessionRequired.value).toBe(true);

    // 登录成功路径：服务端重新签发 dashboard cookie 后清除标志并恢复认证视图。
    stubFetch((url) => {
      if (url === '/api/auth/login') {
        return jsonResponse(200, { code: 0, msg: 'ok', data: { status: 'success', message: 'welcome' } });
      }
      if (url === '/api/dashboard/session') {
        return jsonResponse(200, { code: 0, msg: 'ok', data: { status: 'success' } });
      }
      return jsonResponse(200, { code: 0, msg: 'ok', data: activeSnapshot() });
    });
    expect(await auth.loginWithPassword({ email: 'alice@example.com', password: 'secret' })).toBe(true);
    expect(store.dashboardSessionRequired.value).toBe(false);
    expect(store.isAuthenticated.value).toBe(true);
  });
});
