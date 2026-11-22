// TerminalWorkspace 多窗编排(P2.4,计划 2026-11-22-web-terminal-phase23):三层覆盖——
// ① 纯函数直测(cascadePos/classifyDead,任务契约必测项);② useTerminalDesktop
// 编排态直测(真注册表单例,同 PinGuard 用真 useWebTerminal 的取向);③ 全工作台
// 挂载集成(mock services/agentApi 走真 useWebTerminal 解锁链 + TerminalPane 桩
// 化——真 xterm/WS 与本任务无关且 jsdom 无量纲)。手法 = createApp + h(同
// PinGuard.test.js);断言走 DOM 可观测量(壳 style/pill class/注册表状态),
// 不做内部函数 spy。面板保活铁律以「body display:none 且面板元素仍在 DOM」为
// 可观测等价断言(v-show 藏不销毁)。
// P2.5 追加:pruneOrphans 直测(②)+ 任务栏✕ 旁路修剪/尾「+」钮接线/外部关闸
// watcher/窗体点击聚焦直通四条挂载路径(③)。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';

vi.mock('../services/agentApi', () => ({
  getWebTerminalState: vi.fn(async () => ({})),
  webTerminalCommand: vi.fn(async () => ({ ok: true })),
}));

// TerminalPane 桩:只保留 token/dead/exit 契约面;按钮触发 dead 供分类路径测试。
// 工厂经动态 import 取 h(vi.mock 工厂不得引用外部变量,vitest 既定约束)。
vi.mock('./terminal/TerminalPane.vue', async () => {
  const { h: hVue } = await import('vue');
  return {
    default: {
      name: 'TerminalPaneStub',
      props: { token: { type: String, required: true } },
      emits: ['exit', 'dead'],
      setup(_, { emit }) {
        return () =>
          hVue('div', { 'data-testid': 'terminal-pane-stub' }, [
            hVue(
              'button',
              { 'data-testid': 'stub-emit-dead', type: 'button', onClick: () => emit('dead') },
              'emit-dead'
            ),
          ]);
      },
    },
  };
});

import TerminalWorkspace from './TerminalWorkspace.vue';
import { useI18n } from '../i18n';
import { getWebTerminalState, webTerminalCommand } from '../services/agentApi';
import { resetForTests as resetWebTerminal, useWebTerminal } from '../composables/useWebTerminal';
import {
  resetForTests as resetWindows,
  useTerminalWindows,
} from '../composables/useTerminalWindows';
import {
  cascadePos,
  classifyDead,
  DEAD_GLOBAL_RELOCK,
  DEAD_WINDOW_ONLY,
  resetForTests as resetDesktop,
  useTerminalDesktop,
} from '../composables/useTerminalDesktop';

const reg = useTerminalWindows();
const desk = useTerminalDesktop();
const wt = useWebTerminal(); // 外部关闸路径:直接驱单例 refresh 触发工作台 watcher
const { t } = useI18n(); // 文案期望经 t() 动态取(测试环境 locale 随 navigator,同 PinGuard.test.js)

// 保险箱健康态(agent_web_terminal.go Status 字段语义)
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

// 走真 PinGuard 解锁链:输 PIN → verify ok 落 token → 门链进已解锁分支
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

// ── ① 纯函数(任务契约必测) ────────────────────────────────────────────

describe('cascadePos', () => {
  it('契约值 40+(count%5)*28:0..4 递进、5 绕回、负数/非数值防御为 0 档', () => {
    expect(cascadePos(0)).toEqual({ x: 40, y: 40 });
    expect(cascadePos(1)).toEqual({ x: 68, y: 68 });
    expect(cascadePos(4)).toEqual({ x: 152, y: 152 });
    expect(cascadePos(5)).toEqual({ x: 40, y: 40 }); // 5 级绕回
    expect(cascadePos(7)).toEqual({ x: 96, y: 96 });
    expect(cascadePos(-3)).toEqual(cascadePos(2)); // 负数取模绕回同档
    expect(cascadePos(Number.NaN)).toEqual({ x: 40, y: 40 });
    expect(cascadePos('junk')).toEqual({ x: 40, y: 40 });
  });
});

