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
