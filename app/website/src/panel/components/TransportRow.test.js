// TransportRow.test.js — 传输行交互契约(spec §4.2 + §6-1/2/3 + 拍板 2)。
// 组件测试 mock api 层(实现计划既定),state.js 派生函数用真实现。
// 挂载手法沿用仓内约定(无 @vue/test-utils):createApp + app.provide +
// jsdom 挂载断言 DOM——provide 的 panelData 形状与 App.vue 根壳一致
// (含 localTransition,组件写入、测试持有同一 ref)。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, createApp, h, nextTick, ref } from 'vue';
import TransportRow from './TransportRow.vue';
import { derivePanelState } from '../state';
import { useToast } from './Toast.vue';
import { restartProxy, setMode, startProxy, stopProxy } from '../api';

// api 层全部 mock:组件动作链路(启停/重启/模式落盘)不真发请求。
vi.mock('../api', () => ({
  startProxy: vi.fn(),
  stopProxy: vi.fn(),
  restartProxy: vi.fn(),
  setMode: vi.fn(),
  getSummary: vi.fn(),
  isHosted: vi.fn(() => false),
  postMessageHost: vi.fn(() => false),
}));

// makeSummary 按 tray_summary.go 契约构造聚合快照(同 state.test.js 形状)。
function makeSummary({ isRunning = false, mode = 'http', blockedCode = '', blockedMsg = '' } = {}) {
  return {
    version: 'v1.1.124',
    run: {
      is_running: isRunning,
      mode,
      status_desc: '',
      listen_addr: '127.0.0.1:56432',
      blocked: { code: blockedCode, msg: blockedMsg },
    },
    ai: null,
    agents: null,
    endpoints: null,
  };
}

const mounted = [];
// mountRow — 复刻 App.vue 根壳的数据接线(panelState = 七态派生 + 本地过渡覆盖),
// 测试持有 summary/connLost/localTransition 三个 ref,可模拟轮询回读与点击过渡。
function mountRow({ summaryInit = {}, connLostInit = false } = {}) {
  const summary = ref(makeSummary(summaryInit));
  const connLost = ref(connLostInit);
  const localTransition = ref(null);
  const panelState = computed(() =>
    derivePanelState(summary.value, connLost.value, localTransition.value)
  );
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(TransportRow) });
  app.provide('panelData', { summary, connLost, panelState, localTransition });
  app.provide('panelToast', useToast());
  app.mount(el);
  mounted.push(app);
  return { el, summary, connLost, localTransition };
}

// flush — 等 watcher(pre flush)与重渲染都落定。
async function flush() {
  await nextTick();
  await nextTick();
}

function q(el, sel) {
  return el.querySelector(sel);
}

beforeEach(() => {
  vi.clearAllMocks();
  setMode.mockResolvedValue({}); // 默认模式落盘成功,失败路径用例内覆盖
  useToast().clear();
});

afterEach(() => {
  while (mounted.length) mounted.pop().unmount();
  document.body.innerHTML = '';
  useToast().clear();
});

describe('TransportRow 静态形态', () => {
  it('running:主按钮=红⏹停止,重启可用,seg 禁用', () => {
    const { el } = mountRow({ summaryInit: { isRunning: true } });
    const main = q(el, '[data-act="main"]');
    expect(main.className).toContain('stop-live');
    expect(main.textContent).toContain('停止');
    const restart = q(el, '[data-act="restart"]');
    expect(restart.className).not.toContain('off');
    expect(restart.disabled).toBe(false);
  });

  it('stopped:主按钮=绿▶启动(--brand-dim 类),重启 0.35 禁用', () => {
    const { el } = mountRow({ summaryInit: { isRunning: false } });
    const main = q(el, '[data-act="main"]');
    expect(main.className).toContain('start-idle');
    expect(main.textContent).toContain('启动');
    const restart = q(el, '[data-act="restart"]');
    expect(restart.className).toContain('off');
    expect(restart.disabled).toBe(true);
  });
});

