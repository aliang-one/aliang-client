// FooterBar.test.js — 面板底部动作行契约(spec §4.6 + §6-8 + 无宿主降级修订)。
// 覆盖:宿主模式三键可见、退出内联二次确认流程(确认后 quit 消息发出、
// 取消不发)、仪表盘/设备互访消息 kind 正确;无宿主模式退出键隐藏 +
// muted 说明出现、两键导航到仪表盘根页;版本号渲染与空值兜底;
// 宿主通道异常(open 返回 false)的同窗导航兜底。
// 宿主探测口径:组件用 isHosted()(零副作用,main.js 同族先例);
// open/quit 动作走 postMessageHost 真投递,返回值即送达语义。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import FooterBar from './FooterBar.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';

// api 层 mock:按用例改写 isHosted/postMessageHost 返回值,驱动宿主/无宿主
// 两态与通道异常兜底路径。
vi.mock('../api', () => ({
  isHosted: vi.fn(() => false),
  postMessageHost: vi.fn(() => false),
  startProxy: vi.fn(),
  stopProxy: vi.fn(),
  restartProxy: vi.fn(),
  setMode: vi.fn(),
  getSummary: vi.fn(),
}));

import * as api from '../api';

// ---- 契约构造器(与 AgentZone.test.js 同款聚合快照形状) ----
function makeSummary({ version = 'v1.1.124' } = {}) {
  return {
    version,
    run: {
      is_running: true,
      mode: 'http',
      status_desc: '',
      listen_addr: '127.0.0.1:56432',
      blocked: { code: '', msg: '' },
    },
    ai: { enabled: true, providers: [] },
    agents: null,
    endpoints: null,
  };
}

const mounted = [];
// mountBar — 复刻 App.vue 根壳的数据接线(panelData 形状与根壳一致)。
// hosted:该用例期望的宿主态(在 mount 前设好 isHosted 的返回值——组件
// setup 时同步取值,必须先于挂载)。
function mountBar({ summary = null, hosted = false } = {}) {
  api.isHosted.mockReturnValue(hosted);
  const summaryRef = ref(summary);
  const connLost = ref(false);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summaryRef.value, connLost.value, localTransition.value)
  );
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(FooterBar) });
  app.provide('panelData', { summary: summaryRef, connLost, panelState, localTransition });
  app.provide('panelToast', useToast());
  app.mount(el);
  mounted.push(app);
  return { el, summaryRef };
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
  api.isHosted.mockReturnValue(false);
  api.postMessageHost.mockReturnValue(false);
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
  useToast().clear();
  vi.clearAllMocks();
  vi.unstubAllGlobals();
});

describe('FooterBar 宿主模式(isHosted=true)', () => {
  it('三键可见(仪表盘/设备互访/退出),无「浏览器面板暂不支持退出」说明', () => {
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    expect(q(el, '[data-act="dashboard"]')).not.toBeNull();
    expect(q(el, '[data-act="peer"]')).not.toBeNull();
    expect(q(el, '[data-act="quit"]')).not.toBeNull();
    expect(q(el, '.f-note')).toBeNull();
    expect(el.textContent).not.toContain('浏览器面板暂不支持退出');
    // 键名文案与图标(内联 SVG)齐备
    expect(q(el, '[data-act="dashboard"]').textContent).toContain('仪表盘');
    expect(q(el, '[data-act="dashboard"]').querySelectorAll('svg')).toHaveLength(1);
    expect(q(el, '[data-act="peer"]').textContent).toContain('设备互访');
    expect(q(el, '[data-act="quit"]').textContent).toContain('退出');
    expect(q(el, '[data-act="quit"]').className).toContain('danger'); // 危险语义(hover 变红由 CSS 承载)
  });

  it('仪表盘点击 → postMessageHost({type:"open", kind:"dashboard"}) 且不导航', () => {
    api.postMessageHost.mockReturnValue(true);
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="dashboard"]').click();
    expect(api.postMessageHost).toHaveBeenCalledTimes(1);
    expect(api.postMessageHost).toHaveBeenCalledWith({ type: 'open', kind: 'dashboard' });
    expect(window.location.href).toBe('http://localhost/panel.html'); // 未导航
  });

  it('设备互访点击 → postMessageHost({type:"open", kind:"peer"})', () => {
    api.postMessageHost.mockReturnValue(true);
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="peer"]').click();
    expect(api.postMessageHost).toHaveBeenCalledWith({ type: 'open', kind: 'peer' });
    expect(window.location.href).toBe('http://localhost/panel.html');
  });

  it('退出确认流程:点击退出先出确认条不发消息;确认 → quit 消息发出并收条', async () => {
    api.postMessageHost.mockReturnValue(true);
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="quit"]').click();
    await flush();
    // 确认条出现:确认/取消两键在,原三键收起
    expect(q(el, '[data-act="quit-yes"]')).not.toBeNull();
    expect(q(el, '[data-act="quit-no"]')).not.toBeNull();
    expect(el.textContent).toContain('确认退出');
    expect(q(el, '[data-act="dashboard"]')).toBeNull();
    // 确认前零消息
    expect(api.postMessageHost).not.toHaveBeenCalled();
    // 确认 → quit 消息发出,确认条收起
    q(el, '[data-act="quit-yes"]').click();
    await flush();
    expect(api.postMessageHost).toHaveBeenCalledTimes(1);
    expect(api.postMessageHost).toHaveBeenCalledWith({ type: 'quit' });
    expect(q(el, '[data-act="quit-yes"]')).toBeNull();
    expect(q(el, '[data-act="dashboard"]')).not.toBeNull();
  });

  it('退出确认流程:取消 → 不发消息、回到正常行', async () => {
    api.postMessageHost.mockReturnValue(true);
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="quit"]').click();
    await flush();
    q(el, '[data-act="quit-no"]').click();
    await flush();
    expect(api.postMessageHost).not.toHaveBeenCalled();
    expect(q(el, '[data-act="quit"]')).not.toBeNull();
    expect(q(el, '[data-act="quit-yes"]')).toBeNull();
  });
});

