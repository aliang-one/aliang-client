import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, defineComponent, h } from 'vue';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage,node 环境无 DOM）,
// mock 掉后 t 原样回键,断言不硬编码文案(RulesSettings.test.js 同范式)。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k, p) => k }) }));
vi.mock('../../services/statusApi', () => ({
  getStatusSummary: vi.fn(),
  getStatusConnections: vi.fn(),
  getIngressSessions: vi.fn(),
  kickIngressSession: vi.fn(),
  blockIngressSession: vi.fn(),
  unblockIngressSession: vi.fn()
}));

import { getIngressSessions, getStatusConnections, getStatusSummary } from '../../services/statusApi';
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
  getIngressSessions.mockReset();
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

function mountStatus({ summary, connections, ingress } = {}) {
  getStatusSummary.mockResolvedValue({ data: summary || { ai: {}, traffic: {}, http: {} } });
  getStatusConnections.mockResolvedValue({ data: connections || { active: [], closed: [] } });
  getIngressSessions.mockResolvedValue({ data: ingress || { ok: false, active: [], closed: [] } });

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
    expect(el.querySelector('[data-ssh-chain-canvas]')).toBeNull();
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

    const canvas = el.querySelector('[data-ssh-chain-canvas]');
    expect(canvas).toBeTruthy();
    const lanes = canvas.querySelector('[data-ssh-chain-lanes]').textContent;
    expect(lanes).toContain('status_sshChainAlive');
    expect(lanes).toContain('status_sshChainBackoffIn');
    expect(lanes).toContain('status_sshChainIdle');
    expect(lanes).toContain('status_sshChainActive');
  });

  it('画布钉出单条节点链路径,并行连接呈泳道,防止「一条配置多条链」误读', async () => {
    const el = mountStatus({
      summary: {
        ai: {}, traffic: {}, http: {},
        ssh: {
          ok: true,
          addr: '172.16.0.123:56432',
          jumps: 0,
          chains: [
            { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 16 },
            { index: 1, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 17 },
            { index: 2, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 16 }
          ]
        }
      }
    });
    await flush();

    const canvas = el.querySelector('[data-ssh-chain-canvas]');
    expect(canvas).toBeTruthy();
    const svgText = canvas.querySelector('[data-ssh-chain-svg]').textContent;
    // 节点链只钉一条路径:本机→出口终点(地址透传进 SVG 文本)。
    expect(svgText).toContain('status_sshNodeLocal');
    expect(svgText).toContain('172.16.0.123:56432');
    // jumps=0(直连出口)不渲染跳板节点。
    expect(svgText).not.toContain('status_sshNodeJump');
    // 池内多条「链」是同一路径的并行连接,呈 N 条泳道。
    const laneRows = canvas.querySelectorAll('[data-ssh-chain-lanes] > g');
    expect(laneRows.length).toBe(3);
  });

  it('配置了跳板时节点链追加跳板节点', async () => {
    const el = mountStatus({
      summary: {
        ai: {}, traffic: {}, http: {},
        ssh: {
          ok: true,
          addr: 'exit.example.com:22',
          jumps: 1,
          chains: [
            { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 0 }
          ]
        }
      }
    });
    await flush();

    const canvas = el.querySelector('[data-ssh-chain-canvas]');
    expect(canvas).toBeTruthy();
    expect(canvas.querySelector('[data-ssh-chain-svg]').textContent).toContain('status_sshNodeJump');
  });

  it('单条连接(pool_size=1)时画布渲染单泳道', async () => {
    const el = mountStatus({
      summary: {
        ai: {}, traffic: {}, http: {},
        ssh: {
          ok: true,
          addr: '172.16.0.123:56432',
          chains: [
            { index: 0, alive: true, backoff: false, backoffRemainingMs: 0, activeChannels: 5 }
          ]
        }
      }
    });
    await flush();

    const canvas = el.querySelector('[data-ssh-chain-canvas]');
    expect(canvas).toBeTruthy();
    const laneRows = canvas.querySelectorAll('[data-ssh-chain-lanes] > g');
    expect(laneRows.length).toBe(1);
    expect(laneRows[0].textContent).toContain('status_sshChainLabel');
  });

  it('summary 无 ssh 字段(旧后端)时按未配置渲染不报错', async () => {
    const el = mountStatus({
      summary: { ai: {}, traffic: {}, http: {} }
    });
    await flush();
    expect(el.textContent).toContain('status_sshNotConfigured');
    expect(el.querySelector('[data-ssh-chain-canvas]')).toBeNull();
  });
});

