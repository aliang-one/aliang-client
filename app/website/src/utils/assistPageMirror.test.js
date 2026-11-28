// 远程协助 P3:观看页镜像按宽缩放纯函数测试(assistPageMirror.js)。
//
// ⚠️ jsdom 测不了 transform/clip 的复合几何(transform 不占布局、overflow
// 裁剪在变换前本地坐标系——jsdom 根本不跑布局/Paint),本文件的断言只能钉
// 「写了哪些样式值」,钉不住渲染结果。P3 终审的 Critical(wrapper 布局=
// 缩放尺寸 + wrapper transform 双重缩放、裁半页)就是 1479 绿测放行、
// headless-Chrome 结构探针才抓到的。本表面改几何必须配真浏览器探针
// (probe 页 + getBoundingClientRect + elementFromPoint + 截图)。
import { describe, it, expect } from 'vitest';
import { computePageScale, applyPageMirrorLayout } from './assistPageMirror';

describe('computePageScale 按宽缩放比', () => {
  it('容器宽 / Meta 宽(放大与缩小两向)', () => {
    expect(computePageScale(1200, 1000)).toBe(1.2);
    expect(computePageScale(800, 1600)).toBe(0.5);
    expect(computePageScale(1280, 1280)).toBe(1);
  });

  it('极端 0 宽保护:容器宽 / Meta 宽任一非正 → 1(原尺寸直出,不缩没)', () => {
    expect(computePageScale(0, 1000)).toBe(1); // 隐藏 pane 的 clientWidth=0
    expect(computePageScale(1200, 0)).toBe(1);
    expect(computePageScale(-5, 1000)).toBe(1);
    expect(computePageScale(1200, -5)).toBe(1);
    expect(computePageScale(NaN, 1000)).toBe(1);
    expect(computePageScale(1200, undefined)).toBe(1);
  });
});

describe('applyPageMirrorLayout 尺寸恢复 + 缩放施加(几何形状见 util 头注)', () => {
  function fakeReplayer() {
    return {
      iframe: {
        attrs: {},
        style: {},
        setAttribute(k, v) { this.attrs[k] = v; },
      },
      wrapper: { style: {} },
    };
  }

  const DIMS = { width: 1000, height: 700 };

  it('尺寸恢复:iframe width/height 属性 + style 双写回 Meta 尺寸(0×0 塌缩修复)', () => {
    const rp = fakeReplayer();
    const sizer = { style: {} };
    const scale = applyPageMirrorLayout(rp, sizer, 1000, DIMS);
    expect(scale).toBe(1);
    expect(rp.iframe.attrs).toEqual({ width: '1000', height: '700' });
    expect(rp.iframe.style.width).toBe('1000px');
    expect(rp.iframe.style.height).toBe('700px');
  });

  it('按宽缩放(终审 Fix 2 定案几何):wrapper 布局 RAW Meta + 单层 scale;sizer=缩放尺寸+overflow hidden', () => {
    const rp = fakeReplayer();
    const sizer = { style: {} };
    const scale = applyPageMirrorLayout(rp, sizer, 500, DIMS);
    expect(scale).toBe(0.5);
    // wrapper 层单层复合变换:子元素 (x,y) → (x·s,y·s),指针/轨迹/内容对齐
    expect(rp.wrapper.style.transform).toBe('scale(0.5)');
    expect(rp.wrapper.style.transformOrigin).toBe('0 0');
    // wrapper 布局尺寸 = RAW Meta(transform 不占布局,布局盒只做子元素定位
    // 底;写成缩放尺寸会让自身 border-box 被自己的 transform 再缩一次 = 双重
    // 缩放 + 本地坐标裁半页,终审 Critical 即此)
    expect(rp.wrapper.style.width).toBe('1000px');
    expect(rp.wrapper.style.height).toBe('700px');
    // sizer(不变换的层)承载缩放后布局占位 + 裁剪边界(post-transform 空间)
    expect(sizer.style.width).toBe('500px');
    expect(sizer.style.height).toBe('350px');
    expect(sizer.style.overflow).toBe('hidden');
    // iframe 不带 transform(缩放责任收口在 wrapper 单层)
    expect(rp.iframe.style.transform).toBeUndefined();
  });

  it('放大向:容器宽于 Meta → scale>1,sizer 同步放大', () => {
    const rp = fakeReplayer();
    const sizer = { style: {} };
    const scale = applyPageMirrorLayout(rp, sizer, 2000, DIMS);
    expect(scale).toBe(2);
    expect(rp.wrapper.style.transform).toBe('scale(2)');
    expect(rp.wrapper.style.width).toBe('1000px'); // 布局恒 RAW Meta
    expect(sizer.style.width).toBe('2000px');
    expect(sizer.style.height).toBe('1400px');
  });

  it('s=1 清空 wrapper transform,sizer=RAW Meta 尺寸', () => {
    const rp = fakeReplayer();
    const sizer = { style: {} };
    applyPageMirrorLayout(rp, sizer, 2000, DIMS); // 先放大
    expect(rp.wrapper.style.transform).toBe('scale(2)');
    applyPageMirrorLayout(rp, sizer, 1000, DIMS); // 回到 1:1
    expect(rp.wrapper.style.transform).toBe('');
    expect(sizer.style.width).toBe('1000px');
    expect(sizer.style.height).toBe('700px');
  });

  it('防御:无 replayer / 无 sizer / 无 dims / 无 iframe → null 且零副作用', () => {
    const sizer = { style: {} };
    expect(applyPageMirrorLayout(null, sizer, 1000, DIMS)).toBeNull();
    expect(applyPageMirrorLayout(fakeReplayer(), null, 1000, DIMS)).toBeNull();
    expect(applyPageMirrorLayout(fakeReplayer(), sizer, 1000, null)).toBeNull();
    const noIframe = { wrapper: { style: {} } };
    expect(applyPageMirrorLayout(noIframe, sizer, 1000, DIMS)).toBeNull();
    expect(sizer.style).toEqual({});
    expect(noIframe.wrapper.style).toEqual({});
  });
});