describe('classifyDead', () => {
  it('保险箱不可用信号(关闸/PIN重置/锁定)→global-relock;健康/拉取失败→window-only', () => {
    expect(classifyDead({ enabled: false, pin_set: false, locked: false })).toBe(DEAD_GLOBAL_RELOCK);
    expect(classifyDead({ enabled: true, pin_set: false, locked: false })).toBe(DEAD_GLOBAL_RELOCK);
    expect(classifyDead({ enabled: true, pin_set: true, locked: true })).toBe(DEAD_GLOBAL_RELOCK);
    expect(classifyDead(HEALTHY)).toBe(DEAD_WINDOW_ONLY);
    expect(classifyDead(null)).toBe(DEAD_WINDOW_ONLY); // 拉取失败≠锁箱,不武断踢出
    expect(classifyDead(undefined)).toBe(DEAD_WINDOW_ONLY);
    expect(classifyDead('junk')).toBe(DEAD_WINDOW_ONLY);
  });
});

// ── ② 桌面编排态(真注册表) ────────────────────────────────────────────

describe('useTerminalDesktop', () => {
  it('createTerminalWindow:注册表登记+激活+缺省几何(级联/760x480/未最大化未死)', () => {
    const a = desk.createTerminalWindow('终端 1');
    expect(reg.windows.value).toHaveLength(1);
    expect(desk.activated[a.id]).toBe(true);
    expect(desk.geometry[a.id]).toMatchObject({
      x: 40, y: 40, width: 760, height: 480, maximized: false, dead: false,
    });
    const b = desk.createTerminalWindow('终端 2'); // 第二窗级联进位
    expect(desk.geometry[b.id]).toMatchObject({ x: 68, y: 68 });
  });

  it('closeAllWindows:清注册表全量(含未激活恢复壳)+几何/挂载标记同步清', () => {
    desk.createTerminalWindow('a');
    desk.createTerminalWindow('b');
    const shell = reg.createWindow('restored-shell'); // 模拟刷新恢复壳:注册表有、未激活
    expect(desk.activated[shell.id]).toBeUndefined();
    desk.closeAllWindows();
    expect(reg.windows.value).toHaveLength(0);
    expect(Object.keys(desk.geometry)).toHaveLength(0);
    expect(Object.keys(desk.activated)).toHaveLength(0);
  });

  it('几何操作:setPos 回写(非数值忽略)/markDead/toggleMaximize 满幅取容器量纲且原值保留', () => {
    const a = desk.createTerminalWindow('a');
    desk.setPos(a.id, 120, 66);
    expect(desk.geometry[a.id].x).toBe(120);
    expect(desk.geometry[a.id].y).toBe(66);
    desk.setPos(a.id, 'bad', 88);
    expect(desk.geometry[a.id].x).toBe(120); // 非数值忽略
    expect(desk.geometry[a.id].y).toBe(88);

    desk.markDead(a.id);
    expect(desk.geometry[a.id].dead).toBe(true);

    desk.toggleMaximize(a.id);
    desk.syncViewport(1000, 640);
    expect(desk.effectiveGeom(a.id)).toEqual({ x: 0, y: 0, width: 1000, height: 640 });
    desk.toggleMaximize(a.id); // 还原回原位(最大化不丢原值)
    expect(desk.effectiveGeom(a.id)).toEqual({ x: 120, y: 88, width: 760, height: 480 });
  });

  it('ensureActivated 幂等:已有几何不覆盖(重开恢复壳不跳位)', () => {
    const a = desk.createTerminalWindow('a');
    desk.setPos(a.id, 300, 200);
    desk.ensureActivated(a.id, 4); // 再次激活携带另一级联档
    expect(desk.geometry[a.id].x).toBe(300);
    expect(desk.geometry[a.id].y).toBe(200);
  });

  it('pruneOrphans(P2.5):注册表旁路摘除后孤儿几何/挂载标记被裁,存活窗不受牵连', () => {
    const a = desk.createTerminalWindow('a');
    const b = desk.createTerminalWindow('b');
    reg.closeWindow(b.id); // 旁路:任务栏✕ 路径,直连注册表不经 desk.closeWindow
    expect(desk.geometry[b.id]).not.toBeUndefined(); // 修剪前残留(泄漏在案)
    expect(desk.activated[b.id]).not.toBeUndefined();
    desk.pruneOrphans();
    expect(desk.geometry[b.id]).toBeUndefined();
    expect(desk.activated[b.id]).toBeUndefined();
    expect(desk.geometry[a.id]).not.toBeUndefined(); // 存活窗原样
    expect(desk.activated[a.id]).toBe(true);
  });
});

// ── ③ 全工作台挂载集成 ─────────────────────────────────────────────────

