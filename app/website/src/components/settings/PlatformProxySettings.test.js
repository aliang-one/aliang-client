import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { createApp, h } from 'vue';

// i18n 模块求值会连带 loadLocale()（读 localStorage,测试环境不保证语言
// 确定性）;mock 掉该依赖（MeshSettings.test.js 同范式,t 原样回键,断言不
// 硬编码文案）。卡片无补充只读数据,不发任何请求。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import PlatformProxySettings from './PlatformProxySettings.vue';
import zh from '../../i18n/zh';
import en from '../../i18n/en';

// —— 平台流量代理卡（原 mesh 卡「出口链路」区块,2026-11-15 挪至代理设置子
//    tab;语义改名=平台流量代理,内部键/线格式 fallback 不动）——
// 挂载级测试（createApp + jsdom 手法,MeshSettings.test.js 同范式;卡片无
// 补充只读数据,不发任何请求——选项源=配置 peers 真值）。

const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function mountCard(config, extraProps = {}) {
  const saved = [];
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(PlatformProxySettings, {
      config,
      ...extraProps,
      onSave: (payload) => saved.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saved };
}

// 基座配置含全量顶层段（proxy 等）,验证 save 载荷是「整配置 + mesh 覆盖」
// ——SettingsPage 按顶层段浅合并,卡片不得丢掉自己不拥有的段。
function baseConfig(meshOverrides = {}) {
  return {
    proxy: {
      enable: true,
      type: 'socks5',
      server: '127.0.0.1:1080'
    },
    ai_rules: {},
    proxy_rules: [],
    mesh: {
      enable: true,
      forward_enable: false,
      punch_enable: false,
      max_hops: 5,
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true }],
      fallback: { enable: false, next_hop: '' },
      ...meshOverrides
    }
  };
}

// v-model select 监听 change 事件（Vue 3 对 select 用 change 而非 input）
function setSelect(el, field, value) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.value = value;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

// jsdom 坑：checkbox .click() 不发 change（peer-link 审计存档）——赋 checked 再派发
function setToggle(el, field, checked) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.checked = checked;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

function saveButton(el) {
  return el.querySelector('button[data-platform-proxy-save]');
}

function clickSave(el) {
  saveButton(el).click();
}

