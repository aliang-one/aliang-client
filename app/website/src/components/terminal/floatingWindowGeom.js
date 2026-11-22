// 浮窗几何钳制纯函数(P2.2,计划 2026-11-22-web-terminal-phase23):拖拽与程序化
// 定位共用的视口边界钳制。独立成模块而非组件内联,是为了 vitest 直测(内联则
// 必须起 DOM 拖拽序列才能覆盖几何分支)。
// 约束语义:窗口任何情况下至少 KEEP_VISIBLE_X px 宽、KEEP_VISIBLE_Y px 高留在
// 视口内——头栏永远可被再次抓住,不会拖丢。x 下限=-width+KEEP_X(右缘留 80px)、
// 上限=viewport-KEEP_X(左缘留 80px);y 下限=0(头栏不进负区)、上限=
// viewport-KEEP_Y(底缘留 40px)。钳制区间倒置(视口比窗口还小)时取下限:
// 仍保证有一段窗口贴视口左/上缘可见。

// 可视保底:横向 80px / 纵向 40px(P2.2 任务契约值)
export const KEEP_VISIBLE_X = 80;
export const KEEP_VISIBLE_Y = 40;

function readViewport(vw, vh) {
  // 视口参数缺省时读全局 window(jsdom 亦有量纲);无 window 环境(SSR 兜底)取 0
  const w = Number.isFinite(vw) ? vw : typeof window !== 'undefined' ? window.innerWidth : 0;
  const h = Number.isFinite(vh) ? vh : typeof window !== 'undefined' ? window.innerHeight : 0;
  return { w, h };
}

// 钳制窗口左上角坐标:(x, y) + 窗口量纲 → 视口内合法 {x, y}。纯函数,无副作用。
export function clampWindowPos(x, y, width, height, viewportWidth, viewportHeight) {
  const { w, h } = readViewport(viewportWidth, viewportHeight);
  const loX = -width + KEEP_VISIBLE_X;
  const hiX = Math.max(loX, w - KEEP_VISIBLE_X); // 视口过窄:钳回 loX(右缘仍留 80px)
  const loY = 0;
  const hiY = Math.max(loY, h - KEEP_VISIBLE_Y); // 视口过矮:钳回 0(头栏贴顶可抓)
  return {
    x: Math.min(Math.max(x, loX), hiX),
    y: Math.min(Math.max(y, loY), hiY),
  };
}
