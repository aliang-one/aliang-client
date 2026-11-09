import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// RulesSettings.vue 模块求值会连带执行 i18n 的 loadLocale()（读 localStorage，
// node 测试环境无 DOM）；本测试只测 sanitizeList 纯函数，mock 掉该依赖即可导入 SFC。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { createApp, h } from 'vue';
import {
  detectCommaCollapseIssue,
  detectGeoipCommaIssue,
  extractProxyRuleDomain,
  findRuleOverlaps,
  formatEntryIssue,
  includeSummaryText,
  mergeProviderOrder,
  providerLabelFor,
  sanitizeList,
  seedMissingProviders,
  validateRuleEntry
} from './RulesSettings.vue';
import RulesSettings from './RulesSettings.vue';
import en from '../../i18n/en';
import zh from '../../i18n/zh';

// 分词器以逗号/分号/换行切分 include 文本；geoip 多国条目（+ 分隔）必须原样穿过，
// 否则 geoip:CN+HK+US 会被误拆（geoip 条目校验见 RulesSettings.vue validateRuleEntry）。
describe('RulesSettings sanitizeList', () => {
  it('preserves geoip multi-country entry through the comma tokenizer', () => {
    expect(sanitizeList('geoip:CN+HK+US')).toEqual(['geoip:CN+HK+US']);
  });
});

// —— validateRuleEntry geoip 形态矩阵（对齐后端 processor/match ClassifyEntry）——
// 红线：前端从不拒绝后端会接受的输入（geoip,CN,proxy 后端接受 → 必须过）；
//      后端拒绝的逗号多国形态（geoip,CN,HK,proxy）→ 前端给出本地提示。
// 文案分派：含逗号的整行只有 proxy_rules 面会走到本函数（include 面 sanitizeList
// 先拆逗号、detectGeoipCommaIssue 拆分前拦截）→ 逗号多国断言用 proxy 面文案 key。
const probe = (v) => validateRuleEntry(v, (k) => k);

describe('RulesSettings validateRuleEntry geoip', () => {
  it('accepts single-country Clash form (with proxy tail)', () => {
    expect(probe('geoip,CN,proxy')).toBe('');
    expect(probe('geoip,JP')).toBe('');
    expect(probe('GEOIP,cn,proxy')).toBe('');
  });

  it('rejects comma multi-country form with + separator hint', () => {
    expect(probe('geoip,CN,HK,proxy')).toBe('rules_geoipCommaMultiProxy');
    expect(probe('geoip,CN,HK')).toBe('rules_geoipCommaMultiProxy');
    expect(probe('geoip,CN,hk')).toBe('rules_geoipCommaMultiProxy');
  });

  it('rejects bare geoip keyword and missing/invalid country code', () => {
    expect(probe('geoip')).toBe('rules_geoipInvalid');
    expect(probe('geoip,')).toBe('rules_geoipInvalid');
    expect(probe('geoip,USA')).toBe('rules_geoipInvalid');
  });

  it('keeps colon-style geoip matrix intact', () => {
    expect(probe('geoip:CN+HK+US')).toBe('');
    expect(probe('geoip:cn')).toBe('');
    expect(probe('geoip:CN,HK')).toBe('rules_geoipCommaMultiProxy');
    expect(probe('geoip:')).toBe('rules_geoipInvalid');
  });

  it('keeps non-geoip Clash forms intact', () => {
    expect(probe('ip-cidr,192.168.0.0/16,proxy')).toBe('');
    expect(probe('ip-range,1.1.1.1-1.1.1.5')).toBe('');
    expect(probe('domain,example.com')).toBe('');
    expect(probe('DOMAIN-SUFFIX,example.com,proxy')).toBe('');
  });
});

// —— 逗号兜底分支尾段动作词集合（对齐后端 processor/match legacyActionTail）——
// 集合 = {proxy, direct, reject, no-resolve}（大小写不敏感，仅剥一层尾段）。
// 红线：前端从不拒绝后端接受的输入，两边集合必须同步维护。
describe('RulesSettings validateRuleEntry action tail', () => {
  it('accepts direct/reject/no-resolve tails like proxy', () => {
    expect(probe('example.com,direct')).toBe('');
    expect(probe('example.com,REJECT')).toBe('');
    expect(probe('example.com,no-resolve')).toBe('');
    expect(probe('DOMAIN-SUFFIX,example.com,DIRECT')).toBe('');
  });

  it('validates the stripped segment, not the tail action word', () => {
    // 剥离出 example..com（空标签）→ 报错；后端 ClassifyEntry("example..com,direct") 同样拒绝
    expect(probe('example..com,direct')).toBe('rules_entryEmptyLabel');
    // 单次剥离语义：尾段 extra 非动作词 → 校验尾段（中间段 direct 不参与）
    expect(probe('example.com,direct,extra!')).toBe('rules_entryInvalidChars');
  });

  it('keeps geoip branch behavior unchanged (action tails are noise there)', () => {
    expect(probe('geoip,CN,direct')).toBe('');
    expect(probe('geoip,CN,no-resolve,extra')).toBe('');
    expect(probe('geoip,CN,HK,direct')).toBe('rules_geoipCommaMultiProxy');
  });

  it('keeps legacy proxy and non-action tail semantics intact', () => {
    expect(probe('a.com,b.com,proxy')).toBe('');
    expect(probe('foo,bar')).toBe('');
    expect(probe('example.com,proxy-server')).toBe('');
  });
});

