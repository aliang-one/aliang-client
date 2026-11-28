<!-- AgentZone.vue — 面板⑤AI AGENT · 24H 区(spec §4.5 + §6-7)。
     分区标题「AI AGENT · 24H」(mockup .sec-head 原值);每个探测到的 AI 内核
     一张卡片(圆角 12px;2026-11-28 用户反馈单行紧凑化:内边距收 7×10,
     卡头一行放下 🤖 图标 + 内核名 + 统计芯片 + 右箭头,原独立芯片第二行
     取消);点卡片展开/收起 24h 会话明细(展开态卡头仍是同一行)。
     统计芯片(10.5px/600):「活跃 N」绿底 brand-dim ·「空闲 N」·
     「24h 共 N」;活跃为 0 不显活跃芯片——空闲为 0 同款隐藏(mockup
     runN/idleN 双判空口径,24h 芯片恒显)。展开明细每条 = 状态点(活跃绿
     带光晕 / 空闲灰)+ 会话标题(11px/600 截断)+ 「来源 · 相对时间」。
     降级硬约束:kernels 缺失/null/空数组/条目全坏 → 整区不渲染,无空壳
     (与 Task 10 同款);sep 放区尾并随整区显隐(实现层决定,mockup 未含
     此 sep;不渲染时连 sep 一起消失,不留孤线/双线)。
     零请求纪律:本区纯只读展示,展开/收起是纯本地状态,不发任何请求。 -->
<script>
import { computed, inject, ref } from 'vue';

// 🤖 图标:逐字抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
// I.bot 段。经 v-html 渲染,内容为本文件字面常量,无外部输入,无 XSS 面。
const BOT_SVG =
  '<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="6" width="16" height="12" rx="3"/><path d="M12 6V3M8 3h8"/><circle cx="9" cy="12" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="12" r="1" fill="currentColor" stroke="none"/></svg>';

// relativeTime(ts, now) — 相对时间纯函数(导出可测,契约必测项)。
//   ts:unix 秒(服务端 traySessionUnixTs 出参口径);now:unix 秒,缺省当下。
//   档位(实现计划 Task 11 原文):<60s「刚刚」/<60min「N 分钟前」/
//   <24h「N 小时前」/否则「N 天前」,整点进位(60s→1 分钟前)。
//   容错(选定案,报告已说明):
//     未来时间戳(diff<0,含 ms/s 单位错配)→ 钳位按 0s 已过 →「刚刚」;
//     坏时间戳(非有限数/≤0,0 为 1970 纪元必是坏数据)→ 空串,明细行只剩来源。
export function relativeTime(ts, now = Math.floor(Date.now() / 1000)) {
  if (typeof ts !== 'number' || !Number.isFinite(ts) || ts <= 0) {
    return '';
  }
  let diff = Math.floor(now) - ts;
  if (diff < 0) diff = 0; // 未来时间钳位(优雅降级)
  if (diff < 60) return '刚刚';
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}

// agentZoneModel(agents) — AGENT 区内容纯派生(spec §4.5 + 数据防御)。
//   agents:parseSummary 归一后的 agents 子源(TrayAgentsSummary,可 null
//   ——服务端单源降级/error 态下无数据)。返回 { available, kernels }:
//     available kernels 过滤后为非空数组 → true,其余一律 false → 整区不渲染
//     kernels   逐内核归一 { name, chips, sessions }
//   chips 逐枚 { key, cls, dot, dimDot, text }:active>0 →「活跃 N」live 绿底;
//   idle>0 →「空闲 N」(cdot 半透明,mockup opacity 0.4);total_24h 恒显
//   「24h 共 N」。坏数值字段按 0 容错(不炸、不显示 NaN)。
//   sessions 逐条 { title, meta, active }:meta =「来源 · 相对时间」,来源/时间
//   缺一省一(坏 ts → 只剩来源;缺 source → 只剩时间);坏条目跳过不炸。
//   防御纪律:坏类型子源降级、坏条目逐条跳过;纯函数不读全局不改入参。
export function agentZoneModel(agents) {
  const src =
    agents !== null && agents !== undefined && typeof agents === 'object' && !Array.isArray(agents)
      ? agents
      : {};
  const rawKernels = Array.isArray(src.kernels) ? src.kernels : [];

  const num = (v) => (typeof v === 'number' && Number.isFinite(v) ? v : 0);
  const kernels = [];
  for (const k of rawKernels) {
    if (k === null || k === undefined || typeof k !== 'object' || Array.isArray(k)) continue;
    const name = typeof k.name === 'string' ? k.name.trim() : '';
    if (!name) continue; // name 是 :key 与展开态身份,缺失的内核无法稳定渲染
    const active = num(k.active);
    const idle = num(k.idle);
    // 芯片三枚口径:活跃/空闲判空隐藏(mockup runN/idleN 同款),24h 恒显
    const chips = [];
    if (active > 0) {
      chips.push({ key: 'active', cls: 'live', dot: true, dimDot: false, text: `活跃 ${active}` });
    }
    if (idle > 0) {
      chips.push({ key: 'idle', cls: '', dot: true, dimDot: true, text: `空闲 ${idle}` });
    }
    chips.push({ key: 'total', cls: 'num', dot: false, dimDot: false, text: `24h 共 ${num(k.total_24h)}` });

    // 会话明细归一(服务端已按 24h 窗与 cap=20 截好,这里只做防御性过滤)
    const rawSessions = Array.isArray(k.sessions) ? k.sessions : [];
    const sessions = [];
    for (const s of rawSessions) {
      if (s === null || s === undefined || typeof s !== 'object' || Array.isArray(s)) continue;
      const title = typeof s.title === 'string' ? s.title : '';
      const source = typeof s.source === 'string' ? s.source : '';
      const time = relativeTime(s.ts);
      sessions.push({
        title,
        // 「来源 · 相对时间」:缺一省一,两项全缺为空串(行只剩状态点+标题)
        meta: [source, time].filter(Boolean).join(' · '),
        active: s.active === true,
      });
    }
    kernels.push({ name, chips, sessions });
  }
  // available 收窄到过滤后:kernels 数组全坏或空 → 整区不渲染,不留零子元素
  // 空壳 + 尾随 sep(与 Task 10 的 I-1 同款自述契约)。
  return { available: kernels.length > 0, kernels };
}

