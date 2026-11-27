import { describe, expect, it, vi } from 'vitest';

// SettingsPage.vue 求值连带 i18n loadLocale()(node 环境无 DOM),mock 掉该
// 依赖(t 原样回键,断言不硬编码文案;MeshSettings.test.js 同范式)。
vi.mock('../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import * as agentCoreSettingsModule from './agentCoreSettings';
import {
  agentCoreTuningToLineFormat,
  normalizeAgentCoreTuning,
  normalizePeerMCPConfig,
  validateAgentCoreTuning,
} from './agentCoreSettings';
import { buildCustomerSavePatch } from '../components/SettingsPage.vue';
import zh from '../i18n/zh';
import en from '../i18n/en';

// —— endpoint 二期 Task 8 退役:agentcore_llm(端点)层前端纯函数全删 ——
// spec §7.3/§9:出网端点由 endpoint 管理接管,Go POST 白名单保留接受该键
// (值被忽略避免 403),前端归一链同步摘键。
describe('agentcore_llm 前端纯函数退役', () => {
  it('三函数不再导出(若有活引用此处红——先摘引用再删)', () => {
    expect(agentCoreSettingsModule.normalizeAgentCoreLLMConfig).toBeUndefined();
    expect(agentCoreSettingsModule.validateAgentCoreLLMConfig).toBeUndefined();
    expect(agentCoreSettingsModule.applyAgentCoreLLMClearing).toBeUndefined();
  });

  it('行为调优/peer_mcp 四函数仍导出(退役只摘端点层,不动调优层)', () => {
    expect(typeof agentCoreSettingsModule.normalizeAgentCoreTuning).toBe('function');
    expect(typeof agentCoreSettingsModule.normalizePeerMCPConfig).toBe('function');
    expect(typeof agentCoreSettingsModule.validateAgentCoreTuning).toBe('function');
    expect(typeof agentCoreSettingsModule.agentCoreTuningToLineFormat).toBe('function');
  });
});

// —— 保存链不再读写 customer.agentcore_llm ——
// GET /api/config/customer 仍回显旧键(Go 白名单保留),前端归一链摘键后:
// 回显不再进 config 态(无伪 diff),patch 永不带该键(后端 deep-merge 保持
// 旧值不动,前端不再有能力写它——spec §9 评审修正⑥接受的终态)。
describe('agentcore_llm 保存链退役(SettingsPage normalize 链)', () => {
  const legacyCurrent = {
    agentcore_llm: { enabled: true, base_url: 'https://old.example.com', api_key: 'sk-old' },
    agentcore: { max_identical_tool_turns: 5 }
  };

  it('GET 回显携带旧键,归一化后不产出 diff(无改动的保存 patch 为 undefined)', () => {
    expect(buildCustomerSavePatch({}, legacyCurrent)).toBeUndefined();
  });

  it('载荷携带旧键(旧前端/手改载荷)时 patch 也不带 agentcore_llm 键', () => {
    const patch = buildCustomerSavePatch(
      { agentcore_llm: { enabled: false, base_url: '', api_key: '' }, agentcore: { max_identical_tool_turns: 9 } },
      legacyCurrent
    );
    expect(patch.agentcore).toEqual({ max_identical_tool_turns: 9 });
    expect(patch.agentcore_llm).toBeUndefined();
    expect('agentcore_llm' in patch).toBe(false);
  });
});

// —— i18n:「代理设置」→「网络设置」改名 + agentCore 端点键退役 ——
describe('Task 8 i18n 改名与退役键', () => {
  it('代理设置→网络设置(tab/子 tab/出口守卫提示语,zh/en 成对)', () => {
    expect(zh.settings_tabSettings).toBe('网络设置');
    expect(zh.settings_subProxy).toBe('网络设置');
    expect(en.settings_tabSettings).toBe('Network');
    expect(en.settings_subProxy).toBe('Network');
    expect(zh.egress_guardProxyMissing).toContain('网络设置');
    expect(zh.egress_guardProxyMissing).not.toContain('代理设置');
    expect(en.egress_guardProxyMissing).toContain('network settings');
    expect(en.egress_guardProxyMissing).not.toContain('proxy settings');
  });

  it('端点半卡退役的 i18n 键两边同删(parity 同步)', () => {
    const retired = [
      'agentCore_enable',
      'agentCore_baseUrl',
      'agentCore_baseUrlPlaceholder',
      'agentCore_apiKey',
      'agentCore_apiKeyPlaceholder',
      'agentCore_showKey',
      'agentCore_hideKey',
      'agentCore_error_urlRequired',
      'agentCore_error_urlInvalid',
      'agentCore_error_keyRequired',
      'agentCore_error_scheme'
    ];
    for (const key of retired) {
      expect(key in zh).toBe(false);
      expect(key in en).toBe(false);
    }
  });

  it('卡片改名「内核调优」+ 新增旧配置灰字键,守卫阈值键保留', () => {
    expect(zh.agentCore_title).toBe('内核调优');
    expect(en.agentCore_title).toBe('Kernel tuning');
    expect(typeof zh.agentCore_legacyHint).toBe('string');
    expect(zh.agentCore_legacyHint.length).toBeGreaterThan(0);
    expect(typeof en.agentCore_legacyHint).toBe('string');
    expect(en.agentCore_legacyHint.length).toBeGreaterThan(0);
    expect(typeof zh.agentCore_guardTitle).toBe('string');
    expect(typeof en.agentCore_guardHint).toBe('string');
  });
});

// ---- customer.agentcore(行为调优) ----

describe('normalizeAgentCoreTuning', () => {
  it('非对象/缺键 → 0(= 未配置,与 Go nil 层等价)', () => {
    expect(normalizeAgentCoreTuning(undefined)).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning(null)).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning('oops')).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({})).toEqual({ max_identical_tool_turns: 0 });
  });

  it('合法数字原样,非法数字归一成 0(报错是 validate 的职责)', () => {
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 7 })).toEqual({ max_identical_tool_turns: 7 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 0 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: -3 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 1.5 })).toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: '7' })).toEqual({ max_identical_tool_turns: 0 });
  });
});

