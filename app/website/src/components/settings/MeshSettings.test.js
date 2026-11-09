import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, h } from 'vue';

// SettingsPage.vue → i18n 模块求值会连带 loadLocale()（读 localStorage，
// 测试环境不保证语言确定性）；mock 掉该依赖（SshIngressSettings.test.js 同
// 范式，t 原样回键，断言不硬编码文案）。卡片补充只读数据走全局 fetch，
// vi.stubGlobal 注入；onSave prop 捕获 'save' 事件载荷。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { normalizeMeshConfig, parseMeshRuleTargetCount, MESH_FALLBACK_CLASSES } from './MeshSettings.js';
import { buildCustomerSavePatch } from '../SettingsPage.vue';
import { getMeshLanPeers, getMeshStatus } from '../../services/agentApi';
import MeshSettings from './MeshSettings.vue';
import en from '../../i18n/en';
import zh from '../../i18n/zh';

// —— normalizeMeshConfig（线格式归一；SettingsPage 归一化链共用）——
// 线格式 customer.mesh{enable,forward_enable,max_hops,peers[],fallback}；
// 后端三态指针语义（processor/config types.go MeshConfig.Effective*）：
// enable nil=true / forward_enable nil=false / peer.relay nil=true /
// fallback.enable nil=false——读侧恒产出显式布尔（ssh_ingress「恒产出显式」
// 先例），保存才不会漏键。
describe('MeshSettings normalizeMeshConfig', () => {
  it('缺失段产出全默认：三态布尔恒显式（enable=true/forward=false/fallback.enable=false）', () => {
    expect(normalizeMeshConfig(undefined)).toEqual({
      enable: true,
      forward_enable: false,
      max_hops: 0,
      peers: [],
      fallback: { enable: false, classes: [], addr: '' }
    });
    // 显式布尔断言：nil → 显式 true/false，而非 undefined（镜像 ssh_ingress 先例）
    const defaults = normalizeMeshConfig(undefined);
    expect(typeof defaults.enable).toBe('boolean');
    expect(typeof defaults.forward_enable).toBe('boolean');
    expect(typeof defaults.fallback.enable).toBe('boolean');
  });

  it('非对象载荷（null/数组/字符串）一律归默认', () => {
    expect(normalizeMeshConfig(null)).toEqual(normalizeMeshConfig(undefined));
    expect(normalizeMeshConfig(['nope'])).toEqual(normalizeMeshConfig(undefined));
    expect(normalizeMeshConfig('mesh')).toEqual(normalizeMeshConfig(undefined));
    expect(normalizeMeshConfig(42)).toEqual(normalizeMeshConfig(undefined));
  });

  it('显式布尔原样保留（含反向值）', () => {
    const out = normalizeMeshConfig({
      enable: false,
      forward_enable: true,
      fallback: { enable: true, addr: '192.168.1.20:56441', classes: ['ai'] }
    });
    expect(out.enable).toBe(false);
    expect(out.forward_enable).toBe(true);
    expect(out.fallback.enable).toBe(true);
    expect(out.fallback.addr).toBe('192.168.1.20:56441');
    expect(out.fallback.classes).toEqual(['ai']);
  });

  it('max_hops：[1,8] 保留、>8 钳到 8（对齐后端读侧 EffectiveMaxHops）、≤0/非数归 0（默认 5）', () => {
    expect(normalizeMeshConfig({ max_hops: 5 }).max_hops).toBe(5);
    expect(normalizeMeshConfig({ max_hops: 1 }).max_hops).toBe(1);
    expect(normalizeMeshConfig({ max_hops: 8 }).max_hops).toBe(8);
    expect(normalizeMeshConfig({ max_hops: 0 }).max_hops).toBe(0);
    expect(normalizeMeshConfig({ max_hops: -3 }).max_hops).toBe(0);
    // >8 钳到 8：后端 EffectiveMaxHops 读侧同款收口——9 的运行表现是 8，
    // 不是默认 5（归 0 会把 9 错读成 5）；钳制值不透传给后端 Validate
    expect(normalizeMeshConfig({ max_hops: 9 }).max_hops).toBe(8);
    expect(normalizeMeshConfig({ max_hops: 100 }).max_hops).toBe(8);
    expect(normalizeMeshConfig({ max_hops: 4.5 }).max_hops).toBe(4);
    expect(normalizeMeshConfig({ max_hops: '5' }).max_hops).toBe(0);
    expect(normalizeMeshConfig({ max_hops: Number.NaN }).max_hops).toBe(0);
  });

  it('peers 逐项 TrimSpace：device_id/lan_addrs 规整、空白项剔除、relay nil/非布尔→true', () => {
    const out = normalizeMeshConfig({
      peers: [
        { device_id: ' dev-b ', lan_addrs: [' 192.168.1.20:56441 ', '', 'x.com:1 '], relay: false },
        { device_id: 'dev-c' }, // relay 缺省 → 显式 true
        { device_id: '   ' }, // 空白 device_id = 读侧垃圾，剔除
        'junk', // 非对象条目剔除
        { device_id: 'dev-d', relay: 'yes' } // 非布尔 relay → 默认 true
      ]
    });
    expect(out.peers).toEqual([
      { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441', 'x.com:1'], relay: false },
      { device_id: 'dev-c', lan_addrs: [], relay: true },
      { device_id: 'dev-d', lan_addrs: [], relay: true }
    ]);
    expect(normalizeMeshConfig({ peers: 'nope' }).peers).toEqual([]);
  });

  it('fallback：addr TrimSpace；classes 只认 ai/self/custom（规整去重）、其余剔除', () => {
    const out = normalizeMeshConfig({
      fallback: {
        enable: true,
        addr: '  192.168.1.20:56441  ',
        classes: [' ai ', 'bogus', 'custom', 'ai', 'self', 42, '']
      }
    });
    expect(out.fallback.addr).toBe('192.168.1.20:56441');
    expect(out.fallback.classes).toEqual(['ai', 'custom', 'self']);
    expect(MESH_FALLBACK_CLASSES).toEqual(['ai', 'self', 'custom']);
    // fallback 非对象（字符串/数组）→ 默认；enable 非布尔 → false
    expect(normalizeMeshConfig({ fallback: 'x' }).fallback).toEqual({ enable: false, classes: [], addr: '' });
    expect(normalizeMeshConfig({ fallback: { enable: 'yes' } }).fallback.enable).toBe(false);
    expect(normalizeMeshConfig({ fallback: { addr: 42 } }).fallback.addr).toBe('');
  });
});

// —— parseMeshRuleTargetCount（proxy_rules 条目 → mesh:<id> 目标计数）——
// 客户端解析与后端 processor/routing splitMeshTail 语义对齐：任一逗号段
// （TrimSpace+小写）以 mesh: 开头即 mesh 条目；mesh: 段后至多一个策略段
// direct|deny|fallback:<id2>（关键字大小写不敏感、目标 TrimSpace 非空）；
// deviceID/base 为空、未知或多余策略段 → 非法不计入（后端整条静默跳过）。
describe('MeshSettings parseMeshRuleTargetCount', () => {
  it('非 mesh 条目与非数组入参不计入', () => {
    expect(parseMeshRuleTargetCount(undefined)).toEqual({});
    expect(parseMeshRuleTargetCount('nope')).toEqual({});
    expect(parseMeshRuleTargetCount(['x.com,direct', 'geoip:cn,proxy', 'dns', ''])).toEqual({});
  });

  it('mesh 尾段计数并按目标累加', () => {
    expect(parseMeshRuleTargetCount([
      'x.com,mesh:dev-b',
      'a.cn,mesh:dev-b',
      'y.com,mesh:dev-c'
    ])).toEqual({ 'dev-b': 2, 'dev-c': 1 });
  });

  it('策略段三形态（direct/deny/fallback:<id2>）与大小写不敏感均计入 mesh 目标', () => {
    expect(parseMeshRuleTargetCount([
      'x.com,mesh:dev-b,direct',
      'a.cn,mesh:dev-b,DENY',
      'y.com,mesh:dev-b,fallback:dev-c',
      'z.com,mesh:dev-b,FALLBACK:dev-c'
    ])).toEqual({ 'dev-b': 4 });
  });

  it('mesh: 前缀大小写不敏感；deviceID 保留原大小写；中段 base 完整保留', () => {
    expect(parseMeshRuleTargetCount(['x.com,MESH:Dev-B'])).toEqual({ 'Dev-B': 1 });
    expect(parseMeshRuleTargetCount(['domains,x.com,mesh:dev-b'])).toEqual({ 'dev-b': 1 });
    expect(parseMeshRuleTargetCount([' x.com , mesh:dev-b , direct '])).toEqual({ 'dev-b': 1 });
  });

  it('非法形态不计入：空 deviceID/空 base/未知策略段/多余段/空策略段', () => {
    expect(parseMeshRuleTargetCount([
      'x.com,mesh:', // 空 deviceID
      'mesh:dev-b', // 空 base
      'x.com,mesh:dev-b,bogus', // 未知策略段
      'x.com,mesh:dev-b,direct,extra', // 多余段
      'x.com,mesh:dev-b,', // 空策略段（尾逗号）
      'x.com,mesh:dev-b,fallback:', // fallback 空目标
      'x.com,mesh:dev-b,fallback:  ', // fallback 目标 TrimSpace 后为空
      [null, 42] // 非字符串条目跳过
    ])).toEqual({});
  });
});

// —— agentApi mesh 端点（request + 信封拆包；fetch stub）——
const okEnvelope = (data) => ({ ok: true, status: 200, json: async () => ({ code: 0, msg: '', data }) });

describe('agentApi mesh 端点', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('getMeshStatus：GET /api/mesh/status 并拆 data 信封', async () => {
    const statusPayload = {
      device_id: 'dev-a',
      enable: true,
      forward_enable: false,
      lan_listen_port: 56441,
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
      lan_peers: [{ device_id: 'dev-b', mesh_port: 56441, cert_fingerprint: '', last_seen_unix: 123 }]
    };
    const fetchMock = vi.fn().mockResolvedValue(okEnvelope(statusPayload));
    vi.stubGlobal('fetch', fetchMock);
    await expect(getMeshStatus()).resolves.toEqual(statusPayload);
    expect(fetchMock).toHaveBeenCalledWith('/api/mesh/status', expect.objectContaining({
      method: 'GET',
      credentials: 'same-origin'
    }));
  });

  it('getMeshLanPeers：GET /api/mesh/lan_peers 并拆 data.peers', async () => {
    const peersPayload = { peers: [{ device_id: 'dev-b', mesh_port: 56441, cert_fingerprint: '', last_seen_unix: 123 }] };
    const fetchMock = vi.fn().mockResolvedValue(okEnvelope(peersPayload));
    vi.stubGlobal('fetch', fetchMock);
    await expect(getMeshLanPeers()).resolves.toEqual(peersPayload);
    expect(fetchMock).toHaveBeenCalledWith('/api/mesh/lan_peers', expect.objectContaining({ method: 'GET' }));
  });

  it('非零 code 抛错并透传 msg（调用方处理错误）', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ code: 100, msg: 'mesh 未装配', data: null })
    }));
    await expect(getMeshStatus()).rejects.toThrow('mesh 未装配');
  });
});

