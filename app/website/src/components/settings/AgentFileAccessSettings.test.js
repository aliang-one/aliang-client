import { describe, expect, it, vi } from 'vitest';

// 模块求值会连带执行 i18n 的 loadLocale()（读 localStorage，node 环境无 DOM）；
// 本测试只测组件里的纯逻辑，mock 掉该依赖即可导入 SFC。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import Comp from './AgentFileAccessSettings.vue';

// 用 options 对象直接调用方法/computed，不挂载 DOM（挂载需要 jsdom + 交互，
// 这里要守的是「状态 → 文案/样式」的映射不出错）。
const ctx = (over = {}) => ({
  snapshot: { supported: true, attended: true, state: 'idle', categories: [], ...(over.snapshot || {}) },
  t: (key, params) => (params ? `${key}:${JSON.stringify(params)}` : key),
  ...Comp.methods,
  ...over,
});

describe('AgentFileAccessSettings 状态映射', () => {
  it('每个状态都有对应的 i18n key，绝不出现 undefined 文案', () => {
    const states = ['granted', 'denied', 'absent', 'pending', 'skipped', 'failed', 'unattended', 'unsupported', 'unknown'];
    const c = ctx();
    for (const s of states) {
      expect(c.stateLabelKey(s)).toBe(`agent_fileAccess_state_${s}`);
    }
    // 空状态回落 unknown，避免 undefined 拼进 key
    expect(c.stateLabelKey('')).toBe('agent_fileAccess_state_unknown');
  });

  it('每个类别都有 i18n key（与后端 Category.Key 对齐）', () => {
    const keys = ['desktop', 'documents', 'downloads', 'trash', 'music', 'pictures', 'icloud', 'cloudstorage'];
    const c = ctx();
    for (const k of keys) {
      expect(c.categoryLabelKey(k)).toBe(`agent_fileAccess_cat_${k}`);
    }
  });

  it('granted / denied / pending 的图标与徽标各自区分', () => {
    const c = ctx();
    expect(c.iconName('granted')).toBe('check_circle');
    expect(c.iconName('denied')).toBe('block');
    expect(c.iconName('pending')).toBe('hourglass_top');
    expect(c.badgeClass('granted')).toContain('emerald');
    expect(c.badgeClass('denied')).toContain('rose');
    expect(c.badgeClass('pending')).toContain('amber');
  });
});

describe('AgentFileAccessSettings 关键保护', () => {
  it('无人值守时「立即授权」必须不可点（绝不制造无人应答的系统弹窗）', () => {
    const c = ctx({ snapshot: { supported: true, attended: false } });
    expect(Comp.computed.canRequest.call({ snapshot: c.snapshot, busy: false })).toBe(false);
  });

  it('不支持时同样不可点', () => {
    const c = ctx({ snapshot: { supported: false, attended: true } });
    expect(Comp.computed.canRequest.call({ snapshot: c.snapshot, busy: false })).toBe(false);
  });

  it('支持且有人值守时可点', () => {
    const c = ctx();
    expect(Comp.computed.canRequest.call({ snapshot: c.snapshot, busy: false })).toBe(true);
  });

  it('有 denied 类别时才提示「去系统设置」', () => {
    expect(Comp.computed.hasDenied.call({ snapshot: { categories: [{ state: 'granted' }] } })).toBe(false);
    expect(Comp.computed.hasDenied.call({ snapshot: { categories: [{ state: 'denied' }] } })).toBe(true);
  });

  it('汇总按状态计数', () => {
    const c = ctx({
      snapshot: {
        categories: [
          { state: 'granted' },
          { state: 'granted' },
          { state: 'denied' },
          { state: 'absent' },
        ],
      },
    });
    const out = Comp.computed.summaryText.call(c);
    expect(out).toContain('"granted":2');
    expect(out).toContain('"denied":1');
    expect(out).toContain('"absent":1');
    expect(out).toContain('"total":4');
  });
});
