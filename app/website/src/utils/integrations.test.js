import { describe, expect, it } from 'vitest';
import { kernelStateBadge, mcpEndpointLabel, sourceLabels } from './integrations';

describe('kernelStateBadge', () => {
  it('三态各有独立文案键与配色', () => {
    expect(kernelStateBadge('injected').key).toBe('agent_integrations_kernelInjected');
    expect(kernelStateBadge('skipped_headers').key).toBe('agent_integrations_kernelSkippedHeaders');
    expect(kernelStateBadge('not_collected').key).toBe('agent_integrations_kernelNotCollected');
    const classes = [
      kernelStateBadge('injected').className,
      kernelStateBadge('skipped_headers').className,
      kernelStateBadge('not_collected').className,
    ];
    expect(new Set(classes).size).toBe(3); // 三态必须视觉可区分
  });

  it('未知/缺失态 fail-safe 归到「未采集」', () => {
    expect(kernelStateBadge('').key).toBe('agent_integrations_kernelNotCollected');
    expect(kernelStateBadge(undefined).key).toBe('agent_integrations_kernelNotCollected');
    expect(kernelStateBadge('bogus').key).toBe('agent_integrations_kernelNotCollected');
  });
});

describe('mcpEndpointLabel', () => {
  it('stdio 优先命令行，http 回落 URL', () => {
    expect(mcpEndpointLabel({ command: 'npx -y shell-mcp' })).toBe('npx -y shell-mcp');
    expect(mcpEndpointLabel({ url: 'https://api.example.com/mcp' })).toBe('https://api.example.com/mcp');
    expect(mcpEndpointLabel({ command: '  ', url: 'https://x/mcp' })).toBe('https://x/mcp');
  });

  it('脏输入安全返回空串', () => {
    expect(mcpEndpointLabel(null)).toBe('');
    expect(mcpEndpointLabel(undefined)).toBe('');
    expect(mcpEndpointLabel('nope')).toBe('');
    expect(mcpEndpointLabel({})).toBe('');
  });
});

describe('sourceLabels', () => {
  it('逗号串拆成标签，config 映射为 codex', () => {
    expect(sourceLabels('user,plugin')).toEqual(['user', 'plugin']);
    expect(sourceLabels('config')).toEqual(['codex']);
    expect(sourceLabels('user,config')).toEqual(['user', 'codex']);
  });

  it('脏输入返回空数组', () => {
    expect(sourceLabels('')).toEqual([]);
    expect(sourceLabels(null)).toEqual([]);
    expect(sourceLabels(undefined)).toEqual([]);
  });
});