// —— geoip 逗号多国文案按输入面分派 ——
// include 面（sanitizeList 按逗号拆分，真会拆坏）→ 拆分前扫描 detectGeoipCommaIssue
// 用 rules_geoipCommaMulti；proxy_rules 面（按行拆分不拆坏，后端按格式拒绝）→
// validateRuleEntry 逗号分支用 rules_geoipCommaMultiProxy（不带「拆坏」表述）。
describe('RulesSettings geoip comma message facets', () => {
  const t = (k) => k;

  it('include-side pre-split scan keeps the broken-by-tokenizer wording', () => {
    expect(detectGeoipCommaIssue('geoip:CN,HK', t)).toBe('rules_geoipCommaMulti');
    expect(detectGeoipCommaIssue('geoip,CN,HK\na.com', t)).toBe('rules_geoipCommaMulti');
    expect(detectGeoipCommaIssue('geoip:CN+HK\na.com', t)).toBe('');
  });

  it('proxy_rules-side entry validation uses the format-rejection wording', () => {
    expect(probe('geoip,CN,HK,proxy')).toBe('rules_geoipCommaMultiProxy');
    expect(probe('geoip:CN,HK')).toBe('rules_geoipCommaMultiProxy');
  });
});

// —— extractProxyRuleDomain 条件提取（重叠比对 HIGH#3 的纯函数核心；提取语义与
// validateRuleEntry / 后端 ClassifyEntry 同源：commaFallbackPart / normalizeConditionHost 共用）——
describe('RulesSettings extractProxyRuleDomain', () => {
  it('normalizes bare values, URLs and host:port to lowercase hostnames', () => {
    expect(extractProxyRuleDomain('OpenAI.com')).toBe('openai.com');
    expect(extractProxyRuleDomain('https://ChatGPT.com/path')).toBe('chatgpt.com');
    expect(extractProxyRuleDomain('x.com:8080')).toBe('x.com');
    expect(extractProxyRuleDomain('')).toBe('');
  });

  it('extracts the effective segment from Clash comma forms', () => {
    expect(extractProxyRuleDomain('domain,example.com')).toBe('example.com');
    expect(extractProxyRuleDomain('domains,example.com,proxy')).toBe('example.com');
    expect(extractProxyRuleDomain('a.com,b.com')).toBe('b.com');
    expect(extractProxyRuleDomain('a.com,b.com,proxy')).toBe('b.com');
    expect(extractProxyRuleDomain('DOMAIN-SUFFIX,Example.COM,DIRECT')).toBe('example.com');
  });

  it('normalizes geoip and ip keyword forms for overlap comparison', () => {
    expect(extractProxyRuleDomain('geoip,CN,proxy')).toBe('geoip:cn');
    expect(extractProxyRuleDomain('GEOIP:CN')).toBe('geoip:cn');
    expect(extractProxyRuleDomain('ip-cidr,192.168.0.0/16,proxy')).toBe('192.168.0.0/16');
    expect(extractProxyRuleDomain('IP-CIDR:10.0.0.0/8')).toBe('10.0.0.0/8');
    // ip-from 冒号对整行返回（include 面不存在可比形态）
    expect(extractProxyRuleDomain('ip-from:1.2.3.4,ip-to:1.2.3.9')).toBe('ip-from:1.2.3.4,ip-to:1.2.3.9');
  });
});

