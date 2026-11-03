import { describe, expect, it, vi } from 'vitest';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage，node 环境无 DOM），
// mock 掉后只测 paginateItems 纯函数（RulesSettings.test.js 同范式）。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import { paginateItems } from './AgentIntegrationsSettings.vue';

// 用户态 agent 子 tab 的 MCP / 技能列表分页（2026-11-04 用户要求）。
describe('paginateItems 集成报告列表分页', () => {
  const items = Array.from({ length: 25 }, (_, i) => ({ name: `item-${i + 1}` }));

  it('第 1 页返回前 pageSize 条', () => {
    const page = paginateItems(items, 1, 20);
    expect(page).toHaveLength(20);
    expect(page[0].name).toBe('item-1');
    expect(page[19].name).toBe('item-20');
  });

  it('第 2 页返回余量（不足 pageSize 也照返）', () => {
    const page = paginateItems(items, 2, 20);
    expect(page).toHaveLength(5);
    expect(page[0].name).toBe('item-21');
  });

  it('页码越界返回空数组（不抛错）', () => {
    expect(paginateItems(items, 9, 20)).toEqual([]);
  });

  it('页码 < 1 归一为第 1 页', () => {
    const page = paginateItems(items, 0, 20);
    expect(page).toHaveLength(20);
    expect(page[0].name).toBe('item-1');
  });

  it('空列表返回空数组', () => {
    expect(paginateItems([], 1, 20)).toEqual([]);
  });

  it('整除边界：恰好 N 页时最后一页满员', () => {
    const exact = Array.from({ length: 40 }, (_, i) => i);
    expect(paginateItems(exact, 2, 20)).toHaveLength(20);
  });
});