// 组件本体:只渲染派生结果 + 纯本地展开状态,零请求。
export default {
  name: 'AgentZone',
  setup() {
    // 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询。
    const data = inject('panelData');
    const model = computed(() => agentZoneModel(data.summary.value ? data.summary.value.agents : null));

    // 展开状态按内核名记忆:2s 轮询回读新快照不打断用户展开的卡片
    // (mockup demo 默认全部收起——芯片统计已可见,面板空间紧凑)。
    const expanded = ref({});
    function isOpen(name) {
      return expanded.value[name] === true;
    }

    // onCard — 卡片点击展开/收起(spec §6-7)。
    function onCard(k) {
      expanded.value = { ...expanded.value, [k.name]: !isOpen(k.name) };
    }

    return { model, BOT_SVG, isOpen, onCard };
  },
};
</script>

<template>
  <!-- sep 放区尾并随整区显隐:整区不渲染时连 sep 一起消失——App.vue 不为⑤
       放静态 sep,避免可用性切换时出现孤线/双线(实现层决定,mockup 未含)。 -->
  <template v-if="model.available">
    <div class="agent-zone">
      <!-- 分区标题(mockup .sec-head 原值):10.5px/700/全大写/0.08em 字距
           /尾随渐隐分隔线(::after 承载) -->
      <div class="sec-head">AI AGENT · 24H</div>
      <div
        v-for="k in model.kernels"
        :key="k.name"
        class="agent-card"
        :class="{ open: isOpen(k.name) }"
        :data-nm="k.name"
        @click="onCard(k)"
      >
        <div class="top">
          <!-- 🤖 图标(内联 SVG,mockup I.bot 原值) -->
          <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
          <span class="a-ico" aria-hidden="true" v-html="BOT_SVG"></span>
          <span class="name">{{ k.name }}</span>
          <!-- 统计芯片(2026-11-28 用户反馈单行紧凑化:原独立第二行并进
               头部行,紧跟内核名、右箭头之前;判空隐藏由 model 承载) -->
          <div class="stats">
            <span v-for="(c, i) in k.chips" :key="i" class="stat-chip" :class="c.cls">
              <span v-if="c.dot" class="cdot" :style="c.dimDot ? { opacity: 0.4 } : null"></span>{{ c.text }}
            </span>
          </div>
          <!-- 右箭头(mockup chevRight 原值):展开 rotate 90° -->
          <svg
            class="chev"
            :class="{ rot: isOpen(k.name) }"
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2.2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="M9 18l6-6-6-6" />
          </svg>
        </div>
        <!-- 24h 会话明细:展开 max-height 0→260px 过渡 0.25s(mockup 原值) -->
        <div class="sess-list">
          <div v-for="(s, i) in k.sessions" :key="i" class="sess-item">
            <span class="sdot" :class="s.active ? 'run' : 'idle'" aria-hidden="true"></span>
            <span class="st">{{ s.title }}</span>
            <span class="sm">{{ s.meta }}</span>
          </div>
        </div>
      </div>
    </div>
    <div class="sep"></div>
  </template>
</template>

