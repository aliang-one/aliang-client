// EndpointZone.test.js — 面板 ENDPOINT 区契约(spec §4.4 + §6-4 + 钉选激活
// plan 2026-11-27 Task 3)。覆盖:available=false / endpoints null / groups
// 空数组三种输入整区不渲染(硬性降级,无空壳)、cooldown/disabled 行徽标与
// 置灰、radio 真激活(请求体用行真实 id / 乐观勾移 / 失败回滚 / 回读确认 /
// cooldown 成功 toast / rank=0 与 disabled 本地 toast 零请求 / 在途防重)、
// 勾选态按行字段推定不用 current==name(重名行不撞车)、AI 关整区降透明+锁、
// 组头展开/收起、rank=0 与 current 空串的「—」兜底。
// 挂载手法沿用仓内约定(createApp + provide + jsdom DOM 断言)。
// ⚠️ 本文件顶部的 vi.mock 只拦组件行为链路;真实 api.js 的契约(路径/body/
// 三层判失败/超时档)在 ../api.test.js 用真实模块直测——两边职责分家。
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import EndpointZone, { endpointZoneModel } from './EndpointZone.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';
import * as api from '../api';

// api 层全部 mock:组件行为测试只断言「组件怎么用 api」(调没调、参数是
// 什么),不碰网络;fetch 全局 stub 双保险,连绕过 api 层的直接 fetch 也拦。
vi.mock('../api', () => ({
  startProxy: vi.fn(),
  stopProxy: vi.fn(),
  restartProxy: vi.fn(),
  setMode: vi.fn(),
  activateEndpoint: vi.fn(),
  getSummary: vi.fn(),
  isHosted: vi.fn(() => false),
  postMessageHost: vi.fn(() => false),
}));

// fetch 全局 stub:连组件绕过 api 层直接 fetch 的路径也拦住——「无网络请求」
// 的本质断言(rank=0/disabled 行的零请求契约,实现计划 vitest 必测原话)。
beforeAll(() => {
  vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new Error('测试期禁止网络请求'))));
});
afterAll(() => {
  vi.unstubAllGlobals();
});

// ---- 契约构造器(按 tray_summary.go TrayEndpoints*/出参 JSON 键名) ----
// id=源行 ID 透传(钉选定位键):请求体断言必须用行真实 id,不能用名字。
function makeItem(name, { rank = 1, state = 'ok', cd = 0, id } = {}) {
  return { id: id === undefined ? `id-${name}` : id, name, rank, state, cooldown_remaining_sec: cd };
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
// mountZone — 复刻 App.vue 根壳的数据接线(panelData 形状与根壳一致,含
// refresh:钉选激活的回读确认复用同一条 summary 刷新管道)。refreshedEndpoints
// 给定时 refresh 完成后把 summary 换成该快照(模拟回读拿到新态);不给则
// refresh 为空操作(模拟轮询失败保留旧 summary——回读未发生的路径)。
function mountZone({ endpoints = null, ai = null, refreshedEndpoints } = {}) {
  const summary = ref(makeSummary({ endpoints, ai }));
  const connLost = ref(false);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summary.value, connLost.value, localTransition.value)
  );
  const refresh = vi.fn(async () => {
    if (refreshedEndpoints !== undefined) {
      summary.value = makeSummary({ endpoints: refreshedEndpoints, ai });
    }
  });
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(EndpointZone) });
  app.provide('panelData', { summary, connLost, panelState, localTransition, refresh });
  app.provide('panelToast', useToast());
  app.mount(el);
  mounted.push(app);
  return { el, summary, refresh };
}

// flush — 等派生重渲染落定。
async function flush() {
  await nextTick();
  await nextTick();
}
// flushAsync — 等整条异步链(点击 → api mock → refresh mock → toast/回滚)
// 与重渲染全部落定(全微任务,无定时器,4 次 nextTick 足够)。
async function flushAsync() {
  for (let i = 0; i < 4; i += 1) await nextTick();
}

function q(el, sel) {
  return el.querySelector(sel);
}
function qa(el, sel) {
  return Array.from(el.querySelectorAll(sel));
}
// demoEndpoints — 两组齐全的标准快照,刻意对齐 mockup 视觉 demo(§6-6 演示
// 的 P1/P2/「—」与 builtin 亮勾形态)。注意:mockup 里 builtin 展示为 P1 只是
// 视觉样板,后端不变量是 builtin priority 恒 0 → rank 恒 0(tray_summary.go
// 只给 auto_group ∧ priority>0 授 rank);真实语义锚见 realEndpoints。
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

