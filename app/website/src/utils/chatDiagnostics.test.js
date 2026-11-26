// 会话信息抽屉纯工具测试(spec 2026-11-26-chat-info-drawer-design):守卫阈值
// 常量与百分比数学的边界行为。归一化逻辑在 useAliangChat 侧测(与帧消费同址)。
import { describe, it, expect } from 'vitest';
import { GUARD_MAXES, guardPct, contextPct } from './chatDiagnostics';

describe('GUARD_MAXES', () => {
  it('与手机端 DiagnosticsPanel 同源:dup 3 / err 5 / trunc 2', () => {
    expect(GUARD_MAXES).toEqual({ dupStreak: 3, errStreak: 5, truncStreak: 2 });
  });
});

describe('guardPct', () => {
  it('常规比例取整为整数百分比', () => {
    expect(guardPct(1, 3)).toBe(33);
    expect(guardPct(3, 5)).toBe(60);
  });

  it('越过阈值封顶 100(超载呈现,不回绕)', () => {
    expect(guardPct(7, 5)).toBe(100);
    expect(guardPct(100, 2)).toBe(100);
  });

  it('0 与负值连击返回 0%(合法的「未连击」)', () => {
    expect(guardPct(0, 5)).toBe(0);
    expect(guardPct(-1, 5)).toBe(0);
  });

  it('非数值 streak 或非法 max 返回 null(渲染层隐藏该条)', () => {
    expect(guardPct('abc', 5)).toBeNull();
    expect(guardPct(2, 0)).toBeNull();
    expect(guardPct(2, -3)).toBeNull();
    expect(guardPct(null, 5)).toBeNull();
    expect(guardPct(undefined, 5)).toBeNull();
  });
});

describe('contextPct', () => {
  it('与 guardPct 同一数学:used/window 整数百分比', () => {
    expect(contextPct(50000, 200000)).toBe(25);
    expect(contextPct(250000, 200000)).toBe(100);
  });

  it('窗口缺失返回 null(不画 0% 假表)', () => {
    expect(contextPct(1234, 0)).toBeNull();
    expect(contextPct(1234, undefined)).toBeNull();
  });
});