describe('TransportRow 模式 segmented(拍板 2:运行/过渡禁用,仅停止可点;落盘走 setMode)', () => {
  it('running 时 seg 禁用且点击无效(不切选中、不弹 toast、不落盘)', async () => {
    const { el } = mountRow({ summaryInit: { isRunning: true, mode: 'http' } });
    const seg = q(el, '.seg');
    expect(seg.className).toContain('seg-disabled');
    // I-2:禁用解释上移行容器(seg 子元素 pointer-events:none,title 不再生效)
    expect(q(el, '.transport-row').title).toBe('停止后可切换模式');
    const regular = seg.querySelectorAll('.opt')[0];
    const deep = seg.querySelectorAll('.opt')[1];
    expect(regular.className).toContain('on'); // 服务端 http=常规
    deep.click();
    await flush();
    expect(deep.className).not.toContain('on'); // 选中未移动
    expect(regular.className).toContain('on');
    expect(useToast().toasts.value.length).toBe(0); // 无 toast
    expect(setMode).not.toHaveBeenCalled(); // 不发落盘请求
  });

  it('stopped 点击深度:调 setMode("tun") 落盘 + 即时选中 + 成功 toast', async () => {
    setMode.mockResolvedValue({ status: 'switched' });
    const { el } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const seg = q(el, '.seg');
    expect(seg.className).not.toContain('seg-disabled');
    const regular = seg.querySelectorAll('.opt')[0];
    const deep = seg.querySelectorAll('.opt')[1];
    deep.click();
    await flush();
    expect(setMode).toHaveBeenCalledTimes(1);
    expect(setMode).toHaveBeenCalledWith('tun'); // 参数正确(wire 值,非中文)
    expect(deep.className).toContain('on');
    expect(regular.className).not.toContain('on');
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('success');
    expect(t.msg).toContain('深度');
    expect(t.msg).toContain('下次启动生效');
  });

  it('stopped 点击后轮询滞后回读旧值(http)不弹回选中', async () => {
    setMode.mockResolvedValue({ status: 'switched' });
    const { el, summary } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const deep = q(el, '.seg').querySelectorAll('.opt')[1];
    deep.click();
    await flush();
    expect(deep.className).toContain('on');
    // 回读滞后:轮询仍报旧值 http(lastServerMode 未变)→ 乐观选中保留。
    summary.value = makeSummary({ isRunning: false, mode: 'http' });
    await flush();
    expect(deep.className).toContain('on');
  });

  it('setMode 失败:选中回滚 + warn toast 带服务端 msg', async () => {
    setMode.mockRejectedValue(new Error('Invalid target mode: tun. Must be \'http\' or \'tun\''));
    const { el } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const regular = q(el, '.seg').querySelectorAll('.opt')[0];
    const deep = q(el, '.seg').querySelectorAll('.opt')[1];
    deep.click();
    await flush();
    expect(setMode).toHaveBeenCalledWith('tun');
    expect(deep.className).not.toContain('on'); // 回滚:深度落选
    expect(regular.className).toContain('on'); // 常规回到选中
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('warn');
    expect(t.msg).toContain('http');
  });

  it('服务端模式值变化(如仪表盘改的)→ segmented 跟随回读值', async () => {
    const { el, summary } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const deep = q(el, '.seg').querySelectorAll('.opt')[1];
    summary.value = makeSummary({ isRunning: false, mode: 'tun' });
    await flush();
    expect(deep.className).toContain('on');
  });

  it('stopped 乐观选「深度」后启动成功 → 运行态以服务端为准回正', async () => {
    startProxy.mockResolvedValue({});
    const { el, summary } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const deep = q(el, '.seg').querySelectorAll('.opt')[1];
    deep.click();
    await flush();
    expect(deep.className).toContain('on');
    // 启动 → summary 回读 running(mode 仍 http):选中回正常规,与状态头副标题一致。
    summary.value = makeSummary({ isRunning: true, mode: 'http' });
    await flush();
    expect(deep.className).not.toContain('on');
  });
});

