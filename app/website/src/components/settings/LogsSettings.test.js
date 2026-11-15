import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, defineComponent, h } from 'vue';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage,node 环境无 DOM）,
// mock 掉后 t 原样回键,断言不硬编码文案(RulesSettings.test.js 同范式)。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k, p) => k }) }));
vi.mock('../../services/statusApi', () => ({
  getStatusSummary: vi.fn(),
  getStatusConnections: vi.fn()
}));

import { getStatusConnections, getStatusSummary } from '../../services/statusApi';
import LogsSettings from './LogsSettings.vue';

const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];
let timers;

beforeEach(() => {
  timers = { ids: [] };
  const originalSetInterval = window.setInterval;
  const originalClearInterval = window.clearInterval;
  vi.stubGlobal('setInterval', (...args) => {
    const id = originalSetInterval(...args);
    timers.ids.push(id);
    return id;
  });
  vi.stubGlobal('clearInterval', (...args) => originalClearInterval(...args));
  getStatusSummary.mockReset();
  getStatusConnections.mockReset();
});

afterEach(() => {
  for (const { app, el } of mountedApps) {
    app.unmount();
    el.remove();
  }
  mountedApps.length = 0;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function mountStatus({ summary, connections } = {}) {
  getStatusSummary.mockResolvedValue({ data: summary || { ai: {}, traffic: {}, http: {} } });
  getStatusConnections.mockResolvedValue({ data: connections || { active: [], closed: [] } });

  const el = document.createElement('div');
  document.body.appendChild(el);
  // LogsSettings 的 compat-anchors 模板块依赖若干全局 id 容器,直接挂载即可。
  const app = createApp({ render: () => h(LogsSettings) });
  app.mount(el);
  mountedApps.push({ app, el });
  return el;
}

const activeConn = (overrides = {}) => ({
  id: 'a-1',
  network: 'tcp',
  host: 'api.cursor.sh',
  destination: '93.184.216.34:443',
  source: '10.0.0.2:51000',
  route: 'RouteToALiang',
  uploadTotal: 1024,
  downloadTotal: 4096,
  startUnix: 1700000000,
  durationMillis: 65000,
  ...overrides
});

const closedConn = (overrides = {}) => ({
  id: 'c-1',
  network: 'tcp',
  host: 'api.anthropic.com',
  destination: '70.0.0.1:443',
  source: '10.0.0.2:51001',
  route: 'RouteToSocks',
  uploadTotal: 10,
  downloadTotal: 20,
  startUnix: 1700000000,
  durationMillis: 1200,
  endUnix: 1700000300,
  ...overrides
});

describe('状态页连接记录面板', () => {
  it('挂载即拉取 summary 与 connections', async () => {
    mountStatus();
    await flush();
    expect(getStatusSummary).toHaveBeenCalled();
    expect(getStatusConnections).toHaveBeenCalled();
  });

  it('活跃 tab 渲染目标/路由/字节,直连不出现在列表', async () => {
    const el = mountStatus({
      connections: { active: [activeConn()], closed: [] }
    });
    await flush();

    const table = el.querySelector('[data-connections-table]');
    expect(table).toBeTruthy();
    const text = table.textContent;
    expect(text).toContain('api.cursor.sh');
    expect(text).not.toContain('10.0.0.2:51000');
    // 路由标签走 i18n mock,t 原样回键。
    expect(text).toContain('status_routeToALiang');
  });

  it('切到已关闭 tab 展示历史记录(新→旧)', async () => {
    const el = mountStatus({
      connections: {
        active: [],
        closed: [closedConn({ id: 'c-2', host: 'newer.com', endUnix: 1700000400 }), closedConn()]
      }
    });
    await flush();

    const closedTab = el.querySelector('[data-connections-tab="closed"]');
    closedTab.click();
    await flush();

    const text = el.querySelector('[data-connections-table]').textContent;
    expect(text).toContain('newer.com');
    expect(text).toContain('api.anthropic.com');
    // 新→旧:较新的 endUnix 排前面。
    const rows = [...el.querySelectorAll('[data-connections-table] tbody tr')];
    expect(rows[0].textContent).toContain('newer.com');
    expect(rows[1].textContent).toContain('api.anthropic.com');
  });

  it('无记录时渲染空态', async () => {
    const el = mountStatus();
    await flush();
    expect(el.querySelector('[data-connections-table]')).toBeNull();
    expect(el.textContent).toContain('status_connectionsEmpty');
  });

  it('connections 接口失败不影响主状态面板', async () => {
    getStatusConnections.mockRejectedValue(new Error('boom'));
    const el = mountStatus();
    await flush();
    expect(el.textContent).toContain('status_connectionsEmpty');
  });
});

describe('状态页 SSH 出口链路卡', () => {
  it('未配置 SSH 出口时渲染占位', async () => {
    const el = mountStatus({
      summary: { ai: {}, traffic: {}, http: {}, ssh: { ok: false, chains: [] } }
    });
    await flush();
    expect(el.querySelector('[data-ssh-chains]')).toBeNull();
    expect(el.textContent).toContain('status_sshNotConfigured');
  });

  it('链状态按 alive/backoff 渲染连通与重连态', async () => {
    const el = mountStatus({
      summary: {
        ai: {}, traffic: {}, http: {},
        ssh: {
          ok: true,
          addr: 'jump.example.com:22',
          chains: [
            { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 3 },
            { index: 1, alive: false, backoff: true, backoffRemainingMs: 8000, activeChannels: 0 },
            { index: 2, alive: false, backoff: false, backoffRemainingMs: 0, activeChannels: 0 }
          ]
        }
      }
    });
    await flush();

    const chains = el.querySelector('[data-ssh-chains]');
    expect(chains).toBeTruthy();
    const text = chains.textContent;
    expect(text).toContain('status_sshChainAlive');
    expect(text).toContain('status_sshChainBackoffIn');
    expect(text).toContain('status_sshChainIdle');
    expect(text).toContain('status_sshChainActive');
  });

  it('summary 无 ssh 字段(旧后端)时按未配置渲染不报错', async () => {
    const el = mountStatus({
      summary: { ai: {}, traffic: {}, http: {} }
    });
    await flush();
    expect(el.textContent).toContain('status_sshNotConfigured');
  });
});
