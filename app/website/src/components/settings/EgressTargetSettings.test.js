import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// EgressTargetSettings.vue 求值连带 i18n loadLocale()(node 环境无 DOM),
// mock 掉即可导入 SFC(t 原样回键);canonical 读写走全局 fetch,vi.stubGlobal
// 注入。卡片自管 fetch 链(不经父层 save 事件)——SshIngressSettings.test.js
// 同范式。(2026-11-16 toSocks 孤儿开关事故修复的回归测试)
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import EgressTargetSettings, { applyEgressTargetToRules, mapProxyUpstreamType } from './EgressTargetSettings.vue';

// —— 纯函数:upstream.type 映射(镜像后端 resolveToSocksUpstreamType)——
describe('mapProxyUpstreamType', () => {
  it('按后端映射 socks5→socks/http→http/ssh→ssh,大小写与空白容错', () => {
    expect(mapProxyUpstreamType('socks5')).toBe('socks');
    expect(mapProxyUpstreamType('http')).toBe('http');
    expect(mapProxyUpstreamType('ssh')).toBe('ssh');
    expect(mapProxyUpstreamType(' SSH ')).toBe('ssh');
  });

  it('未知类型与空值返回空串(守卫禁开)', () => {
    expect(mapProxyUpstreamType('shadowsocks')).toBe('');
    expect(mapProxyUpstreamType('')).toBe('');
    expect(mapProxyUpstreamType(undefined)).toBe('');
  });
});

// —— 纯函数:开关两向落载 ——
describe('applyEgressTargetToRules', () => {
  const rules = [
    { id: 'ai_allowlist_0', type: 'domain', condition: '*.cursor.sh', enabled: true, target: 'toAliang' },
    { id: 'proxy_rule_0', type: 'domain', condition: 'google.com', enabled: true, target: 'direct' },
    { id: 'proxy_rule_1', type: 'domain', condition: 'youtube.com', enabled: true, target: 'toSocks' },
    { id: 'proxy_rule_2', type: 'domain', condition: 'x.com,mesh:dev-a', enabled: true, target: 'toNode:dev-a' }
  ];

  it('开:proxy_rule 的 direct→toSocks;ai_allowlist 与 toNode: 不动', () => {
    const next = applyEgressTargetToRules(rules, true);
    expect(next[0].target).toBe('toAliang');
    expect(next[1].target).toBe('toSocks');
    expect(next[2].target).toBe('toSocks');
    expect(next[3].target).toBe('toNode:dev-a');
  });

  it('关:proxy_rule 的 toSocks→direct;其余不动', () => {
    const next = applyEgressTargetToRules(rules, false);
    expect(next[0].target).toBe('toAliang');
    expect(next[1].target).toBe('direct');
    expect(next[2].target).toBe('direct');
    expect(next[3].target).toBe('toNode:dev-a');
  });

  it('空/非法输入安全', () => {
    expect(applyEgressTargetToRules([], true)).toEqual([]);
    expect(applyEgressTargetToRules(undefined, true)).toEqual([]);
    expect(applyEgressTargetToRules([null], true)[0]).toBeNull();
  });
});

// —— 挂载级测试 ——
const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function mountCard(config) {
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(EgressTargetSettings, { config })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el };
}

function setToggle(el, field, checked) {
  const node = el.querySelector(`[data-field="${field}"]`);
  node.checked = checked;
  node.dispatchEvent(new Event('change', { bubbles: true }));
}

const jsonResponse = (payload, ok = true, status) => ({
  ok,
  status: status ?? (ok ? 200 : 500),
  json: async () => payload
});

const baseConfig = () => ({
  proxy: { enable: true, type: 'ssh', server: '172.16.222.1:56432', username: 'u', private_key: 'k' }
});

const canonicalPayload = () => ({
  version: 3,
  ingress: { mode: 'tun' },
  egress: {
    direct: { enabled: true },
    toAliang: { enabled: true },
    toSocks: { enabled: false, upstream: { type: '' } }
  },
  routing: {
    rules: [
      { id: 'ai_allowlist_0', type: 'domain', condition: '*.cursor.sh', enabled: true, target: 'toAliang' },
      { id: 'proxy_rule_0', type: 'domain', condition: 'google.com', enabled: true, target: 'direct' },
      { id: 'proxy_rule_1', type: 'domain', condition: 'github.com', enabled: true, target: 'direct' }
    ],
    default_egress: 'direct'
  }
});

const routingEnvelope = (canonical) => ({ code: 0, msg: '', data: canonical });