describe('validateAgentCoreTuning', () => {
  it('空串/纯空白 = 用默认,合法', () => {
    expect(validateAgentCoreTuning('')).toBe('');
    expect(validateAgentCoreTuning('   ')).toBe('');
    expect(validateAgentCoreTuning(undefined)).toBe('');
  });

  it('0 = 用默认,合法;1..50 合法', () => {
    expect(validateAgentCoreTuning('0')).toBe('');
    expect(validateAgentCoreTuning('1')).toBe('');
    expect(validateAgentCoreTuning('5')).toBe('');
    expect(validateAgentCoreTuning('50')).toBe('');
  });

  it('负数/非整数/越界/非数字 拒绝', () => {
    expect(validateAgentCoreTuning('-1')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('2.5')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('51')).toBe('agentCore_error_guardInvalid');
    expect(validateAgentCoreTuning('abc')).toBe('agentCore_error_guardInvalid');
  });
});

describe('agentCoreTuningToLineFormat', () => {
  it('空串/0 → 0(后端 omitempty 抹掉 = 回默认)', () => {
    expect(agentCoreTuningToLineFormat('')).toBe(0);
    expect(agentCoreTuningToLineFormat('0')).toBe(0);
    expect(agentCoreTuningToLineFormat(undefined)).toBe(0);
  });

  it('正整数原样,非法兜底归 0(提交前已被 validate 挡住)', () => {
    expect(agentCoreTuningToLineFormat('5')).toBe(5);
    expect(agentCoreTuningToLineFormat('50')).toBe(50);
    expect(agentCoreTuningToLineFormat('abc')).toBe(0);
  });
});

