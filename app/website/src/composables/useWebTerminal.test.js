import { beforeEach, describe, expect, it, vi } from 'vitest';

const mockState = vi.fn();
const mockCommand = vi.fn();
vi.mock('../services/agentApi', () => ({
  getWebTerminalState: (...a) => mockState(...a),
  webTerminalCommand: (...a) => mockCommand(...a),
}));

import { resetForTests, useWebTerminal } from './useWebTerminal';

describe('useWebTerminal', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetForTests();
  });

  it('refresh 拉取状态', async () => {
    mockState.mockResolvedValue({ enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    await wt.refresh();
    expect(wt.status.value.enabled).toBe(true);
  });

  it('verify 成功存令牌,失败结构化透出', async () => {
    mockCommand.mockResolvedValueOnce({ ok: true, token: 't1' });
    const wt = useWebTerminal();
    const res = await wt.verify('1234');
    expect(res.ok).toBe(true);
    expect(wt.unlocked.value).toBe(true);
    expect(wt.token.value).toBe('t1');

    mockCommand.mockResolvedValueOnce({ ok: false, reason: 'locked', lock_remaining_seconds: 42 });
    const res2 = await wt.verify('9999');
    expect(res2.ok).toBe(false);
    expect(res2.lock_remaining_seconds).toBe(42);
    expect(wt.unlocked.value).toBe(true); // 失败不动既有令牌(重锁走服务端 sweep)
  });

  it('lock 清令牌', async () => {
    mockCommand.mockResolvedValue({ ok: true });
    mockState.mockResolvedValue({ enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    await wt.verify('1234');
    await wt.lock();
    expect(wt.unlocked.value).toBe(false);
    expect(mockCommand).toHaveBeenCalledWith({ action: 'lock' }, expect.any(String));
  });

  it('setEnabled false 走 gates 且清令牌', async () => {
    mockCommand.mockResolvedValue({ ok: true });
    mockState.mockResolvedValue({ enabled: false, pin_set: false, locked: false, lock_remaining_seconds: 0 });
    const wt = useWebTerminal();
    wt.token.value = 't';
    await wt.setEnabled(false);
    expect(mockCommand).toHaveBeenCalledWith({ action: 'gates', enabled: false }, 't');
    expect(wt.unlocked.value).toBe(false);
  });

  it('invalidateToken 清令牌回落解锁盘', () => {
    const wt = useWebTerminal();
    wt.token.value = 'tk';
    wt.invalidateToken();
    expect(wt.unlocked.value).toBe(false);
    expect(wt.token.value).toBe('');
  });

  it('loading 在 refresh/command 在途时为 true,结束后回落', async () => {
    let resolveState;
    mockState.mockImplementation(() => new Promise((r) => { resolveState = r; }));
    let resolveCmd;
    mockCommand.mockImplementation(() => new Promise((r) => { resolveCmd = r; }));
    const wt = useWebTerminal();

    const p = wt.refresh();
    expect(wt.loading.value).toBe(true);
    resolveState({ enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    await p;
    expect(wt.loading.value).toBe(false);

    const p2 = wt.killall();
    expect(wt.loading.value).toBe(true);
    resolveCmd({ ok: true });
    await p2;
    expect(wt.loading.value).toBe(false);
  });

  it('refresh 失败置 error 保旧 status,下次成功清除 error', async () => {
    const stale = { enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 };
    mockState.mockResolvedValue(stale);
    const wt = useWebTerminal();
    await wt.refresh();
    expect(wt.error.value).toBe(null);

    mockState.mockRejectedValue(new Error('boom'));
    const out = await wt.refresh();
    expect(out).toBe(null);
    expect(wt.error.value).toBeInstanceOf(Error);
    expect(wt.status.value).toEqual(stale); // 失败保陈旧真值(非置空)

    mockState.mockResolvedValue({ enabled: false, pin_set: true, locked: false, lock_remaining_seconds: 0 });
    await wt.refresh();
    expect(wt.error.value).toBe(null);
  });
});
