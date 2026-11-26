<!-- StatusHeader.vue — 面板①状态头(spec §4.1 + §3 状态矩阵)。
     纯展示组件:七态文案/点色/logo 灰化全部由本文件命名导出的
     statusHeaderContent 纯函数派生(挂载测试与组件共用同一份派生,
     拒绝两处各写一份矩阵);数据经 inject('panelData') 取 App.vue
     根壳提供的同一份——panelState 已含本地过渡态(TransportRow 写入
     localTransition),本组件只读渲染,不发起任何动作。 -->
<script>
import { computed, inject } from 'vue';
import { modeLabel } from '../state';

// statusHeaderContent(kind, run) — 状态头七态内容派生(spec §3 矩阵)。
//   kind:derivePanelState 的七态输出;run:parseSummary 归一后的 run
//   子源(可 null——error 态下本就无数据)。返回
//   { title, subtitle, busy, dotClass, logoOff }:
//     busy     true=过渡态,状态点换成 13px spinner(§4.1)
//     dotClass 状态点语义色类(green pulse/gray/red/amber;过渡态为空)
//     logoOff  logo 灰化渐变(已停止/未登录,§4.1 停止态灰化)
// 纯函数:不读全局、不改入参,run 字段缺失走容错分支而非抛错。
export function statusHeaderContent(kind, run) {
  const obj = run !== null && run !== undefined && typeof run === 'object' ? run : {};
  const mode = typeof obj.mode === 'string' ? obj.mode : '';
  const addr = typeof obj.listen_addr === 'string' ? obj.listen_addr : '';
  const label = modeLabel(mode);
  const blockedMsg =
    obj.blocked && typeof obj.blocked === 'object' ? String(obj.blocked.msg || '') : '';

  // 过渡态三支(§3:琥珀 spinner +「正在启动/停止/重启…」;副标题给一句
  // 模式上下文,mockup head() 同款——stopping 无副标题)。
  if (kind === 'starting') {
    return { title: '正在启动…', subtitle: label ? `模式：${label}模式` : '', busy: true, dotClass: '', logoOff: false };
  }
  if (kind === 'stopping') {
    return { title: '正在停止…', subtitle: '', busy: true, dotClass: '', logoOff: false };
  }
  if (kind === 'restarting') {
    return { title: '正在重启…', subtitle: label ? `当前模式：${label}模式` : '', busy: true, dotClass: '', logoOff: false };
  }
  if (kind === 'running') {
    // 「代理运行中 · 深度模式 · 127.0.0.1:56432」拆主/副两行(mockup 同款)。
    const parts = [];
    if (label) parts.push(`${label}模式`);
    if (addr) parts.push(addr);
    return { title: '代理运行中', subtitle: parts.join(' · '), busy: false, dotClass: 'green pulse', logoOff: false };
  }
  if (kind === 'stopped') {
    // 「下次启动 · ××模式」读 summary.run.mode——segmented 点击已落盘到
    // 服务端(setMode),轮询回读后本值即反映真实下次启动模式;回读滞后
    // 的瞬间以服务端为准不说谎。
    return { title: '代理已停止', subtitle: label ? `下次启动 · ${label}模式` : '下次启动', busy: false, dotClass: 'gray', logoOff: true };
  }
  if (kind === 'logged_out') {
    // §3:未登录原因由状态行承载——副标题直接放 blocked.msg。
    return { title: '未登录', subtitle: blockedMsg || '登录后可启动代理', busy: false, dotClass: 'amber', logoOff: true };
  }
  // error(summary null / connLost / run 子源降级):无具体错误详情可取,
  // 给固定错误摘要;轮询持续,恢复即回常规态。
  return { title: '状态获取失败', subtitle: '与核心服务连接异常', busy: false, dotClass: 'red', logoOff: false };
}

// 组件本体:只渲染派生结果,不含业务。
export default {
  name: 'StatusHeader',
  setup() {
    const data = inject('panelData');
    const content = computed(() =>
      statusHeaderContent(data.panelState.value, data.summary.value ? data.summary.value.run : null)
    );
    return { content };
  },
};
</script>

<template>
  <div class="a-head">
    <!-- 左:34×34 圆角 10px logo(§4.1;停止/未登录态灰化渐变) -->
    <div class="a-logo" :class="{ off: content.logoOff }">
      <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v18M5 8l7-5 7 5M5 16l7 5 7-5" /></svg>
    </div>
    <!-- 中:主标题 13.5px/700 + 副标题 11.5px muted -->
    <div class="a-mid">
      <div class="t1">{{ content.title }}</div>
      <div v-if="content.subtitle" class="t2">{{ content.subtitle }}</div>
    </div>
    <!-- 右:8px 状态点(语义色+pulse 呼吸);过渡态换 13px spinner(§4.1) -->
    <div v-if="content.busy" class="spinner" aria-label="处理中"></div>
    <div v-else class="dot" :class="content.dotClass"></div>
  </div>
</template>

<style scoped>
/* 数值全部抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
   .a-head/.a-logo/.dot/.spinner 段原值;深色变体按仓内 theme.css 口径
   用 prefers-color-scheme 媒体查询(mockup 的 body.dark 等价改写)。 */
.a-head {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 14px 16px 12px;
}
.a-logo {
  width: 34px;
  height: 34px;
  border-radius: 10px;
  flex-shrink: 0;
  background: linear-gradient(135deg, #2fd96e, #17a84c);
  display: flex;
  align-items: center;
  justify-content: center;
  box-shadow: 0 2px 8px rgba(33, 196, 93, 0.35);
  transition: all 0.3s;
}
.a-logo.off {
  background: linear-gradient(135deg, #9fb2a6, #6e8277);
  box-shadow: none;
}
.a-mid {
  flex: 1;
  min-width: 0;
}
.a-head .t1 {
  font-size: 13.5px;
  font-weight: 700;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.a-head .t2 {
  font-size: 11.5px;
  color: #7d8f85;
  margin-top: 1px;
  transition: color 0.2s;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
@media (prefers-color-scheme: dark) {
  .a-head .t2 {
    color: #7e9186;
  }
}

/* 状态点(mockup .dot 原值):8px 圆 + 语义色;绿点带 pulse 2s 呼吸。 */
.dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot.green {
  background: var(--brand);
  box-shadow: 0 0 6px var(--brand-glow);
}
.dot.gray {
  background: #9aa8a0;
}
.dot.amber {
  background: var(--warn);
  box-shadow: 0 0 6px rgba(230, 162, 60, 0.4);
}
.dot.red {
  background: var(--danger);
  box-shadow: 0 0 6px rgba(229, 72, 77, 0.4);
}
.dot.pulse {
  animation: pulse 2s ease-in-out infinite;
}
@keyframes pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.45;
  }
}

/* 过渡态 spinner(mockup .spinner 原值):13px 琥珀顶弧 0.8s 旋转。 */
.spinner {
  width: 13px;
  height: 13px;
  border-radius: 50%;
  border: 2px solid rgba(0, 0, 0, 0.12);
  border-top-color: var(--warn);
  animation: spin 0.8s linear infinite;
  flex-shrink: 0;
}
@media (prefers-color-scheme: dark) {
  .spinner {
    border-color: rgba(255, 255, 255, 0.15);
    border-top-color: var(--warn);
  }
}
@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
