// AISection.test.js — AI 加速区契约(spec §4.3 + §3「AI 加速关」行 + §6-4)。
// 覆盖:检测驱动点亮(预留位/未知 key 永不点亮)、providers 缺失容错、
// 计数文案两分支(开/关)、AI 关降透明+锁交互、switch 只读降级 toast、
// provider 点击详情 toast。
// 本组件为只读展示(开关降级),不 import api 层,无需 mock。
// 挂载手法沿用仓内约定(createApp + provide + jsdom DOM 断言)。
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import AISection, { aiSectionModel } from './AISection.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';

// makeAI 按 tray_summary.go TrayAISummary 契约构造 ai 子源
// ({enabled, providers:[{key,label,detected,detail}]},键名即后端 JSON 契约)。
function makeAI({ enabled = true, providers = [] } = {}) {
  return { enabled, providers };
}

// makeProv 按 TrayAIProvider 契约构造单条 provider。
function makeProv(key, { label = '', detected = false, detail = '' } = {}) {
  return { key, label, detected, detail };
}

// makeSummary 聚合快照(run 子源取 running 稳态,与本区无关)。
function makeSummary(ai) {
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
    endpoints: null,
  };
}

const mounted = [];
// mountSection — 复刻 App.vue 根壳的数据接线(panelData 形状与根壳一致)。
function mountSection({ ai = null } = {}) {
  const summary = ref(makeSummary(ai));
  const connLost = ref(false);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summary.value, connLost.value, localTransition.value)
  );
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(AISection) });
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

beforeEach(() => {
  useToast().clear();
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
  useToast().clear();
});

