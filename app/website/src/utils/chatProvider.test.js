import { describe, expect, it } from 'vitest';
import { slashProviderFor } from './chatProvider';

describe('slashProviderFor', () => {
  it('agentcore 三种写法都映射为 agentcore（内核命令面：本机命令 + skills/MCP 条目）', () => {
    expect(slashProviderFor('agentcore')).toBe('agentcore');
    expect(slashProviderFor('agent-core')).toBe('agentcore');
    expect(slashProviderFor('agent_core')).toBe('agentcore');
    expect(slashProviderFor('AgentCore')).toBe('agentcore');
  });

  it('codex 映射为 codex', () => {
    expect(slashProviderFor('codex')).toBe('codex');
    expect(slashProviderFor('CODEX')).toBe('codex');
  });

  it('opencode 映射为 opencode', () => {
    expect(slashProviderFor('opencode')).toBe('opencode');
    expect(slashProviderFor('open-code')).toBe('opencode');
    expect(slashProviderFor('Open_Code')).toBe('opencode');
  });

  it('其余（含缺省）映射为 claude', () => {
    expect(slashProviderFor('claudecode')).toBe('claude');
    expect(slashProviderFor('claude')).toBe('claude');
    expect(slashProviderFor('')).toBe('claude');
    expect(slashProviderFor(undefined)).toBe('claude');
    expect(slashProviderFor(null)).toBe('claude');
  });

  it('未知 provider 回落 claude（默认桶，与缺省 provider=claudecode 一致）', () => {
    expect(slashProviderFor('gemini')).toBe('claude');
  });
});
