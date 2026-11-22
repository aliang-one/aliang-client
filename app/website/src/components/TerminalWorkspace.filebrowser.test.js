// TerminalWorkspace 文件浏览窗集成(P3.6,spec §7.4):「文件」钮→FileBrowserWindow
// 挂载/三态循环(开/最小化/恢复)/关闭复开几何保留/z 恒浮终端之上/高亮互斥/
// 锁箱连带关闭。手法同 TerminalWorkspace.multiwindow.test.js(mock agentApi 走
// 真 useWebTerminal 解锁链 + TerminalPane 桩),FileBrowserWindow 亦桩化——本文件
// 只钉「工作台编排接线」,窗内上传闭环在 FileBrowserWindow.test.js 用真身覆盖。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';

vi.mock('../services/agentApi', () => ({
  getWebTerminalState: vi.fn(async () => ({})),
  webTerminalCommand: vi.fn(async () => ({ ok: true })),
}));

// TerminalPane 桩:同 multiwindow 测试(真 xterm/WS 与本文件无关)
vi.mock('./terminal/TerminalPane.vue', async () => {
  const { h: hVue } = await import('vue');
  return {
    default: {
      name: 'TerminalPaneStub',
      props: { token: { type: String, required: true } },
      emits: ['exit', 'dead'],
      setup() {
        return () => hVue('div', { 'data-testid': 'terminal-pane-stub' });
      },
    },
  };
});

// FileBrowserWindow 桩:透传几何/态 props 到可观测 data-*;四按钮回抛窗事件
vi.mock('./terminal/FileBrowserWindow.vue', async () => {
  const { h: hVue } = await import('vue');
  return {
    default: {
      name: 'FileBrowserWindowStub',
      props: ['token', 'x', 'y', 'width', 'height', 'zIndex', 'active', 'minimized'],
      emits: ['move', 'minimize', 'maximize-toggle', 'close', 'focus'],
      setup(props, { emit }) {
        return () =>
          hVue(
            'div',
            {
              'data-testid': 'file-browser-stub',
              'data-x': String(props.x),
              'data-y': String(props.y),
              'data-w': String(props.width),
              'data-z': String(props.zIndex),
              'data-active': String(props.active),
              'data-minimized': String(props.minimized),
            },
            [
              hVue('button', { 'data-testid': 'stub-browser-close', type: 'button', onClick: () => emit('close') }, 'close'),
              hVue('button', { 'data-testid': 'stub-browser-minimize', type: 'button', onClick: () => emit('minimize') }, 'min'),
              hVue('button', { 'data-testid': 'stub-browser-maximize', type: 'button', onClick: () => emit('maximize-toggle') }, 'max'),
              hVue('button', { 'data-testid': 'stub-browser-focus', type: 'button', onClick: () => emit('focus') }, 'focus'),
            ]
          );
      },
    },
  };
});

import TerminalWorkspace from './TerminalWorkspace.vue';
import { getWebTerminalState, webTerminalCommand } from '../services/agentApi';
import { resetForTests as resetWebTerminal } from '../composables/useWebTerminal';
import { resetForTests as resetWindows, useTerminalWindows } from '../composables/useTerminalWindows';
import { resetForTests as resetDesktop, useTerminalDesktop } from '../composables/useTerminalDesktop';

const reg = useTerminalWindows();
const desk = useTerminalDesktop();

const HEALTHY = { enabled: true, pin_set: true, locked: false, lock_remaining_seconds: 0 };
let vaultState;

async function flushMicrotasks() {
  for (let i = 0; i < 10; i += 1) await Promise.resolve();
  await nextTick();
}

let app = null;

async function mountWorkspace() {
  const el = document.createElement('div');
  document.body.appendChild(el);
  app = createApp({ render: () => h(TerminalWorkspace) });
  app.mount(el);
  await flushMicrotasks();
  const q = (sel) => el.querySelector(sel);
  const qa = (sel) => [...el.querySelectorAll(sel)];
  return { el, q, qa };
}

async function unlock(m) {
  const input = m.q('[data-testid="vault-pin-input"]');
  input.value = '1234';
  input.dispatchEvent(new Event('input'));
  m.q('[data-testid="vault-submit"]').click();
  await flushMicrotasks();
}

async function createN(m, n) {
  for (let i = 0; i < n; i += 1) {
    m.q('[data-testid="terminal-new"]').click();
    await nextTick();
  }
}

function clickFiles(m) {
  m.q('[data-testid="terminal-files"]').click();
}