// —— AI include 与 proxy_rules 的重叠比对（HIGH#3：后端 (kind,cond) seen 表剪除，
// AI 恒优先 → 软警告不阻断保存）——
describe('RulesSettings findRuleOverlaps', () => {
  it('flags entries present in both includes and proxy rules', () => {
    expect(findRuleOverlaps(['openai.com'], ['openai.com', 'domain,anthropic.com'])).toEqual(['openai.com']);
    expect(findRuleOverlaps(['openai.com'], ['domain,openai.com,proxy'])).toEqual(['openai.com']);
    expect(findRuleOverlaps(['openai.com'], ['a.com,openai.com,proxy'])).toEqual(['openai.com']);
  });

  it('normalizes case and trailing dots before comparing', () => {
    expect(findRuleOverlaps(['OPENAI.com.'], ['Domain,OpenAI.COM,proxy'])).toEqual(['openai.com']);
  });

  it('returns sorted deduped overlaps across multiple lines and entries', () => {
    expect(
      findRuleOverlaps(
        ['chatgpt.com', 'openai.com'],
        ['openai.com', 'domain,chatgpt.com,proxy', 'chatgpt.com']
      )
    ).toEqual(['chatgpt.com', 'openai.com']);
  });

  it('keeps geoip country codes separate from bare-domain includes (kind differs)', () => {
    // include 裸域 cn（domain 类）vs proxy geoip,cn（geoip 类）——后端 (kind,cond) 不同键不剪除
    expect(findRuleOverlaps(['cn'], ['geoip,CN'])).toEqual([]);
  });

  it('flags geoip overlap via the shared colon form', () => {
    expect(findRuleOverlaps(['geoip:CN'], ['geoip,CN,proxy'])).toEqual(['geoip:cn']);
  });

  it('returns empty when nothing overlaps', () => {
    expect(findRuleOverlaps(['openai.com'], ['anthropic.com'])).toEqual([]);
    expect(findRuleOverlaps([], ['openai.com'])).toEqual([]);
    expect(findRuleOverlaps(['openai.com'], [])).toEqual([]);
  });
});

// —— 逗号塌缩软提示（HIGH#4）：非关键字首段多段行仅保留末段，合法但不提示会少存规则 ——
describe('RulesSettings detectCommaCollapseIssue', () => {
  const hint = (v) => detectCommaCollapseIssue(v, (k, p) => `${k}|${p?.kept ?? ''}`);

  it('hints the actually-kept segment for multi-segment lines without keyword head', () => {
    expect(hint('a.com,b.com')).toBe('rules_commaCollapseHint|b.com');
    expect(hint('a.com,b.com,proxy')).toBe('rules_commaCollapseHint|b.com');
    expect(hint('a.com,B.com')).toBe('rules_commaCollapseHint|B.com');
    expect(hint('a.com,b.com,c.com,direct')).toBe('rules_commaCollapseHint|c.com');
  });

  it('stays silent for keyword-headed lines, colon forms, single domains and empty text', () => {
    expect(hint('domains,x.com,proxy')).toBe('');
    expect(hint('DOMAIN,example.com')).toBe('');
    expect(hint('domain,example.com,proxy')).toBe('');
    expect(hint('ip-cidr,1.2.3.0/24,proxy')).toBe('');
    expect(hint('ip-range,1.1.1.1-1.1.1.5')).toBe('');
    expect(hint('GEOIP,CN,proxy')).toBe('');
    expect(hint('IP-FROM,1.2.3.4,ip-to:1.2.3.9')).toBe('');
    // 冒号前缀行由前缀分支整行处理（ip-from:a,ip-to:b 合法整行），不进入兜底塌缩
    expect(hint('ip-from:1.2.3.4,ip-to:1.2.3.9')).toBe('');
    expect(hint('geoip:CN+HK')).toBe('');
    expect(hint('example.com')).toBe('');
    expect(hint('')).toBe('');
  });

  it('reports only the first offending line', () => {
    expect(hint('domain,ok.com\nbad.com,worse.com')).toBe('rules_commaCollapseHint|worse.com');
  });
});

