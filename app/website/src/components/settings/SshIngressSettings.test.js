import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// SshIngressSettings.vue 模块求值会连带执行 i18n 的 loadLocale()（读 localStorage，
// node 测试环境无 DOM）；mock 掉该依赖即可导入 SFC（RulesSettings.test.js 同范式，
// t 原样回键，断言不硬编码文案）。指纹区块走全局 fetch，vi.stubGlobal 注入；
// onSave prop 捕获 'save' 事件载荷。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h, reactive } from 'vue';
import SshIngressSettings, { normalizeSSHIngressConfig } from './SshIngressSettings.vue';
import { buildCustomerSavePatch } from '../SettingsPage.vue';
import en from '../../i18n/en';
import zh from '../../i18n/zh';

// —— normalizeSSHIngressConfig（线格式归一；SettingsPage 归一化链共用）——
// 线格式 customer.ssh_ingress{enable,port,authorized_keys[],mesh_authorized_keys[]}；
// 后端 enable 是三态指针 nil=false，port 0=跟随统一口 56432（56442 旧默认
// 哨兵由显示层映射为空、保存发 0）。
describe('SshIngressSettings normalizeSSHIngressConfig', () => {
  it('produces wire-format defaults for a missing section', () => {
    expect(normalizeSSHIngressConfig(undefined)).toEqual({
      enable: false,
      port: 0,
      authorized_keys: [],
      mesh_authorized_keys: []
    });
  });

  it('keeps valid values and normalizes the two key lists', () => {
    expect(normalizeSSHIngressConfig({
      enable: true,
      port: 56442,
      authorized_keys: ['ssh-ed25519 AAA key1', '  ', 'ssh-ed25519 BBB key2'],
      mesh_authorized_keys: 'not-a-list'
    })).toEqual({
      enable: true,
      port: 56442,
      authorized_keys: ['ssh-ed25519 AAA key1', 'ssh-ed25519 BBB key2'],
      mesh_authorized_keys: []
    });
  });

  it('maps non-boolean enable, non-number/outrange port and non-object payloads to defaults', () => {
    expect(normalizeSSHIngressConfig({ enable: 'yes', port: '56442' })).toEqual({
      enable: false,
      port: 0,
      authorized_keys: [],
      mesh_authorized_keys: []
    });
    // uint16 上界：越界端口读侧归 0（=用默认），写侧由卡片的 port 校验拦截
    expect(normalizeSSHIngressConfig({ enable: true, port: 65536 }).port).toBe(0);
    expect(normalizeSSHIngressConfig(null)).toEqual(normalizeSSHIngressConfig(undefined));
    expect(normalizeSSHIngressConfig(['nope'])).toEqual(normalizeSSHIngressConfig(undefined));
  });
});

