// 任务栏(P2.3,计划 2026-11-22-web-terminal-phase23):轻组件测试,仓内手法 =
// createApp + h 挂载(同 PinGuard.test.js)。行为断言走真实 useTerminalWindows
// 单例注册表(同 PinGuard 用真实 useWebTerminal 的取向):点击副作用 = 注册表
// 状态翻转/移除,不做内部函数 spy。✕ 区 @click.stop 属防御纵深(冒泡序下 close
// 先于 pill 处理器执行,移除后 pill 处理器本就 no-op),以「移除干净 + 兄弟窗
// 不受牵连」为可观测等价断言。P2.5 补齐两例:尾「+」钮 emit create(经挂载根
// onXxx 计数捕获,同 FloatingWindow.test.js 手法);✕ 键盘可达(Enter/Space)。
import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import TerminalTaskbar from './TerminalTaskbar.vue';
import { useI18n } from '../../i18n';
import {
  resetForTests,
  useTerminalWindows,
} from '../../composables/useTerminalWindows';

const reg = useTerminalWindows();
const { t } = useI18n();

async function mount(props = {}) {
  const el = document.createElement('div');
  let created = 0; // 尾「+」钮 create 上抛计数(onCreate 捕获)
  document.body.appendChild(el);
  const app = createApp({
    render: () =>
      h(TerminalTaskbar, { activeId: '', onCreate: () => { created += 1; }, ...props }),
  });
  app.mount(el);
  await nextTick();
  const q = (sel) => el.querySelector(sel);
  const qa = (sel) => [...el.querySelectorAll(sel)];
  return { el, app, q, qa, createdCount: () => created };
}

// 种子:两窗(A/B),B 最小化——契约场景的最小形态
function seedTwo() {
  const a = reg.createWindow('终端 A');
  const b = reg.createWindow('终端 B');
  reg.minimizeWindow(b.id);
  return { a, b };
}

function pill(q, id) {
  return q(`[data-testid="taskbar-item-${id}"]`);
}

function click(el) {
  el.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }));
}

beforeEach(() => {
  resetForTests();
});

afterEach(() => {
  document.body.innerHTML = '';
});

