import { beforeEach, describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉
vi.mock('../services/agentApi', () => ({
  getAgentSessions: vi.fn(),
}));

import { getAgentSessions } from '../services/agentApi';
import {
  DEVICE_SESSION_DETAIL_TTL_MS,
  DEVICE_SESSIONS_REFRESH_THROTTLE_MS,
  deviceResumeSessionId,
  isTerminalPrefixedSessionId,
  useDeviceSessions,
} from './useDeviceSessions';

const AGENTCORE_ENTRY = {
  id: 'conv-1',
  provider: 'agentcore',
  title: '手机会话',
  status: 'running',
  origin: 'phone',
  project_path: '/home/a/proj',
  updated_at: '2026-10-29T10:00:00Z',
};
const CLAUDE_ENTRY = {
  id: 'claude_native9',
  provider: 'claude',
  title: '终端会话',
  status: 'idle',
  project_path: '/home/a/term',
  updated_at: '2026-10-28T08:00:00Z',
};

describe('isTerminalPrefixedSessionId', () => {
  it('只认 claude_/codex_ 前缀，脏输入安全', () => {
    expect(isTerminalPrefixedSessionId('claude_abc')).toBe(true);
    expect(isTerminalPrefixedSessionId('codex_x1')).toBe(true);
    expect(isTerminalPrefixedSessionId('conv-1')).toBe(false);
    expect(isTerminalPrefixedSessionId('claudecodex')).toBe(false);
    expect(isTerminalPrefixedSessionId('')).toBe(false);
    expect(isTerminalPrefixedSessionId(null)).toBe(false);
    expect(isTerminalPrefixedSessionId(42)).toBe(false);
  });
});

describe('deviceResumeSessionId', () => {
  it('source_session_id 优先，缺省剥前缀，非前缀原样', () => {
    expect(deviceResumeSessionId({ id: 'claude_native9', source_session_id: 'managed-7' })).toBe('managed-7');
    expect(deviceResumeSessionId({ id: 'claude_native9' })).toBe('native9');
    expect(deviceResumeSessionId({ id: 'codex_th1' })).toBe('th1');
    expect(deviceResumeSessionId({ id: 'conv-1' })).toBe('conv-1');
    expect(deviceResumeSessionId({ id: 'claude_x', source_session_id: '  ' })).toBe('x'); // 空白视同缺省
    expect(deviceResumeSessionId(null)).toBe('');
  });
});

describe('useDeviceSessions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('loadDeviceSessions 成功：清单就位、available=true，壳派生稳定且字段钉死', async () => {
    getAgentSessions.mockResolvedValue({ sessions: [AGENTCORE_ENTRY, CLAUDE_ENTRY] });
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();
    expect(ds.available.value).toBe(true);
    expect(ds.deviceSessions.value).toHaveLength(2);

    const shell = ds.shellForEntry(AGENTCORE_ENTRY);
    expect(shell).not.toBeNull();
    expect(shell.id).toBe('dev-conv-1');
    expect(shell.aiSessionId).toBe('conv-1'); // 裸 id 直连
    expect(shell.resumeSessionId).toBeUndefined(); // 裸 id 不带 resume 字段
    expect(shell.running).toBe(true);
    expect(shell.dirLocked).toBe(true); // 目录由清单权威
    expect(shell.updatedAt).toBe(Date.parse('2026-10-29T10:00:00Z')); // 归一 epoch ms
    expect(shell.messages).toEqual([]);
    expect(shell.provider).toBe('agentcore');
    expect(shell.deviceSession).toBe(AGENTCORE_ENTRY);

    const termShell = ds.shellForEntry(CLAUDE_ENTRY);
    expect(termShell.aiSessionId).toBe('local-dev-claude_native9'); // 前缀 id 新建托管会话
    expect(termShell.resumeSessionId).toBe('native9'); // 清单无 source_session_id → 剥前缀
    expect(termShell.resumeProjectPath).toBe('/home/a/term');

    // 同一条目重复派生返回同一对象（缓存防重建）
    expect(ds.shellForEntry(AGENTCORE_ENTRY)).toBe(shell);
  });

  it('shellForEntry 原位刷新：同对象换清单字段，打开态（messages）保留', async () => {
    getAgentSessions.mockResolvedValue({ sessions: [AGENTCORE_ENTRY] });
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();
    const shell = ds.shellForEntry(AGENTCORE_ENTRY);
    shell.messages.push({ role: 'user', content: '已打开' });

    const refreshed = { ...AGENTCORE_ENTRY, title: '改名了', status: 'idle', updated_at: '2026-10-29T11:00:00Z' };
    const again = ds.shellForEntry(refreshed);
    expect(again).toBe(shell);
    expect(again.title).toBe('改名了');
    expect(again.running).toBe(false);
    expect(again.updatedAt).toBe(Date.parse('2026-10-29T11:00:00Z'));
    expect(again.messages).toEqual([{ role: 'user', content: '已打开' }]);
    expect(again.deviceSession).toBe(refreshed);
  });

  it('resumeConsumed 后清单刷新不再复活 resume 字段（仅首次 create 语义）', async () => {
    getAgentSessions.mockResolvedValue({ sessions: [CLAUDE_ENTRY] });
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();
    const shell = ds.shellForEntry(CLAUDE_ENTRY);
    expect(shell.resumeSessionId).toBe('native9');

    // 模拟 created ack 消费（ChatPage 侧：置位 + delete）
    shell.resumeConsumed = true;
    delete shell.resumeSessionId;
    delete shell.resumeProjectPath;

    const again = ds.shellForEntry({ ...CLAUDE_ENTRY, project_path: '/home/a/term-new' });
    expect(again).toBe(shell);
    expect('resumeSessionId' in again).toBe(false); // 刷新不复活
    expect('resumeProjectPath' in again).toBe(false);
    expect(again.projectPath).toBe('/home/a/term-new'); // 其余清单字段照常刷新
  });

  it('网络错 → available=false；200 空列表 → available=true（core 代理空表歧义已接受）', async () => {
    getAgentSessions.mockRejectedValue(new Error('boom'));
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();
    expect(ds.available.value).toBe(false);
    expect(ds.deviceSessions.value).toEqual([]);

    getAgentSessions.mockResolvedValue({ sessions: [] });
    await ds.loadDeviceSessions();
    expect(ds.available.value).toBe(true);
    expect(ds.deviceSessions.value).toEqual([]);
  });

  it('findDeviceShellBySessionId 三键命中：entry.id / source_session_id / managed_conversation_id', async () => {
    getAgentSessions.mockResolvedValue({ sessions: [{
      id: 'conv-1', provider: 'agentcore', title: 't', status: 'idle',
      source_session_id: 'managed-7', managed_conversation_id: 'conv-1',
    }] });
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();

    expect(ds.findDeviceShellBySessionId('conv-1')?.id).toBe('dev-conv-1');
    expect(ds.findDeviceShellBySessionId('managed-7')?.id).toBe('dev-conv-1');
    expect(ds.findDeviceShellBySessionId('nope')).toBeNull();
    expect(ds.findDeviceShellBySessionId('')).toBeNull();
  });

  it('脏清单条目安全：非对象/缺 id 不产生壳也不炸', async () => {
    getAgentSessions.mockResolvedValue({ sessions: [null, {}, { id: '' }, { id: 42 }, AGENTCORE_ENTRY] });
    const ds = useDeviceSessions();
    await ds.loadDeviceSessions();
    const shells = ds.deviceSessions.value.map(e => ds.shellForEntry(e)).filter(Boolean);
    expect(shells).toHaveLength(1);
    expect(shells[0].id).toBe('dev-conv-1');
  });

  it('refreshOnEvent 节流：首发即时，窗口内合并成一次尾随刷新', async () => {
    vi.useFakeTimers();
    try {
      getAgentSessions.mockResolvedValue({ sessions: [] });
      const { refreshOnEvent } = useDeviceSessions();
      refreshOnEvent();
      refreshOnEvent();
      refreshOnEvent();
      await vi.advanceTimersByTimeAsync(0);
      expect(getAgentSessions).toHaveBeenCalledTimes(1); // 首发即时，后续合并
      await vi.advanceTimersByTimeAsync(DEVICE_SESSIONS_REFRESH_THROTTLE_MS);
      expect(getAgentSessions).toHaveBeenCalledTimes(2); // 尾随一次，不丢末次状态
      await vi.advanceTimersByTimeAsync(DEVICE_SESSIONS_REFRESH_THROTTLE_MS + 1);
      refreshOnEvent(); // 窗口已过 → 再次首发
      expect(getAgentSessions).toHaveBeenCalledTimes(3);
    } finally {
      vi.useRealTimers();
    }
  });

  it('详情 TTL 常量为 5 分钟', () => {
    expect(DEVICE_SESSION_DETAIL_TTL_MS).toBe(5 * 60 * 1000);
  });
});