// —— SettingsPage 归一化链（buildCustomerSavePatch：卡片载荷→提交 patch）——
// 红线①：RulesSettings 只发 proxy/ai_rules/proxy_rules 三段——父层合并兜底必须
// 保证其余段（含 ssh_ingress 与存量 model_mapping）不进置零 patch。
// 红线②：卡片四字段 diff 精确（未变字段不进 patch;数组清空要显式带出）。
describe('SettingsPage buildCustomerSavePatch（ssh_ingress 透传链）', () => {
  it('RulesSettings 三段载荷不得把 ssh_ingress 归零（父层合并兜底）', () => {
    const current = {
      proxy: { enable: true, type: 'socks5', server: 'old:1080' },
      model_mapping: { enable: true, rules: { 'gpt-4': 'glm-4' } },
      ssh_ingress: { enable: true, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [MESH_KEY] }
    };
    const patch = buildCustomerSavePatch(
      { proxy: { enable: true, type: 'ssh', server: 'new:22' }, ai_rules: {}, proxy_rules: [] },
      current
    );
    expect(patch.ssh_ingress).toBeUndefined();
    // 同类存量潜伏缺陷（保存代理设置会关掉模型映射）一并由父层收口
    expect(patch.model_mapping).toBeUndefined();
    expect(patch.proxy.server).toBe('new:22');
    expect(patch.proxy.type).toBe('ssh');
  });

  it('SSH 入口卡片载荷产出四字段 diff：启用带钥匙、关闭只发 enable:false、删空显式带 []', () => {
    const disabled = { ssh_ingress: { enable: false, port: 0, authorized_keys: [], mesh_authorized_keys: [] } };
    const enabled = buildCustomerSavePatch(
      { ssh_ingress: { enable: true, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [] } },
      disabled
    );
    // mesh 空对空不进 patch（deep-merge 保留后端既有值）
    expect(enabled.ssh_ingress).toEqual({ enable: true, port: 56442, authorized_keys: [HUMAN_KEY] });

    // 关闭开关：仅 enable diff（port/keys 未变,照发也不产生 patch 键）
    const off = buildCustomerSavePatch(
      { ssh_ingress: { enable: false, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [] } },
      { ssh_ingress: { enable: true, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [] } }
    );
    expect(off.ssh_ingress).toEqual({ enable: false });

    // 关闭并删空人用钥匙：数组置零显式带出（buildConfigPatch 对数组自然 diff）
    const cleared = buildCustomerSavePatch(
      { ssh_ingress: { enable: false, port: 56442, authorized_keys: [], mesh_authorized_keys: [] } },
      { ssh_ingress: { enable: true, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [] } }
    );
    expect(cleared.ssh_ingress).toEqual({ enable: false, authorized_keys: [] });
  });

  it('GET 回显值原样透传不产生伪 diff（无改动 → 无 patch,不发 POST）', () => {
    const current = {
      ssh_ingress: { enable: true, port: 56442, authorized_keys: [HUMAN_KEY], mesh_authorized_keys: [MESH_KEY] }
    };
    const same = buildCustomerSavePatch(
      { ...current, ssh_ingress: { ...current.ssh_ingress } },
      current
    );
    expect(same).toBeUndefined();
  });
});

// —— 挂载级测试（createApp + jsdom 手法，RulesSettings.test.js 同范式）——
const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function mountCard(config) {
  const saved = [];
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(SshIngressSettings, {
      config,
      onSave: (payload) => saved.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saved };
}

// 基座配置含全量顶层段（proxy 等），验证卡片 save 载荷是「整配置 + ssh_ingress
// 覆盖」——SettingsPage 按顶层段浅合并，卡片不得丢掉自己不拥有的段。
function baseConfig(sshOverrides = {}) {
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
    ssh_ingress: {
      enable: false,
      port: 0,
      authorized_keys: [],
      mesh_authorized_keys: [],
      ...sshOverrides
    }
  };
}

const HUMAN_KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHUMAN human@laptop';
const MESH_KEY = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMESH mesh-node';

// —— 一键生成并启用（spec 2026-11-12 §2）——
// mountLive 与 mountCard 同手法，但 error/successMessage 走逐键读 reactive——
// render 闭包内读 props 对象的属性才是响应写法（展开进 h() 是快照）。
const HUMAN_KEY2 = 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHUMAN2 human@pc';
const keygenEnvelope = (key) => ({ code: 0, msg: '', data: { key } });
const keygenPayload = () => ({
  public_key: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIONECLICK aliang-ingress-node-20261112120000',
  private_key: '-----BEGIN OPENSSH PRIVATE KEY-----\nFAKE\n-----END OPENSSH PRIVATE KEY-----\n',
  fingerprint: 'SHA256:oneclickfingerprint0000000000000000000',
  algorithm: 'ssh-ed25519',
  comment: 'aliang-ingress-node-20261112120000',
  suggested_filename: 'aliang-ingress-node-20261112.pem'
});

// fetch 分流：GET hostkey 走默认信封，POST keygen 走注入的响应
function stubFetchRouted(keygenImpl) {
  vi.stubGlobal('fetch', vi.fn((url, opts) => {
    if ((opts?.method || 'GET') === 'POST' && String(url).includes('/ssh-ingress/keygen')) {
      return Promise.resolve(keygenImpl());
    }
    return Promise.resolve(jsonResponse(hostkeyEnvelope(hostkeyPayload())));
  }));
}

function setField(el, field, value) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.value = value;
  node.dispatchEvent(new Event('input', { bubbles: true }));
}

