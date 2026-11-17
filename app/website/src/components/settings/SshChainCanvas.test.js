import { afterEach, describe, expect, it, vi } from 'vitest';
import { createApp, h } from 'vue';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage,node 环境无 DOM）,
// mock 掉后 t 原样回键,断言不硬编码文案(LogsSettings.test.js 同范式)。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k, p) => k }) }));

import SshChainCanvas from './SshChainCanvas.vue';

const mountedApps = [];
const mountCanvas = (ssh) => {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(SshChainCanvas, { ssh }) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
};

afterEach(() => {
  mountedApps.forEach(({ app, el }) => {
    app.unmount();
    el.remove();
  });
  mountedApps.length = 0;
});

const configured = {
  ok: true,
  addr: 'exit.host:22',
  jumps: 1,
  jump_addrs: ['relay.host:2222'],
  chains: [
    { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 12 },
    { index: 1, alive: false, backoff: true, backoffRemainingMs: 8000, activeChannels: 0 },
    { index: 2, alive: false, backoff: false, backoffRemainingMs: 0, activeChannels: 0 }
  ]
};

const svgText = (el) => el.querySelector('[data-ssh-chain-svg]')?.textContent || '';

describe('SshChainCanvas', () => {
  it('ok=false 渲染未配置空态(键=status_sshNotConfigured)', () => {
    const el = mountCanvas({ ok: false, chains: [] });
    expect(el.querySelector('[data-ssh-chain-empty]')).not.toBeNull();
    expect(el.querySelector('[data-ssh-chain-empty]').textContent).toContain('status_sshNotConfigured');
    expect(el.querySelector('[data-ssh-chain-svg]')).toBeNull();
  });

  it('ok=true 且 chains 空渲染瞬时空态(键=rules_sshChainIdleHint)', () => {
    const el = mountCanvas({ ok: true, addr: 'a:1', chains: [] });
    expect(el.querySelector('[data-ssh-chain-empty]').textContent).toContain('rules_sshChainIdleHint');
  });

  it('ssh prop 为 null 安全渲染空态不抛错', () => {
    const el = mountCanvas(null);
    expect(el.querySelector('[data-ssh-chain-empty]')).not.toBeNull();
  });

  it('节点链完整:本机/跳板/出口三节点齐,跳板带地址与出口带地址', () => {
    const el = mountCanvas(configured);
    const text = svgText(el);
    expect(text).toContain('status_sshNodeLocal');
    expect(text).toContain('status_sshNodeJump');
    expect(text).toContain('relay.host:2222');
    expect(text).toContain('status_sshNodeExit');
    expect(text).toContain('exit.host:22');
  });

  it('jump_addrs 缺失(旧 agent)降级:跳板节点仍在但不显地址', () => {
    const el = mountCanvas({ ...configured, jump_addrs: undefined });
    const text = svgText(el);
    expect(text).toContain('status_sshNodeJump');
    expect(text).not.toContain('relay.host:2222');
  });

  it('泳道三态键齐且语义色正确:存活绿/退避琥珀/空闲灰;活跃通道键仅>0 显', () => {
    const el = mountCanvas(configured);
    const lanes = el.querySelector('[data-ssh-chain-lanes]').textContent;
    expect(lanes).toContain('status_sshChainAlive');
    expect(lanes).toContain('status_sshChainBackoffIn');
    expect(lanes).toContain('status_sshChainIdle');
    expect(lanes).toContain('status_sshChainLabel');
    expect(lanes).toContain('status_sshChainActive');
    const svg = el.querySelector('[data-ssh-chain-svg]').outerHTML;
    expect(svg).toContain('#22c55e');
    expect(svg).toContain('#f59e0b');
    expect(svg).toContain('#94a3b8');
  });
});
