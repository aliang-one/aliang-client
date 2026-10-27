import { describe, expect, it } from 'vitest';
import { slashProviderFor } from './chatProvider';

describe('slashProviderFor', () => {
  it('agentcore 三种写法都映射为空串（不展示斜杠命令面）', () => {
    expect(slashProviderFor('agentcore')).toBe('');
    expect(slashProviderFor('agent-core')).toBe('');
    expect(slashProviderFor('agent_core')).toBe('');
    expect(slashProviderFor('AgentCore')).toBe('');
  });

  it('codex 映射为 codex', () => {
    expect(slashProviderFor('codex')).toBe('codex');
    expect(slashProviderFor('CODEX')).toBe('codex');
  });

  it('其余（含缺省）映射为 claude', () => {
    expect(slashProviderFor('claudecode')).toBe('claude');
    expect(slashProviderFor('claude')).toBe('claude');
    expect(slashProviderFor('')).toBe('claude');
    expect(slashProviderFor(undefined)).toBe('claude');
    expect(slashProviderFor(null)).toBe('claude');
  });
});