// —— mesh 转发尾段（对齐后端 processor/routing splitMeshTail）——
// 后端在 ClassifyEntry **之前**剥尾，故前端校验/提取也必须在任何逗号分支前剥，
// 否则是「前端拒绝后端接受的输入」：今日真实缺陷=`x.com,mesh:dev-b` 被逗号兜底
// 分支的 ENTRY_CHARS_RE 拒（冒号不在字符集），后端接受。
// 红线重申：ACTION_TAIL_WORDS 集合不动（后端 legacyActionTail 不含 mesh）。
describe('RulesSettings validateRuleEntry mesh tail', () => {
  it('accepts the bare comma form (today rejected by the fallthrough branch)', () => {
    expect(probe('x.com,mesh:dev-b')).toBe('');
    expect(probe('x.com,MESH:dev-b')).toBe('');
    expect(probe('x.com, mesh:dev-b ')).toBe('');
    // base 仍走常规语法：多域名行照旧按兼容语义（只校验有效段）
    expect(probe('a.com,b.com,mesh:dev-b')).toBe('');
  });

  it('accepts one policy segment after mesh:<id>', () => {
    expect(probe('x.com,mesh:dev-b,direct')).toBe('');
    expect(probe('x.com,mesh:dev-b,DENY')).toBe('');
    expect(probe('x.com,mesh:dev-b,fallback:dev-c')).toBe('');
    expect(probe('x.com,mesh:dev-b, FALLBACK: dev-c ')).toBe('');
  });

  it('rejects malformed mesh tails instead of silently accepting them', () => {
    // 空 deviceID / 空 base / 多策略段 / 未知策略段 / 空 fallback 目标
    expect(probe('x.com,mesh:')).toBe('rules_meshInvalid');
    expect(probe('mesh:dev-b')).toBe('rules_meshInvalid');
    expect(probe('x.com,mesh:dev-b,direct,extra')).toBe('rules_meshInvalid');
    expect(probe('x.com,mesh:dev-b,bogus')).toBe('rules_meshInvalid');
    expect(probe('x.com,mesh:dev-b,fallback:')).toBe('rules_meshInvalid');
  });

  it('validates the base, so colon-keyword bases are no longer mis-rejected', () => {
    // 这两条今日都错：ip-cidr 冒号分支整串拒、geoip 判逗号多国；后端接受
    expect(probe('ip-cidr:1.2.3.0/24,mesh:dev-b')).toBe('');
    expect(probe('ip-cidr:1.2.3.0/24,mesh:dev-b,direct')).toBe('');
    expect(probe('geoip:cn,mesh:dev-b')).toBe('');
    expect(probe('dns,mesh:dev-b')).toBe('');
    // base 自身非法仍报 base 的错误文案（不是 mesh 文案）
    expect(probe('ip-cidr:1.2.3.0/99,mesh:dev-b')).toBe('rules_ipInvalid');
    expect(probe('exam..ple.com,mesh:dev-b')).toBe('rules_entryEmptyLabel');
  });

  it('keeps every non-mesh entry byte-for-byte unchanged', () => {
    expect(probe('example.com,direct')).toBe('');
    expect(probe('example.com,proxy-server')).toBe('');
    expect(probe('geoip,CN,proxy')).toBe('');
    expect(probe('geoip,CN,HK,proxy')).toBe('rules_geoipCommaMultiProxy');
    expect(probe('ip-cidr,1.2.3.0/24,proxy')).toBe('');
    expect(probe('example..com')).toBe('rules_entryEmptyLabel');
  });
});

describe('RulesSettings extractProxyRuleDomain mesh tail', () => {
  it('extracts the base condition, not the mesh target', () => {
    // 不剥尾就会提取出 'mesh:dev-b' → 与 AI include 假重叠
    expect(extractProxyRuleDomain('x.com,mesh:dev-b')).toBe('x.com');
    expect(extractProxyRuleDomain('OpenAI.com,mesh:dev-b,direct')).toBe('openai.com');
    expect(extractProxyRuleDomain('ip-cidr:1.2.3.0/24,mesh:dev-b')).toBe('1.2.3.0/24');
    expect(extractProxyRuleDomain('geoip:cn,mesh:dev-b')).toBe('geoip:cn');
    expect(extractProxyRuleDomain('a.com,b.com,mesh:dev-b')).toBe('b.com');
    // 非法 mesh 形态整条不参与比对
    expect(extractProxyRuleDomain('x.com,mesh:')).toBe('');
  });
});

describe('RulesSettings detectCommaCollapseIssue mesh tail', () => {
  const hint = (v) => detectCommaCollapseIssue(v, (k, p) => `${k}|${p?.kept ?? ''}`);

  it('never flags a mesh entry as a collapsed multi-domain line', () => {
    // 不剥尾会误报 kept=mesh:dev-b（把 mesh 目标当成被保留的域名）
    expect(hint('x.com,mesh:dev-b')).toBe('');
    // base 本身确实是塌缩形态 → 提示的是 base 的真实保留段（b.com），不是 mesh:dev-b
    expect(hint('a.com,b.com,mesh:dev-b')).toBe('rules_commaCollapseHint|b.com');
    expect(hint('a.com,b.com,c.com,mesh:dev-b')).toBe('rules_commaCollapseHint|c.com');
    // 非法 mesh 形态不提示（红色错误优先）
    expect(hint('x.com,mesh:')).toBe('');
  });
});

// —— 错误文案组装（LOW#18）：firstEntryIssue 的 index 透传为「第 N 行」前缀 ——
// 前缀 N 从 1 起（index 0 → 第 1 行）；nil issue 返回空串（无错误不渲染）。
describe('RulesSettings formatEntryIssue', () => {
  const t = (k, params) => {
    if (k === 'rules_linePrefix') return `第 ${params.line} 行：`;
    if (k === 'rules_entryErrorItem') return `条目“${params.entry}”：${params.message}`;
    return k;
  };

  it('prepends the 1-based line number to the concrete message', () => {
    const issue = { index: 2, entry: 'geoip:CHN', message: 'rules_geoipInvalid' };
    expect(formatEntryIssue(issue, t)).toBe('条目“geoip:CHN”：第 3 行：rules_geoipInvalid');
  });

  it('numbers the first entry as line 1', () => {
    const issue = { index: 0, entry: 'a..b', message: 'rules_entryEmptyLabel' };
    expect(formatEntryIssue(issue, t)).toBe('条目“a..b”：第 1 行：rules_entryEmptyLabel');
  });

  it('returns empty string for a null issue', () => {
    expect(formatEntryIssue(null, t)).toBe('');
  });
});

