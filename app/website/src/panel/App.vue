<!-- App.vue — 托盘面板根壳:
     theme + 340px 容器 + Toast 挂载 + 数据层接线(useSummary 轮询 +
     derivePanelState 七态派生,provide 给区块组件)。
     区块组件:①状态头/②传输行已落地(Task 8);③AI 加速/④ENDPOINT/
     ⑤AI AGENT/⑥底部动作行仍是占位(Task 9-11),绝不提前实现。 -->
<script setup>
import { computed, provide, ref } from 'vue';
import { useSummary } from './summary';
import { derivePanelState } from './state';
import Toast, { useToast } from './components/Toast.vue';
import StatusHeader from './components/StatusHeader.vue';
import TransportRow from './components/TransportRow.vue';

// 数据层:启动即拉一次 + 2s 轮询;connLost 连续 2 次失败置位(§3 错误态)。
// 仅本根壳调用一次;区块组件一律经 inject('panelData') 取同一份数据。
const { summary, connLost } = useSummary();

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
provide('panelData', { summary, connLost, panelState, localTransition });
// toast 队列单例(接口 toast.show(msg, kind))也一并注入。
const toast = useToast();
provide('panelToast', toast);
</script>

<template>
  <div class="panel">
    <!-- ① 状态头部:logo + 状态 + 副标题 + 状态点(Task 8) -->
    <StatusHeader />
    <div class="sep"></div>

    <!-- ② 传输行:启停合一 ▶/⏹ + 重启 + 常规|深度 segmented(Task 8) -->
    <TransportRow />
    <div class="sep"></div>

    <!-- ③ AI 加速:总开关行 + provider 图标行(Task 10) -->
    <div class="panel-block" data-block="ai">AI 加速(Task 10 占位)</div>
    <div class="sep"></div>

    <!-- ④ ENDPOINT:Anthropic 组 / OpenAI 组;available=false 整区不渲染(Task 10/11) -->
    <div class="panel-block" data-block="endpoints">ENDPOINT(Task 10-11 占位)</div>
    <div class="sep"></div>

    <!-- ⑤ AI AGENT:每内核一张卡片(24h 统计)(Task 11) -->
    <div class="panel-block" data-block="agents">AI AGENT · 24H(Task 11 占位)</div>
    <div class="sep"></div>

    <!-- ⑥ 底部动作行:版本 · 仪表盘 · 设备互访 · 退出(Task 8) -->
    <div class="panel-block" data-block="footer">底部动作行(Task 8 占位)</div>

    <!-- 轻提示挂载点(全局单例队列,Task 8-11 组件经 useToast 注入使用) -->
    <Toast />
  </div>
</template>

<style scoped>
/* 占位块最小可读样式:仅剩 Task 9-11 的四个占位在用,落一删一,全落整段删。 */
.panel-block {
  padding: 12px 14px;
  font-size: 11.5px;
  color: var(--text-muted);
}
</style>