// —— SettingsPage 归一化链（buildCustomerSavePatch：卡片载荷→提交 patch）——
// 红线①：RulesSettings 只发 proxy/ai_rules/proxy_rules 三段——父层合并兜底
// 必须保证其余段（含 mesh）不进置零 patch。
// 红线②：mesh 卡片载荷恒产出显式布尔；兜底 addr 清空是显式意图，必须
// 显式发空串（buildConfigPatch 对空串默认跳过）。
describe('SettingsPage buildCustomerSavePatch（mesh 透传链）', () => {
  const meshConfigured = {
    enable: true,
    forward_enable: false,
    max_hops: 5,
    peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
    fallback: { enable: false, classes: [], addr: '' }
  };

  it('RulesSettings 三段载荷不得把 mesh 归零（父层合并兜底）', () => {
    const current = {
      proxy: { enable: true, type: 'socks5', server: 'old:1080' },
      mesh: meshConfigured
    };
    const patch = buildCustomerSavePatch(
      { proxy: { enable: true, type: 'ssh', server: 'new:22' }, ai_rules: {}, proxy_rules: [] },
      current
    );
    expect(patch.mesh).toBeUndefined();
    expect(patch.proxy.server).toBe('new:22');
  });

  it('mesh 卡片载荷产出兜底 diff（含显式布尔与 peers 规整透传）', () => {
    const current = { mesh: meshConfigured };
    const patch = buildCustomerSavePatch(
      {
        mesh: {
          enable: true,
          forward_enable: false,
          max_hops: 5,
          peers: [{ device_id: ' dev-b ', lan_addrs: ['192.168.1.20:56441'], relay: true }],
          fallback: { enable: true, addr: '192.168.1.20:56441', classes: ['ai'] }
        }
      },
      current
    );
    // peers 带空白透传规整后与现值等价 → 不进 patch；只有 fallback 变化
    expect(patch.mesh).toEqual({
      fallback: { enable: true, addr: '192.168.1.20:56441', classes: ['ai'] }
    });
  });

  it('关兜底并清空 addr：空串显式带出（防旧地址在再启用时复活）', () => {
    const current = {
      mesh: { ...meshConfigured, fallback: { enable: true, classes: ['ai'], addr: '192.168.1.20:56441' } }
    };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, fallback: { enable: false, classes: ['ai'], addr: '' } } },
      current
    );
    expect(patch.mesh.fallback).toEqual({ enable: false, addr: '' });
  });

  it('GET 回显值原样透传不产生伪 diff（无改动 → 无 patch）', () => {
    const current = { mesh: meshConfigured };
    const same = buildCustomerSavePatch({ mesh: { ...meshConfigured } }, current);
    expect(same).toBeUndefined();
  });
});

