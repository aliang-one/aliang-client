import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, h, reactive } from 'vue';

// SettingsPage.vue → i18n 模块求值会连带 loadLocale()（读 localStorage，
// 测试环境不保证语言确定性）；mock 掉该依赖（SshIngressSettings.test.js 同
// 范式，t 原样回键，断言不硬编码文案）。卡片补充只读数据走全局 fetch，
// vi.stubGlobal 注入；onSave prop 捕获 'save' 事件载荷。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import {
  listMeshDeviceIdMismatches,
  normalizeMeshConfig,
  parseAuthorizedKeyComment,
  parseMeshRuleTargetCount,
  MESH_FALLBACK_CLASSES
} from './MeshSettings.js';
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

// —— 密钥字段透传（SPEC §13.3.1：后端 customer.mesh 新增三个密钥字段
//    private_key/key_passphrase/peers[].host_key/fallback.host_key——读侧归一
//    不抹、保存 patch 不丢；空串不落键 = 「该处没有密钥」，后端 deep-merge
//    语义一致，与 enable/classes 等可选键的省缺口径一致）——
describe('MeshSettings 密钥字段透传', () => {
  it('peers[].host_key 透传；空串/缺键不落键（该 peer 没有 host_key）', () => {
    const out = normalizeMeshConfig({
      peers: [
        { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'peer-host-key' },
        { device_id: 'dev-c', lan_addrs: [], relay: true, host_key: '' },
        { device_id: 'dev-d', lan_addrs: [], relay: true }
      ]
    });
    expect(out.peers[0]).toEqual({
      device_id: 'dev-b',
      lan_addrs: ['192.168.1.20:56441'],
      relay: true,
      host_key: 'peer-host-key'
    });
    expect('host_key' in out.peers[1]).toBe(false);
    expect('host_key' in out.peers[2]).toBe(false);
    // 非字符串照 String() 归一（与既有键写法形态一致）
    expect(normalizeMeshConfig({ peers: [{ device_id: 'dev-e', host_key: 42 }] }).peers[0].host_key).toBe('42');
  });

  it('fallback.host_key 透传；空串不落键', () => {
    const out = normalizeMeshConfig({
      fallback: { enable: true, addr: '10.0.0.5:56441', classes: ['ai'], host_key: 'fallback-host-key' }
    });
    expect(out.fallback).toEqual({
      enable: true,
      addr: '10.0.0.5:56441',
      classes: ['ai'],
      host_key: 'fallback-host-key'
    });
    expect('host_key' in normalizeMeshConfig({ fallback: { enable: true, host_key: '' } }).fallback).toBe(false);
  });

  it('private_key/key_passphrase 透传；空串不落键（省缺即「未配置密钥」）', () => {
    const out = normalizeMeshConfig({ private_key: 'mesh-private-key', key_passphrase: 'mesh-passphrase' });
    expect(out.private_key).toBe('mesh-private-key');
    expect(out.key_passphrase).toBe('mesh-passphrase');
    const bare = normalizeMeshConfig({ private_key: '', key_passphrase: '' });
    expect('private_key' in bare).toBe(false);
    expect('key_passphrase' in bare).toBe(false);
    // 默认段同样不落键
    expect(normalizeMeshConfig(undefined).private_key).toBeUndefined();
    expect(normalizeMeshConfig(undefined).key_passphrase).toBeUndefined();
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

  // —— mesh 凭据清空补救（SPEC §13.3.3）：G1 之后 private_key/key_passphrase/
  // fallback.host_key 空串不落键 → normalizedNext.mesh.<key> 为 undefined,
  // buildConfigPatch 对「next 缺键」直接跳过 → 旧值经后端 deep-merge 存活,
  // 用户以为撤销了密钥实际没有。三键必须显式发空串覆盖旧值。
  it('private_key 从有值改空：空串显式进 patch.mesh（撤销语义）', () => {
    const current = { mesh: { ...meshConfigured, private_key: 'sk-old' } };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, private_key: '' } },
      current
    );
    expect(Object.prototype.hasOwnProperty.call(patch.mesh, 'private_key')).toBe(true);
    expect(patch.mesh.private_key).toBe('');
  });

  it('key_passphrase 从有值改空：空串显式进 patch.mesh（撤销语义）', () => {
    const current = { mesh: { ...meshConfigured, key_passphrase: 'ph-old' } };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, key_passphrase: '' } },
      current
    );
    expect(Object.prototype.hasOwnProperty.call(patch.mesh, 'key_passphrase')).toBe(true);
    expect(patch.mesh.key_passphrase).toBe('');
  });

  it('fallback.host_key 从有值改空：空串显式进 patch.mesh.fallback（撤销语义）', () => {
    const current = {
      mesh: { ...meshConfigured, fallback: { enable: true, classes: ['ai'], addr: 'fb:56441', host_key: 'HK-fb-old' } }
    };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, fallback: { enable: true, classes: ['ai'], addr: 'fb:56441', host_key: '' } } },
      current
    );
    expect(Object.prototype.hasOwnProperty.call(patch.mesh.fallback, 'host_key')).toBe(true);
    expect(patch.mesh.fallback.host_key).toBe('');
  });

  it('private_key 有值→有值（不变）：不产生清空伪 patch（「未改动」语义不被补救块破坏）', () => {
    const current = { mesh: { ...meshConfigured, private_key: 'sk-keep' } };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, private_key: 'sk-keep' } },
      current
    );
    expect(patch?.mesh && Object.prototype.hasOwnProperty.call(patch.mesh, 'private_key')).toBeFalsy();
  });

  // peers[].host_key 清空不走显式空串补救：peers 是数组走整体替换分支——
  // normalizeMeshPeers 对空串不落键（同 G1）,但 current 侧非空 host_key 仍
  // 保留 → 两侧 JSON 串不等 → 数组 diff 自然带出（清空生效）。若两侧本就
  // 都无 host_key,归一化后恒等 → 无 diff = 清「本来没有的东西」为 no-op,
  // 同样正确。
  it('peers[].host_key 清空走数组整体替换分支（无需显式空串补救）', () => {
    const current = {
      mesh: { ...meshConfigured, peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'HK-peer-old' }] }
    };
    const patch = buildCustomerSavePatch(
      { mesh: { ...meshConfigured, peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: '' }] } },
      current
    );
    expect(patch.mesh.peers).toEqual([{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }]);
    expect(Object.prototype.hasOwnProperty.call(patch.mesh.peers[0], 'host_key')).toBe(false);
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
  let card = null;
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(MeshSettings, {
      config,
      ...extraProps,
      // 函数 ref：暴露子组件实例供画布位置等具名方法调用（jsdom 无布局，
      // 真拖拽链路 pointer 事件不可测，直接测具名方法）
      ref: (instance) => { card = instance; },
      onSave: (payload) => saved.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saved, card: () => card };
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

// 带全量密钥的基座配置（G3 密钥输入位共用;含 saving 态禁用断言）:peer dev-b
// 有 host_key、dev-c 没有（渲染空行）、兜底 host_key + 节点私钥/口令齐备。
function keyConfig() {
  return baseConfig({
    private_key: 'sk-old',
    key_passphrase: 'ph-old',
    peers: [
      { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'HK-peer-old' },
      { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
    ],
    fallback: { enable: false, classes: [], addr: '', host_key: 'HK-fb-old' }
  });
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

// peer 行内 host_key 输入位（每 peer 一行，按 device_id 定位；同名 data-field
// 有多份，querySelector 只认第一个会拿错行）
function peerKeyInput(el, deviceId) {
  return el.querySelector(`[data-mesh-peer-row="${deviceId}"] [data-field="meshPeerHostKey"]`);
}

function setPeerKey(el, deviceId, value) {
  const node = peerKeyInput(el, deviceId);
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
    const { el } = mountCard(keyConfig(), { saving: true });
    await flush();
    expect(el.querySelector('[data-field="meshFallbackEnable"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackAddr"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackClassAi"], [data-field="meshClassAi"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackCandidate"]').disabled).toBe(true);
    // G3 密钥输入位与兄弟控件同口径禁用
    expect(el.querySelector('[data-field="meshFallbackHostKey"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshPrivateKey"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshKeyPassphrase"]').disabled).toBe(true);
    expect(peerKeyInput(el, 'dev-b').disabled).toBe(true);
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

  it('save 载荷：fallback.host_key 经 spread 透传不被字面量抹掉，peers/private_key/key_passphrase 同保（SPEC §13.3.2）', async () => {
    const { el, saved } = mountCard(baseConfig({
      private_key: 'mesh-private-key',
      key_passphrase: 'mesh-passphrase',
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'peer-host-key' }],
      fallback: { enable: false, classes: [], addr: '', host_key: 'fallback-host-key' }
    }));
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    setField(el, 'meshFallbackAddr', ' 10.0.0.5:56441 ');
    setToggle(el, 'meshClassAi', true);
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 整配置透传三密钥字段：js 归一化不再抹掉
    expect(saved[0].mesh.private_key).toBe('mesh-private-key');
    expect(saved[0].mesh.key_passphrase).toBe('mesh-passphrase');
    expect(saved[0].mesh.peers[0].host_key).toBe('peer-host-key');
    // fallback 字面量改造：表单三键覆盖用户可编辑值，host_key 从既有 config spread 透传
    expect(saved[0].mesh.fallback).toEqual({
      enable: true,
      addr: '10.0.0.5:56441',
      classes: ['ai'],
      host_key: 'fallback-host-key'
    });
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
      // G3 密钥输入位（SPEC §13.3.6）
      'mesh_peersTitle',
      'mesh_peerHostKey',
      'mesh_peerHostKeyPlaceholder',
      'mesh_peerHostKeyHint',
      'mesh_fallbackHostKey',
      'mesh_privateKey',
      'mesh_privateKeyPlaceholder',
      'mesh_privateKeyHint',
      'mesh_keyPassphrase',
      // G4 依赖与身份提示（SPEC §10.3）
      'mesh_sshDependencyTitle',
      'mesh_sshDependencyWarning',
      'mesh_sshEnableTogether',
      'mesh_deviceIdMismatchHint',
      'mesh_save',
      'mesh_saving'
    ];
    for (const key of meshKeys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});

// —— 密钥输入位（G3/SPEC §13.3.4+§13.3.6）：peer 行 host_key / 兜底行 host_key /
//    节点身份私钥（textarea）+ 口令（password 框）。没有这组输入位用户只能手改
//    配置文件；验收链=填→保存→载荷带出；清空→保存→G1 归一化省缺→G2 清空块
//    显式空串带出（挂载级测试串联两级验证全链）。——
describe('MeshSettings 密钥输入位（G3）', () => {
  it('回显：peer 行/兜底 host_key/私钥/口令四类输入位从配置渲染现值', async () => {
    const { el } = mountCard(keyConfig());
    await flush();
    // peer 行：每个配置 peer 一行，行内 host_key 输入位绑定行数据
    expect(el.querySelector('[data-mesh-peer-row="dev-b"]')).not.toBeNull();
    expect(el.querySelector('[data-mesh-peer-row="dev-c"]')).not.toBeNull();
    expect(peerKeyInput(el, 'dev-b').value).toBe('HK-peer-old');
    expect(peerKeyInput(el, 'dev-c').value).toBe(''); // 无 host_key 的 peer 行渲染空
    // 兜底行 host_key（与 addr 并列）
    expect(el.querySelector('[data-field="meshFallbackHostKey"]').value).toBe('HK-fb-old');
    // 私钥=textarea 容 PEM 多行；口令=password 框；两处 autocomplete 关闭
    const pk = el.querySelector('[data-field="meshPrivateKey"]');
    expect(pk.tagName).toBe('TEXTAREA');
    expect(pk.value).toBe('sk-old');
    expect(pk.getAttribute('autocomplete')).toBe('off');
    const ph = el.querySelector('[data-field="meshKeyPassphrase"]');
    expect(ph.type).toBe('password');
    expect(ph.getAttribute('autocomplete')).toBe('off');
    expect(ph.value).toBe('ph-old');
  });

  it('改输入 → save：载荷携带四个新值（peer 行内编辑值随行带出）', async () => {
    const { el, saved } = mountCard(keyConfig());
    await flush();
    setField(el, 'meshPrivateKey', '-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----');
    setField(el, 'meshKeyPassphrase', 'ph-new');
    setField(el, 'meshFallbackHostKey', 'HK-fb-new');
    setPeerKey(el, 'dev-b', 'SHA256:peer-new');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].mesh.private_key).toContain('BEGIN OPENSSH PRIVATE KEY');
    expect(saved[0].mesh.key_passphrase).toBe('ph-new');
    expect(saved[0].mesh.fallback.host_key).toBe('HK-fb-new');
    expect(saved[0].mesh.peers[0].host_key).toBe('SHA256:peer-new');
    expect('host_key' in saved[0].mesh.peers[1]).toBe(false); // 未编辑的 peer 行不落键
    // 父层 patch 侧：改动值 ≠ current → 全部随 patch 带出（不被吞）
    const patch = buildCustomerSavePatch(saved[0], keyConfig());
    expect(patch.mesh.private_key).toContain('BEGIN OPENSSH PRIVATE KEY');
    expect(patch.mesh.key_passphrase).toBe('ph-new');
    expect(patch.mesh.fallback.host_key).toBe('HK-fb-new');
    expect(patch.mesh.peers[0].host_key).toBe('SHA256:peer-new');
  });

  it('清空 → save：卡片载荷省缺四键（G1），经 buildCustomerSavePatch 显式空串带出（G2 清空块激活）', async () => {
    const current = keyConfig();
    const { el, saved } = mountCard(current);
    await flush();
    setField(el, 'meshPrivateKey', '');
    setField(el, 'meshKeyPassphrase', '');
    setField(el, 'meshFallbackHostKey', '');
    setPeerKey(el, 'dev-b', '');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 卡片载荷：空串经归一化不落键（G1 省缺口径）
    expect('private_key' in saved[0].mesh).toBe(false);
    expect('key_passphrase' in saved[0].mesh).toBe(false);
    expect('host_key' in saved[0].mesh.fallback).toBe(false);
    expect('host_key' in saved[0].mesh.peers[0]).toBe(false);
    // G2 清空块：private_key/key_passphrase/fallback.host_key 显式空串；
    // peers 是数组走整体替换分支，清空经数组 diff 自然带出
    const patch = buildCustomerSavePatch(saved[0], current);
    expect(patch.mesh.private_key).toBe('');
    expect(patch.mesh.key_passphrase).toBe('');
    expect(patch.mesh.fallback.host_key).toBe('');
    expect(patch.mesh.peers).toEqual([
      { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true },
      { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
    ]);
  });

  it('未填私钥但有 peers（其余合法）：表单不强校验，save 照常提交（后端保存期才是权威）', async () => {
    const { el, saved } = mountCard(keyConfig());
    await flush();
    setField(el, 'meshPrivateKey', '');
    setField(el, 'meshFallbackHostKey', '');
    await flush();
    expect(saveButton(el).disabled).toBe(false);
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1); // 无表单层硬拒——错误回显走卡片 error prop
  });
});