describe('TerminalWorkspace 多窗编排(挂载集成)', () => {
  it('未解锁:PinGuard 解锁面(门链分支 3 不动),无桌面入口', async () => {
    const m = await mountWorkspace();
    expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null);
    expect(m.q('[data-testid="terminal-new"]')).toBe(null);
    expect(m.q('[data-testid="terminal-desktop"]')).toBe(null);
  });

  it('解锁→空桌面提示;新建×2:两壳两面板+级联落点+z 序进壳+活动窗高亮+标题编号', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    expect(m.q('[data-testid="terminal-empty"]')).not.toBe(null); // 零窗空态
    await createN(m, 2);

    const shells = m.qa('[data-testid="floating-window"]');
    expect(shells).toHaveLength(2);
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(2); // 每窗恰一面板
    // 级联契约:40/40 → 68/68
    expect(shells[0].style.left).toBe('40px');
    expect(shells[0].style.top).toBe('40px');
    expect(shells[1].style.left).toBe('68px');
    expect(shells[1].style.top).toBe('68px');
    // 注册表 z 原样进壳(窗层带内竞争,不与任务栏带比)
    expect(shells[0].style.zIndex).toBe('1');
    expect(shells[1].style.zIndex).toBe('2');
    // 活动窗=z 最高 open 窗:第二窗高亮
    expect(shells[1].className).toContain('border-primary');
    expect(shells[0].className).not.toContain('border-primary');
    const titles = m.qa('[data-testid="floating-window-header"]').map((el) => el.textContent);
    expect(titles[0]).toContain(t('win_title', { n: 1 }));
    expect(titles[1]).toContain(t('win_title', { n: 2 }));
    // 空态撤下;任务栏两 pill
    expect(m.q('[data-testid="terminal-empty"]')).toBe(null);
    expect(m.qa('[data-testid^="taskbar-item-"]')).toHaveLength(2);
  });

  it('最小化保活:body v-show 藏不销毁;任务栏重开复显置顶成活动窗', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2);
    const [aId, bId] = reg.windows.value.map((w) => w.id);

    m.qa('[data-testid="floating-minimize"]')[0].click();
    await nextTick();
    expect(reg.windows.value.find((w) => w.id === aId).status).toBe('minimized');
    const bodies = m.qa('[data-testid="floating-window-body"]');
    expect(bodies[0].style.display).toBe('none'); // v-show 藏
    expect(bodies[1].style.display).toBe(''); // 兄弟窗不受牵连
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(2); // 面板未销毁(保活铁律)

    m.q(`[data-testid="taskbar-item-${aId}"]`).click();
    await nextTick();
    expect(reg.windows.value.find((w) => w.id === aId).status).toBe('open');
    expect(m.qa('[data-testid="floating-window-body"]')[0].style.display).toBe('');
    expect(m.q(`[data-testid="taskbar-item-${aId}"]`).className).toContain('border-primary');
    expect(m.q(`[data-testid="taskbar-item-${bId}"]`).className).not.toContain('border-primary');
  });

  it('关窗:✕ 摘注册表,v-for 卸载面板,兄弟窗不受牵连', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2);
    const [aId, bId] = reg.windows.value.map((w) => w.id);

    m.qa('[data-testid="floating-close"]')[0].click();
    await nextTick();
    expect(reg.windows.value.map((w) => w.id)).toEqual([bId]);
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(1);
    expect(m.qa('[data-testid^="taskbar-item-"]')).toHaveLength(1);
  });

  it('锁箱:清注册表(含最小化条目)+面板卸载+回落 PinGuard', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2);
    reg.minimizeWindow(reg.windows.value[0].id); // 一最小化一开:陈旧条目也须清
    await nextTick();

    m.q('[data-testid="terminal-lock"]').click();
    await flushMicrotasks();
    expect(reg.windows.value).toHaveLength(0);
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(0);
    expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null); // 回落密码盘
    expect(m.q('[data-testid="terminal-new"]')).toBe(null); // 桌面入口随门链消失
  });

  it('急停:confirm 确认后清注册表+回落 PinGuard', async () => {
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
    try {
      const m = await mountWorkspace();
      await unlock(m);
      await createN(m, 1);
      m.q('[data-testid="terminal-killall"]').click();
      await flushMicrotasks();
      expect(confirmSpy).toHaveBeenCalled();
      expect(reg.windows.value).toHaveLength(0);
      expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null);
    } finally {
      confirmSpy.mockRestore();
    }
  });

  it('单窗 dead 且保险箱健康:窗级死——⚠ 头栏徽标,桌面不退场注册表不动', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 1);
    m.q('[data-testid="stub-emit-dead"]').click();
    await flushMicrotasks();
    expect(m.q('[data-testid="terminal-new"]')).not.toBe(null); // 仍解锁
    expect(reg.windows.value).toHaveLength(1);
    expect(m.q('[data-testid="floating-window-header"]').textContent).toContain('⚠');
  });

  it('dead 且保险箱锁定:全局回退——清注册表+回落 PinGuard(阶段 1 行为)', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 1);
    vaultState = { enabled: true, pin_set: true, locked: true, lock_remaining_seconds: 30 };
    m.q('[data-testid="stub-emit-dead"]').click();
    await flushMicrotasks();
    expect(reg.windows.value).toHaveLength(0);
    expect(m.q('[data-testid="floating-window"]')).toBe(null);
    expect(m.q('[data-testid="vault-pin-input"]')).not.toBe(null);
  });

  it('刷新恢复语义:恢复壳只出任务栏 pill;点开才挂壳+面板(=新会话,标题保留)', async () => {
    // 造刷新后真实态:注册表有最小化窗、桌面零激活(模块 initPersistence 的等价终态)
    const shell = reg.createWindow('终端 9');
    reg.minimizeWindow(shell.id);

    const m = await mountWorkspace();
    await unlock(m);
    expect(m.qa('[data-testid^="taskbar-item-"]')).toHaveLength(1); // pill 在
    expect(m.q('[data-testid="floating-window"]')).toBe(null); // 壳不在(activated 门控)
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(0); // 无面板
    expect(m.q('[data-testid="terminal-empty"]')).toBe(null); // 有窗≠空态(仅 pill)

    m.q(`[data-testid="taskbar-item-${shell.id}"]`).click();
    await nextTick();
    expect(m.q('[data-testid="floating-window"]')).not.toBe(null); // 激活挂壳
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(1); // 面板此时才挂(新会话)
    expect(m.q('[data-testid="floating-window-header"]').textContent).toContain('终端 9');
  });

  it('任务栏✕ 旁路(P2.5 泄漏修剪):工作台 watch 注册表→孤儿几何/挂载标记被裁', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2);
    const [aId, bId] = reg.windows.value.map((w) => w.id);
    // ✕ 直连注册表 closeWindow(不经 desk.closeWindow 的本地清理)= 泄漏路径
    m.q(`[data-testid="taskbar-close-${bId}"]`).click();
    await nextTick();
    expect(reg.windows.value.map((w) => w.id)).toEqual([aId]);
    expect(desk.geometry[bId]).toBeUndefined(); // 孤儿已由工作台 watcher 修剪
    expect(desk.activated[bId]).toBeUndefined();
    expect(desk.geometry[aId]).not.toBeUndefined(); // 存活窗不受牵连
    expect(desk.activated[aId]).toBe(true);
  });

  it('任务栏尾「+」钮(P2.5):@create 接工作台 createTerminal——新窗入注册表+挂壳', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 1);
    m.q('[data-testid="taskbar-create"]').click();
    await nextTick();
    expect(reg.windows.value).toHaveLength(2);
    expect(m.qa('[data-testid="floating-window"]')).toHaveLength(2);
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(2);
  });

  it('外部关闸(P2.5 补测):enabled 经刷新翻 false → 清全部窗+门链退回未开启分支', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2);
    vaultState = { ...HEALTHY, enabled: false }; // 设置页等外部路径关闸
    await wt.refresh(); // 单例状态翻新 → 工作台 st.enabled watcher 观测
    await flushMicrotasks();
    expect(reg.windows.value).toHaveLength(0);
    expect(m.qa('[data-testid="terminal-pane-stub"]')).toHaveLength(0);
    expect(m.q('[data-testid="terminal-new"]')).toBe(null); // 桌面入口随门链消失
    expect(m.q('[data-testid="terminal-enable"]')).not.toBe(null); // 退回未开启分支
  });

  it('窗体点击聚焦直通(P2.5 补测):pane 区 pointerdown → focusWindow 抬 z 成活动窗', async () => {
    const m = await mountWorkspace();
    await unlock(m);
    await createN(m, 2); // A z1 / B z2(B 为活动窗)
    const panes = m.qa('[data-testid="terminal-pane-stub"]');
    panes[0].dispatchEvent(new MouseEvent('pointerdown', { bubbles: true })); // 不 .stop:xterm 侧仍可收
    await nextTick();
    const shells = m.qa('[data-testid="floating-window"]');
    expect(shells[0].style.zIndex).toBe('3'); // A 抬到 max+1
    expect(shells[0].className).toContain('border-primary'); // A 转活动窗
    expect(shells[1].className).not.toContain('border-primary');
  });
});