describe('TransportRow 启停合一(§6-1 过渡态 + 防抖)', () => {
  it('stopped→点启动→立即进 starting(按钮 spinner+文案、三控件 locked)', async () => {
    let resolveStart;
    startProxy.mockImplementation(() => new Promise((res) => { resolveStart = res; }));
    const { el } = mountRow({ summaryInit: { isRunning: false } });
    const row = q(el, '.transport-row');
    const main = q(el, '[data-act="main"]');
    expect(row.className).not.toContain('locked');
    main.click();
    await nextTick();
    expect(startProxy).toHaveBeenCalledTimes(1);
    expect(main.className).toContain('busy');
    expect(main.textContent).toContain('启动中');
    expect(main.querySelector('.spin')).not.toBeNull(); // 按钮内 spinner
    expect(row.className).toContain('locked'); // 三控件全禁
    expect(q(el, '.seg').className).toContain('seg-disabled');

    // 防抖(§6-1):过渡期间重复点击无效——api 不再被调。
    main.click();
    main.click();
    await flush();
    expect(startProxy).toHaveBeenCalledTimes(1);

    // api 已返回但 summary 未回读 → 仍处过渡(等回读,§6-1);真实回读
    // 解除流程由下一用例完整覆盖。
    resolveStart({});
    await flush();
    expect(main.className).toContain('busy');
  });

  it('stopped→点启动→summary 回 running→解除 + 成功 toast', async () => {
    let resolveStart;
    startProxy.mockImplementation(() => new Promise((res) => { resolveStart = res; }));
    const ctx = mountRow({ summaryInit: { isRunning: false } });
    const main = q(ctx.el, '[data-act="main"]');
    main.click();
    await nextTick();
    resolveStart({});
    await flush();
    ctx.summary.value = makeSummary({ isRunning: true });
    await flush();
    expect(main.className).toContain('stop-live');
    expect(main.textContent).toContain('停止');
    expect(q(ctx.el, '.transport-row').className).not.toContain('locked');
    expect(useToast().toasts.value.some((t) => t.msg.includes('已启动'))).toBe(true);
  });

  it('running→点停止→stopping 过渡→summary 回 stopped→解除', async () => {
    let resolveStop;
    stopProxy.mockImplementation(() => new Promise((res) => { resolveStop = res; }));
    const ctx = mountRow({ summaryInit: { isRunning: true } });
    const main = q(ctx.el, '[data-act="main"]');
    main.click();
    await nextTick();
    expect(stopProxy).toHaveBeenCalledTimes(1);
    expect(main.className).toContain('busy');
    expect(main.textContent).toContain('停止中');
    resolveStop({});
    await flush();
    ctx.summary.value = makeSummary({ isRunning: false });
    await flush();
    expect(main.className).toContain('start-idle');
    expect(q(ctx.el, '.transport-row').className).not.toContain('locked');
    expect(useToast().toasts.value.some((t) => t.msg.includes('已停止'))).toBe(true);
  });

  it('启动失败(api 抛错)→ 重置过渡态 + warn toast', async () => {
    startProxy.mockRejectedValueOnce(new Error('登录闸拦截'));
    const { el } = mountRow({ summaryInit: { isRunning: false } });
    const main = q(el, '[data-act="main"]');
    main.click();
    await nextTick();
    expect(main.className).toContain('busy');
    await flush();
    expect(main.className).not.toContain('busy'); // 过渡已重置
    expect(q(el, '.transport-row').className).not.toContain('locked');
    const last = useToast().toasts.value.at(-1);
    expect(last.kind).toBe('warn');
    expect(last.msg).toContain('登录闸拦截');
  });

  it('api 成功但轮询迟迟不翻转 → 6s 看门狗强撤过渡(控件不永久锁死)', async () => {
    vi.useFakeTimers();
    try {
      let resolveStart;
      startProxy.mockImplementation(() => new Promise((res) => { resolveStart = res; }));
      const { el } = mountRow({ summaryInit: { isRunning: false } });
      const main = q(el, '[data-act="main"]');
      main.click();
      await nextTick();
      resolveStart({});
      await nextTick();
      expect(main.className).toContain('busy');
      vi.advanceTimersByTime(6000);
      await nextTick();
      expect(main.className).not.toContain('busy');
      expect(q(el, '.transport-row').className).not.toContain('locked');
    } finally {
      vi.useRealTimers();
    }
  });
});