// fetch 分流:GET /api/config/routing 走 canonical 信封,POST 回成功信封并记账。
function stubFetchRouted({ canonical = canonicalPayload(), postImpl } = {}) {
  const posts = [];
  globalThis.fetch = vi.fn((url, opts) => {
    if ((opts?.method || 'GET') === 'POST' && String(url).includes('/api/config/routing')) {
      posts.push(JSON.parse(opts.body));
      const resp = postImpl ? postImpl(posts[posts.length - 1]) : jsonResponse({ code: 0, msg: '', data: { message: 'ok' } });
      return Promise.resolve(resp);
    }
    return Promise.resolve(jsonResponse(routingEnvelope(typeof canonical === 'function' ? canonical() : canonical)));
  });
  return posts;
}

beforeEach(() => {
  stubFetchRouted();
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

describe('EgressTargetSettings 卡片', () => {
  it('挂载拉取 canonical 并诚实展示各规则实际出口;开关反映 toSocks 现值', async () => {
    const { el } = mountCard(baseConfig());
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/config/routing', expect.anything());
    expect(el.querySelector('[data-field="egressTargetEnabled"]').checked).toBe(false);
    const badges = [...el.querySelectorAll('[data-egress-rule-target]')];
    expect(badges).toHaveLength(2);
    expect(badges[0].getAttribute('data-egress-rule-target')).toBe('google.com');
    expect(badges[0].textContent).toBe('egress_target_direct');
  });

  it('开启并保存:POST 整份 canonical,toSocks 开+upstream.type 映射+direct 规则改写,ai_allowlist 不动,保存后重 GET 收敛', async () => {
    const posts = stubFetchRouted();
    const { el } = mountCard(baseConfig());
    await flush();

    setToggle(el, 'egressTargetEnabled', true);
    el.querySelector('[data-egress-target-save]').click();
    await flush(16);

    expect(posts).toHaveLength(1);
    const body = posts[0];
    // 整份替换:四键齐、含 ingress/version(DisallowUnknownFields 铁律)
    expect(Object.keys(body).sort()).toEqual(['egress', 'ingress', 'routing', 'version']);
    expect(body.egress.toSocks).toEqual({ enabled: true, upstream: { type: 'ssh' } });
    const targets = Object.fromEntries(body.routing.rules.map((r) => [r.id, r.target]));
    expect(targets.ai_allowlist_0).toBe('toAliang');
    expect(targets.proxy_rule_0).toBe('toSocks');
    expect(targets.proxy_rule_1).toBe('toSocks');
    // 保存后重 GET 收敛(customer 保存链可能交错重编译)
    expect(globalThis.fetch.mock.calls.filter(([, o]) => (o?.method || 'GET') === 'GET')).toHaveLength(2);
    expect(el.querySelector('[data-egress-target-save]').textContent).toBe('egress_save');
    expect(el.textContent).toContain('egress_saveSuccess');
  });

  it('关闭并保存:toSocks 规则改写回 direct,upstream.type 清空', async () => {
    const posts = stubFetchRouted({
      canonical: (() => {
        const c = canonicalPayload();
        c.egress.toSocks = { enabled: true, upstream: { type: 'ssh' } };
        c.routing.rules = c.routing.rules.map((r) => (r.id === 'proxy_rule_0' ? { ...r, target: 'toSocks' } : r));
        return c;
      })()
    });
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.querySelector('[data-field="egressTargetEnabled"]').checked).toBe(true);

    setToggle(el, 'egressTargetEnabled', false);
    el.querySelector('[data-egress-target-save]').click();
    await flush();

    const body = posts[0];
    expect(body.egress.toSocks).toEqual({ enabled: false, upstream: { type: '' } });
    const targets = Object.fromEntries(body.routing.rules.map((r) => [r.id, r.target]));
    expect(targets.proxy_rule_0).toBe('direct');
    expect(targets.ai_allowlist_0).toBe('toAliang');
  });

  it('客户代理缺失:守卫提示+开关禁用,保存仍可见但动作以 canonical 现状为准', async () => {
    const { el } = mountCard({});
    await flush();
    expect(el.querySelector('[data-egress-target-guard]').textContent).toContain('egress_guardProxyMissing');
    expect(el.querySelector('[data-field="egressTargetEnabled"]').disabled).toBe(true);
  });

  it('代理类型不支持:守卫提示类型不支持', async () => {
    const { el } = mountCard({ proxy: { enable: true, type: 'shadowsocks', server: '1.2.3.4:1' } });
    await flush();
    expect(el.querySelector('[data-egress-target-guard]').textContent).toContain('egress_guardTypeUnsupported');
    expect(el.querySelector('[data-field="egressTargetEnabled"]').disabled).toBe(true);
  });

  it('canonical 加载失败:错误条可见,不白屏', async () => {
    stubFetchRouted();
    globalThis.fetch = vi.fn().mockRejectedValue(new Error('network down'));
    const { el } = mountCard(baseConfig());
    await flush();
    expect(el.textContent).toContain('egress_loadFailed');
    expect(el.querySelector('[data-egress-target-save]')).toBeTruthy();
  });
});
