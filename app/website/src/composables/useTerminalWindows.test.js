import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  WINDOWS_STORAGE_KEY,
  initPersistence,
  resetForTests,
  useTerminalWindows,
} from './useTerminalWindows';

function readRaw() {
  try {
    return window.localStorage.getItem(WINDOWS_STORAGE_KEY);
  } catch {
    return null;
  }
}

describe('useTerminalWindows', () => {
  beforeEach(() => {
    resetForTests();
  });

  it('createWindow 注册新窗:入表、status open、三连建 id 唯一、创建即落盘', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a');
    const b = tw.createWindow('b');
    const c = tw.createWindow('c');
    expect(tw.windows.value).toHaveLength(3);
    for (const [entry, title] of [[a, 'a'], [b, 'b'], [c, 'c']]) {
      expect(entry.title).toBe(title);
      expect(entry.status).toBe('open');
      expect(entry.id).toMatch(/^win-/);
      expect(entry.createdAt).toBeTypeOf('number');
      expect(entry.zIndex).toBeGreaterThan(0);
    }
    expect(new Set([a.id, b.id, c.id]).size).toBe(3);
    expect(readRaw()).toBeTruthy(); // 每次变更即持久化
    expect(JSON.parse(readRaw())).toHaveLength(3);
  });

  it('minimize→restore:状态迁移 + restore 置顶 zIndex=max+1;openWindows 按 z 排序', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a'); // z=1
    const b = tw.createWindow('b'); // z=2
    const c = tw.createWindow('c'); // z=3
    expect(tw.openWindows.value.map((w) => w.id)).toEqual([a.id, b.id, c.id]);

    tw.minimizeWindow(a.id);
    expect(a.status).toBe('minimized');
    expect(tw.openWindows.value.map((w) => w.id)).toEqual([b.id, c.id]);
    expect(tw.minimizedWindows.value.map((w) => w.id)).toEqual([a.id]);

    tw.restoreWindow(a.id);
    expect(a.status).toBe('open');
    expect(a.zIndex).toBe(c.zIndex + 1); // 恢复=置顶(当前最大 z + 1)
    expect(tw.openWindows.value.map((w) => w.id)).toEqual([b.id, c.id, a.id]);
  });

  it('closeWindow 移除;focusWindow 对 open=仅置顶、对 minimized=恢复语义', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a'); // z=1
    const b = tw.createWindow('b'); // z=2

    tw.focusWindow(a.id);
    expect(a.status).toBe('open'); // open 聚焦不改状态
    expect(a.zIndex).toBe(b.zIndex + 1); // 只置顶

    tw.minimizeWindow(a.id);
    tw.focusWindow(a.id); // minimized 聚焦=恢复(决策见实现注释)
    expect(a.status).toBe('open');
    // 此前 focus 已把 a 顶到 3(b 仍 2):恢复再置顶 → a=4=max+1
    expect(a.zIndex).toBe(4);
    expect(b.zIndex).toBe(2);

    tw.closeWindow(a.id);
    expect(tw.windows.value.map((w) => w.id)).toEqual([b.id]);
    expect(() => tw.closeWindow('win-not-exist')).not.toThrow(); // 未知 id 幂等
    expect(tw.windows.value).toHaveLength(1);
  });

  it('z 越界 renumber:手置 zIndex 10001 → nextZ 把 open 窗压回紧凑序列', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a'); // z=1
    const b = tw.createWindow('b'); // z=2
    const m = tw.createWindow('m'); // z=3
    tw.minimizeWindow(m.id);

    b.zIndex = 10001; // 模拟长会话/旧数据越界
    const z = tw.nextZ();
    expect(a.zIndex).toBe(1); // open 按原序(低→高)重排为 1..n
    expect(b.zIndex).toBe(2);
    expect(z).toBe(3); // 返回 n+1
    expect(m.zIndex).toBe(3); // minimized 不参与 renumber,保持原值
  });

  it('持久化往返:两条落盘 → 清内存态从同键恢复 → status/zIndex 原样保留(会话保活刷新语义)、id/title/createdAt 保留', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a');
    const b = tw.createWindow('b');
    const raw = readRaw();
    expect(JSON.parse(raw)).toHaveLength(2);

    // 模拟刷新:resetForTests 连内存带存储键一起清(先备份载荷),再回填同键恢复
    resetForTests();
    expect(tw.windows.value).toHaveLength(0); // 顺带覆盖:resetForTests 清 windows
    expect(readRaw()).toBe(null); // 顺带覆盖:resetForTests 清存储键
    window.localStorage.setItem(WINDOWS_STORAGE_KEY, raw);

    tw.loadPersisted();
    expect(tw.windows.value).toHaveLength(2);
    // 会话保活(2026-11-23):sessionId=窗 id 后刷新恢复的 open 窗直接回桌面,
    // openWindows immediate watch 自动挂面板 attach 同 id 会话续屏
    for (const w of tw.windows.value) {
      expect(w.status).toBe('open');
      expect(w.zIndex).toBeGreaterThan(0);
    }
    const byId = Object.fromEntries(tw.windows.value.map((w) => [w.id, w]));
    expect(byId[a.id].title).toBe('a');
    expect(byId[a.id].createdAt).toBe(a.createdAt);
    expect(byId[b.id].title).toBe('b');
    expect(tw.minimizedWindows.value).toHaveLength(0);
    expect(tw.openWindows.value).toHaveLength(2);
  });

  it('initPersistence:启动恢复(同 loadPersisted),脏条目/脏载荷安全忽略', () => {
    window.localStorage.setItem(
      WINDOWS_STORAGE_KEY,
      JSON.stringify([
        { id: 'win-x', title: 'x', status: 'open', zIndex: 9, createdAt: 111 },
        { title: '缺 id 脏条目' },
        'garbage',
      ])
    );
    const tw = useTerminalWindows();
    initPersistence();
    expect(tw.windows.value).toHaveLength(1);
    expect(tw.windows.value[0]).toMatchObject({
      id: 'win-x',
      title: 'x',
      status: 'open', // 会话保活:落盘 open 恢复 open
      zIndex: 9,
      createdAt: 111,
    });

    window.localStorage.setItem(WINDOWS_STORAGE_KEY, 'not-json{');
    tw.loadPersisted();
    expect(tw.windows.value).toHaveLength(0);
    window.localStorage.setItem(WINDOWS_STORAGE_KEY, JSON.stringify({ no: 'array' }));
    tw.loadPersisted();
    expect(tw.windows.value).toHaveLength(0);
  });

  it('setWindowTitle 改名并落盘', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a');
    tw.setWindowTitle(a.id, 'build server');
    expect(a.title).toBe('build server');
    expect(JSON.parse(readRaw())[0].title).toBe('build server');
  });

  it('localStorage 抛错(隐私模式/配额)不炸:内存态动作仍生效', () => {
    const spy = vi.spyOn(window.localStorage, 'setItem').mockImplementation(() => {
      throw new Error('quota exceeded');
    });
    try {
      const tw = useTerminalWindows();
      let entry = null;
      expect(() => { entry = tw.createWindow('a'); }).not.toThrow();
      expect(tw.windows.value).toHaveLength(1);
      expect(() => tw.setWindowTitle(entry.id, 'renamed')).not.toThrow();
      expect(entry.title).toBe('renamed');
      expect(() => tw.closeWindow(entry.id)).not.toThrow();
      expect(tw.windows.value).toHaveLength(0);
    } finally {
      spy.mockRestore();
    }
  });
});

