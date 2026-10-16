import { describe, expect, it, vi } from 'vitest';

// RulesSettings.vue 模块求值会连带执行 i18n 的 loadLocale()（读 localStorage，
// node 测试环境无 DOM）；本测试只测 sanitizeList 纯函数，mock 掉该依赖即可导入 SFC。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { sanitizeList, validateRuleEntry } from './RulesSettings.vue';

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
const probe = (v) => validateRuleEntry(v, (k) => k);

describe('RulesSettings validateRuleEntry geoip', () => {
  it('accepts single-country Clash form (with proxy tail)', () => {
    expect(probe('geoip,CN,proxy')).toBe('');
    expect(probe('geoip,JP')).toBe('');
    expect(probe('GEOIP,cn,proxy')).toBe('');
  });

  it('rejects comma multi-country form with + separator hint', () => {
    expect(probe('geoip,CN,HK,proxy')).toBe('rules_geoipCommaMulti');
    expect(probe('geoip,CN,HK')).toBe('rules_geoipCommaMulti');
    expect(probe('geoip,CN,hk')).toBe('rules_geoipCommaMulti');
  });

  it('rejects bare geoip keyword and missing/invalid country code', () => {
    expect(probe('geoip')).toBe('rules_geoipInvalid');
    expect(probe('geoip,')).toBe('rules_geoipInvalid');
    expect(probe('geoip,USA')).toBe('rules_geoipInvalid');
  });

  it('keeps colon-style geoip matrix intact', () => {
    expect(probe('geoip:CN+HK+US')).toBe('');
    expect(probe('geoip:cn')).toBe('');
    expect(probe('geoip:CN,HK')).toBe('rules_geoipCommaMulti');
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
    expect(probe('geoip,CN,HK,direct')).toBe('rules_geoipCommaMulti');
  });

  it('keeps legacy proxy and non-action tail semantics intact', () => {
    expect(probe('a.com,b.com,proxy')).toBe('');
    expect(probe('foo,bar')).toBe('');
    expect(probe('example.com,proxy-server')).toBe('');
  });
});
