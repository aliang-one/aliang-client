<!-- App.vue — 托盘面板根壳(本任务=Task 7 只做骨架):
     theme + 340px 容器 + Toast 挂载 + 数据层接线(useSummary 轮询 +
     derivePanelState 七态派生,provide 给后续区块组件)。
     六区块组件(状态头/传输行/AI 加速/endpoint/agent/底部动作行)是
     Task 8-11 的活,此处只放占位注释,绝不提前实现。 -->
<script setup>
import { computed, provide } from 'vue';
import { postMessageHost } from './api';
import { useSummary } from './summary';
import { derivePanelState } from './state';
import Toast, { useToast } from './components/Toast.vue';

// 数据层:启动即拉一次 + 2s 轮询;connLost 连续 2 次失败置位(§3 错误态)。
const { summary, connLost } = useSummary();
const panelState = computed(() => derivePanelState(summary.value, connLost.value, null));

// 注入给 Task 8-11 的区块组件:同一份数据源,免逐层 props。
provide('panelData', { summary, connLost, panelState });
// toast 队列单例(接口 toast.show(msg, kind))也一并注入。
const toast = useToast();
provide('panelToast', toast);

// 宿主就绪通知:面板加载完成即告知宿主(无宿主模式返回 false,静默忽略)。
postMessageHost({ type: 'panel_ready' });
</script>

<template>
  <div class="panel">
    <!-- ① 状态头部:logo + 状态 + 副标题 + 状态点(Task 8) -->
    <div class="panel-block" data-block="header">状态头部(Task 8 占位)</div>
    <div class="sep"></div>

    <!-- ② 传输行:启停合一 ▶/⏹ + 重启 + 常规|深度 segmented(Task 8/9) -->
    <div class="panel-block" data-block="transport">传输行(Task 8-9 占位)</div>
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
/* 占位块最小可读样式:Task 8-11 落组件后整段删除。 */
.panel-block {
  padding: 12px 14px;
  font-size: 11.5px;
  color: var(--text-muted);
}
</style>