describe('TerminalTaskbar', () => {
  it('渲染:开窗+最小化窗各一颗 pill,标题可见,状态点绿/琥珀分明', async () => {
    const { a, b } = seedTwo();
    const m = await mount();
    const pills = m.qa('[data-testid^="taskbar-item-"]');
    expect(pills.length).toBe(2);
    expect(pill(m.q, a.id)).not.toBe(null);
    expect(pill(m.q, b.id)).not.toBe(null);
    expect(pill(m.q, a.id).textContent).toContain('终端 A');
    expect(pill(m.q, b.id).textContent).toContain('终端 B');
    // 状态点:open=绿, minimized=琥珀
    const dotA = pill(m.q, a.id).querySelector(`[data-testid="taskbar-dot-${a.id}"]`);
    const dotB = pill(m.q, b.id).querySelector(`[data-testid="taskbar-dot-${b.id}"]`);
    expect(dotA.className).toContain('bg-emerald-500');
    expect(dotB.className).toContain('bg-amber-400');
  });

  it('排序拍板:z 序合并(最近交互居右)——minimize 不动 z,focus 抬 z', async () => {
    const a = reg.createWindow('A'); // z1
    const b = reg.createWindow('B'); // z2
    reg.createWindow('C'); // z3
    reg.minimizeWindow(b.id); // B 仍 z2
    reg.focusWindow(a.id); // A 抬到 z4
    const m = await mount();
    // pill 文本 = 标题+✕,取标题 span 断言序
    const order = m.qa('[data-testid^="taskbar-title-"]').map((p) => p.textContent);
    expect(order).toEqual(['B', 'C', 'A']);
  });

  it('点最小化 pill → 恢复:status 回 open 且置顶(z=max+1)', async () => {
    const { b } = seedTwo();
    const m = await mount();
    const maxZ = Math.max(...reg.windows.value.map((w) => w.zIndex));
    click(pill(m.q, b.id));
    await nextTick();
    const row = reg.windows.value.find((w) => w.id === b.id);
    expect(row.status).toBe('open');
    expect(row.zIndex).toBe(maxZ + 1);
    // 恢复后状态点翻绿
    const dot = m.q(`[data-testid="taskbar-dot-${b.id}"]`);
    expect(dot.className).toContain('bg-emerald-500');
  });

  it('点开窗 pill → 聚焦置顶不改状态(status 仍 open,z=max+1)', async () => {
    const { a, b } = seedTwo(); // A z1 open / B z2 minimized
    const m = await mount();
    click(pill(m.q, a.id));
    await nextTick();
    const row = reg.windows.value.find((w) => w.id === a.id);
    expect(row.status).toBe('open');
    expect(row.zIndex).toBe(3); // max(1,2)+1
    expect(reg.windows.value.find((w) => w.id === b.id).status).toBe('minimized');
  });

  it('✕ 区:窗从注册表移除,兄弟窗不受牵连', async () => {
    const { a, b } = seedTwo();
    const m = await mount();
    click(m.q(`[data-testid="taskbar-close-${b.id}"]`));
    await nextTick();
    expect(reg.windows.value.some((w) => w.id === b.id)).toBe(false);
    expect(reg.windows.value.some((w) => w.id === a.id)).toBe(true);
    expect(m.qa('[data-testid^="taskbar-item-"]').length).toBe(1);
  });

  it('active 高亮:activeId 命中开窗才亮,命中最小化窗不亮', async () => {
    const { a, b } = seedTwo();
    const m = await mount({ activeId: a.id });
    expect(pill(m.q, a.id).className).toContain('border-primary');
    expect(pill(m.q, b.id).className).not.toContain('border-primary');
    // activeId 指向最小化窗:不算活动(活动窗必须是 open)
    const m2 = await mount({ activeId: b.id });
    expect(pill(m2.q, b.id).className).not.toContain('border-primary');
  });

  it('空注册表:任务栏不渲染(零窗不留空条)', async () => {
    const m = await mount();
    expect(m.q('[data-testid="terminal-taskbar"]')).toBe(null);
  });

  it('尾「+」钮(P2.5):emit create 一次,提示词条走 win_new;仅随非空栏渲染', async () => {
    const m0 = await mount(); // 零窗:栏自隐,+ 钮不存在(头部/空态入口覆盖)
    expect(m0.q('[data-testid="taskbar-create"]')).toBe(null);

    reg.createWindow('A');
    const m = await mount();
    const plus = m.q('[data-testid="taskbar-create"]');
    expect(plus).not.toBe(null);
    expect(plus.getAttribute('title')).toBe(t('win_new'));
    click(plus);
    expect(m.createdCount()).toBe(1);
  });

  it('✕ 键盘可达(P2.3 评审遗留):tabindex=0,Enter/Space 同 closeWindow 且不牵连兄弟', async () => {
    const { a, b } = seedTwo();
    const m = await mount();
    const closeA = m.q(`[data-testid="taskbar-close-${a.id}"]`);
    expect(closeA.getAttribute('tabindex')).toBe('0'); // 原为 -1(不可 Tab)
    closeA.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    );
    await nextTick();
    expect(reg.windows.value.some((w) => w.id === a.id)).toBe(false);
    expect(reg.windows.value.some((w) => w.id === b.id)).toBe(true); // 兄弟不牵连
    m.q(`[data-testid="taskbar-close-${b.id}"]`).dispatchEvent(
      new KeyboardEvent('keydown', { key: ' ', bubbles: true, cancelable: true })
    );
    await nextTick();
    expect(reg.windows.value).toHaveLength(0);
  });
});