// 全局总闸键透传（2026-10-31 验收缺陷①）：normalizeAgentCoreTuning 的输出
// 经 normalizeCustomerConfig → buildCustomerConfigPatch 原样进 POST patch 与
// GET 回显——在这里剥掉的话，两张全局开关卡「保存 200 但永不落盘、刷新恒
// 显默认开」。本组用例锁死：布尔键透传、非布尔/缺键不产出键。
describe('normalizeAgentCoreTuning 全局总闸透传', () => {
  it('布尔键显式透传', () => {
    expect(normalizeAgentCoreTuning({ mcp_enabled: false }))
      .toEqual({ max_identical_tool_turns: 0, mcp_enabled: false });
    expect(normalizeAgentCoreTuning({ skills_enabled: true }))
      .toEqual({ max_identical_tool_turns: 0, skills_enabled: true });
    expect(normalizeAgentCoreTuning({ memory_enabled: false }))
      .toEqual({ max_identical_tool_turns: 0, memory_enabled: false });
    expect(normalizeAgentCoreTuning({ ai_mcp: false }))
      .toEqual({ max_identical_tool_turns: 0, ai_mcp: false });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 3, mcp_enabled: true, skills_enabled: false }))
      .toEqual({ max_identical_tool_turns: 3, mcp_enabled: true, skills_enabled: false });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 3, memory_enabled: true }))
      .toEqual({ max_identical_tool_turns: 3, memory_enabled: true });
    expect(normalizeAgentCoreTuning({ max_identical_tool_turns: 3, ai_mcp: true }))
      .toEqual({ max_identical_tool_turns: 3, ai_mcp: true });
    expect(normalizeAgentCoreTuning({ mcp_enabled: true, skills_enabled: false, memory_enabled: true }))
      .toEqual({ max_identical_tool_turns: 0, mcp_enabled: true, skills_enabled: false, memory_enabled: true });
    expect(normalizeAgentCoreTuning({ mcp_enabled: true, skills_enabled: false, memory_enabled: true, ai_mcp: false }))
      .toEqual({ max_identical_tool_turns: 0, mcp_enabled: true, skills_enabled: false, memory_enabled: true, ai_mcp: false });
  });

  it('非布尔值不透传', () => {
    expect(normalizeAgentCoreTuning({ mcp_enabled: 'false' }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ skills_enabled: null }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ mcp_enabled: 1 }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ memory_enabled: 'true' }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ memory_enabled: null }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ ai_mcp: 'true' }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ ai_mcp: null }))
      .toEqual({ max_identical_tool_turns: 0 });
    expect(normalizeAgentCoreTuning({ ai_mcp: 1 }))
      .toEqual({ max_identical_tool_turns: 0 });
  });

  it('缺键不产出键(=未配置,后端 nil 视为开启)', () => {
    const next = normalizeAgentCoreTuning({ max_identical_tool_turns: 2 });
    expect('mcp_enabled' in next).toBe(false);
    expect('skills_enabled' in next).toBe(false);
    expect('memory_enabled' in next).toBe(false);
    expect('ai_mcp' in next).toBe(false);
    const empty = normalizeAgentCoreTuning(undefined);
    expect('mcp_enabled' in empty).toBe(false);
    expect('skills_enabled' in empty).toBe(false);
    expect('memory_enabled' in empty).toBe(false);
    expect('ai_mcp' in empty).toBe(false);
  });
});

// peer_mcp 归一:布尔透传为线格式 enable;缺键/非对象/非布尔 → undefined
// (不进 patch 与回显,后端 nil 视为开启——三态与 mcp_enabled 同族)。
// ⚠️ 还必须在 SettingsPage.normalizeCustomerConfig 挂键,否则「保存 HTTP 200
// 但永不落盘、刷新恒显默认开」(2026-10-31 验收缺陷①同族)。
describe('normalizePeerMCPConfig', () => {
  it('四键显式布尔透传(总开关+三目标子键,2026-11-05 细分)', () => {
    expect(normalizePeerMCPConfig({ enable: false })).toEqual({ enable: false });
    expect(normalizePeerMCPConfig({ enable: true })).toEqual({ enable: true });
    expect(normalizePeerMCPConfig({
      enable: true, agentcore_enabled: true, claude_enabled: false, codex_enabled: true
    })).toEqual({ enable: true, agentcore_enabled: true, claude_enabled: false, codex_enabled: true });
  });

  it('子键缺键不产出(nil=跟随总开关,不清用户配置)', () => {
    expect(normalizePeerMCPConfig({ enable: true })).toEqual({ enable: true });
    expect(normalizePeerMCPConfig({ enable: true, claude_enabled: false }))
      .toEqual({ enable: true, claude_enabled: false });
  });

  it('缺键/非对象/非布尔 → undefined', () => {
    expect(normalizePeerMCPConfig(undefined)).toBeUndefined();
    expect(normalizePeerMCPConfig('x')).toBeUndefined();
    expect(normalizePeerMCPConfig({})).toBeUndefined();
    expect(normalizePeerMCPConfig({ enable: 'yes' })).toBeUndefined();
    expect(normalizePeerMCPConfig({ claude_enabled: 1 })).toBeUndefined();
  });

  it('数组输入 → undefined', () => {
    expect(normalizePeerMCPConfig([])).toBeUndefined();
    expect(normalizePeerMCPConfig([{ enable: true }])).toBeUndefined();
  });

  it('多余键剔除(只保留四键)', () => {
    expect(normalizePeerMCPConfig({ enable: false, junk: 1 })).toEqual({ enable: false });
    expect(normalizePeerMCPConfig({ enable: true, claude_enabled: true, junk: 'x' }))
      .toEqual({ enable: true, claude_enabled: true });
  });
});
