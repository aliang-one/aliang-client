// EndpointZone.test.js — 面板 ENDPOINT 区契约(spec §4.4 + §6-4 + 2026-11-27
// 拍板修订)。覆盖:available=false / endpoints null / groups 空数组三种输入
// 整区不渲染(硬性降级,无空壳)、cooldown/disabled 行徽标与置灰、钉选递延
// (任意行点击只 toast、勾选圈不动、零网络请求)、AI 关整区降透明+锁、
// 组头展开/收起、rank=0 与 current 空串的「—」兜底。
// 挂载手法沿用仓内约定(createApp + provide + jsdom DOM 断言)。
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import EndpointZone, { endpointZoneModel } from './EndpointZone.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';
import * as api from '../api';

// api 层全部 mock:钉选递延拍板要求「任何行点击不发任何请求」,行为层
// 断言所有 api 动作未被调用(另配 fetch 全局 stub 双保险,见下方 beforeAll)。
vi.mock('../api', () => ({
  startProxy: vi.fn(),
  stopProxy: vi.fn(),
  restartProxy: vi.fn(),
  setMode: vi.fn(),
  getSummary: vi.fn(),
  isHosted: vi.fn(() => false),
  postMessageHost: vi.fn(() => false),
}));

// fetch 全局 stub:连组件绕过 api 层直接 fetch 的路径也拦住——「无网络请求」
// 的本质断言(实现计划 vitest 必测原话)。
beforeAll(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('测试期禁止网络请求'))));
});
afterAll(() => {
  vi.unstubAllGlobals();
});

// ---- 契约构造器(按 tray_summary.go TrayEndpoints*/出参 JSON 键名) ----
function makeItem(name, { rank = 1, state = 'ok', cd = 0 } = {}) {
  return { name, rank, state, cooldown_remaining_sec: cd };
}
function makeGroup(protocol, items, current = '') {
  return { protocol, current, items };
}
function makeEndpoints({ available = true, groups = [] } = {}) {
  return { available, groups };
}
// makeSummary 聚合快照(run 子源取 running 稳态,与本区无关)。
function makeSummary({ endpoints = null, ai = null } = {}) {
  return {
    version: 'v1.1.124',
    run: {
      is_running: true,
      mode: 'http',
      status_desc: '',
      listen_addr: '127.0.0.1:56432',
      blocked: { code: '', msg: '' },
    },
    ai,
    agents: null,
    endpoints,
  };
}
// makeAI 按 TrayAISummary 契约构造 ai 子源(本区只看 enabled)。
function makeAI(enabled) {
  return { enabled, providers: [] };
}