// —— include 收起态副文案（LOW#16）：空 → Empty 键；单条 → 条目本身；≥2 → first+N more ——
describe('RulesSettings includeSummaryText', () => {
  const t = (k, params) => {
    if (k === 'rules_includeDomainsEmpty') return 'rules_includeDomainsEmpty';
    if (k === 'rules_includeDomainsPreview') return `rules_includeDomainsPreview|${params.first}|${params.count}`;
    return k;
  };

  it('shows the empty hint when nothing is configured', () => {
    expect(includeSummaryText([], t)).toBe('rules_includeDomainsEmpty');
    expect(includeSummaryText(undefined, t)).toBe('rules_includeDomainsEmpty');
  });

  it('shows the single entry itself', () => {
    expect(includeSummaryText(['openai.com'], t)).toBe('openai.com');
  });

  it('shows first + N more for multiple entries (count excludes the first)', () => {
    expect(includeSummaryText(['chatgpt.com', 'openai.com'], t))
      .toBe('rules_includeDomainsPreview|chatgpt.com|1');
    expect(includeSummaryText(['a.com', 'b.com', 'c.com', 'd.com'], t))
      .toBe('rules_includeDomainsPreview|a.com|3');
  });
});

// —— AI provider 键大小写不敏感（后端已折叠大小写变体，此为前端防御层）：
// 存量配置里的 anthropic 与预置里的 Anthropic 必须合成一张卡，保存时不得把
// 预置补种键写回配置文件制造重复 ——
describe('RulesSettings provider case-insensitivity', () => {
  const PRESETS = [
    { key: 'Anthropic', label: 'Anthropic', editable: true },
    { key: 'openai', label: 'OpenAI', editable: true },
    { key: 'cursor', label: 'Cursor', editable: false }
  ];

  it('mergeProviderOrder folds configured key case-variants into one card', () => {
    const order = mergeProviderOrder(['anthropic', 'openai'], PRESETS);
    expect(order).toEqual(['anthropic', 'openai', 'cursor']);
  });

  it('mergeProviderOrder does not re-add a preset whose case-variant is configured', () => {
    const order = mergeProviderOrder(['anthropic', 'openai', 'cursor'], PRESETS);
    expect(order.filter((key) => key.toLowerCase() === 'anthropic')).toHaveLength(1);
  });

  it('mergeProviderOrder still appends genuinely missing presets', () => {
    const order = mergeProviderOrder(['Anthropic'], PRESETS);
    expect(order).toEqual(['Anthropic', 'openai', 'cursor']);
  });

  it('seedMissingProviders does not seed a preset whose case-variant is configured', () => {
    const aiRules = { anthropic: { enble: true, include: ['api.anthropic.com'] } };
    const seeded = seedMissingProviders(aiRules, PRESETS);
    expect(Object.keys(seeded).filter((key) => key.toLowerCase() === 'anthropic')).toHaveLength(1);
    expect(seeded.anthropic.enble).toBe(true);
    expect(seeded.Anthropic).toBeUndefined();
  });

  it('seedMissingProviders backfills editable for an existing entry missing it', () => {
    const aiRules = { Anthropic: { enble: true, include: [] } };
    const seeded = seedMissingProviders(aiRules, PRESETS);
    expect(seeded.Anthropic.editable).toBe(true);
  });

  it('seedMissingProviders keeps unrelated providers untouched', () => {
    const aiRules = { deepseek: { enble: true, include: ['api.deepseek.com'] } };
    const seeded = seedMissingProviders(aiRules, PRESETS);
    expect(seeded.deepseek).toEqual({ enble: true, include: ['api.deepseek.com'] });
    expect(seeded.Anthropic).toBeDefined();
  });

  it('providerLabelFor resolves labels case-insensitively', () => {
    expect(providerLabelFor('anthropic', PRESETS)).toBe('Anthropic');
    expect(providerLabelFor('Anthropic', PRESETS)).toBe('Anthropic');
    expect(providerLabelFor('deepseek', PRESETS)).toBe('deepseek');
  });
});

