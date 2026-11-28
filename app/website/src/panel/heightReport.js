// heightReport.js —— 面板高度上报器(首帧尺寸闪变修复批 P1-1,从 App.vue
// 抽出的可测单元):量内容高(scrollHeight 取整)经宿主通道发 resize 消息,
// 壳 clamp 后跟随改窗口高度。
//
// ⚠️ 定谳与取舍(2026-11-28 评审 P1-1):合帧机制用「pending 标志 +
// nextTick」,【不用 requestAnimationFrame】——WebKit/Chromium 对未上屏
// 窗口挂起 scripted animations,而面板壳的出生序列是「先隐藏、首次 resize
// 到位才上屏」,首个 resize 若押在 rAF 上则大概率永远发不出,面板盲等
// 1.2s 兜底(比被否决的 600ms 固定延迟更差)。nextTick 在隐藏窗照常执行;
// scrollHeight 读取本身是同步强制布局,不依赖渲染帧,隐藏窗里照样量得到
// 真实内容高。RO(ResizeObserver)对可见性的敏感度存疑,只作增量补充:
// onMounted 首报直调 report(不经 schedule,同步强布局读取),保证至少
// 一条链路必然发出 resize(接线锚见 heightReport.test.js 的 App.vue 段)。
//
// 浏览器无宿主时 send 返回 false:静默跳过且不更新 lastSent——无宿主模式
// 高度即视口,不上报;一旦上报失败(含无宿主),下次增长仍会尝试同值。
import { nextTick } from 'vue';

// createHeightReporter —— 工厂:注入量高(measure:→ number|null,null=根
// 节点未挂载,静默跳过)与发送(send:payload→ boolean,true=已送达)。
// 返回 { report, schedule }:report 同步量高上报;schedule 合帧到下一个
// microtask(nextTick)后量高上报。
export function createHeightReporter({ measure, send }) {
  let lastSentHeight = 0;

  function report() {
    const raw = measure();
    if (raw == null) return;
    const h = Math.ceil(raw);
    if (Math.abs(h - lastSentHeight) <= 1) return; // ≤1px 抖动不上报(RO 逐帧回调共用的降噪)
    if (send({ type: 'resize', height: h })) {
      lastSentHeight = h;
    }
  }

  // 合帧:pending 标志 + nextTick(不用 rAF,定谳见文件头)——同窗口期多
  // 触发源(RO/watch/手动)合并为一次量高上报;上报完成后标志清零,后续
  // 变化开新一轮。
  let heightReportPending = false;
  function schedule() {
    if (heightReportPending) return;
    heightReportPending = true;
    nextTick(() => {
      heightReportPending = false;
      report();
    });
  }

  return { report, schedule };
}