// —— 接入客户端台账卡（服务端视角「谁连进来了」）——
const meshSession = (overrides = {}) => ({
  source_ip: '172.16.0.196',
  user: 'deploy',
  form: 'mesh',
  device_id: 'dev-win',
  auth_at_unix: Math.floor(Date.now() / 1000) - 300,
  last_seen_unix: Math.floor(Date.now() / 1000),
  active_channels: 2,
  duration_millis: 300000,
  closed_at_unix: 0,
  ...overrides
});

const bareSession = (overrides = {}) => meshSession({
  form: 'bare',
  device_id: '',
  active_channels: 0,
  ...overrides
});

describe('状态页接入客户端卡', () => {
  it('入口未运行时渲染「未运行」占位且无在线徽标', async () => {
    const el = mountStatus({ ingress: { ok: false, active: [], closed: [] } });
    await flush();
    expect(el.querySelector('[data-ingress-online-badge]')).toBeNull();
    expect(el.textContent).toContain('status_ingressNotRunning');
    expect(el.querySelector('[data-ingress-active-list]')).toBeNull();
  });

  it('在线客户端列表：mesh 显示 device_id+来源,bare 显示 IP+用户,徽标计数', async () => {
    const el = mountStatus({
      ingress: { ok: true, active: [meshSession(), bareSession()], closed: [] }
    });
    await flush();

    const badge = el.querySelector('[data-ingress-online-badge]');
    expect(badge).toBeTruthy();
    // i18n mock 原样回键（不插值）,断言徽标键被消费即可
    expect(badge.textContent).toContain('status_ingressOnlineBadge');

    const list = el.querySelector('[data-ingress-active-list]');
    expect(list).toBeTruthy();
    const text = list.textContent;
    // mesh：主标题是 device_id,副标题键被消费（i18n mock 原样回键,插值参数
    // 来源 IP 以参数形态传入,断言其出现在模板插值前的字面量里不可行——
    // 副标题断言键、主标题断言真实数据值）
    expect(text).toContain('dev-win');
    expect(text).toContain('status_ingressMeshSubtitle');
    // bare：主标题是 IP,副标题用户键被消费
    expect(text).toContain('172.16.0.196');
    expect(text).toContain('status_ingressUserSubtitle');
    // 形态徽标走 i18n mock
    expect(text).toContain('status_ingressFormMesh');
    expect(text).toContain('status_ingressFormBare');
    // 两行（mesh+bare）
    expect(list.children.length).toBe(2);
  });

  it('最近断开列表渲染来源与时间,且不占在线徽标', async () => {
    const el = mountStatus({
      ingress: {
        ok: true,
        active: [],
        closed: [meshSession({ closed_at_unix: Math.floor(Date.now() / 1000) - 60 })]
      }
    });
    await flush();

    expect(el.querySelector('[data-ingress-active-list]')).toBeNull();
    expect(el.textContent).toContain('status_ingressEmpty');
    const closed = el.querySelector('[data-ingress-closed-list]');
    expect(closed).toBeTruthy();
    expect(closed.textContent).toContain('dev-win');
    expect(closed.textContent).toContain('status_ingressFormMesh');
  });

  it('接口失败时静默降级为未运行态,不报错', async () => {
    getIngressSessions.mockRejectedValue(new Error('boom'));
    const el = mountStatus();
    await flush();
    expect(el.textContent).toContain('status_ingressNotRunning');
  });
});

// —— 接入客户端管理动作（踢出 / 拉黑 / 解封）——
import { blockIngressSession, kickIngressSession, unblockIngressSession } from '../../services/statusApi';

// mock 需补齐三个动作函数（上方 vi.mock 工厂是静态对象）。
beforeEach(() => {
  kickIngressSession.mockReset();
  blockIngressSession.mockReset();
  unblockIngressSession.mockReset();
});