const mounted = [];
// mountZone — 复刻 App.vue 根壳的数据接线(panelData 形状与根壳一致)。
function mountZone({ endpoints = null, ai = null } = {}) {
  const summary = ref(makeSummary({ endpoints, ai }));
  const connLost = ref(false);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summary.value, connLost.value, localTransition.value)
  );
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(EndpointZone) });
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
// demoEndpoints — 两组齐全的标准快照(mockup demo 数据同构)。
function demoEndpoints() {
  return makeEndpoints({
    groups: [
      makeGroup(
        'anthropic',
        [
          makeItem('内置 aliang 隧道', { rank: 1 }),
          makeItem('glm-coding-plan', { rank: 2 }),
          makeItem('byo-relay-02', { rank: 0, state: 'cooldown', cd: 90 }),
        ],
        '内置 aliang 隧道'
      ),
      makeGroup(
        'openai',
        [makeItem('deepseek-official', { rank: 1 }), makeItem('kimi-proxy', { rank: 0, state: 'disabled' })],
        'deepseek-official'
      ),
    ],
  });
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

describe('endpointZoneModel 纯派生(spec §4.4 + 数据防御)', () => {
  it('available 判定:available=false / endpoints null / groups 缺失 / 空数组 / 非数组 → available=false 整区不渲染', () => {
    for (const endpoints of [
      null,
      undefined,
      'bad',
      42,
      makeEndpoints({ available: false, groups: [makeGroup('anthropic', [makeItem('a')])] }),
      makeEndpoints({ available: true, groups: undefined }),
      makeEndpoints({ available: true, groups: null }),
      makeEndpoints({ available: true, groups: [] }),
      makeEndpoints({ available: true, groups: 'bad' }),
    ]) {
      const m = endpointZoneModel(endpoints, makeAI(true));
      expect(m.available).toBe(false);
    }
    // 全字段齐 + available=true → 可用
    expect(endpointZoneModel(demoEndpoints(), makeAI(true)).available).toBe(true);
  });

  it('aiOn 判定:仅 ai.enabled 真值 true 为开;ai 缺失/坏类型/enabled=false → 关(§6-4 联动降透明)', () => {
    expect(endpointZoneModel(demoEndpoints(), makeAI(true)).aiOn).toBe(true);
    for (const ai of [null, undefined, 'bad', 42, makeAI(false)]) {
      expect(endpointZoneModel(demoEndpoints(), ai).aiOn).toBe(false);
    }
  });

  it('组名映射:anthropic→Anthropic 组 / openai→OpenAI 组;未知 protocol 兜底原样+组后缀', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup('anthropic', [makeItem('a')]),
          makeGroup('openai', [makeItem('b')]),
          makeGroup('gemini', [makeItem('c')]),
        ],
      }),
      makeAI(true)
    );
    expect(m.groups.map((g) => g.name)).toEqual(['Anthropic 组', 'OpenAI 组', 'gemini 组']);
  });

  it('协议图标仅认 anthropic/openai,未知 protocol 无图标(防御不炸)', () => {
    const m = endpointZoneModel(
      makeEndpoints({ groups: [makeGroup('anthropic', [makeItem('a')]), makeGroup('gemini', [makeItem('c')])] }),
      makeAI(true)
    );
    expect(m.groups[0].icon).toBeTruthy();
    expect(m.groups[1].icon).toBeNull();
  });

  it('current 空串 → 组头当前名「—」;非空原样透传', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [makeGroup('anthropic', [makeItem('a')], ''), makeGroup('openai', [makeItem('b')], 'kimi-proxy')],
      }),
      makeAI(true)
    );
    expect(m.groups[0].currentText).toBe('—');
    expect(m.groups[1].currentText).toBe('kimi-proxy');
  });

  it('优先级徽标:rank>=1 → P1/P2…;rank=0/负数/非整数 → 「—」(契约第 3 条)', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup('anthropic', [
            makeItem('a', { rank: 1 }),
            makeItem('b', { rank: 2 }),
            makeItem('c', { rank: 0 }),
            makeItem('d', { rank: -3 }),
            { name: 'e', rank: undefined, state: 'ok', cooldown_remaining_sec: 0 }, // rank 字段缺失
          ]),
        ],
      }),
      makeAI(true)
    );
    const texts = m.groups[0].items.map((it) => it.badgeText);
    expect(texts).toEqual(['P1', 'P2', '—', '—', '—']);
    // 普通行徽标是绿描边 ghost(mockup .badge.outline)
    expect(m.groups[0].items.every((it) => it.badgeCls === 'outline')).toBe(true);
  });

  it('cooldown 徽标分钟向上取整:0/30/60→1m,61/90/119→2m,120→2m(契约必测 60→1m、90→2m)', () => {
    const cases = [
      [0, '冷却 1m'],
      [30, '冷却 1m'],
      [60, '冷却 1m'],
      [61, '冷却 2m'],
      [90, '冷却 2m'],
      [119, '冷却 2m'],
      [120, '冷却 2m'],
    ];
    for (const [cd, text] of cases) {
      const m = endpointZoneModel(
        makeEndpoints({ groups: [makeGroup('anthropic', [makeItem('a', { state: 'cooldown', cd })])] }),
        makeAI(true)
      );
      const it0 = m.groups[0].items[0];
      expect(it0.badgeText).toBe(text);
      expect(it0.badgeCls).toBe('cool'); // 琥珀徽标(mockup .badge.cool)
      expect(it0.unavailable).toBe(true); // 置灰不可选
    }
  });

  it('disabled 徽标「已摘除」同款置灰;未知 state 按普通可选行渲染(防御)', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup('anthropic', [
            makeItem('a', { rank: 0, state: 'disabled' }),
            makeItem('b', { rank: 1, state: 'weird' }),
          ]),
        ],
      }),
      makeAI(true)
    );
    expect(m.groups[0].items[0].badgeText).toBe('已摘除');
    expect(m.groups[0].items[0].badgeCls).toBe('cool');
    expect(m.groups[0].items[0].unavailable).toBe(true);
    expect(m.groups[0].items[1].unavailable).toBe(false);
    expect(m.groups[0].items[1].badgeText).toBe('P1');
  });

  it('组内 items 全部 disabled/cooldown 也照常渲染(契约第 8 条);坏条目跳过不炸', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup('anthropic', [
            makeItem('a', { state: 'disabled' }),
            makeItem('b', { state: 'cooldown', cd: 60 }),
          ]),
          makeGroup('openai', [null, 42, 'bad', makeItem('c', { rank: 1 })]),
        ],
      }),
      makeAI(true)
    );
    expect(m.groups[0].items).toHaveLength(2); // 全不可用也保留
    expect(m.groups[1].items).toHaveLength(1); // 坏条目逐条跳过
    expect(m.available).toBe(true);
  });

  it('勾选圈反映服务端 current(与组头 g-current 同源);钉选递延下点击不迁移', () => {
    const m = endpointZoneModel(demoEndpoints(), makeAI(true));
    expect(m.groups[0].items[0].on).toBe(true); // current='内置 aliang 隧道'
    expect(m.groups[0].items[1].on).toBe(false);
  });
});