describe('窗口几何持久化(2026-11-23 排布保活)', () => {
  it('saveWindowGeom 落条目→模拟刷新→loadPersisted 几何原样恢复', () => {
    const tw = useTerminalWindows();
    const a = tw.createWindow('a');
    tw.saveWindowGeom(a.id, { x: 120, y: 80, width: 900, height: 600, maximized: true });
    const raw = readRaw();

    resetForTests();
    window.localStorage.setItem(WINDOWS_STORAGE_KEY, raw);
    tw.loadPersisted();

    const w = tw.windows.value.find((x) => x.id === a.id);
    expect(w.geom).toEqual({ x: 120, y: 80, width: 900, height: 600, maximized: true });
  });

  it('脏 geom(缺数值/负宽高/非对象)整组弃用回落 null,不恢复畸形窗', () => {
    window.localStorage.setItem(
      WINDOWS_STORAGE_KEY,
      JSON.stringify([
        { id: 'win-g1', title: '', status: 'open', zIndex: 1, createdAt: 1, geom: { x: 10, y: 20, width: -5, height: 100, maximized: false } },
        { id: 'win-g2', title: '', status: 'open', zIndex: 2, createdAt: 2, geom: { x: 'abc', y: 0, width: 100, height: 100, maximized: false } },
        { id: 'win-g3', title: '', status: 'open', zIndex: 3, createdAt: 3, geom: 'garbage' },
        { id: 'win-g4', title: '', status: 'open', zIndex: 4, createdAt: 4, geom: null },
      ])
    );
    const tw = useTerminalWindows();
    tw.loadPersisted();
    const byId = Object.fromEntries(tw.windows.value.map((w) => [w.id, w]));
    expect(byId['win-g1'].geom).toBe(null);
    expect(byId['win-g2'].geom).toBe(null);
    expect(byId['win-g3'].geom).toBe(null);
    expect(byId['win-g4'].geom).toBe(null);
  });

  it('saveWindowGeom 非有限坐标不落盘(拖拽中间态 NaN 不污染持久化)', () => {
    resetForTests(); // 显式清场:断言按 title 定位本用例窗,不受前用例表残留影响
    const tw = useTerminalWindows();
    const a = tw.createWindow('a');
    tw.saveWindowGeom(a.id, { x: 10, y: 10, width: 500, height: 400, maximized: false });
    tw.saveWindowGeom(a.id, { x: NaN, y: undefined, width: 0, height: -1, maximized: false });
    const parsed = JSON.parse(readRaw());
    const mine = parsed.find((row) => row.title === 'a');
    expect(mine).toBeTruthy();
    expect(mine.geom).toEqual({ x: 10, y: 10, width: 500, height: 400, maximized: false });
  });
});
