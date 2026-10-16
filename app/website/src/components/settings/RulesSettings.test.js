import { describe, expect, it, vi } from 'vitest';

// RulesSettings.vue 模块求值会连带执行 i18n 的 loadLocale()（读 localStorage，
// node 测试环境无 DOM）；本测试只测 sanitizeList 纯函数，mock 掉该依赖即可导入 SFC。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import {
  detectCommaCollapseIssue,
  detectGeoipCommaIssue,
  extractProxyRuleDomain,
  findRuleOverlaps,
  sanitizeList,
  validateRuleEntry
} from './RulesSettings.vue';

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
