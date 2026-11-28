// AgentZone.test.js — 面板 AI AGENT · 24H 区契约(spec §4.5 + 实现计划 Task 11)。
// 覆盖:kernels 缺失/null/空数组/全坏条目 → 整区不渲染(硬性降级,无空壳,
// 与 Task 10 同款)、统计芯片三枚且活跃 0 无活跃芯片、卡片点击展开/收起
// 明细(展开态随轮询保持)、明细条渲染(状态点/标题/来源/相对时间)、
// relativeTime 纯函数全档位 + 坏/未来时间戳容错。
// 挂载手法沿用仓内约定(createApp + provide + jsdom DOM 断言)。
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import AgentZone, { agentZoneModel, relativeTime } from './AgentZone.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';
import * as api from '../api';

// api 层 mock:本区只读展示、零请求(展开/收起是纯本地状态)。行为层断言
// api 动作全未被调用;fetch 全局 stub 双保险,拦住绕层直 fetch 的路径。
vi.mock('../api', () => ({
  startProxy: vi.fn(),
  stopProxy: vi.fn(),
  restartProxy: vi.fn(),
  setMode: vi.fn(),
  getSummary: vi.fn(),
  isHosted: vi.fn(() => false),
  postMessageHost: vi.fn(() => false),
}));

beforeAll(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('测试期禁止网络请求'))));
});
afterAll(() => {
  vi.unstubAllGlobals();
});

// ---- 契约构造器(按 tray_summary.go TrayAgents*/出参 JSON 键名) ----
// ts 为 unix 秒(服务端 traySessionUnixTs 出参口径),ageSec = 距今秒数。
function makeSession(title, { source = '网页', active = false, ageSec = 120 } = {}) {
  return { title, source, active, ts: Math.floor(Date.now() / 1000) - ageSec };
}
function makeKernel(name, { active = 0, idle = 0, total, sessions = [] } = {}) {
  return { name, active, idle, total_24h: total === undefined ? sessions.length : total, sessions };
}
function makeAgents(kernels) {
  return { kernels };
}
// makeSummary 聚合快照(run 子源取 running 稳态,与本区无关)。
function makeSummary({ agents = null } = {}) {
  return {
    version: 'v1.1.124',
    run: {
      is_running: true,
      mode: 'http',
      status_desc: '',
      listen_addr: '127.0.0.1:56432',
      blocked: { code: '', msg: '' },
    },
    ai: { enabled: true, providers: [] },
    agents,
    endpoints: null,
  };
}

const mounted = [];
// mountZone — 复刻 App.vue 根壳的数据接线(panelData 形状与根壳一致)。
function mountZone({ agents = null } = {}) {
  const summary = ref(makeSummary({ agents }));
  const connLost = ref(false);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summary.value, connLost.value, localTransition.value)
  );
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(AgentZone) });
  app.provide('panelData', { summary, connLost, panelState, localTransition });
  app.provide('panelToast', useToast());
  app.mount(el);
  mounted.push(app);
  return { el, summary };
}

// flush — 等派生重渲染落定。
async function flush() {
  await nextTick();
  await nextTick();
}

function q(el, sel) {
  return el.querySelector(sel);
}
function qa(el, sel) {
  return Array.from(el.querySelectorAll(sel));
}
// demoKernels — mockup demo 数据同构:两内核,一活跃一全空闲。
function demoKernels() {
  return [
    makeKernel('claude-code', {
      active: 1,
      idle: 1,
      total: 2,
      sessions: [makeSession('修复托盘面板', { source: '网页', active: true, ageSec: 30 }), makeSession('写周报', { source: '终端', ageSec: 3600 })],
    }),
    makeKernel('codex', {
      active: 0,
      idle: 1,
      total: 1,
      sessions: [makeSession('重构 api 层', { source: '手机', ageSec: 7200 })],
    }),
  ];
}

beforeEach(() => {
  useToast().clear();
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
  useToast().clear();
  vi.clearAllMocks();
});