// jsdom 坑：checkbox .click() 不发 change（peer-link 审计存档）——直接赋 checked 再派发
function setToggle(el, field, checked) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.checked = checked;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

// jsdom/nwsapi 坑：同 document 多 app 并存时,#id 作用域查询会命中全局首个
// 同名元素（在子树外→null）——一律用 data 属性选择器（按钮保留 id 供 e2e）。
function saveButton(el) {
  return el.querySelector('button[data-ssh-save]');
}

function clickSave(el) {
  saveButton(el).click();
}

const jsonResponse = (payload, ok = true, status) => ({
  ok,
  status: status ?? (ok ? 200 : 500),
  json: async () => payload
});

const hostkeyEnvelope = (hostkey) => ({ code: 0, msg: '', data: { hostkey } });
const hostkeyPayload = () => ({
  fingerprint: 'SHA256:0123456789abcdefghijklmnopqrstuv',
  algorithm: 'ssh-ed25519',
  generated_at: '2026-11-08T01:02:03Z'
});

describe('SshIngressSettings 卡片', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(hostkeyEnvelope(hostkeyPayload()))));
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

  it('挂载拉取指纹信封并展示指纹与算法；开关默认关、端口输入为空（0=默认）', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/proxy/ssh-ingress/hostkey', expect.anything());
    // 读 data 信封：json.data.hostkey.{fingerprint,algorithm,generated_at}
    expect(el.querySelector('[data-ssh-hostkey]').textContent).toContain('SHA256:0123456789abcdefghijklmnopqrstuv');
    expect(el.querySelector('[data-ssh-hostkey]').textContent).toContain('ssh-ed25519');
    expect(el.querySelector('[data-ssh-hostkey]').textContent).toContain('2026-11-08T01:02:03Z');
    expect(el.querySelector('[data-field="sshIngressEnable"]').checked).toBe(false);
    // 端口输入回归:未配(port=0)显示空 + 留空占位提示
    expect(el.querySelector('input[data-field="sshIngressPort"]')).toBeTruthy();
    expect(el.querySelector('[data-field="sshIngressPort"]').value).toBe('');
  });

  it('复制按钮写入剪贴板并切换反馈文案', async () => {
    const writeText = vi.fn().mockResolvedValue();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    const { el } = mountCard(baseConfig());
    await flush();
    const copyBtn = el.querySelector('[data-ssh-hostkey-copy]');
    expect(copyBtn).toBeTruthy();
    expect(copyBtn.textContent).toContain('sshIngress_hostkeyCopy');
    copyBtn.click();
    await flush();
    expect(writeText).toHaveBeenCalledWith('SHA256:0123456789abcdefghijklmnopqrstuv');
    expect(el.querySelector('[data-ssh-hostkey-copy]').textContent).toContain('sshIngress_hostkeyCopied');
  });

  it('指纹缺失（404=入口未启动）：显示「启用并保存后自动生成」引导文案', async () => {
    globalThis.fetch.mockResolvedValue(jsonResponse({ code: 100, msg: 'SSH 入口尚未启动' }, false, 404));
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.textContent).toContain('sshIngress_hostkeyNotStarted');
    expect(el.textContent).not.toContain('sshIngress_hostkeyLoadFailed');
  });

  it('保存成功后重拉指纹：引导文案当场过渡为真指纹（host key 由 Server.Start 生成）', async () => {
    globalThis.fetch.mockResolvedValueOnce(jsonResponse({ code: 100, msg: 'SSH 入口尚未启动' }, false, 404));
    const state = reactive({ success: '' });
    const saved = [];
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({
      render: () => h(SshIngressSettings, {
        config: baseConfig(),
        successMessage: state.success,
        onSave: (payload) => saved.push(payload)
      })
    });
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();
    expect(el.textContent).toContain('sshIngress_hostkeyNotStarted');

    // 保存成功（父页 success 消息置位）→ 重拉命中真指纹
    globalThis.fetch.mockResolvedValueOnce(jsonResponse(hostkeyEnvelope(hostkeyPayload())));
    state.success = '配置已保存';
    await flush();
    expect(el.textContent).not.toContain('sshIngress_hostkeyNotStarted');
    expect(el.querySelector('[data-ssh-hostkey]').textContent).toContain('SHA256:0123456789abcdefghijklmnopqrstuv');
  });

  it('指纹加载失败静默降级 amber 文案，不阻塞主表单保存', async () => {
    globalThis.fetch.mockRejectedValue(new Error('network down'));
    const { el, saved } = mountCard(baseConfig({ authorized_keys: [HUMAN_KEY] }));
    await flush();
    expect(el.textContent).toContain('sshIngress_hostkeyLoadFailed');
    expect(el.textContent).not.toContain('sshIngress_hostkeyNotStarted');
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
  });

  it('人用公钥粘贴逐行添加（空行剔除、与既有键去重）与删除', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    setField(el, 'sshIngressKeyDraft', `${HUMAN_KEY}\n\n${MESH_KEY}\n${HUMAN_KEY}`);
    el.querySelector('[data-ssh-key-add]').click();
    await flush();
    const rows = el.querySelectorAll('[data-ssh-key-row]');
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain(HUMAN_KEY);
    expect(rows[1].textContent).toContain(MESH_KEY);
    // 草稿清空
    expect(el.querySelector('[data-field="sshIngressKeyDraft"]').value).toBe('');

    // 删除第一行
    rows[0].querySelector('[data-ssh-key-remove]').click();
    await flush();
    const remaining = el.querySelectorAll('[data-ssh-key-row]');
    expect(remaining).toHaveLength(1);
    expect(remaining[0].textContent).not.toContain(HUMAN_KEY);
    expect(remaining[0].textContent).toContain(MESH_KEY);
  });

  it('校验：启用且两列表合计为空时拦截（文案+按钮禁用）；mesh 列表有钥匙即放行', async () => {
    const blocked = mountCard(baseConfig());
    await flush();
    setToggle(blocked.el, 'sshIngressEnable', true);
    await flush();
    expect(blocked.el.textContent).toContain('sshIngress_validationNeedKey');
    expect(saveButton(blocked.el).disabled).toBe(true);
    clickSave(blocked.el);
    await flush();
    expect(blocked.saved).toHaveLength(0);

    // mesh 有钥匙（人用为空）→ 合计非空，与后端 Validate「至少一把钥匙」对齐
    const meshOnly = mountCard(baseConfig({ enable: true, mesh_authorized_keys: [MESH_KEY] }));
    await flush();
    expect(meshOnly.el.textContent).not.toContain('sshIngress_validationNeedKey');
    clickSave(meshOnly.el);
    await flush();
    expect(meshOnly.saved).toHaveLength(1);
    expect(meshOnly.saved[0].ssh_ingress.mesh_authorized_keys).toEqual([MESH_KEY]);
    expect(meshOnly.saved[0].ssh_ingress.authorized_keys).toEqual([]);
  });

  it('端口非法值拦截保存（1-65535 之外）', async () => {
    const { el, saved } = mountCard(baseConfig({ enable: true, authorized_keys: [HUMAN_KEY] }));
    await flush();
    setField(el, 'sshIngressPort', '70000');
    await flush();
    expect(el.textContent).toContain('sshIngress_portInvalid');
    expect(saveButton(el).disabled).toBe(true);
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(0);
  });

  it('提交载荷：整配置透传 + ssh_ingress 四字段；端口空发 0（跟随统一口）、显式值原样', async () => {
    const { el, saved } = mountCard(baseConfig());
    await flush();
    setToggle(el, 'sshIngressEnable', true);
    setField(el, 'sshIngressPort', '56443');
    setField(el, 'sshIngressKeyDraft', HUMAN_KEY);
    el.querySelector('[data-ssh-key-add]').click();
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    // 卡片载荷是「整配置 + ssh_ingress 覆盖」：父层浅合并不丢其他顶层段
    expect(saved[0].proxy.server).toBe('127.0.0.1:1080');
    expect(saved[0].ssh_ingress).toEqual({
      enable: true,
      port: 56443,
      authorized_keys: [HUMAN_KEY],
      mesh_authorized_keys: []
    });

    // 端口清空 → 0(=跟随统一口)
    setField(el, 'sshIngressPort', '');
    await flush();
    clickSave(el);
    await flush();
    expect(saved[1].ssh_ingress.port).toBe(0);
  });

  it('体默认折叠(v-show 隐藏),点头部按钮展开后可操作', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    const body = el.querySelector('[data-ssh-ingress-body]');
    expect(body).toBeTruthy();
    expect(body.style.display).toBe('none'); // 默认收起,低频功能不占版面
    expect(el.querySelector('[data-field="sshIngressExpand"]').getAttribute('aria-expanded')).toBe('false');

    el.querySelector('[data-field="sshIngressExpand"]').click();
    await flush();
    expect(body.style.display).not.toBe('none');
    expect(el.querySelector('[data-field="sshIngressExpand"]').getAttribute('aria-expanded')).toBe('true');
    // 展开后输入框可正常交互(展开即用,无二次门槛)
    setField(el, 'sshIngressPort', '56443');
    expect(el.querySelector('[data-field="sshIngressPort"]').value).toBe('56443');
  });

  it('哨兵：配置 port=56442(旧默认)显示为空,保存发 0(翻译层归零=跟随统一口)', async () => {
    const { el, saved } = mountCard(baseConfig({ enable: true, authorized_keys: [HUMAN_KEY], port: 56442 }));
    await flush();
    expect(el.querySelector('[data-field="sshIngressPort"]').value).toBe('');
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.port).toBe(0);
  });

  it('置零：关闭开关载荷 enable:false 且其余字段照发（enable=false 时后端跳过钥匙校验）', async () => {
    const { el, saved } = mountCard(baseConfig({
      enable: true,
      port: 56442,
      authorized_keys: [HUMAN_KEY],
      mesh_authorized_keys: [MESH_KEY]
    }));
    await flush();
    setToggle(el, 'sshIngressEnable', false);
    setField(el, 'sshIngressPort', '');
    await flush();
    clickSave(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress).toEqual({
      enable: false,
      // 端口输入已清空 → 发 0(=跟随统一口)
      port: 0,
      authorized_keys: [HUMAN_KEY],
      mesh_authorized_keys: [MESH_KEY]
    });
  });

  it('回显：开关/端口/人用公钥/mesh 公钥从配置回填；mesh 行只读无删除钮', async () => {
    const { el } = mountCard(baseConfig({
      enable: true,
      port: 56442,
      authorized_keys: [HUMAN_KEY],
      mesh_authorized_keys: [MESH_KEY]
    }));
    await flush();
    expect(el.querySelector('[data-field="sshIngressEnable"]').checked).toBe(true);
    // 56442 是旧默认哨兵:显示为空(保存发 0=跟随统一口),不回显数字
    expect(el.querySelector('[data-field="sshIngressPort"]').value).toBe('');
    const humanRows = el.querySelectorAll('[data-ssh-key-row]');
    expect(humanRows).toHaveLength(1);
    expect(humanRows[0].textContent).toContain(HUMAN_KEY);
    expect(humanRows[0].querySelector('[data-ssh-key-remove]')).toBeTruthy();

    const meshRows = el.querySelectorAll('[data-ssh-mesh-row]');
    expect(meshRows).toHaveLength(1);
    expect(meshRows[0].textContent).toContain(MESH_KEY);
    // mesh 互连公钥只读（编辑入口归组网页）：行内没有任何删除按钮
    expect(meshRows[0].querySelector('[data-ssh-key-remove]')).toBeNull();
    expect(meshRows[0].querySelector('button')).toBeNull();
  });

  it('i18n：SSH 入口新键 zh/en 同步存在', () => {
    const sshIngressKeys = [
      'sshIngress_title',
      'sshIngress_toggle',
      'sshIngress_description',
      'sshIngress_enable',
      'sshIngress_port',
      'sshIngress_portPlaceholder',
      'sshIngress_portInvalid',
      'sshIngress_portHint',
      'sshIngress_hostkeyTitle',
      'sshIngress_hostkeyDesc',
      'sshIngress_hostkeyNotStarted',
      'sshIngress_hostkeyLoadFailed',
      'sshIngress_hostkeyCopy',
      'sshIngress_hostkeyCopied',
      'sshIngress_loading',
      'sshIngress_authorizedKeysTitle',
      'sshIngress_authorizedKeysDesc',
      'sshIngress_authorizedKeysPlaceholder',
      'sshIngress_authorizedKeysAdd',
      'sshIngress_authorizedKeysEmpty',
      'sshIngress_authorizedKeysRemove',
      'sshIngress_meshKeysTitle',
      'sshIngress_meshKeysDesc',
      'sshIngress_meshKeysEmpty',
      'sshIngress_bindHostHint',
      'sshIngress_validationNeedKey',
      'sshIngress_save',
      'sshIngress_saving',
      'sshIngress_oneClick',
      'sshIngress_privKeyTitle',
      'sshIngress_privKeyOnceWarning',
      'sshIngress_privKeyDownload',
      'sshIngress_privKeyCopy',
      'sshIngress_privKeyCopied',
      'sshIngress_privKeyConnectExample',
      'sshIngress_keygenFailed',
      'sshIngress_privKeyClose'
    ];
    for (const key of sshIngressKeys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});

describe('SshIngressSettings 一键生成并启用', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(hostkeyEnvelope(hostkeyPayload()))));
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

  function mountLive(initialConfig) {
    const props = reactive({ config: initialConfig, error: '', successMessage: '' });
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({
      render: () => h(SshIngressSettings, {
        config: props.config,
        error: props.error,
        successMessage: props.successMessage,
        onSave: () => {}
      })
    });
    app.mount(el);
    mountedApps.push({ app, el });
    return { el, props };
  }

  it('一键流：keygen → 公钥插队头+enable=true → emit save（整配置透传）', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    // 注意：mountCard 的 props 展开进 h() 后是快照、非响应——本例只断言 emit
    // 载荷不受影响；需要模拟父层回流的用例用 mountLive（逐键读 reactive）
    const { el, saved } = mountCard(baseConfig());
    await flush();

    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();

    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.enable).toBe(true);
    expect(saved[0].ssh_ingress.authorized_keys[0]).toBe(keygenPayload().public_key);
    expect(saved[0].proxy).toEqual(baseConfig().proxy); // 整配置透传不丢段
  });

  it('双击防重：keygen 在途期间第二次点击不产生第二个请求', async () => {
    let postCount = 0;
    let resolveKeygen;
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      if ((opts?.method || 'GET') === 'POST' && String(url).includes('/ssh-ingress/keygen')) {
        postCount += 1;
        return new Promise((res) => { resolveKeygen = res; });
      }
      return Promise.resolve(jsonResponse(hostkeyEnvelope(hostkeyPayload())));
    }));
    const saved = [];
    const el = document.createElement('div');
    document.body.appendChild(el);
    const app = createApp({ render: () => h(SshIngressSettings, { config: baseConfig(), onSave: (p) => saved.push(p) }) });
    app.mount(el);
    mountedApps.push({ app, el });
    await flush();

    el.querySelector('button[data-ssh-oneclick]').click(); // 第 1 击：fetch 挂起
    el.querySelector('button[data-ssh-oneclick]').click(); // 第 2 击：在途旗标拦截
    expect(postCount).toBe(1);

    resolveKeygen(jsonResponse(keygenEnvelope(keygenPayload())));
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].ssh_ingress.authorized_keys[0]).toBe(keygenPayload().public_key);
  });

  it('恢复链·换新钥：失败后删钥匙重按一键 → 第二次 POST 覆盖 pending（v3 恢复路径）', async () => {
    let postCount = 0;
    let resolveKeygen;
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      if ((opts?.method || 'GET') === 'POST' && String(url).includes('/ssh-ingress/keygen')) {
        postCount += 1;
        return new Promise((res) => { resolveKeygen = res; });
      }
      return Promise.resolve(jsonResponse(hostkeyEnvelope(hostkeyPayload())));
    }));
    const { el, props } = mountLive(baseConfig());
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click(); // 第 1 击 → pending=K1
    resolveKeygen(jsonResponse(keygenEnvelope(keygenPayload())));
    await flush();
    props.error = 'boom';                                   // 保存失败,pending=K1 保留
    await flush();

    // 用户删钥匙（重置为无钥匙配置,deep watch 重置表单→按钮复现）
    props.config = baseConfig();
    await flush();
    expect(el.querySelector('button[data-ssh-oneclick]')).not.toBeNull();

    el.querySelector('button[data-ssh-oneclick]').click(); // 重按：必须真发第二次 POST
    expect(postCount).toBe(2);                              // 守卫不得吞掉（pending 不进守卫）
    resolveKeygen(jsonResponse(keygenEnvelope(keygenPayload())));
    await flush();
    // 新钥覆盖旧 pending：列表头是新公钥
    expect(el.querySelector('[data-ssh-ingress-body]')).not.toBeNull();
  });

  it('保存成功回流（successMessage 变非空+pending 在）→ 私钥弹窗渲染全文+下载+复制', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const { el, props } = mountLive(baseConfig());
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();

    props.successMessage = '已保存';
    await flush();

    const modal = el.querySelector('[data-ssh-privkey-modal]');
    expect(modal).not.toBeNull();
    expect(el.querySelector('[data-ssh-privkey-text]').textContent).toContain('BEGIN OPENSSH PRIVATE KEY');
    expect(el.querySelector('a[data-ssh-privkey-download]')).not.toBeNull();
    expect(el.querySelector('button[data-ssh-privkey-copy]')).not.toBeNull();
  });

  it('恢复链（v3 核心）：一键保存失败 pending 保留 → 手工再存成功 → 弹窗补交付原钥', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const { el, props } = mountLive(baseConfig());
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click(); // 一键 → pending=K
    await flush();

    props.error = 'boom';   // 保存失败：error 一概不动 pending（v3）——只不弹窗
    await flush();
    expect(el.querySelector('[data-ssh-privkey-modal]')).toBeNull();

    // 表单不回滚（spec §2.2）：此时 enabled=true 且钥匙在，一键按钮已隐藏——
    // 用户自然恢复路径=手工再点保存按钮。勿在此处点 data-ssh-oneclick（null）。
    props.error = '';       // 重试保存开头清空（error 空串跃迁无 watch）
    await flush();
    props.successMessage = '已保存';
    await flush();
    const modal = el.querySelector('[data-ssh-privkey-modal]');
    expect(modal).not.toBeNull();  // 弹窗补交付原钥（pending 跨失败保留）
    expect(el.querySelector('[data-ssh-privkey-text]').textContent).toContain('BEGIN OPENSSH PRIVATE KEY');
  });

  it('无主私钥防交付：保存失败后公钥已不在列表 → success 回流不弹窗', async () => {
    stubFetchRouted(() => jsonResponse(keygenEnvelope(keygenPayload())));
    const { el, props } = mountLive(baseConfig());
    await flush();
    el.querySelector('button[data-ssh-oneclick]').click();
    await flush();
    props.error = 'boom';
    await flush();

    // 用户把配置换掉（deep config watch 重置表单；pending 存活）——
    // 新配置里没有生成的公钥
    props.config = baseConfig({ enable: true, authorized_keys: [HUMAN_KEY2] });
    await flush();
    props.successMessage = '已保存';
    await flush();
    expect(el.querySelector('[data-ssh-privkey-modal]')).toBeNull(); // 不交付无主私钥
  });

  it('折叠态联动：enabled false→true 且无钥匙 → 正文自动展开', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.querySelector('[data-ssh-ingress-body]').style.display).toBe('none');
    setToggle(el, 'sshIngressEnable', true);
    await flush();
    expect(el.querySelector('[data-ssh-ingress-body]').style.display).not.toBe('none');
    expect(el.querySelector('button[data-ssh-oneclick]')).not.toBeNull();
  });

  it('已启用且有钥匙 → 一键按钮不渲染（归手工流）', async () => {
    const { el } = mountCard(baseConfig({ enable: true, authorized_keys: [HUMAN_KEY2] }));
    await flush();
    expect(el.querySelector('button[data-ssh-oneclick]')).toBeNull();
  });
});