// realEndpoints — builtin rank0 真实形态专项 fixture(评审 item I):后端不变量
// builtin priority 恒 0 → rank 恒 0;池行 glm rank1 承接勾选,冷却池行 byo
// rank2 可点击(rank>=1 且非 disabled)。demoEndpoints 保持 mockup 视觉演示
// 不动(改它牵连 >10 处按索引/文案的断言,且会破坏 mockup 对齐),真实语义
// 在这里钉死。
function realEndpoints() {
  return makeEndpoints({
    groups: [
      makeGroup(
        'anthropic',
        [
          makeItem('内置 aliang 隧道', { rank: 0 }),
          makeItem('glm-coding-plan', { rank: 1 }),
          makeItem('byo-relay-02', { rank: 2, state: 'cooldown', cd: 90 }),
        ],
        'glm-coding-plan'
      ),
    ],
  });
}

beforeEach(() => {
  useToast().clear();
  // resetAllMocks(见 afterEach)会把 beforeAll 里 fetch 的「拒网」实现一并
  // 重置,这里按每个用例重新武装——「零请求」断言之外也绝无真实网络。
  fetch.mockImplementation(() => Promise.reject(new Error('测试期禁止网络请求')));
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
  useToast().clear();
  // resetAllMocks 而非 clearAllMocks:never-resolve 的 mockImplementation
  // 会跨用例残留,下个用例「没显式设实现」时会静默返回旧 promise。
  vi.resetAllMocks();
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

  it('组数组全坏(groups 条目全坏对象)→ available=false 整区不渲染(I-1:available 收窄到过滤后,不留零子元素空壳)', () => {
    // groups 数组条目全坏(null/数字):available 开关开着,但过滤后一个组都不剩
    for (const groups of [[null], [42], [null, 42], ['bad', null]]) {
      expect(endpointZoneModel(makeEndpoints({ groups }), makeAI(true)).available).toBe(false);
    }
    // 注:有效组但组内 items 全坏(组头照渲染)是 M-1,评审记打磨清单刻意不修,
    // 不在本用例钉死
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
      ['90', '冷却 1m'], // 字符串兜底 0 → ceil(0/60)=0 → max(1,·)=1m(M-4:坏类型已定义行为)
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

  it('勾选圈按行字段推定(与组头 g-current 同义):池行里 rank 最小的 ok 行', () => {
    const m = endpointZoneModel(demoEndpoints(), makeAI(true));
    expect(m.groups[0].items[0].on).toBe(true); // rank1 ok = current 的池来源
    expect(m.groups[0].items[1].on).toBe(false);
  });

  it('重名行不撞车:同组两行同名,勾选只亮 rank 最小的 ok 行(不用 current==name 名字匹配)', () => {
    // 评审移交的注意事项:后端 current 仍是名字基,组内重名时名字匹配会让
    // 两行同时亮勾——按行自身字段推定(rank/id)消歧(id 是主键,重名不重 id)。
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup(
            'anthropic',
            [
              makeItem('twin', { rank: 1, id: 'twin-a' }),
              makeItem('twin', { rank: 2, id: 'twin-b' }),
            ],
            'twin'
          ),
        ],
      }),
      makeAI(true)
    );
    expect(m.groups[0].items[0].on).toBe(true);
    expect(m.groups[0].items[1].on).toBe(false);
  });

  it('池全冷却时勾选回落 builtin 兜底行(current 名字基回落,按 name 匹配 rank0 ok 行)', () => {
    const m = endpointZoneModel(
      makeEndpoints({
        groups: [
          makeGroup(
            'anthropic',
            [
              makeItem('内置 aliang 隧道', { rank: 0 }),
              makeItem('byo-relay-02', { rank: 1, state: 'cooldown', cd: 60 }),
            ],
            '内置 aliang 隧道'
          ),
        ],
      }),
      makeAI(true)
    );
    expect(m.groups[0].items[0].on).toBe(true); // builtin 兜底行亮勾
    expect(m.groups[0].items[1].on).toBe(false);
  });

  it('行 id 缺失(降级后端)→ 勾选回退名字匹配,不炸', () => {
    const eps = makeEndpoints({
      groups: [
        makeGroup('anthropic', [
          { name: 'a', rank: 1, state: 'ok', cooldown_remaining_sec: 0 }, // 无 id
          { name: 'b', rank: 2, state: 'ok', cooldown_remaining_sec: 0 },
        ], 'a'),
      ],
    });
    const m = endpointZoneModel(eps, makeAI(true));
    expect(m.groups[0].items[0].on).toBe(true);
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

  it('groups:[null] 全坏数组 → 整区不渲染无空壳(I-1 挂载层:available 过滤后收窄,连尾随 sep 一起消失)', () => {
    const { el } = mountZone({
      endpoints: makeEndpoints({ available: true, groups: [null] }),
      ai: makeAI(true),
    });
    expect(q(el, '.ep-zone')).toBeNull();
    expect(q(el, '.ep-group')).toBeNull();
    expect(el.textContent.trim()).toBe('');
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
    // sep 放区尾并随整区显隐(实现层决定,mockup 未含此 sep;整区不渲染时
    // 连 sep 一起消失,不留孤线/双线)
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

  it('ok 行 radio 点击 → 真激活:请求体用行真实 id,成功后勾移经 summary 回读确认 + 成功 toast', async () => {
    // 激活后快照(ActivateInGroup 语义):组内其余 auto 行 enabled=0 退清单,
    // 只剩目标池行(rank 收敛为组内最小)+builtin 兜底行,current=目标名。
    const refreshed = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [
            makeItem('内置 aliang 隧道', { rank: 0 }),
            makeItem('glm-coding-plan', { rank: 1 }),
          ],
          'glm-coding-plan'
        ),
        makeGroup('openai', [makeItem('deepseek-official', { rank: 1 })], 'deepseek-official'),
      ],
    });
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-glm-coding-plan' });
    const { el, refresh } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true), refreshedEndpoints: refreshed });
    q(el, '.g-head').click(); // 展开
    await flush();
    qa(el, '.ep-item')[1].click(); // 点 glm-coding-plan(非 current 行)
    await flushAsync();
    // 请求体断言:protocol=组协议,id=行真实 id(不是名字)
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    expect(api.activateEndpoint).toHaveBeenCalledWith('anthropic', 'id-glm-coding-plan');
    // 回读确认复用既有 summary 刷新(未新开轮询)
    expect(refresh).toHaveBeenCalledTimes(1);
    // 成功 toast
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('已激活「Anthropic 组 → glm-coding-plan」');
    expect(t.kind).toBe('success');
    // 勾移:新行亮勾,旧行回空圈
    const items = qa(el, '.ep-item');
    expect(items[0].className).not.toContain('on');
    expect(items[1].className).toContain('on');
    expect(items[1].querySelectorAll('.check svg')).toHaveLength(1);
    expect(items[0].querySelectorAll('.check svg')).toHaveLength(0);
  });

  it('激活 API 失败 → 勾移回滚 + warn toast(服务端人话 msg 原样上屏);pending 释放后可重试', async () => {
    api.activateEndpoint.mockRejectedValueOnce(new Error('已摘除的行请先在仪表盘恢复'));
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const before = qa(el, '.ep-item').map((n) => n.className);
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('已摘除的行请先在仪表盘恢复');
    expect(t.kind).toBe('warn');
    // 回滚:勾选圈回到派生真值(current 行)
    expect(qa(el, '.ep-item').map((n) => n.className)).toEqual(before);
    // pending 已释放:可再次发起
    api.activateEndpoint.mockResolvedValueOnce({ protocol: 'anthropic', id: 'id-glm-coding-plan' });
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(2);
  });

  it('激活 API 失败(HTTP 层错)→ humanize 通用文案「激活失败，请稍后重试」', async () => {
    api.activateEndpoint.mockRejectedValueOnce(new Error('POST /api/endpoints/activate 失败: HTTP 500'));
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活失败，请稍后重试');
    expect(t.kind).toBe('warn');
  });

  it('回读不符(刷新拿到旧态,目标行不是组内唯一池行)→ 回滚 + 「激活结果未确认」toast', async () => {
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-glm-coding-plan' });
    // 刷新返回新对象但内容仍是激活前旧态(互斥未落库/被他端改回)
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true), refreshedEndpoints: demoEndpoints() });
    q(el, '.g-head').click();
    await flush();
    const before = qa(el, '.ep-item').map((n) => n.className);
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活结果未确认，请稍后查看');
    expect(t.kind).toBe('warn');
    expect(qa(el, '.ep-item').map((n) => n.className)).toEqual(before);
  });

  it('回读未发生(refresh 失败保留旧 summary 引用)→ 同样回滚 + 「激活结果未确认」toast', async () => {
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-glm-coding-plan' });
    // refreshedEndpoints 不给 → mountZone 的 refresh 为空操作(summary 引用不变)
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活结果未确认，请稍后查看');
    // 勾选仍钉在 current 行(回滚)
    expect(qa(el, '.ep-item')[0].className).toContain('on');
    expect(qa(el, '.ep-item')[1].className).not.toContain('on');
  });

  it('cooldown 行激活成功:toast 带「冷却中，约 2 分钟后恢复」(90s 向上取整);勾选按调度语义落 builtin 兜底行', async () => {
    // 点击时快照:目标行是池内冷却行(rank>=1 才可激活;demo 里 rank0 的
    // 冷却行走「不在池」零请求闸,那是 rank=0 用例的事)。
    const eps = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [
            makeItem('内置 aliang 隧道', { rank: 0 }),
            makeItem('glm-coding-plan', { rank: 1 }),
            makeItem('byo-relay-02', { rank: 2, state: 'cooldown', cd: 90 }),
          ],
          '内置 aliang 隧道'
        ),
      ],
    });
    // 激活后快照:目标行仍在池(rank 收敛 1)但保持冷却(激活不清零冷却,
    // v1 语义);current 回落 builtin 名。
    const refreshed = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [
            makeItem('内置 aliang 隧道', { rank: 0 }),
            makeItem('byo-relay-02', { rank: 1, state: 'cooldown', cd: 90 }),
          ],
          '内置 aliang 隧道'
        ),
      ],
    });
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-byo-relay-02' });
    const { el, refresh } = mountZone({ endpoints: eps, ai: makeAI(true), refreshedEndpoints: refreshed });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[2].click(); // byo-relay-02(池内冷却行)
    await flushAsync();
    expect(api.activateEndpoint).toHaveBeenCalledWith('anthropic', 'id-byo-relay-02');
    expect(refresh).toHaveBeenCalledTimes(1);
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('已激活「Anthropic 组 → byo-relay-02」（冷却中，约 2 分钟后恢复）');
    expect(t.kind).toBe('success');
    // 勾选落 builtin 兜底行(调度真值:目标行冷却中,selectAuto 回落 builtin),
    // 目标行不亮勾——回读确认仍通过(激活本身已生效)。刷新后同行 glm 退清单,
    // 列表只剩 [builtin, 目标冷却行] 两行。
    const items = qa(el, '.ep-item');
    expect(items).toHaveLength(2);
    expect(items[0].className).toContain('on');
    expect(items[1].className).not.toContain('on');
  });

  it('rank=0 行(builtin 兜底/auto_group=0/未设优先级,含冷却中)点击 → 本地 warn toast(中性文案),零请求', async () => {
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const item = qa(el, '.ep-item')[2]; // byo-relay-02:rank=0+cooldown(不在 auto 池)
    expect(q(item, '.badge').textContent).toBe('冷却 2m');
    item.click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    // 中性文案(终审修复批):rank 池缝隙(priority=0 ∧ auto_group=1 的行在
    // selectAuto 池里但 tray 不授 rank)未收口前,旧合并文案对该场景失真,
    // 中性文案对 builtin/非池/在池-rank0 都准确。
    expect(t.msg).toBe('该行不支持在面板钉选，请在仪表盘操作');
    expect(t.kind).toBe('warn');
    // 零请求:api 层与全局 fetch 双双未被触碰
    expect(api.activateEndpoint).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
    // 再点 disabled 行(demo kimi-proxy:rank=0+disabled)同样走 rank=0 闸
    // ——拒绝序与后端 ActivateInGroup 同构(builtin/非池行先于摘除)
    qa(el, '.g-head')[1].click();
    await flush();
    qa(el, '.ep-item')[4].click();
    await flushAsync();
    expect(useToast().toasts.value.at(-1).msg).toBe('该行不支持在面板钉选，请在仪表盘操作');
    expect(api.activateEndpoint).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('disabled 行(池内摘除行,rank>=1)点击 → 本地 warn toast「已摘除的行请先在仪表盘恢复」,零请求', async () => {
    const eps = makeEndpoints({
      groups: [
        makeGroup('anthropic', [
          makeItem('内置 aliang 隧道', { rank: 0 }),
          makeItem('dis-row', { rank: 2, state: 'disabled' }),
        ], '内置 aliang 隧道'),
      ],
    });
    const { el } = mountZone({ endpoints: eps, ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const item = qa(el, '.ep-item')[1];
    expect(item.className).toContain('off');
    item.click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('已摘除的行请先在仪表盘恢复');
    expect(t.kind).toBe('warn');
    expect(api.activateEndpoint).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('在途防重:乐观态期间同行/异行重复点击都不再发请求(单飞)', async () => {
    let release;
    api.activateEndpoint.mockImplementation(
      () => new Promise((resolve) => { release = resolve; })
    );
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    qa(el, '.g-head')[1].click();
    await flush();
    qa(el, '.ep-item')[1].click(); // 首拍:glm-coding-plan
    await flush();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    // 乐观态期间:同行重复点击 + 异行(openai 组 deepseek-official)点击,均不发
    qa(el, '.ep-item')[1].click();
    qa(el, '.ep-item')[3].click();
    await flush();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    release();
    await flushAsync();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
  });

  it('点击已是勾选态的行 = 破坏性空操作早退:零请求、零 toast、勾选不动(A)', async () => {
    // A 行(组内唯一 rank>=1 ok 池行)已是勾选态;同组 B 行 rank2 冷却若被
    // ActivateInGroup 的互斥关停成 enabled=0,就失去懒恢复通道=永久手动停用。
    // 点已选中的 radio 行本就应该无操作,不得触发这组关停。
    const eps = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [
            makeItem('a-current', { rank: 1, id: 'id-a' }),
            makeItem('b-cool', { rank: 2, state: 'cooldown', cd: 60, id: 'id-b' }),
          ],
          'a-current'
        ),
      ],
    });
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-a' });
    const { el, refresh } = mountZone({ endpoints: eps, ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const items = qa(el, '.ep-item');
    expect(items[0].className).toContain('on');
    items[0].click();
    await flushAsync();
    expect(api.activateEndpoint).not.toHaveBeenCalled();
    expect(refresh).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
    expect(useToast().toasts.value).toHaveLength(0);
    // 勾选原地不动(仍是 A)
    expect(qa(el, '.ep-item')[0].className).toContain('on');
    expect(qa(el, '.ep-item')[1].className).not.toContain('on');
    expect(qa(el, '.check-mark')).toHaveLength(1);
  });

  it('乐观勾移:在途期间(pending)DOM 勾选已从旧行移到点击行,不等回读(B)', async () => {
    // activateEndpoint 永不 resolve:点击后只可能靠乐观态点亮,回读永远没来。
    api.activateEndpoint.mockImplementation(() => new Promise(() => {}));
    const eps = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [makeItem('a-rank1', { rank: 1 }), makeItem('b-rank2', { rank: 2 })],
          'a-rank1'
        ),
      ],
    });
    const { el } = mountZone({ endpoints: eps, ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    expect(qa(el, '.ep-item')[0].className).toContain('on');
    qa(el, '.ep-item')[1].click();
    await flush(); // onItem 已同步置 pending,随后 await 未决 promise
    const onItems = qa(el, '.ep-item.on');
    expect(onItems).toHaveLength(1);
    expect(onItems[0].getAttribute('data-nm')).toBe('b-rank2');
    expect(qa(el, '.check-mark')).toHaveLength(1);
    expect(qa(el, '.ep-item')[0].className).not.toContain('on');
  });

  it('回读未发生(旧快照本身可确认)→ 仍 warn「激活结果未确认」(引用比对分支,C)', async () => {
    // 旧快照:目标行是组内唯一 rank>=1 行 → confirmActivated(旧快照) 恒 true;
    // 只有 refresh 前后引用比对(after===before)能识别「回读没发生」。
    // 目标行是冷却池行(勾选按调度语义落 builtin),因此可点击。
    const eps = makeEndpoints({
      groups: [
        makeGroup(
          'anthropic',
          [
            makeItem('内置 aliang 隧道', { rank: 0 }),
            makeItem('only-pool', { rank: 1, state: 'cooldown', cd: 60 }),
          ],
          '内置 aliang 隧道'
        ),
      ],
    });
    api.activateEndpoint.mockResolvedValue({ protocol: 'anthropic', id: 'id-only-pool' });
    // 不给 refreshedEndpoints → refresh 为空操作(summary 引用不变)
    const { el, refresh } = mountZone({ endpoints: eps, ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    expect(refresh).toHaveBeenCalledTimes(1);
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活结果未确认，请稍后查看');
    expect(t.kind).toBe('warn');
  });

  it('单飞闸丢弃可见化:在途期间点任意行 → toast「激活进行中，请稍候」且请求数不变(E)', async () => {
    let release;
    api.activateEndpoint.mockImplementation(
      () =>
        new Promise((resolve) => {
          release = resolve;
        })
    );
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    qa(el, '.g-head')[1].click();
    await flush();
    qa(el, '.ep-item')[1].click(); // 首拍:anthropic/glm
    await flush();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    // 异行(openai 组的勾选行 deepseek-official)在途点击 → 静默丢弃已可见化
    qa(el, '.ep-item')[3].click();
    await flush();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    let t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活进行中，请稍候');
    expect(t.kind).toBe('warn');
    // 重复点同一行也走这条(早退 A 只针对 it.on,pending 期间点任意行都提示)
    qa(el, '.ep-item')[1].click();
    await flush();
    expect(api.activateEndpoint).toHaveBeenCalledTimes(1);
    t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活进行中，请稍候');
    release({ protocol: 'anthropic', id: 'id-glm-coding-plan' });
    await flushAsync();
  });

  it('humanize 超时分支:超时转译串 → toast「激活超时，请重试」(H-1)', async () => {
    api.activateEndpoint.mockRejectedValueOnce(new Error('POST /api/endpoints/activate 超时(10000ms)'));
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活超时，请重试');
    expect(t.kind).toBe('warn');
  });

  it('humanize 网络错分支:TypeError(Failed to fetch)→ toast「激活失败，请稍后重试」(H-1)', async () => {
    api.activateEndpoint.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('激活失败，请稍后重试');
    expect(t.kind).toBe('warn');
  });

  it('builtin rank0 真实形态:徽标「—」、不被推定勾选、点击命中 rank 闸零请求(I)', async () => {
    const { el } = mountZone({ endpoints: realEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    const items = qa(el, '.ep-item');
    expect(q(items[0], '.badge').textContent).toBe('—');
    expect(items[0].className).not.toContain('on');
    expect(items[1].className).toContain('on'); // 池行 rank1 承接勾选
    items[0].click(); // 点真实 builtin 行
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('该行不支持在面板钉选，请在仪表盘操作');
    expect(t.kind).toBe('warn');
    expect(api.activateEndpoint).not.toHaveBeenCalled();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('humanize 401 防御分支:服务端英文信封「Authenticated dashboard session…」→ toast「请先在仪表盘登录后重试」', async () => {
    // 双闸后面板回环请求已不可能 401;此分支只兜非回环/异常态(代理异常改写
    // 来源等)——英文串不上屏,给可执行的人话指引。
    api.activateEndpoint.mockRejectedValueOnce(
      new Error('Authenticated dashboard session is required')
    );
    const { el } = mountZone({ endpoints: demoEndpoints(), ai: makeAI(true) });
    q(el, '.g-head').click();
    await flush();
    qa(el, '.ep-item')[1].click();
    await flushAsync();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('请先在仪表盘登录后重试');
    expect(t.kind).toBe('warn');
  });

  it('勾选圈只钉在推定行(白勾 svg);其余行空圈', () => {
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
    // 行点击被锁(键盘路径绕得开 CSS 锁,handler 双保险),也不发请求
    q(el, '.ep-item').click();
    await flush();
    expect(useToast().toasts.value.length).toBe(0);
    expect(api.activateEndpoint).not.toHaveBeenCalled();
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
