// StatusHeader.test.js — 状态头七态文案/点色/logo 灰化(spec §4.1 + §3 矩阵)。
// 纯函数 statusHeaderContent 覆盖七态矩阵;挂载断言补 spinner 替换点与
// logo off 两条 DOM 契约。挂载手法沿用仓内约定(createApp + provide)。
import { afterEach, describe, expect, it } from 'vitest';
import { computed, createApp, h, ref } from 'vue';
import StatusHeader, { statusHeaderContent } from './StatusHeader.vue';
import { derivePanelState } from '../state';

// makeRun 按 tray_summary.go 契约构造 run 子源(字段名即后端 JSON 契约)。
function makeRun({ isRunning = false, mode = 'http', listenAddr = '127.0.0.1:56432', blockedCode = '', blockedMsg = '' } = {}) {
  return {
    is_running: isRunning,
    mode,
    status_desc: '',
    listen_addr: listenAddr,
    blocked: { code: blockedCode, msg: blockedMsg },
  };
}

function mountHeader({ run = null, connLost = false } = {}) {
  const summary = ref(run === null ? null : { version: 'v1.1.124', run, ai: null, agents: null, endpoints: null });
  const connLostRef = ref(connLost);
  const localTransition = ref(null);
  const panelState = computed(() => derivePanelState(summary.value, connLostRef.value, localTransition.value));
  const el = document.createElement('div');
  document.body.appendChild(el);
  const app = createApp({ render: () => h(StatusHeader) });
  app.provide('panelData', { summary, connLost: connLostRef, panelState, localTransition });
  app.provide('panelToast', { show() {} });
  app.mount(el);
  return { el, app, localTransition };
}

const mountedApps = [];
afterEach(() => {
  while (mountedApps.length) mountedApps.pop().unmount();
  document.body.innerHTML = '';
});

describe('statusHeaderContent 七态矩阵(spec §3/§4.1)', () => {
  it('running:绿点脉冲 +「代理运行中」+ 副标题「深度模式 · 监听地址」+ logo 品牌绿', () => {
    const c = statusHeaderContent('running', makeRun({ isRunning: true, mode: 'tun' }));
    expect(c.title).toBe('代理运行中');
    expect(c.subtitle).toContain('深度模式');
    expect(c.subtitle).toContain('127.0.0.1:56432');
    expect(c.dotClass).toContain('green');
    expect(c.dotClass).toContain('pulse');
    expect(c.busy).toBe(false);
    expect(c.logoOff).toBe(false);
  });

  it('running 常规模式(http)副标题用「常规模式」', () => {
    const c = statusHeaderContent('running', makeRun({ isRunning: true, mode: 'http' }));
    expect(c.subtitle).toContain('常规模式');
    expect(c.subtitle).not.toContain('深度');
  });

  it('stopped:灰点 +「代理已停止」+「下次启动 · ××模式」+ logo 灰化', () => {
    const c = statusHeaderContent('stopped', makeRun({ isRunning: false, mode: 'tun' }));
    expect(c.title).toBe('代理已停止');
    expect(c.subtitle).toBe('下次启动 · 深度模式');
    expect(c.dotClass).toContain('gray');
    expect(c.dotClass).not.toContain('pulse');
    expect(c.logoOff).toBe(true);
  });

  it.each([
    ['starting', '正在启动…'],
    ['stopping', '正在停止…'],
    ['restarting', '正在重启…'],
  ])('过渡态 %s:busy=true(spinner 替换点)+ 文案「%s」', (kind, title) => {
    const c = statusHeaderContent(kind, makeRun({ isRunning: false, mode: 'tun' }));
    expect(c.busy).toBe(true);
    expect(c.title).toBe(title);
    expect(c.dotClass).toBe(''); // 过渡态无点,由 spinner 顶替
  });

  it('error:红点 +「状态获取失败」(summary null/connLost 的错误摘要)', () => {
    const c = statusHeaderContent('error', null);
    expect(c.title).toBe('状态获取失败');
    expect(c.dotClass).toContain('red');
    expect(c.busy).toBe(false);
  });

  it('logged_out:琥珀点 +「未登录」+ 副标题承载拦截原因(blocked.msg)', () => {
    const c = statusHeaderContent(
      'logged_out',
      makeRun({ isRunning: false, blockedCode: 'not_logged_in', blockedMsg: '请先在仪表盘登录' })
    );
    expect(c.title).toBe('未登录');
    expect(c.subtitle).toBe('请先在仪表盘登录');
    expect(c.dotClass).toContain('amber');
    expect(c.logoOff).toBe(true);
  });

  it('logged_out 拦截 msg 为空时副标题兜底文案', () => {
    const c = statusHeaderContent('logged_out', makeRun({ blockedCode: 'x', blockedMsg: '' }));
    expect(c.subtitle).not.toBe('');
  });
});

describe('StatusHeader 挂载 DOM 契约', () => {
  it('running:渲染绿点而非 spinner,logo 不灰化', () => {
    const { el } = mountHeader({ run: makeRun({ isRunning: true }) });
    expect(el.querySelector('.dot.green')).not.toBeNull();
    expect(el.querySelector('.spinner')).toBeNull();
    expect(el.querySelector('.a-logo').className).not.toContain('off');
    expect(el.textContent).toContain('代理运行中');
  });

  it('过渡态:状态点换成 spinner(§4.1 过渡态换 13px spinner)', () => {
    const ctx = mountHeader({ run: makeRun({ isRunning: false }) });
    ctx.localTransition.value = 'starting';
    return Promise.resolve().then(() => {
      expect(ctx.el.querySelector('.spinner')).not.toBeNull();
      expect(ctx.el.querySelector('.dot')).toBeNull();
      expect(ctx.el.textContent).toContain('正在启动…');
    });
  });

  it('stopped:logo 灰化类 off + 灰点', () => {
    const { el } = mountHeader({ run: makeRun({ isRunning: false }) });
    expect(el.querySelector('.a-logo').className).toContain('off');
    expect(el.querySelector('.dot.gray')).not.toBeNull();
    expect(el.textContent).toContain('代理已停止');
  });

  it('error(summary null):红点 + 错误摘要文案', () => {
    const { el } = mountHeader({ run: null });
    expect(el.querySelector('.dot.red')).not.toBeNull();
    expect(el.textContent).toContain('状态获取失败');
  });
});