// —— SSH 上游代理（Task 12）挂载级测试 ——
// 挂载走 createApp + jsdom 手法（仓内无 @vue/test-utils，AgentMCPRegistrySettings.test.js
// 同范式）；i18n 沿用本文件顶部 mock（t 原样回键，断言不硬编码文案）；指纹区块
// 走全局 fetch，vi.stubGlobal 注入。onSave prop 捕获 'save' 事件载荷。
const flush = async (rounds = 8) => {
  for (let i = 0; i < rounds; i += 1) await Promise.resolve();
};

const mountedApps = [];

function mountRules(config) {
  const saved = [];
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({
    render: () => h(RulesSettings, {
      config,
      onSave: (payload) => saved.push(payload)
    })
  });
  app.mount(el);
  mountedApps.push({ app, el });
  return { el, saved };
}

function sshConfig(proxyOverrides = {}) {
  return {
    proxy: {
      enable: true,
      type: 'ssh',
      server: '127.0.0.1:2222',
      username: 'deploy',
      password: 'secret',
      private_key: '',
      key_passphrase: '',
      pool_size: '',
      jump_servers: [],
      ...proxyOverrides
    },
    ai_rules: {},
    proxy_rules: []
  };
}

function setField(el, field, value, root) {
  const node = (root || el).querySelector(`[data-field="${field}"]`);
  node.value = value;
  node.dispatchEvent(new Event('input', { bubbles: true }));
}

function setType(el, type) {
  const select = el.querySelector('[data-field="proxyType"]');
  select.value = type;
  select.dispatchEvent(new Event('change'));
}

function submitForm(el) {
  el.querySelector('form').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
}

function buttonByText(el, text) {
  return [...el.querySelectorAll('button')].find((btn) =>
    btn.textContent.replace(/\s+/g, ' ').trim().includes(text));
}

const jsonResponse = (payload, ok = true) => ({
  ok,
  status: ok ? 200 : 500,
  json: async () => payload
});

const knownHostsEnvelope = (items) => ({
  code: 0,
  msg: '',
  data: { known_hosts: items }
});

const knownHostEntries = () => [
  { host: 'bastion.example.com', port: '22', fingerprint: 'SHA256:0123456789abcdefghijklmnopqrstuv', algorithm: 'ssh-ed25519', added_at: '2026-11-08T01:02:03Z' },
  { host: '10.0.0.8', port: '2222', fingerprint: 'SHA256:zyxwvutsrqponmlkjihgfedcba9876543210', algorithm: 'ssh-rsa', added_at: '2026-11-07T08:09:10Z' }
];

