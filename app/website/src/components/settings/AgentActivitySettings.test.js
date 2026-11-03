import { describe, expect, it, vi } from 'vitest';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage，node 环境无 DOM），
// mock 掉后只测 matchesAgentFilter 纯函数（RulesSettings.test.js 同范式）。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { matchesAgentFilter } from './AgentActivitySettings.vue';

describe('matchesAgentFilter 记录页 agent 类型筛选', () => {
  it('all 恒真', () => {
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'all')).toBe(true);
    expect(matchesAgentFilter({ provider: 'anything-unknown' }, 'all')).toBe(true);
  });

  it('aliangcode 走 isAliangCodeSession 同源判据', () => {
    expect(matchesAgentFilter({ provider: 'agentcore' }, 'aliangcode')).toBe(true);
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'aliangcode')).toBe(false);
  });

  it('claude 包含 claudecode 别名', () => {
    expect(matchesAgentFilter({ provider: 'claudecode' }, 'claude')).toBe(true);
    expect(matchesAgentFilter({ provider: 'agentcore' }, 'claude')).toBe(false);
  });

  it('codex / opencode 按名匹配', () => {
    expect(matchesAgentFilter({ provider: 'codex' }, 'codex')).toBe(true);
    expect(matchesAgentFilter({ provider: 'opencode' }, 'opencode')).toBe(true);
    expect(matchesAgentFilter({ provider: 'codex' }, 'claude')).toBe(false);
  });

  it('tool 字段优先于 provider（session.tool 存在时）', () => {
    expect(matchesAgentFilter({ tool: 'claude', provider: 'agentcore' }, 'claude')).toBe(true);
  });

  it('空会话恒 false', () => {
    expect(matchesAgentFilter(null, 'all')).toBe(false);
  });
});