describe('aiSectionModel 纯派生(spec §4.3 图标池/点亮/计数)', () => {
  it('detected 集合正确点亮:anthropic/cursor live,openai 灰;计数 2/3', () => {
    const m = aiSectionModel(
      makeAI({
        enabled: true,
        providers: [
          makeProv('anthropic', { label: 'Anthropic', detected: true, detail: '检测到 3 次流量经此路由' }),
          makeProv('openai', { label: 'OpenAI', detected: false }),
          makeProv('cursor', { label: 'Cursor', detected: true, detail: '检测到 1 次流量经此路由' }),
        ],
      })
    );
    const byKey = Object.fromEntries(m.tiles.map((t) => [t.key, t]));
    expect(byKey.anthropic.live).toBe(true);
    expect(byKey.openai.live).toBe(false);
    expect(byKey.cursor.live).toBe(true);
    expect(byKey.gemini.live).toBe(false);
    expect(byKey.grok.live).toBe(false);
    expect(m.litCount).toBe(2);
    expect(m.detectableCount).toBe(3);
    expect(m.enabled).toBe(true);
    expect(m.hint).toBe('检测到 2 / 3');
  });

  it('预留位永不点亮:gemini/grok 即使 detected=true 也灰(spec 暂不支持检测)', () => {
    const m = aiSectionModel(
      makeAI({
        providers: [makeProv('gemini', { detected: true }), makeProv('grok', { detected: true })],
      })
    );
    const byKey = Object.fromEntries(m.tiles.map((t) => [t.key, t]));
    expect(byKey.gemini.live).toBe(false);
    expect(byKey.grok.live).toBe(false);
    expect(byKey.gemini.reserved).toBe(true);
    expect(byKey.grok.reserved).toBe(true);
    expect(m.litCount).toBe(0);
    expect(m.hint).toBe('检测到 0 / 3'); // 预留位不计入分母
    expect(byKey.gemini.title).toContain('暂不支持检测');
  });

  it('未知 key 按预留位处理不点亮(vscode detected=true 不点亮,不炸)', () => {
    const m = aiSectionModel(
      makeAI({ providers: [makeProv('vscode', { detected: true, detail: 'x' })] })
    );
    expect(m.litCount).toBe(0);
    expect(m.tiles.every((t) => !t.live)).toBe(true);
    expect(m.hint).toBe('检测到 0 / 3');
  });

  it('providers 缺失/null/坏类型 → 全灰 + 计数 0/3,不抛错', () => {
    // ai 子源整体缺失(null/undefined/非对象):enabled 缺省视为关 → 关态文案
    for (const ai of [null, undefined, 'bad', 42]) {
      const m = aiSectionModel(ai);
      expect(m.tiles).toHaveLength(5); // 图标池固定 5 家,不随数据增减
      expect(m.tiles.every((t) => !t.live)).toBe(true);
      expect(m.litCount).toBe(0);
      expect(m.enabled).toBe(false);
      expect(m.hint).toBe('AI 加速已关 · 图标全灰');
    }
    // enabled=true 但 providers 缺失/null/坏类型/坏条目 → 计数 0/3
    for (const providers of [undefined, null, 'bad', 42, [null, 42, {}]]) {
      const m = aiSectionModel(makeAI({ enabled: true, providers }));
      expect(m.tiles).toHaveLength(5);
      expect(m.tiles.every((t) => !t.live)).toBe(true);
      expect(m.litCount).toBe(0);
      expect(m.enabled).toBe(true);
      expect(m.hint).toBe('检测到 0 / 3');
    }
  });

  it('AI 加速关(enabled=false)→ hint 固定文案「AI 加速已关 · 图标全灰」', () => {
    const m = aiSectionModel(
      makeAI({ enabled: false, providers: [makeProv('anthropic', { detected: true })] })
    );
    expect(m.enabled).toBe(false);
    expect(m.hint).toBe('AI 加速已关 · 图标全灰');
  });

  it('key 归一大小写/空白与服务端 NormalizeAIProviderKey 同口径(Anthropic → anthropic)', () => {
    const m = aiSectionModel(
      makeAI({ providers: [makeProv('Anthropic', { label: 'Anthropic', detected: true })] })
    );
    const byKey = Object.fromEntries(m.tiles.map((t) => [t.key, t]));
    expect(byKey.anthropic.live).toBe(true);
  });

  it('label 兜底:服务端 label 空 → 图标池预设名', () => {
    const m = aiSectionModel(makeAI({ providers: [makeProv('openai')] }));
    const byKey = Object.fromEntries(m.tiles.map((t) => [t.key, t]));
    expect(byKey.openai.label).toBe('OpenAI');
    expect(byKey.gemini.label).toBe('Gemini');
  });
});