// —— 挂载级测试（createApp + jsdom 手法，SshIngressSettings.test.js 同范式）——
// 卡片三路补充只读数据（/api/agent/status、/api/mesh/status、/api/mesh/lan_peers）
// 全部走 agentApi → 全局 fetch：URL 路由 stub 一次覆盖（信封 code=0 拆 data）。
const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function mountCard(config, extraProps = {}) {
  const saved = [];
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(MeshSettings, {
      config,
      ...extraProps,
      onSave: (payload) => saved.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saved };
}

// 基座配置含全量顶层段（proxy 等），验证 save 载荷是「整配置 + mesh 覆盖」
// ——SettingsPage 按顶层段浅合并，卡片不得丢掉自己不拥有的段。
function baseConfig(meshOverrides = {}) {
  return {
    proxy: {
      enable: true,
      type: 'socks5',
      server: '127.0.0.1:1080',
      username: '',
      password: '',
      private_key: '',
      key_passphrase: '',
      pool_size: 0,
      jump_servers: []
    },
    ai_rules: {},
    proxy_rules: [],
    mesh: {
      enable: true,
      forward_enable: false,
      max_hops: 5,
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
      fallback: { enable: false, classes: [], addr: '' },
      ...meshOverrides
    }
  };
}

const jsonOk = (data) => ({ ok: true, status: 200, json: async () => ({ code: 0, msg: '', data }) });

const defaultMeshStatus = () => ({
  device_id: 'dev-a',
  enable: true,
  forward_enable: false,
  lan_listen_port: 56441,
  peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
  lan_peers: []
});

const LAN_PEERS = [
  { device_id: 'dev-b', mesh_port: 56441, cert_fingerprint: '', last_seen_unix: 123 },
  { device_id: 'dev-c', mesh_port: 56442, cert_fingerprint: '', last_seen_unix: 122 }
];

// 三路补充数据的 URL 路由 stub：agentStatus/meshStatus 默认在线+已配置；
// *Error 模拟拉取失败（信封失败或网络拒绝）。
function stubApi({ agentStatus, agentStatusError, meshStatus, meshStatusError, lanPeers, lanPeersError } = {}) {
  vi.stubGlobal('fetch', vi.fn((path) => {
    if (path === '/api/agent/status') {
      return agentStatusError
        ? Promise.reject(new Error('agent down'))
        : Promise.resolve(jsonOk(agentStatus ?? { runtime: { online: true } }));
    }
    if (path === '/api/mesh/status') {
      return meshStatusError
        ? Promise.reject(new Error('mesh down'))
        : Promise.resolve(jsonOk(meshStatus ?? defaultMeshStatus()));
    }
    if (path === '/api/mesh/lan_peers') {
      return lanPeersError
        ? Promise.reject(new Error('lan peers down'))
        : Promise.resolve(jsonOk({ peers: lanPeers ?? [] }));
    }
    return Promise.reject(new Error(`unexpected fetch: ${path}`));
  }));
}

function setField(el, field, value) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.value = value;
  node.dispatchEvent(new Event('input', { bubbles: true }));
}