describe('TransportRow 重启(§6-2 仅运行态可用)', () => {
  it('stopped 点重启:禁用态,不发请求', () => {
    const { el } = mountRow({ summaryInit: { isRunning: false } });
    const restart = q(el, '[data-act="restart"]');
    restart.click();
    expect(restartProxy).not.toHaveBeenCalled();
  });

  it('running 点重启→restarting(图标自转类)→stop 段回 stopped 仍过渡→start 段回 running 解除', async () => {
    let resolveRestart;
    restartProxy.mockImplementation(() => new Promise((res) => { resolveRestart = res; }));
    const ctx = mountRow({ summaryInit: { isRunning: true } });
    const restart = q(ctx.el, '[data-act="restart"]');
    restart.click();
    await nextTick();
    expect(restartProxy).toHaveBeenCalledTimes(1);
    expect(restart.className).toContain('busy'); // svg 自转
    expect(q(ctx.el, '.transport-row').className).toContain('locked');
    // 重启中段:stop 已完成(summary 暂翻 stopped)——过渡不撤
    ctx.summary.value = makeSummary({ isRunning: false });
    await flush();
    expect(restart.className).toContain('busy');
    // start 段完成:summary 回 running → 解除
    resolveRestart({});
    await flush();
    ctx.summary.value = makeSummary({ isRunning: true });
    await flush();
    expect(q(ctx.el, '.transport-row').className).not.toContain('locked');
    expect(useToast().toasts.value.some((t) => t.msg.includes('已重启'))).toBe(true);
  });

  it('error 态重启仍禁用(仅 running 可用)', () => {
    const { el } = mountRow({ summaryInit: { isRunning: false }, connLostInit: true });
    const restart = q(el, '[data-act="restart"]');
    expect(restart.className).toContain('off');
    restart.click();
    expect(restartProxy).not.toHaveBeenCalled();
  });
});

describe('TransportRow error / logged_out 态', () => {
  it('error 态启动键恢复可用(connLost,点启动走 api)', async () => {
    startProxy.mockResolvedValue({});
    const { el } = mountRow({ summaryInit: { isRunning: false }, connLostInit: true });
    const row = q(el, '.transport-row');
    const main = q(el, '[data-act="main"]');
    expect(row.className).not.toContain('locked');
    expect(main.className).toContain('start-idle');
    expect(main.disabled).toBe(false);
    main.click();
    await flush();
    expect(startProxy).toHaveBeenCalledTimes(1);
  });

  it('未登录三键全禁用(spec §3):主按钮/重启/seg 全部禁用且点击无效', async () => {
    const { el } = mountRow({
      summaryInit: { isRunning: false, blockedCode: 'not_logged_in' },
    });
    const main = q(el, '[data-act="main"]');
    const restart = q(el, '[data-act="restart"]');
    const seg = q(el, '.seg');
    const deep = seg.querySelectorAll('.opt')[1];
    expect(main.disabled).toBe(true);
    expect(main.className).toContain('disabled');
    expect(restart.disabled).toBe(true);
    expect(restart.className).toContain('off');
    expect(seg.className).toContain('seg-disabled');
    main.click();
    restart.click();
    deep.click();
    await flush();
    expect(startProxy).not.toHaveBeenCalled();
    expect(restartProxy).not.toHaveBeenCalled();
    expect(deep.className).not.toContain('on');
    expect(useToast().toasts.value.length).toBe(0);
  });
});