describe('AISection 挂载 DOM 契约', () => {
  it('开:总开关行 ⚡+「AI 加速」+switch on;点亮 tile 带 live 类;hint 计数', () => {
    const { el } = mountSection({
      ai: makeAI({
        enabled: true,
        providers: [
          makeProv('anthropic', { label: 'Anthropic', detected: true, detail: '检测到 3 次流量经此路由' }),
        ],
      }),
    });
    expect(el.textContent).toContain('AI 加速');
    expect(q(el, '[data-act="ai-switch"]').className).toContain('on');
    const liveTile = q(el, '.prov[data-key="anthropic"]');
    expect(liveTile.className).toContain('live');
    expect(liveTile.querySelector('.p-dot')).not.toBeNull(); // 状态点(弹性缩放由 CSS 承载)
    expect(q(el, '.prov[data-key="openai"]').className).not.toContain('live');
    expect(q(el, '.prov[data-key="gemini"]').className).not.toContain('live');
    expect(q(el, '.prov-row').className).not.toContain('dimmed');
    expect(q(el, '.prov-row .hint').textContent).toBe('检测到 1 / 3');
  });

  it('关:prov-row.dimmed(0.35+锁)+ hint「AI 加速已关 · 图标全灰」+ switch off', () => {
    const { el } = mountSection({ ai: makeAI({ enabled: false, providers: [] }) });
    expect(q(el, '.prov-row').className).toContain('dimmed');
    expect(q(el, '.prov-row .hint').textContent).toBe('AI 加速已关 · 图标全灰');
    expect(q(el, '[data-act="ai-switch"]').className).not.toContain('on');
  });

  it('AI 关 → 点击被锁:provider 图标点击不弹 toast(锁交互 §6-4)', async () => {
    const { el } = mountSection({ ai: makeAI({ enabled: false, providers: [makeProv('anthropic', { detected: true, detail: 'x' })] }) });
    q(el, '.prov[data-key="anthropic"]').click();
    await flush();
    expect(useToast().toasts.value.length).toBe(0);
  });

  it('AI 关 → switch 点击不翻转状态(仍 off),只弹引导 toast', async () => {
    const { el, summary } = mountSection({ ai: makeAI({ enabled: false, providers: [] }) });
    q(el, '[data-act="ai-switch"]').click();
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('warn');
    expect(t.msg).toBe('请到仪表盘开关 AI 加速');
    expect(q(el, '[data-act="ai-switch"]').className).not.toContain('on'); // 无乐观翻转
    expect(summary.value.ai.enabled).toBe(false); // 数据未被改写
  });

  it('switch 只读降级(AI 开态):点击 toast 引导仪表盘,switch 保持 on 不翻转', async () => {
    const { el, summary } = mountSection({
      ai: makeAI({ enabled: true, providers: [makeProv('anthropic', { detected: true })] }),
    });
    q(el, '[data-act="ai-switch"]').click();
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('warn');
    expect(t.msg).toBe('请到仪表盘开关 AI 加速');
    expect(q(el, '[data-act="ai-switch"]').className).toContain('on'); // 保持服务端值
    expect(summary.value.ai.enabled).toBe(true);
  });

  it('provider 点击:detected 条目 toast「label：detail」', async () => {
    const { el } = mountSection({
      ai: makeAI({
        enabled: true,
        providers: [makeProv('anthropic', { label: 'Anthropic', detected: true, detail: '检测到 3 次流量经此路由' })],
      }),
    });
    q(el, '.prov[data-key="anthropic"]').click();
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('success');
    expect(t.msg).toContain('Anthropic');
    expect(t.msg).toContain('检测到 3 次流量经此路由');
  });

  it('provider 点击:启用规则但 detail 为空 → toast「暂无详情」', async () => {
    const { el } = mountSection({
      ai: makeAI({ enabled: true, providers: [makeProv('openai', { label: 'OpenAI', detected: false })] }),
    });
    q(el, '.prov[data-key="openai"]').click();
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('success');
    expect(t.msg).toBe('OpenAI：暂无详情');
  });

  it('预留位点击(Gemini,无数据条目)→ toast「暂无详情」', async () => {
    const { el } = mountSection({ ai: makeAI({ enabled: true, providers: [] }) });
    q(el, '.prov[data-key="gemini"]').click();
    await flush();
    const t = useToast().toasts.value.at(-1);
    expect(t.msg).toBe('Gemini：暂无详情');
  });

  it('点亮态随轮询回读变化(检测到→未检测到,tile 失去 live)', async () => {
    const ctx = mountSection({
      ai: makeAI({ enabled: true, providers: [makeProv('anthropic', { detected: true })] }),
    });
    expect(q(ctx.el, '.prov[data-key="anthropic"]').className).toContain('live');
    ctx.summary.value = makeSummary(makeAI({ enabled: true, providers: [makeProv('anthropic', { detected: false })] }));
    await flush();
    expect(q(ctx.el, '.prov[data-key="anthropic"]').className).not.toContain('live');
  });

  it('ai 子源降级 null(error/服务端单源 panic)→ 全灰不炸,计数 0/3', () => {
    const { el } = mountSection({ ai: null });
    expect(q(el, '.prov-row').className).toContain('dimmed');
    expect(el.querySelectorAll('.prov')).toHaveLength(5);
    expect(q(el, '.prov-row .hint').textContent).toBe('AI 加速已关 · 图标全灰');
  });
});