describe('FooterBar 无宿主模式(M1 浏览器,isHosted=false)', () => {
  it('退出键隐藏,原位置出现 muted 说明「浏览器面板暂不支持退出」;前两键仍在', () => {
    const { el } = mountBar({ summary: makeSummary(), hosted: false });
    expect(q(el, '[data-act="quit"]')).toBeNull();
    expect(q(el, '.f-note').textContent).toBe('浏览器面板暂不支持退出');
    expect(q(el, '[data-act="dashboard"]')).not.toBeNull();
    expect(q(el, '[data-act="peer"]')).not.toBeNull();
  });

  it('仪表盘点击 → 同窗导航到根页「/」,不发任何宿主消息', () => {
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: makeSummary(), hosted: false });
    q(el, '[data-act="dashboard"]').click();
    expect(window.location.href).toBe('/');
    expect(api.postMessageHost).not.toHaveBeenCalled();
  });

  it('设备互访点击 → 同窗导航到根页「/」,不发任何宿主消息', () => {
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: makeSummary(), hosted: false });
    q(el, '[data-act="peer"]').click();
    expect(window.location.href).toBe('/');
    expect(api.postMessageHost).not.toHaveBeenCalled();
  });
});

describe('FooterBar 兜底与版本', () => {
  it('宿主通道异常(open 投递返回 false)→ 同窗导航兜底,面板动作不断链', () => {
    // hosted 探测为 true,但 open 投递返回 false(如宿主对象被改坏)
    api.isHosted.mockReturnValue(true);
    api.postMessageHost.mockReturnValue(false);
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="dashboard"]').click();
    expect(api.postMessageHost).toHaveBeenCalledWith({ type: 'open', kind: 'dashboard' });
    expect(window.location.href).toBe('/');
  });

  it('版本号渲染 summary.version(muted tabular-nums 由 CSS 承载);缺失/空 → 「—」', () => {
    const { el } = mountBar({ summary: makeSummary({ version: 'v1.1.127' }), hosted: true });
    expect(q(el, '.ver').textContent).toBe('v1.1.127');
  });

  it('summary null(首帧未拉到数据)→ 底行照常渲染,版本「—」,动作可用', () => {
    api.postMessageHost.mockReturnValue(true);
    const { el } = mountBar({ summary: null, hosted: true });
    expect(q(el, '.ver').textContent).toBe('—');
    expect(q(el, '[data-act="dashboard"]')).not.toBeNull();
    q(el, '[data-act="dashboard"]').click();
    expect(api.postMessageHost).toHaveBeenCalledWith({ type: 'open', kind: 'dashboard' });
  });

  it('无宿主 + summary null → 同样降级渲染(退出说明在,导航可用)', () => {
    vi.stubGlobal('location', { href: 'http://localhost/panel.html' });
    const { el } = mountBar({ summary: null, hosted: false });
    expect(q(el, '.f-note')).not.toBeNull();
    q(el, '[data-act="peer"]').click();
    expect(window.location.href).toBe('/');
  });

  it('api 层纪律:本组件零 fetch(getSummary 不在行为链路)', () => {
    const { el } = mountBar({ summary: makeSummary(), hosted: true });
    q(el, '[data-act="dashboard"]').click();
    expect(api.getSummary).not.toHaveBeenCalled();
    expect(api.startProxy).not.toHaveBeenCalled();
    expect(api.stopProxy).not.toHaveBeenCalled();
    expect(api.restartProxy).not.toHaveBeenCalled();
    expect(api.setMode).not.toHaveBeenCalled();
  });
});
