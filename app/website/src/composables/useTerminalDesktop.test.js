// 桌面编排几何持久化直测(2026-11-23 窗口排布保活):ensureActivated 的几何
// 来源优先级(注册表持久化 geom >级联缺省)与 setPos/toggleMaximize 的双写。
// 模块级单例形态(resetForTests 同 useTerminalDesktop/useTerminalWindows)。
import { describe, it, expect, beforeEach } from 'vitest';
import { useTerminalDesktop, resetForTests } from './useTerminalDesktop';
import { useTerminalWindows, resetForTests as resetWindows } from './useTerminalWindows';

describe('useTerminalDesktop 几何持久化(窗口排布保活)', () => {
  beforeEach(() => {
    resetForTests();
    resetWindows();
  });

  it('ensureActivated 优先用注册表持久化几何(刷新后排布原样),无 geom 走级联缺省', () => {
    const reg = useTerminalWindows();
    const entry = reg.createWindow('a');
    reg.saveWindowGeom(entry.id, { x: 333, y: 222, width: 900, height: 700, maximized: false });
    const desk = useTerminalDesktop();
    const g = desk.ensureActivated(entry.id, 0);
    expect(g.x).toBe(333);
    expect(g.y).toBe(222);
    expect(g.width).toBe(900);
    expect(g.height).toBe(700);
    expect(g.maximized).toBe(false);

    const b = reg.createWindow('b');
    const g2 = desk.ensureActivated(b.id, 1);
    expect(g2.width).toBe(760); // 任务契约缺省量纲
    expect(g2.x).not.toBe(333); // 级联落点,不吃别人几何
  });

  it('ensureActivated 幂等:已有内存几何原样保留(不重复吃持久化)', () => {
    const reg = useTerminalWindows();
    const entry = reg.createWindow('a');
    const desk = useTerminalDesktop();
    const g1 = desk.ensureActivated(entry.id, 0);
    g1.x = 500;
    const g2 = desk.ensureActivated(entry.id, 3);
    expect(g2.x).toBe(500);
  });

  it('setPos/toggleMaximize 同步注册表持久化(拖拽/最大化即落盘)', () => {
    const reg = useTerminalWindows();
    const entry = reg.createWindow('a');
    const desk = useTerminalDesktop();
    desk.ensureActivated(entry.id, 0);
    desk.setPos(entry.id, 111, 222);
    desk.toggleMaximize(entry.id);
    expect(reg.getWindowGeom(entry.id)).toEqual({
      x: 111,
      y: 222,
      width: 760,
      height: 480,
      maximized: true,
    });
  });
});