beforeEach(() => {
  vi.clearAllMocks();
  resetWebTerminal();
  resetWindows();
  resetDesktop();
  vaultState = { ...HEALTHY };
  getWebTerminalState.mockImplementation(async () => vaultState);
  webTerminalCommand.mockImplementation(async (payload) => {
    if (payload && payload.action === 'verify') return { ok: true, token: 'tok-1' };
    return { ok: true };
  });
});

afterEach(() => {
  if (app) {
    app.unmount();
    app = null;
  }
  document.body.innerHTML = '';
});

describe('TerminalWorkspace 文件浏览窗(P3.6)', () => {
  it('未解锁:无「文件」钮;解锁后出现(门控同新终端钮)', async () => {
    const m = await mountWorkspace();
    expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null);
    expect(m.q('[data-testid="terminal-files"]')).toBe(null); // 锁面无入口
    await unlock(m);
    expect(m.q('[data-testid="terminal-files"]')).not.toBe(null);
  });

  it('「文件」钮三态循环:关→开(级联落点按终端数);开→最小化;最小化→恢复', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2); // 两终端在册:cascadePos(2)={x:96,y:96}
    expect(m.q('[data-testid="file-browser-stub"]')).toBe(null); // 未开无窗

    clickFiles(m); // 关→开
    await nextTick();
    const stub = m.q('[data-testid="file-browser-stub"]');
    expect(stub).not.toBe(null);
    expect(stub.getAttribute('data-x')).toBe('96');
    expect(stub.getAttribute('data-y')).toBe('96');
    expect(stub.getAttribute('data-minimized')).toBe('false');
    expect(stub.getAttribute('data-active')).toBe('true'); // 开即活动
    // 零终端空态因文件窗在场而退场
    expect(m.q('[data-testid="terminal-empty"]')).toBe(null);

    clickFiles(m); // 开且可见→最小化
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-minimized')).toBe('true');

    clickFiles(m); // 最小化→恢复
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-minimized')).toBe('false');
  });

  it('z 恒浮终端之上:maxTerminalZ+10;终端聚焦抬 z 不越过文件窗', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2); // z1/z2
    clickFiles(m);
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-z')).toBe('12'); // 2+10
    // 窗体点击聚焦第一窗(z→3):文件窗随抬到 13,仍在其上
    m.qa('[data-testid="terminal-pane-stub"]')[0].dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }));
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-z')).toBe('13');
  });

  it('高亮互斥:终端成活动窗撤文件窗高亮;文件窗 focus(含最小化恢复)夺回', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 1);
    clickFiles(m);
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-active')).toBe('true');

    // 新建终端→activeId 变→文件窗高亮撤
    m.q('[data-testid="terminal-new"]').click();
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-active')).toBe('false');

    m.q('[data-testid="stub-browser-focus"]').click(); // 文件窗聚焦夺回
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-active')).toBe('true');

    // 最小化后 focus=恢复+夺回(壳头栏点击即复活的语义)
    m.q('[data-testid="stub-browser-minimize"]').click();
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-minimized')).toBe('true');
    m.q('[data-testid="stub-browser-focus"]').click();
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-minimized')).toBe('false');
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-active')).toBe('true');
  });

  it('关闭→卸载;复开几何保留(拖拽后的原位);最大化切换接线', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    clickFiles(m);
    await nextTick();
    // 桩不真拖拽,直接驱动 move 事件等效几何回写路径?——move 由壳上抛,桩无拖拽
    // 面;以 maximize 切换钉「窗事件→几何状态」接线:jsdom 量纲 0,满幅=0
    m.q('[data-testid="stub-browser-maximize"]').click();
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-w')).toBe('0');
    m.q('[data-testid="stub-browser-maximize"]').click(); // 还原回 860
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-w')).toBe('860');

    m.q('[data-testid="stub-browser-close"]').click(); // 壳✕:browserOpen=false
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]')).toBe(null);
    expect(m.q('[data-testid="terminal-empty"]')).not.toBe(null); // 零窗空态回归

    clickFiles(m); // 复开:缺省几何路径(首开几何已在,复用)
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]').getAttribute('data-w')).toBe('860'); // 还原态非最大化
  });

  it('锁箱连带关文件窗:锁后回落 PinGuard,文件窗不残留', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    clickFiles(m);
    await nextTick();
    expect(m.q('[data-testid="file-browser-stub"]')).not.toBe(null);
    m.q('[data-testid="terminal-lock"]').click();
    await flushMicrotasks();
    expect(m.q('[data-testid="file-browser-stub"]')).toBe(null);
    expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null);
    expect(reg.windows.value).toHaveLength(0);
  });
});
