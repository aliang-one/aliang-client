import { beforeEach, describe, expect, it, vi } from 'vitest';

// i18n/index.js 模块作用域访问 localStorage，node 环境未定义——mock 掉。
// t 的 mock 把参数拼进返回值（'key:1/2'），供占位符类断言使用
vi.mock('../i18n', () => ({
  useI18n: () => ({
    t: (key, params) => (params && typeof params === 'object' ? `${key}:${Object.values(params).join('/')}` : key),
  }),
}));
// stores/auth（经 agentApi 引入）同样有 localStorage 副作用
vi.mock('../services/agentApi', () => ({
  getAgentStatus: vi.fn(),
}));

import { getAgentStatus } from '../services/agentApi';
import { resetForTests, useAgentStatus } from './useAgentStatus';

describe('useAgentStatus', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    resetForTests();
  });

  it('shares one module-level state across consumers', () => {
    const a = useAgentStatus();
    const b = useAgentStatus();
    expect(a.status).toBe(b.status); // 同一份模块级状态 ref
    a.applyStatus({ runtime: { online: true }, enabled: true, bound: true });
    expect(b.agentEnabled.value).toBe(true); // 独立 consumer 的 computed 读到同一份状态
  });

  it('derives runtime/agent state from status', () => {
    const s = useAgentStatus();
    expect(s.runtimeOnline.value).toBe(true); // 无数据时默认视为在线
    s.applyStatus({ runtime: { online: false }, enabled: false, bound: false });
    expect(s.runtimeOnline.value).toBe(false);
    expect(s.agentEnabled.value).toBe(false);

    s.applyStatus({ runtime: { online: true }, enabled: true, bound: true });
    expect(s.agentEnabled.value).toBe(true);
  });

  it('computes tool availability summary', () => {
    const s = useAgentStatus();
    s.applyStatus({
      runtime: { online: true },
      enabled: true,
      bound: true,
      tools: [
        { id: 'codex', available: true },
        { id: 'claude', available: false },
      ],
    });
    expect(s.primaryTools.value).toHaveLength(2);
    expect(s.toolAvailabilitySummary.value).toBe('agent_toolsSummary:1/2');
  });

  it('dedups concurrent ensureLoaded calls into one request', async () => {
    getAgentStatus.mockResolvedValue({ runtime: { online: true }, enabled: true, bound: true });
    const a = useAgentStatus();
    const [ra, rb] = await Promise.all([a.ensureLoaded(), a.ensureLoaded()]);
    expect(getAgentStatus).toHaveBeenCalledTimes(1);
    expect(ra).toEqual(rb);
    await a.ensureLoaded(); // 已 loaded → 缓存命中
    expect(getAgentStatus).toHaveBeenCalledTimes(1);
  });

  it('surfaces errors without losing previous state', async () => {
    const s = useAgentStatus();
    getAgentStatus.mockRejectedValueOnce(new Error('boom'));
    const result = await s.ensureLoaded();
    expect(result).toBe(null);
    expect(s.error.value).toBe('boom');
    expect(s.status.value).toBe(null);
    expect(s.loading.value).toBe(false);
  });
});