describe('relativeTime 纯函数(导出可测,契约必测项)', () => {
  const NOW = 1_800_000_000; // 固定 now(unix 秒),边界档位全走显式入参

  it('全档位:<60s 刚刚 / <60min N 分钟前 / <24h N 小时前 / 否则 N 天前', () => {
    const cases = [
      [0, '刚刚'],
      [1, '刚刚'],
      [59, '刚刚'],
      [60, '1 分钟前'], // 60s 整点入分钟档
      [61, '1 分钟前'],
      [119, '1 分钟前'],
      [3599, '59 分钟前'],
      [3600, '1 小时前'], // 60min 整点入小时档
      [7200, '2 小时前'],
      [86399, '23 小时前'],
      [86400, '1 天前'], // 24h 整点入天档
      [172800, '2 天前'],
      [30 * 86400, '30 天前'],
    ];
    for (const [ageSec, want] of cases) {
      expect(relativeTime(NOW - ageSec, NOW)).toBe(want);
    }
  });

  it('未来时间戳容错:钳位按 0s 已过 → 「刚刚」(选定案,含 ms/s 单位错配的优雅降级)', () => {
    expect(relativeTime(NOW + 1, NOW)).toBe('刚刚');
    expect(relativeTime(NOW + 300, NOW)).toBe('刚刚');
  });

  it('坏时间戳容错:非有限数/非数字类型/≤0 → 空串(明细行只显示来源)', () => {
    for (const ts of [NaN, Infinity, -Infinity, 0, -5, '1719500000', null, undefined, {}, []]) {
      expect(relativeTime(ts, NOW)).toBe('');
    }
  });
});

describe('agentZoneModel 纯派生(spec §4.5 + 数据防御)', () => {
  it('available 判定:agents null/undefined/坏类型/kernels 缺失/null/空数组/非数组 → false 整区不渲染', () => {
    for (const agents of [
      null,
      undefined,
      'bad',
      42,
      [],
      {},
      makeAgents(undefined),
      makeAgents(null),
      makeAgents([]),
      makeAgents('bad'),
    ]) {
      expect(agentZoneModel(agents).available).toBe(false);
    }
    expect(agentZoneModel(makeAgents(demoKernels())).available).toBe(true);
  });

  it('kernels 条目全坏/缺 name → 逐条跳过;一个不剩 → available 收窄 false(不留空壳,I-1 同款)', () => {
    for (const kernels of [[null], [42], ['bad'], [{}], [{ name: '' }], [{ name: '  ' }], [null, 42]]) {
      expect(agentZoneModel(makeAgents(kernels)).available).toBe(false);
    }
    // 好坏混排:坏条目跳过,好条目保留
    const m = agentZoneModel(makeAgents([null, makeKernel('claude-code', { total: 0 }), 42]));
    expect(m.available).toBe(true);
    expect(m.kernels).toHaveLength(1);
    expect(m.kernels[0].name).toBe('claude-code');
  });

  it('芯片派生:活跃>0 且空闲>0 → 三枚(活跃 live/空闲/24h 共);24h 芯片取 total_24h 字段', () => {
    const m = agentZoneModel(makeAgents([makeKernel('k', { active: 2, idle: 3, total: 9 })]));
    expect(m.kernels[0].chips.map((c) => c.text)).toEqual(['活跃 2', '空闲 3', '24h 共 9']);
    expect(m.kernels[0].chips[0].cls).toBe('live');
    expect(m.kernels[0].chips[0].dot).toBe(true);
    expect(m.kernels[0].chips[1].dot).toBe(true);
    expect(m.kernels[0].chips[1].dimDot).toBe(true); // mockup 空闲 cdot opacity 0.4
    expect(m.kernels[0].chips[2].dot).toBe(false); // 24h 芯片无圆点(mockup .num)
  });

  it('活跃为 0 → 无活跃芯片(契约原文);空闲为 0 → 同款隐藏(mockup 原值口径 runN/idleN 双判空)', () => {
    // active=0, idle=1 → 两枚:空闲 + 24h
    const m0 = agentZoneModel(makeAgents([makeKernel('k', { active: 0, idle: 1, total: 1 })]));
    expect(m0.kernels[0].chips.map((c) => c.text)).toEqual(['空闲 1', '24h 共 1']);
    // active=0, idle=0 → 仅 24h 一枚
    const m00 = agentZoneModel(makeAgents([makeKernel('k', { active: 0, idle: 0, total: 0 })]));
    expect(m00.kernels[0].chips.map((c) => c.text)).toEqual(['24h 共 0']);
  });

  it('数值字段坏类型容错:active/idle/total_24h 非数字按 0(不炸、不显示 NaN)', () => {
    const m = agentZoneModel(
      makeAgents([makeKernel('k', { active: '2', idle: null, total: '9', sessions: [makeSession('t')] })])
    );
    expect(m.kernels[0].chips.map((c) => c.text)).toEqual(['24h 共 0']);
  });

  it('sessions 归一:坏条目跳过;title/source 认字符串;active 仅真值 true;meta=「来源 · 相对时间」', () => {
    const m = agentZoneModel(
      makeAgents([
        makeKernel('k', {
          sessions: [
            null,
            42,
            makeSession('标题一', { source: '网页', active: true, ageSec: 30 }),
            makeSession('标题二', { source: '终端', ageSec: 120 }),
            { title: '坏 ts', source: '手机', active: false, ts: 'oops' },
            { title: '缺 source', active: false, ts: Math.floor(Date.now() / 1000) - 60 },
          ],
        }),
      ])
    );
    const rows = m.kernels[0].sessions;
    expect(rows).toHaveLength(4); // 坏条目(null/42)跳过
    expect(rows[0]).toEqual({ title: '标题一', meta: '网页 · 刚刚', active: true });
    expect(rows[1]).toEqual({ title: '标题二', meta: '终端 · 2 分钟前', active: false });
    expect(rows[2]).toEqual({ title: '坏 ts', meta: '手机', active: false }); // 坏 ts → 只剩来源
    expect(rows[3]).toEqual({ title: '缺 source', meta: '1 分钟前', active: false }); // 缺 source → 只剩时间(ts=now-60 恰过分钟档边界)
  });
});

