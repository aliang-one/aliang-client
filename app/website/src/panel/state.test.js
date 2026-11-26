// state.test.js — derivePanelState 七态派生全覆盖(spec §3 矩阵)。
// 覆盖:running / stopped / starting / stopping / restarting / error / logged_out
// + 过渡态优先于远端 + connLost 优先于远端快照。
import { describe, expect, it } from 'vitest';
import { derivePanelState } from './state';

// makeSummary 按 tray_summary.go 契约构造一份聚合快照(字段名即后端 JSON 契约)。
function makeSummary({ isRunning = false, blockedCode = '', withRun = true } = {}) {
  return {
    version: 'v1.1.124',
    run: withRun
      ? {
          is_running: isRunning,
          mode: 'http',
          status_desc: '',
          listen_addr: '127.0.0.1:56432',
          blocked: { code: blockedCode, msg: '' },
        }
      : null,
    ai: null,
    agents: null,
    endpoints: null,
  };
}

describe('derivePanelState 基础远端态', () => {
  it('运行中 → running', () => {
    expect(derivePanelState(makeSummary({ isRunning: true }), false, null)).toBe('running');
  });

  it('已停止(无拦截码) → stopped', () => {
    expect(derivePanelState(makeSummary({ isRunning: false }), false, null)).toBe('stopped');
  });

  it('未运行且带拦截码 → logged_out(未登录不能启动)', () => {
    expect(derivePanelState(makeSummary({ isRunning: false, blockedCode: 'not_logged_in' }), false, null)).toBe(
      'logged_out'
    );
  });

  it('运行中即使带拦截码仍是 running(拦截码只在未运行时有意义)', () => {
    expect(derivePanelState(makeSummary({ isRunning: true, blockedCode: 'not_logged_in' }), false, null)).toBe(
      'running'
    );
  });
});

describe('derivePanelState 出错态', () => {
  it('summary 为 null → error', () => {
    expect(derivePanelState(null, false, null)).toBe('error');
  });

  it('connLost 为 true → error(connLost 优先于远端快照内容)', () => {
    expect(derivePanelState(makeSummary({ isRunning: true }), true, null)).toBe('error');
    expect(derivePanelState(makeSummary({ isRunning: false, blockedCode: 'x' }), true, null)).toBe('error');
  });

  it('summary 在但 run 子源为 null(降级) → error(无状态可渲染)', () => {
    expect(derivePanelState(makeSummary({ withRun: false }), false, null)).toBe('error');
  });
});

describe('derivePanelState 过渡态优先', () => {
  it.each([
    ['starting', 'starting'],
    ['stopping', 'stopping'],
    ['restarting', 'restarting'],
  ])('过渡态 %s 覆盖远端任意态', (transition, expected) => {
    // 远端是运行态时
    expect(derivePanelState(makeSummary({ isRunning: true }), false, transition)).toBe(expected);
    // 远端是停止态时同样覆盖(如 start 点击瞬间远端还是 stopped)
    expect(derivePanelState(makeSummary({ isRunning: false }), false, transition)).toBe(expected);
  });

  it('过渡态优先于 error(远端轮询刚失败但用户刚点了按钮)', () => {
    expect(derivePanelState(makeSummary({ isRunning: true }), true, 'stopping')).toBe('stopping');
    expect(derivePanelState(null, false, 'starting')).toBe('starting');
  });

  it('过渡态优先于 logged_out', () => {
    expect(derivePanelState(makeSummary({ blockedCode: 'not_logged_in' }), false, 'stopping')).toBe('stopping');
  });

  it('空串/undefined 过渡态视同无过渡', () => {
    expect(derivePanelState(makeSummary({ isRunning: true }), false, '')).toBe('running');
    expect(derivePanelState(makeSummary({ isRunning: true }), false, undefined)).toBe('running');
  });

  it('未知过渡态字符串视同无过渡(容错,不炸)', () => {
    expect(derivePanelState(makeSummary({ isRunning: false }), false, 'flying')).toBe('stopped');
  });
});