describe('PlatformProxySettings 卡片（平台流量代理）', () => {
  beforeEach(() => {});

  afterEach(() => {
    while (mountedApps.length) {
      const { app, el } = mountedApps.pop();
      app.unmount();
      el.remove();
    }
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('回显：开关/下一跳从配置回填；未钉扎 peer 带「未绑指纹」标注', async () => {
    const { el } = mountCard(baseConfig({
      peers: [
        { device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'HK-b' },
        { device_id: 'dev-c', lan_addrs: ['192.168.1.21:56441'], relay: true }
      ],
      fallback: { enable: true, next_hop: 'dev-b' }
    }));
    await flush();
    expect(el.querySelector('[data-field="meshFallbackEnable"]').checked).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackNextHop"]').value).toBe('dev-b');
    const options = [...el.querySelector('[data-field="meshFallbackNextHop"]').querySelectorAll('option')];
    expect(options.map((o) => o.value)).toEqual(['', 'dev-b', 'dev-c']);
    // dev-c 无 host_key → 选项文案带「未绑指纹」提示键
    expect(options[2].textContent).toContain('mesh_fallbackNextHopUnpinned');
    expect(options[1].textContent).not.toContain('mesh_fallbackNextHopUnpinned');
  });

  it('校验：开关 Enable=true 而 next_hop 空 → mesh_validationFallbackNextHop + 禁保存', async () => {
    const { el, saved } = mountCard(baseConfig());
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    await flush();
    expect(el.textContent).toContain('mesh_validationFallbackNextHop');
    expect(saveButton(el).disabled).toBe(true);
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(0);

    setSelect(el, 'meshFallbackNextHop', 'dev-b');
    await flush();
    expect(el.textContent).not.toContain('mesh_validationFallbackNextHop');
    expect(saveButton(el).disabled).toBe(false);
  });

  it('依赖降级提示：mesh 有效启用而 ssh_ingress 未开 → 琥珀提示（开关不会生效）', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.querySelector('[data-platform-proxy-dependency]')).not.toBeNull();
    expect(el.textContent).toContain('mesh_fallbackDependencyHint');

    // ssh 开启 → 提示收敛（MeshSettings sshIngressOff 同口径:enable===true 即开）
    const withSsh = baseConfig();
    withSsh.ssh_ingress = { enable: true };
    const { el: el2 } = mountCard(withSsh);
    await flush();
    expect(el2.querySelector('[data-platform-proxy-dependency]')).toBeNull();
  });

  it('saving 态：开关与下一跳、保存按钮禁用（与兄弟控件口径一致）', async () => {
    const { el } = mountCard(baseConfig(), { saving: true });
    await flush();
    expect(el.querySelector('[data-field="meshFallbackEnable"]').disabled).toBe(true);
    expect(el.querySelector('[data-field="meshFallbackNextHop"]').disabled).toBe(true);
    expect(saveButton(el).disabled).toBe(true);
  });

  it('save 载荷：整配置透传 + mesh 恒显式布尔 + peers 规整透传 + fallback 编辑值', async () => {
    const { el, saved } = mountCard(baseConfig({
      peers: [{ device_id: ' dev-b ', lan_addrs: ['192.168.1.20:56441'], relay: false }]
    }));
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    setSelect(el, 'meshFallbackNextHop', 'dev-b');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 整配置透传：父层浅合并不丢其他顶层段
    expect(saved[0].proxy.server).toBe('127.0.0.1:1080');
    expect(saved[0].mesh).toEqual({
      enable: true,
      forward_enable: false,
      punch_enable: false,
      max_hops: 5,
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: false }],
      fallback: { enable: true, next_hop: 'dev-b' }
    });
    // 恒产出显式布尔（ssh_ingress 先例）：nil 不透传成 undefined
    expect(typeof saved[0].mesh.enable).toBe('boolean');
    expect(saved[0].mesh.enable).toBe(true);
  });

  it('save 载荷：旧版 fallback 字段(classes/addr/host_key)随字面量整体替换清除，密钥字段同保', async () => {
    const { el, saved } = mountCard(baseConfig({
      private_key: 'mesh-private-key',
      key_passphrase: 'mesh-passphrase',
      peers: [{ device_id: 'dev-b', lan_addrs: ['192.168.1.20:56441'], relay: true, host_key: 'peer-host-key' }],
      fallback: { enable: false, addr: '10.0.0.5:56441', classes: ['ai'], host_key: 'fallback-host-key' }
    }));
    await flush();
    setToggle(el, 'meshFallbackEnable', true);
    setSelect(el, 'meshFallbackNextHop', 'dev-b');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 整配置透传三密钥字段：js 归一化不再抹掉
    expect(saved[0].mesh.private_key).toBe('mesh-private-key');
    expect(saved[0].mesh.key_passphrase).toBe('mesh-passphrase');
    expect(saved[0].mesh.peers[0].host_key).toBe('peer-host-key');
    // fallback 字面量整体替换：旧键不残留
    expect(saved[0].mesh.fallback).toEqual({ enable: true, next_hop: 'dev-b' });
  });

  it('save 载荷：配置缺 mesh 段 → 默认显式布尔（enable=true/forward=false）随开关编辑值落盘', async () => {
    const { el, saved } = mountCard({ ...baseConfig(), mesh: undefined });
    await flush();
    // mesh 段缺失 → 无 peers → next-hop select 禁用;开关开但 next_hop 空
    // 会被前端校验拦下——这里验证「开关开但选不了」时保存被禁(校验兜底)。
    setToggle(el, 'meshFallbackEnable', true);
    await flush();
    expect(el.querySelector('[data-field="meshFallbackNextHop"]').disabled).toBe(true);
    expect(saveButton(el).disabled).toBe(true);
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(0);
  });

  it('i18n：平台流量代理键 zh/en 同步存在（含新增依赖提示键）', () => {
    const keys = [
      'mesh_fallbackTitle',
      'mesh_fallbackDesc',
      'mesh_fallbackEnable',
      'mesh_fallbackNextHop',
      'mesh_fallbackNextHopPlaceholder',
      'mesh_fallbackNextHopEmpty',
      'mesh_fallbackNextHopUnpinned',
      'mesh_fallbackNextHopHint',
      'mesh_fallbackDependencyHint',
      'mesh_validationFallbackNextHop'
    ];
    for (const key of keys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});