// —— Task 3:SVG 拓扑画布（本机居中 + peers 自动布局;连线=节点对容器,
//    徽标=该 peer 的 mesh 规则条数;点击连线/节点 → 右栏详情;
//    拖拽位置 localStorage 持久,重挂载复原）——
const CANVAS_RULES = [
  'x.com,mesh:dev-b',
  'y.com,mesh:dev-b,direct',
  'z.com,mesh:dev-c',
  'dns',
  'geoip:cn,proxy'
];

function canvasConfig(meshOverrides = {}) {
  return {
    ...baseConfig(meshOverrides),
    proxy_rules: CANVAS_RULES,
    mesh: {
      enable: true,
      forward_enable: false,
      max_hops: 5,
      peers: [
        { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true },
        { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
      ],
      fallback: { enable: false, classes: [], addr: '' },
      ...meshOverrides
    }
  };
}

const canvasEl = (el) => el.querySelector('[data-mesh-canvas]');
const nodeIds = (el) => [...el.querySelectorAll('[data-mesh-node]')].map((n) => n.dataset.meshNode);
const nodeRect = (el, deviceId) => el.querySelector(`[data-mesh-node="${deviceId}"] rect`);
const edgeLine = (el, peerId) => el.querySelector(`[data-mesh-edge="${peerId}"]`);
const badgeOf = (el, peerId) => el.querySelector(`[data-mesh-badge="${peerId}"]`);

describe('MeshSettings 拓扑画布', () => {
  beforeEach(() => {
    window.localStorage.clear();
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
    window.localStorage.clear();
  });

  it('本机节点居中、peers 环形布局渲染（device_id 标签 + 本机 LAN 端口）', async () => {
    const { el } = mountCard(canvasConfig());
    await flush();
    const ids = nodeIds(el);
    expect(ids).toContain('dev-a'); // 本机（mesh status.device_id）
    expect(ids).toContain('dev-b');
    expect(ids).toContain('dev-c');
    // 本机居中：rect x = 320 - 60, y = 180 - 24
    expect(nodeRect(el, 'dev-a').getAttribute('x')).toBe('260');
    expect(nodeRect(el, 'dev-a').getAttribute('y')).toBe('156');
    // 本机额外显示 LAN 监听口
    expect(el.querySelector('[data-mesh-node="dev-a"]').textContent).toContain('dev-a');
    expect(el.querySelector('[data-mesh-node="dev-a"]').textContent).toContain(':56441');
    // peers 环绕：第一个 peer 在正上方（angle -π/2 → y = 180-110 = 70 → rect y 46）
    expect(nodeRect(el, 'dev-b').getAttribute('y')).toBe('46');
    // dev-c 为第二个 peer（angle π/2 → 正下方 y = 290 → rect y 266）
    expect(nodeRect(el, 'dev-c').getAttribute('y')).toBe('266');
  });

  it('连线徽标=该 peer 的 mesh 规则条数（策略段形式计入、非 mesh 条目不计入）', async () => {
    const { el } = mountCard(canvasConfig());
    await flush();
    // dev-b 两条（含 mesh:dev-b,direct 策略段形式）
    expect(badgeOf(el, 'dev-b').textContent.trim()).toBe('2');
    expect(badgeOf(el, 'dev-c').textContent.trim()).toBe('1');
    // 无规则 peer 不画连线（配置有该 peer 但规则数为 0）
    const lonely = canvasConfig({
      peers: [
        { device_id: 'dev-b', lan_addrs: [], relay: true },
        { device_id: 'dev-d', lan_addrs: [], relay: true }
      ]
    });
    const second = mountCard(lonely);
    await flush();
    expect(edgeLine(second.el, 'dev-b')).not.toBeNull();
    expect(edgeLine(second.el, 'dev-d')).toBeNull();
  });

  it('点击连线 → 右栏节点对详情（本机→peer + 规则 chips 原文保序）', async () => {
    const { el } = mountCard(canvasConfig());
    await flush();
    expect(el.querySelector('[data-mesh-detail]').textContent).toContain('mesh_detailPlaceholder');
    edgeLine(el, 'dev-b').dispatchEvent(new Event('click', { bubbles: true }));
    await flush();
    const detail = el.querySelector('[data-mesh-detail]');
    expect(detail.textContent).toContain('mesh_pairDetailTitle');
    expect(detail.textContent).toContain('dev-b');
    const chips = [...detail.querySelectorAll('[data-mesh-rule-chip]')].map((c) => c.textContent.trim());
    expect(chips).toEqual(['x.com,mesh:dev-b', 'y.com,mesh:dev-b,direct']);
  });

  it('连线命中区：14px 透明线承载点击（可见线 2px/可能轴向零包围盒，点不中）', async () => {
    const { el } = mountCard(canvasConfig());
    await flush();
    const hit = edgeLine(el, 'dev-b');
    // 命中线存在且足够粗（SVG 命中测试按描边宽度算,透明描边照样命中）
    expect(hit.getAttribute('stroke')).toBe('transparent');
    expect(hit.getAttribute('stroke-width')).toBe('14');
    // 可见线不抢事件,选中态只改可见线样式
    const visible = el.querySelector('[data-mesh-edge-line="dev-b"]');
    expect(visible.getAttribute('pointer-events')).toBe('none');
    expect(visible.getAttribute('stroke-width')).toBe('2');
    // 点命中线 → 节点对详情（与点徽标等价）
    hit.dispatchEvent(new Event('click', { bubbles: true }));
    await flush();
    expect(el.querySelector('[data-mesh-detail]').textContent).toContain('mesh_pairDetailTitle');
    expect(visible.getAttribute('class')).toContain('stroke-primary');
  });

  it('点击节点 → 右栏节点详情（规则 chips + 被引用节点对数）', async () => {
    const { el } = mountCard(canvasConfig());
    await flush();
    el.querySelector('[data-mesh-node="dev-c"]').dispatchEvent(new Event('click', { bubbles: true }));
    await flush();
    const detail = el.querySelector('[data-mesh-detail]');
    expect(detail.textContent).toContain('mesh_nodeDetailTitle');
    expect(detail.textContent).toContain('dev-c');
    expect(detail.textContent).toContain('mesh_dependedBy');
    const chips = [...detail.querySelectorAll('[data-mesh-rule-chip]')].map((c) => c.textContent.trim());
    expect(chips).toEqual(['z.com,mesh:dev-c']);
  });

  it('拖拽位置持久：setNodePosition 写 localStorage，重挂载复原（含连线端点跟随）', async () => {
    const first = mountCard(canvasConfig());
    await flush();
    first.card().setNodePosition('dev-b', 100, 300);
    await flush();
    // 位置已写入 localStorage
    const stored = JSON.parse(window.localStorage.getItem('meshNodePositions'));
    expect(stored['dev-b']).toEqual({ x: 100, y: 300 });
    // 连线端点跟随节点新位置
    const line = edgeLine(first.el, 'dev-b');
    expect(Number(line.getAttribute('x2'))).toBe(100);
    expect(Number(line.getAttribute('y2'))).toBe(300);
    expect(nodeRect(first.el, 'dev-b').getAttribute('x')).toBe('40'); // 100 - 60

    // 重新挂载（模拟刷新）：位置同源恢复
    const second = mountCard(canvasConfig());
    await flush();
    expect(nodeRect(second.el, 'dev-b').getAttribute('x')).toBe('40');
    expect(nodeRect(second.el, 'dev-b').getAttribute('y')).toBe('276'); // 300 - 24
  });

  it('位置超出画布被夹回可视区（含 NaN 兜底）', async () => {
    const { el, card } = mountCard(canvasConfig());
    await flush();
    card().setNodePosition('dev-b', -500, 9999);
    await flush();
    expect(nodeRect(el, 'dev-b').getAttribute('x')).toBe('4'); // 夹到 NODE_HALF_W+4 - 60
    expect(nodeRect(el, 'dev-b').getAttribute('y')).toBe('308'); // 夹到 332 - 24
    card().setNodePosition('dev-b', Number.NaN, Number.NaN);
    await flush();
    expect(Number.isFinite(Number(nodeRect(el, 'dev-b').getAttribute('x')))).toBe(true);
    expect(Number.isFinite(Number(nodeRect(el, 'dev-b').getAttribute('y')))).toBe(true);
  });

  it('未配置任何节点对 → 空态提示替代画布', async () => {
    // 先立桩再挂载：数据源空（无 peer/不转发/无兜底 = 未装配）
    stubApi({ meshStatus: { ...defaultMeshStatus(), peers: [] } });
    const { el } = mountCard({ ...baseConfig(), mesh: undefined, proxy_rules: [] });
    await flush();
    expect(el.querySelector('[data-mesh-canvas]')).toBeNull();
    const empty = el.querySelector('[data-mesh-canvas-empty]');
    expect(empty).not.toBeNull();
    expect(empty.textContent).toContain('mesh_unconfiguredHint');
  });

  it('mesh status 缺失（降级）仍渲染本机中心节点，label 用本机占位文案', async () => {
    stubApi({ meshStatusError: true });
    const { el } = mountCard(canvasConfig());
    await flush();
    expect(el.querySelector('[data-mesh-canvas]')).not.toBeNull();
    expect(nodeIds(el)).toContain('dev-b'); // 配置 peers 仍在
    const self = el.querySelector('[data-mesh-node="__self__"]');
    expect(self).not.toBeNull();
    expect(self.textContent).toContain('mesh_nodeSelf');
  });

  it('画布新键 zh/en 同步存在', () => {
    const keys = [
      'mesh_canvasTitle',
      'mesh_canvasHint',
      'mesh_unconfiguredHint',
      'mesh_pairDetailTitle',
      'mesh_nodeDetailTitle',
      'mesh_nodeSelf',
      'mesh_ruleChipsEmpty',
      'mesh_editInRules',
      'mesh_dependedBy',
      'mesh_detailPlaceholder'
    ];
    for (const key of keys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});

// —— G4/SPEC §10.3:依赖降级与身份不一致两条仪表盘提示——
// ① mesh 有效启用（enable!==false,后端三态 nil=true）而 ssh_ingress.enable
//    非 true → 依赖警告条 + 「一并开启」按钮;按钮只做一层确认（window.confirm
//    既有形态,RulesSettings clearKnownHosts 同款）后落意图,随下次整体保存
//    提交——不单独发请求、不静默改写。
// ② peers[].device_id 与 ssh_ingress.mesh_authorized_keys 尾注释比对不一致
//    → peer 行警告级提示（amber,非阻断）;全表无注释（对端可省）→ 不提示。

// —— parseAuthorizedKeyComment（尾注释解析,后端 parseSSHIngressMeshKeys 的
//    gossh.ParseAuthorizedKey Comment 前端同形启发）——
// 公钥体（base64）是锚点:各类型公钥线格式首四字节恒 00 00 00 xx → base64
// 恒 AAAA 开头;体前段=选项/算法名（天然排除,即 ssh-ed25519/AAAA 开头段
// 不是 comment）,体后整段=注释（含内部空格取整段其余部分）。
describe('MeshSettings parseAuthorizedKeyComment（G4 尾注释解析）', () => {
  it('常规形态:算法段+公钥体之后的段=注释', () => {
    expect(parseAuthorizedKeyComment('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8 dev-b')).toBe('dev-b');
    expect(parseAuthorizedKeyComment('ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQ dev-c')).toBe('dev-c');
  });

  it('注释含内部空格取整段其余部分', () => {
    expect(parseAuthorizedKeyComment('ssh-ed25519 AAAAxyz dev-b office laptop')).toBe('dev-b office laptop');
  });

  it('算法段与公钥体不进注释;缺公钥体=解析失败、缺注释=空串', () => {
    // 无 AAAA 锚点（坏行/仅算法段）→ 解析失败,空串不提示
    expect(parseAuthorizedKeyComment('ssh-ed25519')).toBe('');
    expect(parseAuthorizedKeyComment('# hand-written note')).toBe('');
    // 公钥体是最后一段 → 无注释（对端可省注释）
    expect(parseAuthorizedKeyComment('ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8')).toBe('');
  });

  it('authorized_keys 选项前缀在公钥体之前,不进注释', () => {
    expect(parseAuthorizedKeyComment('no-pty ssh-ed25519 AAAAC3Nza dev-b')).toBe('dev-b');
  });

  it('空白/非字符串行 → 空串（不提示）', () => {
    expect(parseAuthorizedKeyComment('')).toBe('');
    expect(parseAuthorizedKeyComment('   ')).toBe('');
    expect(parseAuthorizedKeyComment(null)).toBe('');
    expect(parseAuthorizedKeyComment(undefined)).toBe('');
    expect(parseAuthorizedKeyComment(42)).toBe('');
  });

  it('行首尾空白规整（后端 TrimSpace 同款）', () => {
    expect(parseAuthorizedKeyComment('  ssh-ed25519 AAAAxyz dev-b  ')).toBe('dev-b');
  });
});

// —— listMeshDeviceIdMismatches（peers[].device_id 与准入表尾注释比对）——
// 比对口径:表内存在注释==device_id 的行即一致;全表解析不出任何注释
// （对端可省注释/坏行）→ 无从比对,一律不提示。返回不一致 device_id（保序）。
describe('MeshSettings listMeshDeviceIdMismatches（G4 身份比对）', () => {
  const peers = [{ device_id: 'dev-b' }, { device_id: 'dev-c' }];

  it('注释==device_id 的 peer 不提示,缺席者提示（保序）', () => {
    expect(listMeshDeviceIdMismatches(peers, ['ssh-ed25519 AAAAx dev-b'])).toEqual(['dev-c']);
    expect(listMeshDeviceIdMismatches(peers, [
      'ssh-ed25519 AAAAx dev-b',
      'ssh-ed25519 AAAAy dev-c'
    ])).toEqual([]);
    expect(listMeshDeviceIdMismatches(peers, ['ssh-ed25519 AAAAy dev-c'])).toEqual(['dev-b']);
  });

  it('全表无注释（对端可省注释）→ 不提示任何 peer', () => {
    expect(listMeshDeviceIdMismatches(peers, ['ssh-ed25519 AAAAx'])).toEqual([]);
    expect(listMeshDeviceIdMismatches(peers, [])).toEqual([]);
    expect(listMeshDeviceIdMismatches(peers, undefined)).toEqual([]);
    expect(listMeshDeviceIdMismatches(peers, 'nope')).toEqual([]);
  });

  it('空白 device_id 的 peer 不参与比对;peers 非数组 → 空', () => {
    expect(listMeshDeviceIdMismatches([{ device_id: '  ' }, 'junk', null], ['ssh-ed25519 AAAAx dev-b'])).toEqual([]);
    expect(listMeshDeviceIdMismatches('nope', ['ssh-ed25519 AAAAx dev-b'])).toEqual([]);
  });
});

// —— 卡片挂载级:两处提示位条件渲染 + 「一并开启」确认链(save 载荷)——
describe('MeshSettings 依赖与身份提示（G4）', () => {
  // ssh_ingress 段（真实形态=SshIngressSettings normalizeSSHIngressConfig
  // 输出）;mesh_authorized_keys 尾注释默认只认 dev-b,dev-c 缺席。
  function dependencyConfig(sshOverrides = {}, meshOverrides = {}) {
    return {
      ...baseConfig(meshOverrides),
      ssh_ingress: {
        enable: false,
        port: 0,
        authorized_keys: [],
        mesh_authorized_keys: ['ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8 dev-b'],
        ...sshOverrides
      }
    };
  }

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

  it('mesh 启用 + ssh_ingress 未启用 → 依赖警告条（标题+文案+一并开启按钮）', async () => {
    const { el } = mountCard(dependencyConfig());
    await flush();
    const bar = el.querySelector('[data-mesh-ssh-dependency]');
    expect(bar).not.toBeNull();
    expect(bar.textContent).toContain('mesh_sshDependencyTitle');
    expect(bar.textContent).toContain('mesh_sshDependencyWarning');
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).not.toBeNull();
    expect(el.querySelector('[data-mesh-ssh-enable-together]').textContent).toContain('mesh_sshEnableTogether');
  });

  it('ssh_ingress.enable=true 或 mesh.enable=false → 无警告条', async () => {
    const on = mountCard(dependencyConfig({ enable: true }));
    await flush();
    expect(on.el.querySelector('[data-mesh-ssh-dependency]')).toBeNull();

    const meshOff = mountCard(dependencyConfig({}, { enable: false }));
    await flush();
    expect(meshOff.el.querySelector('[data-mesh-ssh-dependency]')).toBeNull();
  });

  it('mesh.enable/ssh_ingress 段缺省（后端三态 nil=true 口径）→ 仍出警告条', async () => {
    const cfg = baseConfig();
    delete cfg.mesh.enable; // 三态 nil=true:有效启用
    const { el } = mountCard(cfg);
    await flush();
    expect(el.querySelector('[data-mesh-ssh-dependency]')).not.toBeNull();
  });

  it('「一并开启」确认 → 落意图（按钮收敛、警告条保持到保存）,save 载荷带 ssh_ingress.enable=true 其余键透传', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    const { el, saved } = mountCard(dependencyConfig({ port: 56442 }));
    await flush();
    el.querySelector('[data-mesh-ssh-enable-together]').click();
    await flush();
    expect(confirmSpy).toHaveBeenCalledWith('mesh_sshEnableTogether');
    // 确认≠改写:配置面未动,警告条仍在;按钮收敛（意图已记,等保存）
    expect(el.querySelector('[data-mesh-ssh-dependency]')).not.toBeNull();
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).toBeNull();

    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress).toEqual({
      enable: true,
      port: 56442,
      authorized_keys: [],
      mesh_authorized_keys: ['ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8 dev-b']
    });
    // 不单独发请求:卡片只发 save 事件,POST 由父层整体保存承担（fetch stub
    // 只认三读端点,任何额外请求都会 reject——本用例无 rejection 即无单独请求）。
    // 父层递归 diff:仅 enable 进 patch（其余键未变被剔除）。
    const patch = buildCustomerSavePatch(saved[0], dependencyConfig({ port: 56442 }));
    expect(patch.ssh_ingress).toEqual({ enable: true });
  });

  it('「一并开启」取消 → 不落意图,save 载荷不翻转 ssh_ingress（不静默改写）', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(false);
    const { el, saved } = mountCard(dependencyConfig());
    await flush();
    el.querySelector('[data-mesh-ssh-enable-together]').click();
    await flush();
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    // 取消后按钮仍在（可再点）
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).not.toBeNull();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 整配置透传的 ssh_ingress 原样带出（enable 仍 false,未被翻转）,父层无 diff
    expect(saved[0].ssh_ingress.enable).toBe(false);
    expect(buildCustomerSavePatch(saved[0], dependencyConfig())?.ssh_ingress).toBeUndefined();
  });

  it('ssh_ingress 已启用时点确认路径不存在（警告条未渲染即无按钮）,save 载荷不额外带 ssh_ingress 覆盖', async () => {
    const { el, saved } = mountCard(dependencyConfig({ enable: true }));
    await flush();
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).toBeNull();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.enable).toBe(true);
  });

  it('device_id 与准入表尾注释不一致 → peer 行警告提示（amber）;一致行无提示;不阻断保存', async () => {
    const { el } = mountCard(dependencyConfig(
      { mesh_authorized_keys: ['ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8 dev-b'] },
      // baseConfig 默认仅 dev-b 一个 peer;双 peer 才能同时验证「一致行无提示」
      { peers: [
        { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true },
        { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
      ] }
    ));
    await flush();
    // dev-b 注释一致 → 无提示;dev-c 缺席 → 警告级提示（非阻断）
    expect(el.querySelector('[data-mesh-device-hint="dev-b"]')).toBeNull();
    const hint = el.querySelector('[data-mesh-device-hint="dev-c"]');
    expect(hint).not.toBeNull();
    expect(hint.textContent).toContain('mesh_deviceIdMismatchHint');
    expect(hint.className).toContain('amber');
    expect(saveButton(el).disabled).toBe(false);
  });

  it('准入表全无注释（对端可省）→ 任何 peer 行都不提示', async () => {
    const { el } = mountCard(dependencyConfig(
      { mesh_authorized_keys: ['ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIKx8'] },
      { peers: [
        { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true },
        { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
      ] }
    ));
    await flush();
    expect(el.querySelector('[data-mesh-device-hint="dev-b"]')).toBeNull();
    expect(el.querySelector('[data-mesh-device-hint="dev-c"]')).toBeNull();
  });

  it('准入表为空/整段缺失 → 不提示（无从比对）', async () => {
    const empty = mountCard(dependencyConfig({ mesh_authorized_keys: [] }));
    await flush();
    expect(empty.el.querySelector('[data-mesh-device-hint]')).toBeNull();

    const missing = mountCard({ ...baseConfig() }); // 无 ssh_ingress 段
    await flush();
    expect(missing.el.querySelector('[data-mesh-device-hint]')).toBeNull();
  });

  // I1 跨回合安全:意图标志只置位不清零的话,「一并开启→保存→用户经 SSH 卡
  // 关掉 ssh→回 mesh 卡」后,陈旧标志经 save 守卫（enable!==true）会静默把
  // ssh_ingress.enable=true 带进 patch,复活一个已关闭的 LAN 暴露面;同时
  // 按钮被陈旧标志隐藏(警告条在而按钮不在的 UI 不一致)。配置回读到 ssh
  // 已开即清零——可观测面=ssh 再被关掉后按钮恢复 + save 载荷不翻转。
  it('I1:配置回读 ssh 已开 → 意图清零;ssh 再被关掉后按钮恢复、保存不静默翻转', async () => {
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    // reactive 包裹:生产中父层 customerConfig 是 data() 深响应代理,config
    // 深度变更(保存回读/SSH 卡改写)经代理触发卡片 deep watch;普通对象
    // prop 无代理,watch 不触发(测试基座须与生产同构)。
    const config = reactive(dependencyConfig());
    const { el, saved } = mountCard(config);
    await flush();
    // 一并开启 → 意图落位(按钮收敛)
    el.querySelector('[data-mesh-ssh-enable-together]').click();
    await flush();
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).toBeNull();
    // 保存生效后父层回读:ssh_ingress.enable=true(deep watch 同对象触发)
    config.ssh_ingress.enable = true;
    await flush();
    // 用户经 SSH 卡片关掉 ssh → 配置面回 false,回 mesh 卡
    config.ssh_ingress.enable = false;
    await flush();
    // 警告条回归且按钮可见(陈旧标志不得隐藏按钮)——修复前此处按钮缺失
    expect(el.querySelector('[data-mesh-ssh-dependency]')).not.toBeNull();
    expect(el.querySelector('[data-mesh-ssh-enable-together]')).not.toBeNull();
    // 任意 mesh 编辑保存:载荷不静默翻转 ssh_ingress(不复活 LAN 暴露面)
    setToggle(el, 'meshFallbackEnable', true);
    setField(el, 'meshFallbackAddr', '192.168.1.20:56441');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.enable).toBe(false);
    // 父层 patch:ssh_ingress 无 diff
    expect(buildCustomerSavePatch(saved[0], config)?.ssh_ingress).toBeUndefined();
  });
});