// jsdom 坑：checkbox .click() 不发 change（peer-link 审计存档）——赋 checked 再派发
function setToggle(el, field, checked) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.checked = checked;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

function selectCandidate(el, field, value) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.value = value;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

function saveButton(el) {
  return el.querySelector('button[data-mesh-save]');
}

function clickSave(el) {
  saveButton(el).click();
}

function statusPill(el) {
  return el.querySelector('[data-mesh-status-pill]');
}

describe('MeshSettings 卡片', () => {
  beforeEach(() => {
    stubApi({ lanPeers: LAN_PEERS });
  });

  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  // —— 状态条三态 + 降级态（离线数据源=/api/agent/status runtime.online,
  //     AgentSettings runtimeOnline 既有模式;mesh status 拉取失败=降级态）——
  it('在线（runtime.online=true 且已配置）：绿态 mesh_statusOnline', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    const pill = statusPill(el);
    expect(pill.textContent).toContain('mesh_statusOnline');
    expect(pill.className).toContain('emerald');
    expect(pill.className).not.toContain('amber');
  });

  it('离线 + 兜底已启用（保存态 enable+addr）：琥珀 mesh_statusOfflineFallback', async () => {
    stubApi({ agentStatus: { runtime: { online: false } } });
    const { el } = mountCard(baseConfig({
      fallback: { enable: true, classes: ['ai'], addr: '192.168.1.20:56441' }
    }));
    await flush();
    const pill = statusPill(el);
    expect(pill.textContent).toContain('mesh_statusOfflineFallback');
    expect(pill.className).toContain('amber');
  });

  it('离线且无兜底：琥珀 mesh_statusOffline（区别于兜底生效文案）', async () => {
    stubApi({ agentStatus: { runtime: { online: false } } });
    const { el } = mountCard(baseConfig());
    await flush();
    expect(statusPill(el).textContent).toContain('mesh_statusOffline');
    expect(statusPill(el).textContent).not.toContain('mesh_statusOfflineFallback');
  });

  it('未装配（status peers 空+forward=false+无兜底）：灰态 mesh_statusIdle；优先于离线判定', async () => {
    stubApi({
      agentStatus: { runtime: { online: false } },
      meshStatus: { ...defaultMeshStatus(), peers: [], forward_enable: false }
    });
    const { el } = mountCard({ ...baseConfig(), mesh: undefined });
    await flush();
    const pill = statusPill(el);
    expect(pill.textContent).toContain('mesh_statusIdle');
    expect(pill.className).toContain('slate');
    expect(pill.className).not.toContain('amber');
  });

  it('mesh status 拉取失败 = 降级态：琥珀 mesh_statusDegraded + 提示（区别于确认离线）', async () => {
    stubApi({ meshStatusError: true });
    const { el } = mountCard(baseConfig());
    await flush();
    const pill = statusPill(el);
    expect(pill.textContent).toContain('mesh_statusDegraded');
    expect(pill.className).toContain('amber');
    expect(el.textContent).toContain('mesh_statusDegradedHint');
    // 降级态不误报「兜底生效中」
    expect(pill.textContent).not.toContain('mesh_statusOfflineFallback');
  });

  // —— 状态组合矩阵（评审回填）——
  it('组合①：agent status 拉取失败而 mesh status 正常 → 乐观在线绿（降级判定归 mesh status）', async () => {
    stubApi({ agentStatusError: true });
    const { el } = mountCard(baseConfig());
    await flush();
    const pill = statusPill(el);
    expect(pill.textContent).toContain('mesh_statusOnline');
    expect(pill.className).toContain('emerald');
    // agent status 单路失败不冒充降级态
    expect(pill.textContent).not.toContain('mesh_statusDegraded');
    expect(el.textContent).not.toContain('mesh_statusDegradedHint');
  });

  it('组合②：lan_peers 拉取失败 → 静默空表不报错（发现缺席是合法态,不冒充降级）', async () => {
    stubApi({ lanPeersError: true });
    const { el } = mountCard(baseConfig());
    await flush();
    const select = el.querySelector('[data-field="meshFallbackCandidate"]');
    expect([...select.querySelectorAll('option')].map((o) => o.value)).toEqual(['']);
    expect(select.textContent).toContain('mesh_lanPeersEmpty');
    expect(statusPill(el).textContent).toContain('mesh_statusOnline');
    expect(el.textContent).not.toContain('mesh_statusDegradedHint');
  });

  it('组合③：lanPeers 空表时候选 select disabled', async () => {
    stubApi({ lanPeers: [] });
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.querySelector('[data-field="meshFallbackCandidate"]').disabled).toBe(true);
  });

  it('saving 态：兜底开关与全部编辑控件、保存按钮禁用（与兄弟控件口径一致）', async () => {
    const { el } = mountCard(baseConfig(), { saving: true });
    await flush();
    expect(el.querySelector('[data-field="meshFallbackEnable"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackAddr"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackClassAi"], [data-field="meshClassAi"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackCandidate"]').disabled).toBe(true);
    expect(saveButton(el).disabled).toBe(true);
  });

  it('卡片挂载拉取 lan_peers 候选并渲染下拉选项', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/mesh/lan_peers', expect.anything());
    const select = el.querySelector('[data-field="meshFallbackCandidate"]');
    const options = [...select.querySelectorAll('option')];
    expect(options.map((o) => o.value)).toEqual(['', 'dev-b', 'dev-c']);
  });

  // —— 兜底常驻卡编辑 + save 载荷 ——
  it('回显：开关/地址/类别勾选从配置回填', async () => {
    const { el } = mountCard(baseConfig({
      fallback: { enable: true, classes: ['ai', 'custom'], addr: '192.168.1.20:56441' }
    }));
    await flush();
    expect(el.querySelector('[data-field="meshFallbackEnable"]').checked).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackAddr"]').value).toBe('192.168.1.20:56441');
    expect(el.querySelector('[data-field="meshClassAi"]').checked).toBe(true);
    expect(el.querySelector('[data-field="meshClassSelf"]').checked).toBe(false);
    expect(el.querySelector('[data-field="meshClassCustom"]').checked).toBe(true);
  });

  it('校验：兜底 Enable=true 而 Addr 空 → mesh_validationFallbackAddr + 禁保存', async () => {
    const { el, saved } = mountCard(baseConfig());
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    await flush();
    expect(el.textContent).toContain('mesh_validationFallbackAddr');
    expect(saveButton(el).disabled).toBe(true);
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(0);

    setField(el, 'meshFallbackAddr', '192.168.1.20:56441');
    await flush();
    expect(el.textContent).not.toContain('mesh_validationFallbackAddr');
    expect(saveButton(el).disabled).toBe(false);
  });

  it('候选点选：已输 host 补全 host:port；无 host 不产出半截地址', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    setField(el, 'meshFallbackAddr', '192.168.1.20');
    selectCandidate(el, 'meshFallbackCandidate', 'dev-b');
    await flush();
    expect(el.querySelector('[data-field="meshFallbackAddr"]').value).toBe('192.168.1.20:56441');

    // 完整地址再点选 → 端口被替换为该候选端口
    selectCandidate(el, 'meshFallbackCandidate', 'dev-c');
    await flush();
    expect(el.querySelector('[data-field="meshFallbackAddr"]').value).toBe('192.168.1.20:56442');

    // 清空后点选 → 无 host 不填半截（beacon 候选无源 IP，host 必须手输）
    setField(el, 'meshFallbackAddr', '');
    selectCandidate(el, 'meshFallbackCandidate', 'dev-b');
    await flush();
    expect(el.querySelector('[data-field="meshFallbackAddr"]').value).toBe('');
  });

  it('save 载荷：整配置透传 + mesh 恒显式布尔 + peers 规整透传 + fallback 编辑值', async () => {
    const { el, saved } = mountCard(baseConfig({
      peers: [{ device_id: ' dev-b ', lan_addrs: ['192.168.1.20:56441'], relay: false }]
    }));
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    setField(el, 'meshFallbackAddr', ' 192.168.1.20:56441 ');
    setToggle(el, 'meshClassSelf', true);
    setToggle(el, 'meshClassCustom', true);
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 整配置透传：父层浅合并不丢其他顶层段
    expect(saved[0].proxy.server).toBe('127.0.0.1:1080');
    expect(saved[0].mesh).toEqual({
      enable: true,
      forward_enable: false,
      max_hops: 5,
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: false }],
      fallback: { enable: true, addr: '192.168.1.20:56441', classes: ['self', 'custom'] }
    });
    // 恒产出显式布尔（ssh_ingress 先例）：nil 不透传成 undefined
    expect(typeof saved[0].mesh.enable).toBe('boolean');
    expect(saved[0].mesh.enable).toBe(true);
  });

  it('save 载荷：配置缺 mesh 段 → 默认显式布尔（enable=true/forward=false）随兜底一起落盘', async () => {
    const { el, saved } = mountCard({ ...baseConfig(), mesh: undefined });
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    setField(el, 'meshFallbackAddr', '10.0.0.5:56441');
    await flush();
    clickSave(el);
    await flush();
    expect(saved[0].mesh.enable).toBe(true);
    expect(saved[0].mesh.forward_enable).toBe(false);
    expect(saved[0].mesh.max_hops).toBe(0);
    expect(saved[0].mesh.peers).toEqual([]);
    expect(saved[0].mesh.fallback).toEqual({ enable: true, addr: '10.0.0.5:56441', classes: [] });
  });

  it('i18n：mesh_* 新键 zh/en 同步存在', () => {
    const meshKeys = [
      'mesh_title',
      'mesh_description',
      'mesh_statusOnline',
      'mesh_statusOffline',
      'mesh_statusOfflineFallback',
      'mesh_statusDegraded',
      'mesh_statusDegradedHint',
      'mesh_statusIdle',
      'mesh_loading',
      'mesh_fallbackTitle',
      'mesh_fallbackDesc',
      'mesh_fallbackEnable',
      'mesh_fallbackAddr',
      'mesh_fallbackAddrPlaceholder',
      'mesh_fallbackCandidate',
      'mesh_fallbackCandidatePlaceholder',
      'mesh_lanPeersEmpty',
      'mesh_fallbackClasses',
      'mesh_fallbackClassAI',
      'mesh_fallbackClassSelf',
      'mesh_fallbackClassCustom',
      'mesh_validationFallbackAddr',
      'mesh_save',
      'mesh_saving'
    ];
    for (const key of meshKeys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});
