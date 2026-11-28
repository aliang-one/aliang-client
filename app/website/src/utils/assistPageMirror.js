// 远程协助 P3:观看页 rrweb 页面镜像的按宽缩放几何(spec §9「镜像按容器宽
// 缩放」)。纯函数 + 纯 DOM 施加,零 Vue 依赖——组件(AssistWatchPage)只在
// Tab 切换 / ResizeObserver / 底座注入 / Meta 更新四个时机调 applyPageMirrorLayout。
//
// 背景 live E2E 实证缺陷:rrweb Replayer 的沙箱 iframe 在 pane 隐藏(v-show
// 切 [终端])期间被浏览器塌成 0×0,切回后画面恒黑;手动把 width/height 写回
// Meta 尺寸立即恢复。本模块把「尺寸恢复 + 按宽缩放」合成一次施加。
//
// ⚠️ 本表面的几何正确性 jsdom 验不了(1479 绿测放行过一版双重缩放+裁半页的
// 实现,P3 终审 headless-Chrome 结构探针才抓到):transform/clip 的复合几何
// 必须真浏览器验证(见仓库 P3 复方:probe 页 + getBoundingClientRect +
// elementFromPoint + 截图)。

// computePageScale 容器宽 / Meta 宽 = 缩放比。极端保护:任一非正数(隐藏
// pane 的 clientWidth=0 / 合成 Meta 兜底缺尺寸)一律回 1——缩放比 0 会把
// 镜像缩没,比「不缩放」更糟;1 = 原尺寸直出,恢复语义仍成立。
export function computePageScale(containerWidth, metaWidth) {
  if (!(containerWidth > 0) || !(metaWidth > 0)) return 1;
  return containerWidth / metaWidth;
}

// applyPageMirrorLayout 把「Meta 尺寸恢复 + 按宽缩放」一次施加到 Replayer 的
// 沙箱 DOM。几何形状(P3 终审定案,勿改回「wrapper 布局=缩放尺寸+wrapper
// transform」的组合——那会让 wrapper 自身 border-box 被自己的 transform 再
// 缩一次,overflow 又在 wrapper 本地(变换前)坐标系裁剪,只见左上四分之一):
//
//   pageHostEl(容器,overflow:auto,不变换)
//     └ sizer(布局盒 = Meta×s,overflow:hidden,不变换)← 裁剪边界在这层
//        └ wrapper(rrweb 建,布局盒 = **RAW Meta 尺寸**,transform:scale(s)
//            origin 0 0)← 唯一一层变换
//           ├ mouse / mouseTail?(absolute,style.left/top=录制坐标 px)
//           └ iframe(width/height=Meta 尺寸)
//
//   ①iframe 的 width/height 属性 + style 双写回 Meta 尺寸(0×0 塌缩修复;
//     属性与 style 都写——rrweb 的 handleResize 走属性,浏览器塌缩走 style,
//     只补一边都会残留另一形态的 0 尺寸);
//   ②wrapper 布局尺寸 = RAW Meta(变换不占布局,布局盒只做子元素定位底),
//     transform: scale(s) 单层复合变换:子元素 (x,y) → 渲染 (x·s,y·s),
//     指针/轨迹/内容几何严格对齐(mouse 的 left/top 是布局属性,逐元素
//     transform 缩不动它——transform-origin 在元素自身空间,锚点不动);
//   ③sizer = Meta×s + overflow hidden:裁剪边界必须住在不变换的层——sizer
//     本地坐标系即 wrapper 变换后的绘制盒,恰好框住全部内容,外层布局占位
//     正确、无横向滚动、无双重缩放。
//   s=1 时清空 transform(不留恒等矩阵),sizer = RAW Meta 尺寸。
//
// replayer.iframe / replayer.wrapper 是 rrweb Replayer 的公开属性;sizer 由
// 组件建(pageHostEl 内常驻 div,rrweb 的 config.root 指向它,wrapper 长在
// sizer 里);测试假体缺属性时逐项可选跳过(返回 null)。
export function applyPageMirrorLayout(replayer, sizer, containerWidth, dims) {
  if (!replayer || !sizer || !dims) return null;
  const scale = computePageScale(containerWidth, dims.width);
  const iframe = replayer.iframe;
  if (!iframe) return null;
  iframe.setAttribute('width', String(dims.width));
  iframe.setAttribute('height', String(dims.height));
  iframe.style.width = `${dims.width}px`;
  iframe.style.height = `${dims.height}px`;
  const wrapper = replayer.wrapper;
  if (wrapper) {
    wrapper.style.width = `${dims.width}px`;
    wrapper.style.height = `${dims.height}px`;
    wrapper.style.transformOrigin = '0 0';
    wrapper.style.transform = scale === 1 ? '' : `scale(${scale})`;
  }
  sizer.style.width = `${Math.round(dims.width * scale)}px`;
  sizer.style.height = `${Math.round(dims.height * scale)}px`;
  sizer.style.overflow = 'hidden';
  return scale;
}
