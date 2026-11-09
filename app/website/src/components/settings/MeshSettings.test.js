import { afterEach, describe, expect, it, vi } from 'vitest';

// SettingsPage.vue → i18n 模块求值会连带 loadLocale()（读 localStorage，
// 测试环境不保证语言确定性）；mock 掉该依赖（SshIngressSettings.test.js 同
// 范式，t 原样回键，断言不硬编码文案）。卡片补充只读数据走全局 fetch，
// vi.stubGlobal 注入；onSave prop 捕获 'save' 事件载荷。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { normalizeMeshConfig, parseMeshRuleTargetCount, MESH_FALLBACK_CLASSES } from './MeshSettings.js';
import { buildCustomerSavePatch } from '../SettingsPage.vue';
import { getMeshLanPeers, getMeshStatus } from '../../services/agentApi';

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

  it('max_hops：[1,8] 保留、非法（≤0 或 >8 或非数）归 0（后端默认 5）', () => {
    expect(normalizeMeshConfig({ max_hops: 5 }).max_hops).toBe(5);
    expect(normalizeMeshConfig({ max_hops: 1 }).max_hops).toBe(1);
    expect(normalizeMeshConfig({ max_hops: 8 }).max_hops).toBe(8);
    expect(normalizeMeshConfig({ max_hops: 0 }).max_hops).toBe(0);
    expect(normalizeMeshConfig({ max_hops: -3 }).max_hops).toBe(0);
    expect(normalizeMeshConfig({ max_hops: 9 }).max_hops).toBe(0);
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