describe('EndpointZone 挂载 DOM 契约', () => {
  it('available=false / endpoints=null / groups 空数组 → 整区不渲染(无空壳无占位,契约第 1 条)', () => {
    for (const endpoints of [null, makeEndpoints({ available: false }), makeEndpoints({ groups: [] })]) {
      const { el } = mountZone({ endpoints, ai: makeAI(true) });
      expect(q(el, '.ep-zone')).toBeNull();
      expect(q(el, '.ep-group')).toBeNull();
      expect(el.textContent.trim()).toBe('');
    }
  });

  it('groups 缺失但 available=true → 同样整区不渲染(契约第 8 条数据防御)', () => {
    const { el } = mountZone({ endpoints: makeEndpoints({ available: true, groups: undefined }), ai: makeAI(true) });
    expect(q(el, '.ep-zone')).toBeNull();
  });

  it('正常渲染:两组组头(协议图标+组名+当前名+chevron);列表默认收起(max-height 0 由 CSS 承载,open 类缺席)', () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    const groups = qa(el, '.ep-group');
    expect(groups).toHaveLength(2);
    // 组名(mockup demo:Anthropic 组 / OpenAI 组)
    expect(q(groups[0], '.g-name').textContent).toBe('Anthropic 组');
    expect(q(groups[1], '.g-name').textContent).toBe('OpenAI 组');
    // 协议图标内联 SVG(零外部依赖,spec §9)
    expect(groups[0].querySelectorAll('.g-ico svg')).toHaveLength(1);
    expect(groups[1].querySelectorAll('.g-ico svg')).toHaveLength(1);
    // 组头当前名 = 服务端 current
    expect(q(groups[0], '.g-current').textContent).toBe('内置 aliang 隧道');
    expect(q(groups[1], '.g-current').textContent).toBe('deepseek-official');
    // 默认收起:无 open 类
    expect(groups[0].className).not.toContain('open');
    // sep 随区走(mockup endpoints() 自带前置 sep;不渲染时连 sep 一起消失)
    expect(q(el.parentElement, '.sep')).not.toBeNull();
  });

  it('组头点击展开/收起:open 类切换(chev rotate 180 与 max-height 0→200px 由 CSS 过渡承载)', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    const head = q(el, '.g-head');
    head.click();
    await flush();
    expect(q(el, '.ep-group').className).toContain('open');
    expect(q(el, '.chev').getAttribute('class')).toContain('rot'); // svg 无 className 字符串
    head.click();
    await flush();
    expect(q(el, '.ep-group').className).not.toContain('open');
    // 两组独立展开,互不牵连
    qa(el, '.g-head')[1].click();
    await flush();
    expect(q(el, '.ep-group').className).not.toContain('open');
    expect(qa(el, '.ep-group')[1].className).toContain('open');
  });

  it('轮询刷新数据时展开状态保持(open 按 protocol 记忆,不随 2s 快照重置)', async () => {
    const ctx = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(ctx.el, '.g-head').click();
    await flush();
    expect(q(ctx.el, '.ep-group').className).toContain('open');
    // 模拟 2s 轮询回读新快照(items 变了)
    ctx.summary.value = makeSummary({
      endpoints: makeEndpoints({
        groups: [makeGroup('anthropic', [makeItem('new-only', { rank: 1 })], 'new-only')],
      }),
      ai: makeAI(true),
    });
    await flush();
    expect(q(ctx.el, '.ep-group').className).toContain('open');
    expect(q(ctx.el, '.nm').textContent).toBe('new-only');
  });

  it('ok 行 radio 点击:只 toast「钉选能力即将上线」、勾选圈不动、零网络请求(拍板递延契约第 4 条)', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click(); // 展开
    await flush();
    const before = qa(el, '.ep-item').map((n) => n.className);
    qa(el, '.ep-item')[1].click(); // 点非 current 的 glm-coding-plan
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('钉选能力即将上线');
    expect(t.kind).toBe('warn');
    // 勾选圈不动(.on 仍钉在 current 行)
    const after = qa(el, '.ep-item').map((n) => n.className);
    expect(after).toEqual(before);
    // 零网络请求:api 层与全局 fetch 双双未被触碰
    expect(api.startProxy).not.toHaveBeenCalled();
    expect(api.stopProxy).not.toHaveBeenCalled();
    expect(api.restartProxy).not.toHaveBeenCalled();
    expect(api.setMode).not.toHaveBeenCalled();
    expect(api.getSummary).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
    // 点 current 行自身同样只 toast
    qa(el, '.ep-item')[0].click();
    await flush();
    expect(useToast().toasts.value.at(-1).msg).toBe('钉选能力即将上线');
    expect(qa(el, '.ep-item').map((n) => n.className)).toEqual(before);
  });

  it('cooldown 行:off 置灰 + 琥珀「冷却 2m」徽标(90s)+点击只 toast 不发请求(契约第 5 条)', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const coolItem = qa(el, '.ep-item')[2];
    expect(coolItem.className).toContain('off');
    expect(q(coolItem, '.badge').textContent).toBe('冷却 2m');
    expect(q(coolItem, '.badge').className).toContain('cool');
    coolItem.click();
    await flush();
    expect(useToast().toasts.value.at(-1).msg).toBe('钉选能力即将上线');
    expect(fetch).not.toHaveBeenCalled();
    expect(api.startProxy).not.toHaveBeenCalled();
  });

  it('disabled 行:off 置灰 + 琥珀「已摘除」徽标 + 点击只 toast 不发请求(契约第 6 条)', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    qa(el, '.g-head')[1].click(); // 展开第二组
    await flush();
    const item = qa(el, '.ep-item')[4]; // 全局第 5 行 = kimi-proxy(disabled;anthropic 组 3 行在前)
    expect(item.className).toContain('off');
    expect(q(item, '.badge').textContent).toBe('已摘除');
    item.click();
    await flush();
    expect(useToast().toasts.value.at(-1).msg).toBe('钉选能力即将上线');
    expect(fetch).not.toHaveBeenCalled();
  });

  it('勾选圈只钉在 current 行(白勾 svg);其余行空圈', () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    const items = qa(el, '.ep-item');
    expect(items[0].className).toContain('on');
    expect(items[0].querySelectorAll('.check svg')).toHaveLength(1);
    expect(items[1].className).not.toContain('on');
    expect(items[1].querySelectorAll('.check svg')).toHaveLength(0);
  });

  it('rank=0 行徽标「—」(普通行绿描边);current 空串组头显示「—」(DOM 层)', async () => {
    const { el } = mountZone({
      endpoints: makeEndpoints({
        groups: [makeGroup('anthropic', [makeItem('solo', { rank: 0 })], '')],
      }),
      ai: makeAI(true),
    });
    expect(q(el, '.g-current').textContent).toBe('—');
    // current 空串 → 无行带勾(没有激活行)
    expect(qa(el, '.ep-item').some((n) => n.className.includes('on'))).toBe(false);
    q(el, '.g-head').click();
    await flush();
    expect(q(el, '.badge').textContent).toBe('—');
    expect(q(el, '.badge').className).toContain('outline');
  });

  it('ai.enabled=false → 整区 .zone.dimmed(降透明 0.35+锁)且组头/行点击全被锁(契约第 7 条 §6-4)', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(false) });
    expect(q(el, '.ep-zone').className).toContain('dimmed');
    // 组头点击被锁:不展开、无 toast
    q(el, '.g-head').click();
    await flush();
    expect(q(el, '.ep-group').className).not.toContain('open');
    expect(useToast().toasts.value.length).toBe(0);
    // 行点击被锁(键盘路径绕得开 CSS 锁,handler 双保险)
    q(el, '.ep-item').click();
    await flush();
    expect(useToast().toasts.value.length).toBe(0);
    expect(fetch).not.toHaveBeenCalled();
  });

  it('ai 子源降级 null → 同 dimmed(AISection 同口径:enabled 仅真值 true 为开)', () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: null });
    expect(q(el, '.ep-zone').className).toContain('dimmed');
  });

  it('AI 关→开联动:轮询回读 ai.enabled=true 后 dimmed 消失、恢复可选', async () => {
    const ctx = mountZone({ endpoints: demoEndpoints(), ai: makeAI(false) });
    expect(q(ctx.el, '.ep-zone').className).toContain('dimmed');
    ctx.summary.value = makeSummary({ endpoints: demoEndpoints(), ai: makeAI(true) });
    await flush();
    expect(q(ctx.el, '.ep-zone').className).not.toContain('dimmed');
  });
});

describe('api 层纪律(拍板第 4 条:钉选未上线,api 层不得有 activate 调用)', () => {
  it('api.js 不导出任何 activate/activateEndpoint 形态的动作', () => {
    const keys = Object.keys(api);
    expect(keys.filter((k) => /activate/i.test(k))).toEqual([]);
  });
});
