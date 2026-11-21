import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// SFC 模块求值会执行 i18n loadLocale()（读 localStorage，node 环境无 DOM），
// mock 掉后只测导出纯函数（AgentActivitySettings.test.js 同范式；面板不深 mount）。
vi.mock('../../i18n', () => ({ useI18n: () => ({ t: (k) => k }) }));

import ListenerSettings, { listenerToneClass, normalizeListenerEvents, resolveListenerPanelRefresh } from './ListenerSettings.vue';

describe('listenerToneClass 徽标色调映射', () => {
  it('四档已知色调各得其所(与 listenerStatusBadge 的 tone 词表对齐)', () => {
    expect(listenerToneClass('ok')).toContain('bg-emerald-100');
    expect(listenerToneClass('warn')).toContain('bg-amber-100');
    expect(listenerToneClass('error')).toContain('bg-red-100');
    expect(listenerToneClass('muted')).toContain('bg-slate-100');
  });

  it('未知/缺省色调兜底 muted', () => {
    expect(listenerToneClass('bogus')).toBe(listenerToneClass('muted'));
    expect(listenerToneClass(undefined)).toBe(listenerToneClass('muted'));
  });

  it('每档都带暗色变体(面板深色主题不掉色)', () => {
    for (const tone of ['ok', 'warn', 'error', 'muted']) {
      expect(listenerToneClass(tone)).toMatch(/dark:bg-/);
      expect(listenerToneClass(tone)).toMatch(/dark:text-/);
    }
  });
});

describe('normalizeListenerEvents 事件区归一', () => {
  it('缺值/非数组载荷兜空数组(agentApi 已解包,非数组一律视为无事件)', () => {
    expect(normalizeListenerEvents(null)).toEqual([]);
    expect(normalizeListenerEvents(undefined)).toEqual([]);
    expect(normalizeListenerEvents({})).toEqual([]);
    expect(normalizeListenerEvents({ events: [] })).toEqual([]); // 信封残留形状不再认
  });

  it('透传 summary/fired_at 并生成稳定 key(同指纹去重不误伤)', () => {
    const items = normalizeListenerEvents([
      { listener_id: 'L-1', fingerprint: 'fp-a', fired_at: '2026-11-21T08:00:00Z', summary: '文件新增一行' },
      { listener_id: 'L-1', fingerprint: 'fp-a', fired_at: '2026-11-21T08:05:00Z', summary: '同指纹再触发' },
    ]);
    expect(items).toHaveLength(2);
    expect(items[0]).toEqual({
      key: 'L-1-fp-a-0',
      summary: '文件新增一行',
      fired_at: '2026-11-21T08:00:00Z',
    });
    expect(items[1].key).not.toBe(items[0].key); // 同 listener_id+fingerprint 也得两个 key
  });

  it('截最近 20 条(§1.5 契约:侧栏展示上限)', () => {
    const events = Array.from({ length: 30 }, (_, i) => ({
      listener_id: 'L-1',
      fingerprint: `fp-${i}`,
      fired_at: `2026-11-21T08:00:${String(i).padStart(2, '0')}Z`,
      summary: `s-${i}`,
    }));
    const items = normalizeListenerEvents(events);
    expect(items).toHaveLength(20);
    expect(items[0].summary).toBe('s-0');
  });

  it('畸形条目不炸:summary/fired_at 缺失归位空串', () => {
    const items = normalizeListenerEvents([{ listener_id: 'L-1' }, null]);
    expect(items).toEqual([
      { key: 'L-1--0', summary: '', fired_at: '' },
      { key: '--1', summary: '', fired_at: '' },
    ]);
  });
});

// --- Task 17 刷新钩子：listener.triggered 窗事件（listener-panel-refresh）消费 ---