describe('AgentZone 挂载 DOM 契约', () => {
  it('kernels 缺失/null/空数组/全坏 → 整区不渲染(无空壳无占位,契约第 5 条)', () => {
    for (const agents of [null, makeAgents(undefined), makeAgents([]), makeAgents([null])]) {
      const { el } = mountZone({ agents });
      expect(q(el, '.agent-zone')).toBeNull();
      expect(q(el, '.agent-card')).toBeNull();
      expect(el.textContent.trim()).toBe('');
    }
  });

  it('正常渲染:分区标题「AI AGENT · 24H」+ 每内核一张卡片(🤖 图标+内核名+右箭头)+ 尾随 sep', () => {
    const { el } = mountZone({ agents: makeAgents(demoKernels()) });
    expect(q(el, '.sec-head').textContent).toBe('AI AGENT · 24H');
    const cards = qa(el, '.agent-card');
    expect(cards).toHaveLength(2);
    expect(q(cards[0], '.name').textContent).toBe('claude-code');
    expect(q(cards[1], '.name').textContent).toBe('codex');
    // 🤖 图标为内联 SVG(零外部依赖,spec §9)
    expect(cards[0].querySelectorAll('.a-ico svg')).toHaveLength(1);
    // 右箭头 chevron 内联 SVG,默认未旋转(无 rot 类)
    expect(cards[0].querySelectorAll('.chev')).toHaveLength(1);
    expect(cards[0].querySelector('.chev').getAttribute('class')).not.toContain('rot');
    // 默认收起:无 open 类(mockup demo 默认收起)
    expect(cards[0].className).not.toContain('open');
    // 尾随 sep 随整区显隐(实现层决定,与 Task 10 同款)
    expect(q(el.parentElement, '.sep')).not.toBeNull();
  });

  it('芯片 DOM:三枚且活跃芯片 live 绿底;活跃 0 的内核无活跃芯片(契约必测)', () => {
    const { el } = mountZone({ agents: makeAgents(demoKernels()) });
    const chips0 = qa(qa(el, '.agent-card')[0], '.stat-chip');
    expect(chips0.map((c) => c.textContent.trim())).toEqual(['活跃 1', '空闲 1', '24h 共 2']);
    expect(chips0[0].className).toContain('live');
    expect(chips0[1].className).not.toContain('live');
    const chips1 = qa(qa(el, '.agent-card')[1], '.stat-chip');
    expect(chips1.map((c) => c.textContent.trim())).toEqual(['空闲 1', '24h 共 1']);
    expect(chips1.some((c) => c.className.includes('live'))).toBe(false);
  });

  it('单行紧凑化(2026-11-28 用户反馈):芯片行在 .top 卡头行内(紧跟内核名、右箭头之前),不留独立第二行', () => {
    const { el } = mountZone({ agents: makeAgents(demoKernels()) });
    for (const card of qa(el, '.agent-card')) {
      const top = q(card, '.top');
      const stats = q(card, '.stats');
      // 芯片容器是 .top 的直接子元素(单行),不是卡片的独立第二行
      expect(stats).not.toBeNull();
      expect(stats.parentElement).toBe(top);
      // 行序:图标 → 内核名 → 芯片 → 右箭头(箭头恒行尾;SVG 的 className
      // 是 SVGAnimatedString,按仓内惯例用 getAttribute('class') 断言)
      const kids = Array.from(top.children);
      expect(kids[0].className).toContain('a-ico');
      expect(kids[1].className).toContain('name');
      expect(kids[kids.length - 1].getAttribute('class')).toContain('chev');
      expect(kids.indexOf(stats)).toBeGreaterThan(kids.indexOf(kids[1]));
      // 收起态也应是单行:卡片下除 .top/.sess-list 外无其他行容器
      expect(Array.from(card.children).map((c) => c.className)).toEqual(['top', 'sess-list']);
    }
  });

  it('卡片点击展开/收起明细:open 类切换;明细条渲染(状态点 run/idle/标题/meta)', async () => {
    const { el } = mountZone({ agents: makeAgents(demoKernels()) });
    const card0 = qa(el, '.agent-card')[0];
    card0.click();
    await flush();
    expect(card0.className).toContain('open');
    expect(card0.querySelector('.chev').getAttribute('class')).toContain('rot');
    const rows = qa(card0, '.sess-item');
    expect(rows).toHaveLength(2);
    // 明细条 1:活跃会话 → run 点(绿带光晕由 CSS 承载)+ 标题 + 「网页 · 刚刚」
    expect(q(rows[0], '.sdot').className).toContain('run');
    expect(q(rows[0], '.st').textContent).toBe('修复托盘面板');
    expect(q(rows[0], '.sm').textContent).toBe('网页 · 刚刚');
    // 明细条 2:空闲会话 → idle 灰点 + 「终端 · 1 小时前」
    expect(q(rows[1], '.sdot').className).toContain('idle');
    expect(q(rows[1], '.sdot').className).not.toContain('run');
    expect(q(rows[1], '.sm').textContent).toBe('终端 · 1 小时前');
    // 再点收起
    card0.click();
    await flush();
    expect(card0.className).not.toContain('open');
    // 两卡独立展开,互不牵连
    qa(el, '.agent-card')[1].click();
    await flush();
    expect(card0.className).not.toContain('open');
    expect(qa(el, '.agent-card')[1].className).toContain('open');
  });

  it('轮询刷新数据时展开状态保持(open 按内核名记忆,不随 2s 快照重置)', async () => {
    const ctx = mountZone({ agents: makeAgents(demoKernels()) });
    q(ctx.el, '.agent-card').click();
    await flush();
    expect(q(ctx.el, '.agent-card').className).toContain('open');
    // 模拟 2s 轮询回读新快照(sessions 变了)
    ctx.summary.value = makeSummary({
      agents: makeAgents([
        makeKernel('claude-code', { active: 0, idle: 1, total: 1, sessions: [makeSession('全新会话', { ageSec: 10 })] }),
      ]),
    });
    await flush();
    expect(q(ctx.el, '.agent-card').className).toContain('open');
    expect(q(ctx.el, '.st').textContent).toBe('全新会话');
  });

  it('坏 ts 明细条:sm 只显示来源,无「 · 」尾巴(容错口径在 DOM 层成立)', () => {
    const { el } = mountZone({
      agents: makeAgents([makeKernel('k', { total: 1, sessions: [{ title: '坏 ts', source: '手机', active: false, ts: null }] })]),
    });
    q(el, '.agent-card').click();
    return flush().then(() => {
      expect(q(el, '.sm').textContent).toBe('手机');
    });
  });

  it('api 层纪律:本区零请求(展开/收起为纯本地状态)', async () => {
    const { el } = mountZone({ agents: makeAgents(demoKernels()) });
    q(el, '.agent-card').click();
    await flush();
    q(el, '.agent-card').click();
    await flush();
    expect(api.startProxy).not.toHaveBeenCalled();
    expect(api.stopProxy).not.toHaveBeenCalled();
    expect(api.restartProxy).not.toHaveBeenCalled();
    expect(api.setMode).not.toHaveBeenCalled();
    expect(api.getSummary).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });
});