describe('RulesSettings SSH 上游代理', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse(knownHostsEnvelope([]))));
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

  it('类型下拉含 ssh 且默认 socks5；ssh 条件字段与指纹区块按类型显隐', async () => {
    const { el } = mountRules(sshConfig({ type: 'socks5', username: '', password: '' }));
    await flush();
    const select = el.querySelector('[data-field="proxyType"]');
    expect(select.value).toBe('socks5');
    expect(select.querySelector('option[value="ssh"]')).toBeTruthy();
    // socks5/http 时 ssh 专属字段与指纹区块不可见
    expect(el.querySelector('[data-ssh-fields]')).toBeNull();
    expect(el.querySelector('[data-known-hosts]')).toBeNull();

    setType(el, 'ssh');
    await flush();
    expect(el.querySelector('[data-ssh-fields]')).toBeTruthy();
    expect(el.querySelector('[data-field="proxyPrivateKey"]')).toBeTruthy();
    expect(el.querySelector('[data-field="proxyKeyPassphrase"]')).toBeTruthy();
    expect(el.querySelector('[data-field="proxyPoolSize"]')).toBeTruthy();
    expect(el.querySelector('[data-jump-add]')).toBeTruthy();
    expect(el.querySelector('[data-known-hosts]')).toBeTruthy();
    // 切到 ssh 触发指纹列表加载
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/proxy/ssh/known-hosts', expect.anything());
  });

  it('校验：ssh 无用户名或密码私钥全空时拦截提交并给出文案', async () => {
    const { el, saved } = mountRules(sshConfig({ username: '', password: '' }));
    await flush();
    // 无 username（密码/私钥也空）→ 用户名文案优先
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(0);
    expect(el.textContent).toContain('rules_sshValidationErrorUsername');
    expect(el.querySelector('#rulesConfigSaveBtn').disabled).toBe(true);

    // 补 username，仍无认证 → 认证文案
    setField(el, 'proxyUsername', 'deploy');
    await flush();
    expect(el.textContent).toContain('rules_sshValidationErrorAuth');
    expect(el.textContent).not.toContain('rules_sshValidationErrorUsername');

    // 补密码后校验通过，提交放行
    setField(el, 'proxyPassword', 'secret');
    await flush();
    expect(el.textContent).not.toContain('rules_sshValidationError');
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
  });

  it('校验：跳板行缺 username 或认证时拦截（文案带序号），补齐后放行', async () => {
    const { el, saved } = mountRules(sshConfig());
    await flush();
    buttonByText(el, 'rules_proxyJumpAdd').click();
    await flush();
    const rows = el.querySelectorAll('[data-jump-row]');
    expect(rows).toHaveLength(1);
    // 只填 server 的半填行 → 拦截（序号从 1 起）
    setField(el, 'jumpServer', 'bastion.example.com:2222', rows[0]);
    await flush();
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(0);
    expect(el.textContent).toContain('rules_sshValidationErrorJumpUsername');

    setField(el, 'jumpUsername', 'jump', rows[0]);
    await flush();
    expect(el.textContent).toContain('rules_sshValidationErrorJumpAuth');
    setField(el, 'jumpPassword', 'jump-pass', rows[0]);
    await flush();
    expect(el.textContent).not.toContain('rules_sshValidationErrorJump');
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
  });

  it('跳板行增删与提交载荷：server 单串原样、行对象只含线格式五字段', async () => {
    const { el, saved } = mountRules(sshConfig());
    await flush();
    buttonByText(el, 'rules_proxyJumpAdd').click();
    buttonByText(el, 'rules_proxyJumpAdd').click();
    await flush();
    expect(el.querySelectorAll('[data-jump-row]')).toHaveLength(2);

    const firstRow = el.querySelectorAll('[data-jump-row]')[0];
    setField(el, 'jumpServer', 'ssh://u2:p2@bastion.example.com:2222', firstRow);
    setField(el, 'jumpUsername', 'jump', firstRow);
    setField(el, 'jumpPassword', 'jump-pass', firstRow);
    // 展开细节面板后补私钥与口令（可选私钥折叠交互）
    firstRow.querySelector('[data-jump-toggle-key]').click();
    await flush();
    setField(el, 'jumpPrivateKey', 'JUMP-PEM', firstRow);
    setField(el, 'jumpKeyPassphrase', 'jump-pp', firstRow);

    // 第二行整行留空 → 提交前被静默丢弃
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].proxy.jump_servers).toEqual([{
      server: 'ssh://u2:p2@bastion.example.com:2222',
      username: 'jump',
      password: 'jump-pass',
      private_key: 'JUMP-PEM',
      key_passphrase: 'jump-pp'
    }]);

    // 删除按钮移除行
    firstRow.querySelector('[data-jump-remove]').click();
    await flush();
    expect(el.querySelectorAll('[data-jump-row]')).toHaveLength(1);
    submitForm(el);
    await flush();
    expect(saved[1].proxy.jump_servers).toEqual([]);
  });

  it('提交载荷：ssh 时带 private_key/key_passphrase/pool_size，空 pool 发 0（后端默认 3）', async () => {
    const { el, saved } = mountRules(sshConfig());
    await flush();
    setField(el, 'proxyPrivateKey', '-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n');
    setField(el, 'proxyKeyPassphrase', 'pp');
    setField(el, 'proxyPoolSize', '5');
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].proxy.private_key).toBe('-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n');
    expect(saved[0].proxy.key_passphrase).toBe('pp');
    expect(saved[0].proxy.pool_size).toBe(5);
    expect(saved[0].proxy.type).toBe('ssh');

    const { el: el2, saved: saved2 } = mountRules(sshConfig());
    await flush();
    submitForm(el2);
    await flush();
    expect(saved2[0].proxy.pool_size).toBe(0);
  });

  it('提交载荷：切回 socks5/http 时新字段置零值不残留（值在表单内保留）', async () => {
    const { el, saved } = mountRules(sshConfig({
      private_key: 'OLD-PEM',
      key_passphrase: 'old-pp',
      pool_size: 4,
      jump_servers: [{ server: 'bastion:22', username: 'u', password: 'p', private_key: '', key_passphrase: '' }]
    }));
    await flush();
    // 回显：ssh 字段从 GET customer 回填
    expect(el.querySelector('[data-field="proxyPrivateKey"]').value).toBe('OLD-PEM');
    expect(el.querySelector('[data-field="proxyKeyPassphrase"]').value).toBe('old-pp');
    expect(el.querySelector('[data-field="proxyPoolSize"]').value).toBe('4');
    expect(el.querySelector('[data-field="jumpServer"]').value).toBe('bastion:22');

    // 切到 socks5：字段隐藏但值保留
    setType(el, 'socks5');
    await flush();
    expect(el.querySelector('[data-ssh-fields]')).toBeNull();
    // 切回 ssh 不丢已填
    setType(el, 'ssh');
    await flush();
    expect(el.querySelector('[data-field="proxyPrivateKey"]').value).toBe('OLD-PEM');
    expect(el.querySelector('[data-field="proxyPoolSize"]').value).toBe('4');
    expect(el.querySelector('[data-field="jumpServer"]').value).toBe('bastion:22');

    // socks5 下提交：新字段置零值，避免后端残留旧值
    setType(el, 'socks5');
    await flush();
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
    expect(saved[0].proxy.type).toBe('socks5');
    expect(saved[0].proxy.private_key).toBe('');
    expect(saved[0].proxy.key_passphrase).toBe('');
    expect(saved[0].proxy.pool_size).toBe(0);
    expect(saved[0].proxy.jump_servers).toEqual([]);
  });

  it('指纹区块：GET 渲染列表（指纹截断+title 全文）、单删带参、清空裸 DELETE、操作后刷新', async () => {
    globalThis.fetch.mockResolvedValue(jsonResponse(knownHostsEnvelope(knownHostEntries())));
    const { el } = mountRules(sshConfig());
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith('/api/proxy/ssh/known-hosts', expect.anything());
    const row = el.querySelector('[data-known-host="bastion.example.com:22"]');
    expect(row).toBeTruthy();
    expect(row.textContent).toContain('bastion.example.com:22');
    expect(row.textContent).toContain('ssh-ed25519');
    // 指纹截断显示（前 20 字符+…），全文挂 title
    expect(row.textContent).toContain('SHA256:0123456789abc…');
    expect(row.textContent).not.toContain('SHA256:0123456789abcdefghijklmnopqrstuv');
    expect(row.querySelector('code').getAttribute('title')).toBe('SHA256:0123456789abcdefghijklmnopqrstuv');

    // 单条删除：DELETE 带 host/port 参数
    globalThis.fetch.mockClear();
    row.querySelector('[data-known-host-remove]').click();
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/ssh/known-hosts?host=bastion.example.com&port=22',
      expect.objectContaining({ method: 'DELETE' })
    );
    // 操作后刷新列表（清零后至少再 GET 一次）
    const getCalls = globalThis.fetch.mock.calls.filter(([path]) => path === '/api/proxy/ssh/known-hosts');
    expect(getCalls.length).toBeGreaterThanOrEqual(1);

    // 清空全部：confirm 后裸 DELETE（无查询参数）
    vi.spyOn(window, 'confirm').mockReturnValue(true);
    globalThis.fetch.mockClear();
    buttonByText(el, 'rules_knownHostsClear').click();
    await flush();
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/proxy/ssh/known-hosts',
      expect.objectContaining({ method: 'DELETE' })
    );
    expect(globalThis.fetch.mock.calls.some(([path]) => String(path).includes('?'))).toBe(false);
  });

  it('指纹区块：GET 失败静默降级为加载失败文案，不阻塞主表单', async () => {
    globalThis.fetch.mockRejectedValue(new Error('network down'));
    const { el, saved } = mountRules(sshConfig());
    await flush();
    expect(el.textContent).toContain('rules_knownHostsLoadFailed');
    // 主表单照常可提交
    submitForm(el);
    await flush();
    expect(saved).toHaveLength(1);
  });

  it('指纹区块：空列表显示空文案，不渲染清空按钮', async () => {
    const { el } = mountRules(sshConfig());
    await flush();
    expect(el.textContent).toContain('rules_knownHostsEmpty');
    expect(buttonByText(el, 'rules_knownHostsClear')).toBeFalsy();
  });

  it('i18n：SSH 新键 zh/en 同步存在', () => {
    const sshKeys = [
      'rules_proxyUsernamePlaceholderSsh',
      'rules_proxyPasswordPlaceholderSsh',
      'rules_proxyPrivateKey',
      'rules_proxyPrivateKeyPlaceholder',
      'rules_proxyKeyPassphrase',
      'rules_proxyKeyPassphrasePlaceholder',
      'rules_proxyPoolSize',
      'rules_proxyPoolSizePlaceholder',
      'rules_proxyJumpTitle',
      'rules_proxyJumpDesc',
      'rules_proxyJumpAdd',
      'rules_proxyJumpRemove',
      'rules_proxyJumpServerPlaceholder',
      'rules_proxyJumpExpandKey',
      'rules_proxyJumpCollapseKey',
      'rules_sshValidationErrorUsername',
      'rules_sshValidationErrorAuth',
      'rules_sshValidationErrorJumpServer',
      'rules_sshValidationErrorJumpUsername',
      'rules_sshValidationErrorJumpAuth',
      'rules_knownHostsTitle',
      'rules_knownHostsDesc',
      'rules_knownHostsEmpty',
      'rules_knownHostsLoadFailed',
      'rules_knownHostsRemove',
      'rules_knownHostsClear',
      'rules_knownHostsClearConfirm'
    ];
    for (const key of sshKeys) {
      expect(typeof zh[key], `zh.${key}`).toBe('string');
      expect(typeof en[key], `en.${key}`).toBe('string');
    }
  });
});
