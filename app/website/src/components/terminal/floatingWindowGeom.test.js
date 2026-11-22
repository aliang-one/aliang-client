// 浮窗几何钳制纯函数测试(P2.2,计划 2026-11-22-web-terminal-phase23)。
// 拖拽边界规则(任务契约):x∈[-width+80, viewportWidth-80]、y∈[0, viewportHeight-40]
// ——窗口任何情况下至少 80px 宽/40px 高留在视口内(头栏永远可再抓,不会拖丢)。
import { describe, it, expect } from 'vitest';
import { clampWindowPos, KEEP_VISIBLE_X, KEEP_VISIBLE_Y } from './floatingWindowGeom';

describe('clampWindowPos', () => {
  it('范围内原样通过(含上边界值)', () => {
    expect(clampWindowPos(100, 80, 600, 400, 1920, 1080)).toEqual({ x: 100, y: 80 });
    // 恰好压在右/下边界也算在界内
    expect(clampWindowPos(1840, 1040, 600, 400, 1920, 1080)).toEqual({ x: 1840, y: 1040 });
  });

  it('左溢出钳到 -width+80(右缘仍留 80px 可抓)', () => {
    expect(clampWindowPos(-1000, 100, 600, 400, 1920, 1080).x).toBe(-600 + KEEP_VISIBLE_X);
  });

  it('右溢出钳到 viewportWidth-80(左缘仍留 80px 可抓)', () => {
    expect(clampWindowPos(5000, 100, 600, 400, 1920, 1080).x).toBe(1920 - KEEP_VISIBLE_X);
  });

  it('底溢出钳到 viewportHeight-40;顶溢出钳到 0(头栏不进负区)', () => {
    const p = clampWindowPos(100, 5000, 600, 400, 1920, 1080);
    expect(p.y).toBe(1080 - KEEP_VISIBLE_Y);
    expect(clampWindowPos(100, -50, 600, 400, 1920, 1080).y).toBe(0);
  });

  it('极小视口退化:钳制区间倒置时取下限(x→-width+80,y→0),仍留一段可抓', () => {
    // 视口 60×30:hiX=60-80=-20 < loX=-100+80=-20 → 相等;y 侧 hiY=30-40=-10 < 0 → 取 0
    const p = clampWindowPos(500, 500, 100, 200, 60, 30);
    expect(p.x).toBe(-100 + KEEP_VISIBLE_X);
    expect(p.y).toBe(0);
  });

  it('视口参数缺省时读 window 量纲(jsdom 默认 1024×768)', () => {
    const p = clampWindowPos(9999, 9999, 600, 400);
    expect(p.x).toBe(1024 - KEEP_VISIBLE_X);
    expect(p.y).toBe(768 - KEEP_VISIBLE_Y);
  });
});