describe('resolveListenerPanelRefresh 窗事件决策纯函数', () => {
  it('命中行正展开：列表重载 + 该行事件区重拉', () => {
    expect(resolveListenerPanelRefresh({ listener_id: 'L-1' }, 'L-1')).toEqual({
      reloadList: true,
      reloadEventsId: 'L-1',
    });
  });

  it('展开的是别的行/无展开：只重载列表，不打扰错位行的事件区缓存', () => {
    expect(resolveListenerPanelRefresh({ listener_id: 'L-2' }, 'L-1')).toEqual({
      reloadList: true,
      reloadEventsId: '',
    });
    expect(resolveListenerPanelRefresh({ listener_id: 'L-2' }, '')).toEqual({
      reloadList: true,
      reloadEventsId: '',
    });
  });

  it('畸形 detail（缺 listener_id）双否：畸形窗事件零动作', () => {
    expect(resolveListenerPanelRefresh(undefined, 'L-1')).toEqual({ reloadList: false, reloadEventsId: '' });
    expect(resolveListenerPanelRefresh({}, 'L-1')).toEqual({ reloadList: false, reloadEventsId: '' });
    expect(resolveListenerPanelRefresh({ listener_id: 7 }, 'L-1')).toEqual({ reloadList: false, reloadEventsId: '' });
  });
});

describe('onPanelRefresh 方法接线（不深 mount，methods 直呼 + 假 this）', () => {
  it('命中展开行：loadListeners + loadEvents 双发且参数正确', () => {
    const ctx = { expandedId: 'L-1', loadListeners: vi.fn(), loadEvents: vi.fn() };
    ListenerSettings.methods.onPanelRefresh.call(ctx, {
      detail: { listener_id: 'L-1', title: 't', summary: 's' },
    });
    expect(ctx.loadListeners).toHaveBeenCalledTimes(1);
    expect(ctx.loadEvents).toHaveBeenCalledTimes(1);
    expect(ctx.loadEvents).toHaveBeenCalledWith('L-1');
  });

  it('非命中/畸形事件：不触碰事件区（列表重载与否随决策纯函数）', () => {
    const ctx = { expandedId: 'L-9', loadListeners: vi.fn(), loadEvents: vi.fn() };
    ListenerSettings.methods.onPanelRefresh.call(ctx, { detail: { listener_id: 'L-1' } });
    expect(ctx.loadListeners).toHaveBeenCalledTimes(1);
    expect(ctx.loadEvents).not.toHaveBeenCalled();
    ListenerSettings.methods.onPanelRefresh.call(ctx, {});
    expect(ctx.loadListeners).toHaveBeenCalledTimes(1); // 畸形：连列表也不重载
    expect(ctx.loadEvents).not.toHaveBeenCalled();
  });
});

describe('mounted/beforeUnmount 窗监听挂摘（listener-panel-refresh 缝名契约）', () => {
  let addSpy;
  let removeSpy;
  beforeEach(() => {
    addSpy = vi.spyOn(window, 'addEventListener');
    removeSpy = vi.spyOn(window, 'removeEventListener');
  });
  afterEach(() => {
    addSpy.mockRestore();
    removeSpy.mockRestore();
  });

  it('挂载注册、卸载以同一函数引用摘除（与 useAliangChat 派发缝名一致）', () => {
    const ctx = {
      loadListeners: vi.fn(),
      pollTimer: null,
      // Vue 实例化时会把 methods 绑上 this；假 ctx 手动带上同一方法引用，
      // mounted 里 this.onPanelRefresh 才与真实组件同形。
      onPanelRefresh: ListenerSettings.methods.onPanelRefresh,
    };
    ListenerSettings.mounted.call(ctx);
    const registered = addSpy.mock.calls.filter(([name]) => name === 'listener-panel-refresh');
    expect(registered).toHaveLength(1);
    expect(typeof registered[0][1]).toBe('function');
    // mounted 还起 5s 轮询定时器，假 ctx 直接借用真 window 定时器——卸载段
    // 前手动清掉，避免泄漏到后续用例。
    ListenerSettings.beforeUnmount.call(ctx);
    if (ctx.pollTimer) window.clearInterval(ctx.pollTimer);
    const removed = removeSpy.mock.calls.filter(([name]) => name === 'listener-panel-refresh');
    expect(removed).toHaveLength(1);
    expect(removed[0][1]).toBe(registered[0][1]); // 同一引用：不会摘错/留挂
  });
});