const fpSession = (overrides = {}) => meshSession({ fingerprint: 'SHA256:fp-win', ...overrides });

describe('状态页接入客户端管理动作', () => {
  it('在线行渲染踢出/拉黑按钮,已拉黑行显示徽标且无拉黑按钮', async () => {
    const el = mountStatus({
      ingress: {
        ok: true,
        active: [fpSession(), fpSession({ fingerprint: 'SHA256:fp-bare', form: 'bare', device_id: '' })],
        closed: [],
        blocks: { fingerprints: ['SHA256:fp-win'], device_ids: [] }
      }
    });
    await flush();

    const kickBtns = el.querySelectorAll('[data-ingress-kick]');
    expect(kickBtns.length).toBe(2);
    const blockBtns = el.querySelectorAll('[data-ingress-block]');
    expect(blockBtns.length).toBe(1); // fp-win 已拉黑,只剩 bare 行可拉黑
    expect(el.querySelector('[data-ingress-blocked-badge]')).toBeTruthy();
  });

  it('确认后调用踢出接口并刷新台账', async () => {
    window.confirm = vi.fn(() => true);
    kickIngressSession.mockResolvedValue({ data: { kicked: true } });
    const el = mountStatus({
      ingress: { ok: true, active: [fpSession()], closed: [], blocks: { fingerprints: [], device_ids: [] } }
    });
    await flush();

    el.querySelector('[data-ingress-kick]').click();
    await flush();
    expect(kickIngressSession).toHaveBeenCalledWith('SHA256:fp-win');
    // 操作后刷新台账（调用次数 2：挂载 1 次 + 刷新 1 次）
    expect(getIngressSessions.mock.calls.length).toBe(2);
  });

  it('确认取消则不调接口', async () => {
    window.confirm = vi.fn(() => false);
    const el = mountStatus({
      ingress: { ok: true, active: [fpSession()], closed: [], blocks: { fingerprints: [], device_ids: [] } }
    });
    await flush();

    el.querySelector('[data-ingress-kick]').click();
    await flush();
    expect(kickIngressSession).not.toHaveBeenCalled();
  });

  it('拉黑动作带指纹,成功后提示并刷新', async () => {
    window.confirm = vi.fn(() => true);
    window.alert = vi.fn();
    blockIngressSession.mockResolvedValue({ data: { blocked: true } });
    const el = mountStatus({
      ingress: { ok: true, active: [fpSession()], closed: [], blocks: { fingerprints: [], device_ids: [] } }
    });
    await flush();

    el.querySelector('[data-ingress-block]').click();
    await flush();
    expect(blockIngressSession).toHaveBeenCalledWith({ fingerprint: 'SHA256:fp-win', deviceId: 'dev-win' });
    expect(window.alert).toHaveBeenCalled();
    expect(getIngressSessions.mock.calls.length).toBe(2);
  });

  it('拒绝名单区渲染条目并可解封', async () => {
    window.confirm = vi.fn(() => true);
    window.alert = vi.fn();
    unblockIngressSession.mockResolvedValue({ data: { blocked: false } });
    const el = mountStatus({
      ingress: {
        ok: true,
        active: [],
        closed: [],
        blocks: { fingerprints: ['SHA256:abc'], device_ids: ['dev-old'] }
      }
    });
    await flush();

    const list = el.querySelector('[data-ingress-blocked-list]');
    expect(list).toBeTruthy();
    expect(list.textContent).toContain('SHA256:abc');
    expect(list.textContent).toContain('dev-old');

    const unblockBtns = el.querySelectorAll('[data-ingress-unblock]');
    expect(unblockBtns.length).toBe(2);
    unblockBtns[1].click();
    await flush();
    expect(unblockIngressSession).toHaveBeenCalledWith({ fingerprint: '', deviceId: 'dev-old' });
  });

  it('无指纹会话不渲染管理按钮（旧会话兜底）', async () => {
    const el = mountStatus({
      ingress: { ok: true, active: [fpSession({ fingerprint: '' })], closed: [], blocks: { fingerprints: [], device_ids: [] } }
    });
    await flush();
    expect(el.querySelector('[data-ingress-kick]')).toBeNull();
    expect(el.querySelector('[data-ingress-block]')).toBeNull();
  });
});