describe('TransportRow 评审收口批(在途闸/容器提示/错误人话化)', () => {
  it('I-1 在途闸:setMode 挂起期间再点另一模式不双发,resolve 后选中与 toast 归位', async () => {
    let resolveSetMode;
    setMode.mockImplementation(() => new Promise((res) => { resolveSetMode = res; }));
    const { el } = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const regular = q(el, '.seg').querySelectorAll('.opt')[0];
    const deep = q(el, '.seg').querySelectorAll('.opt')[1];
    deep.click(); // 点深度:落盘在途
    await nextTick();
    expect(setMode).toHaveBeenCalledTimes(1);
    expect(setMode).toHaveBeenCalledWith('tun');
    expect(deep.className).toContain('on');
    regular.click(); // 再点常规:被在途闸挡住
    await flush();
    expect(setMode).toHaveBeenCalledTimes(1); // 不双发
    expect(regular.className).not.toContain('on'); // 常规未获选中
    expect(useToast().toasts.value.length).toBe(0); // 无成功 toast
    resolveSetMode({ status: 'switched' }); // 首个落盘完成
    await flush();
    expect(deep.className).toContain('on'); // 选中归位(深度)
    const t = useToast().toasts.value.at(-1);
    expect(t.kind).toBe('success');
    expect(t.msg).toContain('下次启动生效');
  });

  it('M-1 在途回读不抢乐观选中(成功路径):回读值变化被闸,落盘完成选中归位', async () => {
    let resolveSetMode;
    setMode.mockImplementation(() => new Promise((res) => { resolveSetMode = res; }));
    const ctx = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const deep = q(ctx.el, '.seg').querySelectorAll('.opt')[1];
    deep.click(); // 乐观深度,setMode('tun') 在途
    await nextTick();
    // 在途期间轮询回读到变化值(http→tun):watch 被闸,乐观选中不被改写
    ctx.summary.value = makeSummary({ isRunning: false, mode: 'tun' });
    await flush();
    expect(deep.className).toContain('on');
    expect(useToast().toasts.value.length).toBe(0); // 纯等落盘,无 toast
    // 落盘成功:选中归位 + 成功 toast;pending 释放点按服务端值补对账
    resolveSetMode({ status: 'switched' });
    await flush();
    expect(deep.className).toContain('on');
    expect(useToast().toasts.value.at(-1).msg).toContain('下次启动生效');
  });

  it('M-1b 失败回滚后,后续轮询回读到的服务端变化照常采纳', async () => {
    setMode.mockRejectedValueOnce(new Error('failed to persist ingress mode tun'));
    const ctx = mountRow({ summaryInit: { isRunning: false, mode: 'http' } });
    const regular = q(ctx.el, '.seg').querySelectorAll('.opt')[0];
    const deep = q(ctx.el, '.seg').querySelectorAll('.opt')[1];
    deep.click();
    await flush();
    expect(regular.className).toContain('on'); // 落盘失败:回滚到点击前选中
    expect(useToast().toasts.value.at(-1).kind).toBe('warn');
    // 服务端后续被外部改到 tun:轮询回读值变化 → 采纳,选中跟服务端走
    ctx.summary.value = makeSummary({ isRunning: false, mode: 'tun' });
    await flush();
    expect(deep.className).toContain('on');
    expect(regular.className).not.toContain('on');
  });

  it('I-2 禁用解释上移容器:running/过渡/logged_out 三档动态给,子元素不再带 title', async () => {
    // running:容器解释 seg 为何禁用
    const running = mountRow({ summaryInit: { isRunning: true } });
    expect(q(running.el, '.transport-row').title).toBe('停止后可切换模式');
    expect(q(running.el, '[data-act="main"]').getAttribute('title')).toBeNull();
    expect(q(running.el, '[data-act="restart"]').getAttribute('title')).toBeNull();
    expect(q(running.el, '.seg').getAttribute('title')).toBeNull();

    // 过渡:三控件全禁,容器给统一执行中提示
    const ctx = mountRow({ summaryInit: { isRunning: false } });
    ctx.localTransition.value = 'starting';
    await nextTick();
    expect(q(ctx.el, '.transport-row').title).toBe('正在执行操作…');

    // logged_out:三键全禁
    const out = mountRow({ summaryInit: { isRunning: false, blockedCode: 'not_logged_in' } });
    expect(q(out.el, '.transport-row').title).toBe('未登录，无法操作');

    // stopped(控件可用):容器不提示
    const idle = mountRow({ summaryInit: { isRunning: false } });
    expect(q(idle.el, '.transport-row').title).toBe('');
  });

  it('M-2 错误 toast 人话化:超时类与网络类转译(服务端 msg 原样已由既有用例护住)', async () => {
    // 超时类(api 层超时转译串)→ 明确提示重试
    startProxy.mockRejectedValueOnce(new Error('POST /api/run/start 超时(10000ms)'));
    let ctx = mountRow({ summaryInit: { isRunning: false } });
    q(ctx.el, '[data-act="main"]').click();
    await flush();
    expect(useToast().toasts.value.at(-1).msg).toBe('代理操作超时，请重试');
    mounted.pop().unmount();
    document.body.innerHTML = '';
    useToast().clear();
    // 网络/HTTP 类(TypeError)→ 通用稍后重试
    startProxy.mockRejectedValueOnce(new TypeError('Failed to fetch'));
    ctx = mountRow({ summaryInit: { isRunning: false } });
    q(ctx.el, '[data-act="main"]').click();
    await flush();
    expect(useToast().toasts.value.at(-1).msg).toBe('操作失败，请稍后重试');
  });
});
