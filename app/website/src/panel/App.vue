<!-- App.vue — 托盘面板根壳:
     theme + 340px 容器 + Toast 挂载 + 数据层接线(useSummary 轮询 +
     derivePanelState 七态派生,provide 给区块组件)+ 高度自适应
     (量内容高经 resize 消息上报宿主,壳跟随改窗高;2026-11-28 用户拍板)。
     区块组件:①状态头/②传输行(Task 8)/③AI 加速(Task 9)/④ENDPOINT
     (Task 10)/⑤AI AGENT + ⑥底部动作行(Task 11)全部落地,无占位。
     ④⑤的分隔线放区尾并随整区显隐:整区不渲染(数据缺失硬性降级)时
     连 trailing sep 一起消失,避免可用性切换出现孤线/双线,故本文件不为
     ④⑤放静态 sep——③ 后的 sep 是 ③ 与「下一个可见区块」之间的那条线,
     ⑥底部行的分隔由其上方最近一个可见区块的区尾 sep 承担(mockup 的 sep
     不随 zone 走,随区显隐是实现层决定;⑥的 mockup border-top 因同因不抄,
     见 FooterBar 文件头)。 -->
<script setup>
import { computed, nextTick, onMounted, provide, ref, watch } from 'vue';
import { postMessageHost } from './api';
import { useSummary } from './summary';
import { derivePanelState } from './state';
import Toast, { useToast } from './components/Toast.vue';
import StatusHeader from './components/StatusHeader.vue';
import TransportRow from './components/TransportRow.vue';
import AISection from './components/AISection.vue';
import EndpointZone from './components/EndpointZone.vue';
import AgentZone from './components/AgentZone.vue';
import FooterBar from './components/FooterBar.vue';

// 数据层:启动即拉一次 + 2s 轮询;connLost 连续 2 次失败置位(§3 错误态)。
// 仅本根壳调用一次;区块组件一律经 inject('panelData') 取同一份数据。
// refresh 同一 useSummary 管道的手动刷新(useSummary 内部就是 poll 本体),
// 经 panelData 一并下发——EndpointZone 钉选激活的「回读确认」复用它,不得
// 新开轮询定时器(纪律 M-4 的动作侧延伸)。
const { summary, connLost, refresh } = useSummary();

// 本地过渡态(Task 8):TransportRow 点启停/重启后写入('starting'/
// 'stopping'/'restarting'),七态派生据此让过渡态优先于远端快照(spec §3)。
// 放根壳提供的原因:StatusHeader(过渡 spinner 文案)与 TransportRow
// (写入方)要共享同一份,避免跨组件再拉一条 props 线。
const localTransition = ref(null);
const panelState = computed(() =>
  derivePanelState(summary.value, connLost.value, localTransition.value)
);

// 注入给区块组件:同一份数据源,免逐层 props(panelState 含过渡覆盖;
// 组件内需要「不含过渡的远端态」时自行 derivePanelState(..., null))。
provide('panelData', { summary, connLost, panelState, localTransition, refresh });
// toast 队列单例(接口 toast.show(msg, kind))也一并注入。
const toast = useToast();
provide('panelToast', toast);

// —— 高度自适应(2026-11-28 用户拍板,M2 follow-up)——
// 页面量内容高(scrollHeight 取整)经宿主通道发 resize 消息,壳 clamp 后
// 跟随改窗口高度;超过屏幕可用高才页内滚动(此前固定 560 内容要滚「有点
// 不合适」,且 AI AGENT 区展开高度可变)。浏览器无宿主时 postMessageHost
// 返回 false,静默跳过(无宿主模式高度即视口,不上报)。
const panelEl = ref(null);
let lastSentHeight = 0;
function reportHeight() {
  const el = panelEl.value;
  if (!el) return;
  const h = Math.ceil(el.scrollHeight);
  if (Math.abs(h - lastSentHeight) <= 1) return; // ≤1px 抖动不上报
  if (postMessageHost({ type: 'resize', height: h })) {
    lastSentHeight = h;
  }
}
// rAF 合帧:MutationObserver 回调高频(展开动画逐帧改 style),按帧合并,
// 一帧最多一次量高与上报。
let heightFrame = 0;
function scheduleReportHeight() {
  if (heightFrame) return;
  heightFrame = requestAnimationFrame(() => {
    heightFrame = 0;
    reportHeight();
  });
}
onMounted(() => {
  // 首帧量一次 + nextTick(等首屏 DOM 布局落定);
  nextTick(reportHeight);
  // 结构变化兜底:AGENT 明细展开/ENDPOINT 组展开等非 summary 驱动的高度
  // 变化不经过 summary,靠子树 MutationObserver(childList + class/style
  // 属性)捕捉后合帧上报。
  new MutationObserver(scheduleReportHeight).observe(panelEl.value, {
    subtree: true,
    childList: true,
    attributeFilter: ['class', 'style'],
  });
});
// 数据驱动:summary 轮询落地(区块显隐/行数变化)后等 DOM 刷完再量。
watch(summary, () => nextTick(scheduleReportHeight));
</script>

<template>
  <div class="panel" ref="panelEl">
    <!-- ① 状态头部:logo + 状态 + 副标题 + 状态点(Task 8) -->
    <StatusHeader />
    <div class="sep"></div>

    <!-- ② 传输行:启停合一 ▶/⏹ + 重启 + 常规|深度 segmented(Task 8) -->
    <TransportRow />
    <div class="sep"></div>

    <!-- ③ AI 加速:总开关行 + provider 图标行,检测驱动点亮(Task 9) -->
    <AISection />
    <div class="sep"></div>

    <!-- ④ ENDPOINT:Anthropic 组 / OpenAI 组;available=false 整区不渲染
         (spec §4.4 硬性降级);sep 随区尾显隐,故此处无静态 sep(Task 10) -->
    <EndpointZone />

    <!-- ⑤ AI AGENT:每内核一张卡片(24h 统计);kernels 缺失整区不渲染
         (硬性降级),sep 放区尾随整区显隐,故此处无静态 sep(Task 11) -->
    <AgentZone />

    <!-- ⑥ 底部动作行:版本 · 仪表盘 · 设备互访 · 退出;恒渲染(无降级),
         其上方分隔由最近可见区块的区尾 sep 承担(Task 11) -->
    <FooterBar />

    <!-- 轻提示挂载点(全局单例队列,Task 8-11 组件经 useToast 注入使用) -->
    <Toast />
  </div>
</template>