<style scoped>
/* 数值全部抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
   .sec-head/.agent-card/.top/.name/.stats/.stat-chip/.cdot/.sess-list/
   .sess-item/.sdot/.st/.sm 段原值;深色变体按仓内 theme.css 口径用
   prefers-color-scheme 媒体查询(mockup body.dark 等价改写)。 */

/* 分区标题(mockup .sec-head 原值):10.5px/700/大写/0.08em 字距,
   ::after 尾随渐隐分隔线 */
.sec-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 10px 14px 4px;
  font-size: 10.5px;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: #85988d;
  text-transform: uppercase;
}
@media (prefers-color-scheme: dark) {
  .sec-head {
    color: #6e8277;
  }
}
.sec-head::after {
  content: '';
  flex: 1;
  height: 1px;
  background: rgba(0, 0, 0, 0.05);
}
@media (prefers-color-scheme: dark) {
  .sec-head::after {
    background: rgba(255, 255, 255, 0.06);
  }
}

/* 内核卡片(mockup .agent-card 原值,2026-11-28 单行紧凑化收窄):
   圆角 12px 不动;内边距 9×11 → 7×10;上下外边距 3 → 2(卡片之间的
   间距 6 → 4 微收,幅度克制) */
.agent-card {
  margin: 2px 12px;
  padding: 7px 10px;
  border-radius: 12px;
  background: rgba(0, 0, 0, 0.03);
  border: 1px solid rgba(0, 0, 0, 0.04);
  cursor: pointer;
  transition: background 0.14s, border-color 0.14s;
}
@media (prefers-color-scheme: dark) {
  .agent-card {
    background: rgba(255, 255, 255, 0.04);
    border-color: rgba(255, 255, 255, 0.05);
  }
}
.agent-card:hover {
  background: rgba(33, 196, 93, 0.06);
  border-color: rgba(33, 196, 93, 0.16);
}
.agent-card .top {
  display: flex;
  align-items: center;
  gap: 8px;
}
.a-ico {
  display: flex;
  flex-shrink: 0;
  pointer-events: none;
}
.agent-card .name {
  font-size: 12px;
  font-weight: 700;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 右箭头(mockup chevRight 内联 opacity 0.4 等价改 CSS):展开 rotate 90° */
.chev {
  opacity: 0.4;
  transition: transform 0.2s;
  flex-shrink: 0;
}
.chev.rot {
  transform: rotate(90deg);
}

/* 统计芯片(mockup .stats 原值;2026-11-28 单行紧凑化:原独立第二行
   margin-top 7px 并进 .top 头部行——紧跟内核名、右箭头之前,一行放下。
   flex-shrink 0 保芯片不被长内核名压扁,挤压由 .name 截断承担) */
.agent-card .stats {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.stat-chip {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 2px 6px;
  border-radius: 6px;
  font-size: 10.5px;
  font-weight: 600;
  background: rgba(0, 0, 0, 0.05);
  color: #51655a;
}
@media (prefers-color-scheme: dark) {
  .stat-chip {
    background: rgba(255, 255, 255, 0.06);
    color: #9db1a5;
  }
}
.stat-chip .cdot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: currentColor;
}
/* 活跃芯片:绿底 brand-dim(mockup .stat-chip.live 原值) */
.stat-chip.live {
  background: var(--brand-dim);
  color: #15803d;
}
@media (prefers-color-scheme: dark) {
  .stat-chip.live {
    color: #4ade80;
  }
}

/* 会话明细展开/收起(mockup .sess-list 原值):max-height 0→260px 过渡 0.25s */
.sess-list {
  max-height: 0;
  overflow: hidden;
  transition: max-height 0.25s ease;
}
.agent-card.open .sess-list {
  max-height: 260px;
}
/* 明细条(mockup .sess-item 原值) */
.sess-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 2px 6px 4px;
  border-top: 1px solid rgba(0, 0, 0, 0.05);
  margin-top: 6px;
}
@media (prefers-color-scheme: dark) {
  .sess-item {
    border-top-color: rgba(255, 255, 255, 0.05);
  }
}
/* 状态点:活跃绿带光晕 / 空闲灰(mockup .sdot.run/.sdot.idle 原值) */
.sess-item .sdot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
}
.sess-item .sdot.run {
  background: var(--brand);
  box-shadow: 0 0 5px var(--brand-glow);
}
.sess-item .sdot.idle {
  background: #9aa8a0;
}
/* 会话标题(mockup .st 原值):11px/600 截断 */
.sess-item .st {
  font-size: 11px;
  font-weight: 600;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 来源与相对时间(mockup .sm 原值):10px muted */
.sess-item .sm {
  font-size: 10px;
  color: #85988d;
  flex-shrink: 0;
}
@media (prefers-color-scheme: dark) {
  .sess-item .sm {
    color: #6e8277;
  }
}
</style>
