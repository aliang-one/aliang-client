<!-- EndpointZone.vue — 面板④ENDPOINT 区(spec §4.4 + §6-4 + 2026-11-27 拍板修订)。
     按协议族分组(Anthropic 组 / OpenAI 组),组头常显(协议图标 + 组名 +
     当前激活 endpoint 名 + chevron),点组头展开/收起(chevron rotate 180°,
     列表 max-height 0→200px 过渡 0.25s)。列表项 radio 语义:14px 勾选圈 +
     名称 + 徽标。
     ⚠️ 拍板修订(覆盖 spec §4.4 原文「单选即激活」):钉选后端未上线,
     任何行(含 ok 行)点击 → 只 toast「钉选能力即将上线」,不勾选、不发
     任何请求——勾选圈仅反映服务端 current(自动调度此刻选中的行,与组头
     g-current 同源),点击不迁移。api 层无 activate 动作(契约硬约束)。
     降级硬约束:available=false 或 groups 缺失/空数组 → 整区不渲染,无空壳、
     无「建设中」占位;sep 放区尾并随整区显隐(实现层决定,mockup 未含此
     sep;不渲染时连 sep 一起消失,不留孤线/双线)。AI 加速关(ai.enabled
     =false)→ 整区 .zone.dimmed(降透明 0.35 + 锁交互,§6-4)。 -->
<script>
import { computed, inject, ref } from 'vue';

// 组名映射(契约第 2 条:Anthropic 组 / OpenAI 组);protocol 只有这两值
// (tray_summary.go trayEndpointHasProtocol 白名单),未知值兜底原样+组后缀。
const GROUP_LABELS = { anthropic: 'Anthropic', openai: 'OpenAI' };

// 协议图标:逐字抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
// I.providers 段(与 AISection.vue 同源拷贝,组件自含不互引)。经 v-html 渲染,
// 内容为本文件字面常量,无外部输入,无 XSS 面。
const GROUP_ICONS = {
  anthropic: '<svg width="15" height="15" viewBox="0 0 24 24" fill="currentColor"><path d="M12.2 4 5.6 20h3.1l1.2-3.1h6.3L17.4 20h3.1L13.9 4zM10.9 14l2-5.3 2 5.3z" transform="translate(-1.5,0)"/></svg>',
  openai: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"><path d="M12 3c-2 1.2-2 3.6-2 3.6S8 5.4 6.2 6.4c-.6 2.3 1.2 4 1.2 4s-2.2.6-2.4 2.9c1.8 1.6 4 .9 4 .9s-.6 2.2 1 3.6c2.2-.5 3-2.8 3-2.8s.8 2.3 3 2.8c1.6-1.4 1-3.6 1-3.6s2.2.7 4-.9c-.2-2.3-2.4-2.9-2.4-2.9s1.8-1.7 1.2-4c-1.8-1-3.8.2-3.8.2S14 4.2 12 3z" transform="scale(0.82) translate(2.6,2.6)"/></svg>',
};

// 勾选圈白勾(mockup I.check 原值,14px 圈内 9px 白勾)。
const CHECK_SVG =
  '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 12.5 9.5 18 20 7"/></svg>';

// normalizeBadge(it) — 行徽标派生(spec §4.4):
//   cooldown → 琥珀「冷却 Xm」(Xm = cooldown_remaining_sec/60 向上取整,
//              兜底 ≥1——服务端 cooldown 必有剩余,0/负/坏值不展示「冷却 0m」);
//   disabled → 琥珀「已摘除」;
//   其余(state 未知按普通行容错)→ 绿描边 ghost 优先级徽标 rank>=1 ? P{rank}
//   :「—」(rank=0/负/非整数,契约第 3 条)。
//   mockup 口径:cooldown/disabled 的琥珀徽标【替换】优先级徽标(单徽标)。
function normalizeBadge(rank, state, remainingSec) {
  if (state === 'cooldown') {
    const sec = typeof remainingSec === 'number' && Number.isFinite(remainingSec) && remainingSec > 0 ? remainingSec : 0;
    const min = Math.max(1, Math.ceil(sec / 60));
    return { cls: 'cool', text: `冷却 ${min}m` };
  }
  if (state === 'disabled') {
    return { cls: 'cool', text: '已摘除' };
  }
  const rankText = typeof rank === 'number' && Number.isInteger(rank) && rank >= 1 ? `P${rank}` : '—';
  return { cls: 'outline', text: rankText };
}

// endpointZoneModel(endpoints, ai) — ENDPOINT 区内容纯派生(spec §4.4 + 数据
// 防御契约第 8 条)。endpoints:parseSummary 归一后的 endpoints 子源
// (TrayEndpointsSummary,可 null);ai:同上(TrayAISummary,只看 enabled)。
// 返回 { available, aiOn, groups }:
//   available available=true 且 groups 为非空数组,其余一律 false → 整区不渲染
//   aiOn      AI 加速开(仅真值 true 为开,与 AISection 同口径)→ false 时整区
//             dimmed(降透明 0.35 + 锁,§6-4)
//   groups    逐组归一 {protocol,label,name,icon,current,currentText,items}
// items 逐条 {name, on(勾选圈=服务端 current 同名), unavailable, badgeCls,
// badgeText};不可用行(cooldown/disabled)照常渲染只加徽标(契约第 8 条)。
// 防御纪律:坏类型子源降级、坏条目逐条跳过不炸;纯函数不读全局不改入参。
export function endpointZoneModel(endpoints, ai) {
  const src = endpoints !== null && endpoints !== undefined && typeof endpoints === 'object' && !Array.isArray(endpoints) ? endpoints : {};
  const rawGroups = Array.isArray(src.groups) ? src.groups : [];
  // available 硬闸:开关关或组表缺失/空 → 整区不渲染(spec §4.4 降级策略,
  // 不是可选项)。
  const available = src.available === true && rawGroups.length > 0;
  const aiOn = ai !== null && ai !== undefined && typeof ai === 'object' && ai.enabled === true;

  const groups = [];
  if (available) {
    for (const g of rawGroups) {
      if (g === null || g === undefined || typeof g !== 'object' || Array.isArray(g)) continue;
      const protocol = typeof g.protocol === 'string' && g.protocol ? g.protocol : '';
      if (!protocol) continue;
      const label = GROUP_LABELS[protocol] || protocol;
      const current = typeof g.current === 'string' ? g.current : '';
      const items = [];
      const rawItems = Array.isArray(g.items) ? g.items : [];
      for (const it of rawItems) {
        if (it === null || it === undefined || typeof it !== 'object' || Array.isArray(it)) continue;
        const name = typeof it.name === 'string' ? it.name : '';
        const rank = typeof it.rank === 'number' ? it.rank : 0;
        const state = typeof it.state === 'string' ? it.state : '';
        const remaining = typeof it.cooldown_remaining_sec === 'number' ? it.cooldown_remaining_sec : 0;
        const badge = normalizeBadge(rank, state, remaining);
        items.push({
          name,
          // 勾选圈钉在服务端 current 行(自动调度此刻选中,与组头同源);
          // 钉选递延拍板下点击不迁移,仅展示语义。
          on: current !== '' && name === current,
          unavailable: state === 'cooldown' || state === 'disabled',
          badgeCls: badge.cls,
          badgeText: badge.text,
        });
      }
      groups.push({
        protocol,
        label,
        name: `${label} 组`,
        icon: GROUP_ICONS[protocol] || null,
        current,
        currentText: current || '—',
        items,
      });
    }
  }
  // available 收窄到过滤后(I-1):组数组全坏(groups:[null,42])或组内条目
  // 全坏时 groups 为空 → 整区不渲染,不留零子元素空壳+尾随 sep(组件头注释
  // 自述契约)。groups 只在本函数 available 分支内填充,收窄语义安全。
  return { available: available && groups.length > 0, aiOn, groups };
}

// 组件本体:渲染派生结果 + 递延降级交互(toast),不发任何请求(api 层
// 无 activate 动作,本组件也不 import api)。
export default {
  name: 'EndpointZone',
  setup() {
    // 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询。
    const data = inject('panelData');
    const toast = inject('panelToast');
    const model = computed(() => {
      const s = data.summary.value;
      return endpointZoneModel(s ? s.endpoints : null, s ? s.ai : null);
    });

    // 展开状态按 protocol 记忆:2s 轮询回读新快照不打断用户展开的组
    // (mockup demo 默认全部收起——组头 current 名已可见,面板空间紧凑)。
    const expanded = ref({});
    function isOpen(protocol) {
      return expanded.value[protocol] === true;
    }

    // onGroupHead — 组头展开/收起。AI 加速关时整区锁(§6-4),handler 再闸
    // 一道(jsdom/键盘路径绕得开 CSS pointer-events 锁,双保险)。
    function onGroupHead(g) {
      if (!model.value.aiOn) return;
      expanded.value = { ...expanded.value, [g.protocol]: !isOpen(g.protocol) };
    }

    // onItem — 行点击:钉选递延拍板,任何行(含 ok/cooldown/disabled)统一
    // 只 toast,不勾选、不迁移、不发请求。cooldown/disabled 行不另弹「冷却中」
    // ——琥珀徽标已承载状态语义,统一文案降低认知负担(报告已说明此取舍)。
    function onItem() {
      if (!model.value.aiOn) return;
      toast.show('钉选能力即将上线', 'warn');
    }

    return { model, CHECK_SVG, isOpen, onGroupHead, onItem };
  },
};
</script>

<template>
  <!-- sep 放区尾并随整区显隐:整区不渲染时连 sep 一起消失——App.vue 不为④
       放静态 sep,避免可用性切换时出现孤线/双线(实现层决定,mockup 未含)。 -->
  <template v-if="model.available">
    <div class="zone ep-zone" :class="{ dimmed: !model.aiOn }">
      <div
        v-for="g in model.groups"
        :key="g.protocol"
        class="ep-group"
        :class="{ open: isOpen(g.protocol) }"
      >
        <!-- 组头(常显):协议图标 + 组名 + 当前激活名(130px 截断) + chevron -->
        <div class="g-head" :data-g="g.protocol" @click="onGroupHead(g)">
          <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
          <span v-if="g.icon" class="g-ico" aria-hidden="true" v-html="g.icon"></span>
          <span class="g-name">{{ g.name }}</span>
          <span class="g-current">{{ g.currentText }}</span>
          <svg
            class="chev"
            :class="{ rot: isOpen(g.protocol) }"
            width="12"
            height="12"
            viewBox="0 0 24 24"
            fill="none"
            stroke="currentColor"
            stroke-width="2.2"
            stroke-linecap="round"
            stroke-linejoin="round"
            style="opacity: 0.45"
            aria-hidden="true"
          >
            <path d="M6 9l6 6 6-6" />
          </svg>
        </div>
        <!-- 列表:展开 max-height 0→200px 过渡 0.25s(mockup .ep-list 原值) -->
        <div class="ep-list">
          <div
            v-for="(it, idx) in g.items"
            :key="idx"
            class="ep-item"
            :class="{ on: it.on, off: it.unavailable }"
            :data-nm="it.name"
            @click="onItem"
          >
            <!-- 14px 勾选圈:选中=品牌绿实底白勾(仅反映服务端 current,
                 钉选递延下点击不迁移) -->
            <span class="check" aria-hidden="true">
              <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
              <span v-if="it.on" class="check-mark" v-html="CHECK_SVG"></span>
            </span>
            <span class="nm">{{ it.name }}</span>
            <!-- 优先级徽标(P1/P2 绿描边 ghost;rank=0「—」)/琥珀冷却·摘除徽标 -->
            <span class="badge" :class="it.badgeCls">{{ it.badgeText }}</span>
          </div>
        </div>
      </div>
    </div>
    <div class="sep"></div>
  </template>
</template>

<style scoped>
/* 数值全部抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
   .zone/.ep-group/.g-head/.g-name/.g-current/.ep-item/.check/.nm/.off/
   .ep-list/.chev/.badge 系列段原值;深色变体按仓内 theme.css 口径用
   prefers-color-scheme 媒体查询(mockup body.dark 等价改写)。 */

/* 整区容器(mockup .zone):AI 关降透明 0.35 + 锁交互(§6-4) */
.zone {
  transition: opacity 0.25s;
}
.zone.dimmed {
  opacity: 0.35;
  pointer-events: none;
}

/* 协议组容器(mockup .ep-group 原值) */
.ep-group {
  margin: 3px 12px;
  border-radius: 12px;
  background: rgba(0, 0, 0, 0.03);
  border: 1px solid rgba(0, 0, 0, 0.04);
  overflow: hidden;
}
@media (prefers-color-scheme: dark) {
  .ep-group {
    background: rgba(255, 255, 255, 0.04);
    border-color: rgba(255, 255, 255, 0.05);
  }
}

/* 组头(mockup .g-head 原值) */
.g-head {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 11px;
  cursor: pointer;
  transition: background 0.12s;
}
.g-head:hover {
  background: rgba(0, 0, 0, 0.03);
}
@media (prefers-color-scheme: dark) {
  .g-head:hover {
    background: rgba(255, 255, 255, 0.03);
  }
}
.g-ico {
  display: flex;
  flex-shrink: 0;
  pointer-events: none;
}
.g-name {
  font-size: 12px;
  font-weight: 700;
  flex: 1;
}
/* 当前激活名(mockup .g-current 原值):10.5px muted,130px 截断 */
.g-current {
  font-size: 10.5px;
  color: #7d8f85;
  max-width: 130px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
@media (prefers-color-scheme: dark) {
  .g-current {
    color: #7e9186;
  }
}
/* chevron(mockup .chev 原值):展开 rotate 180° */
.chev {
  transition: transform 0.2s;
  flex-shrink: 0;
}
.chev.rot {
  transform: rotate(180deg);
}

/* 列表项(mockup .ep-item 原值) */
.ep-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 11px 6px 13px;
  cursor: pointer;
  transition: background 0.12s;
}
.ep-item:hover {
  background: rgba(0, 0, 0, 0.04);
}
@media (prefers-color-scheme: dark) {
  .ep-item:hover {
    background: rgba(255, 255, 255, 0.05);
  }
}
/* 勾选圈(mockup .check 原值):14px 圆,选中品牌绿实底白勾 */
.ep-item .check {
  width: 14px;
  height: 14px;
  border-radius: 50%;
  border: 1.5px solid rgba(0, 0, 0, 0.18);
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  transition: all 0.18s;
}
@media (prefers-color-scheme: dark) {
  .ep-item .check {
    border-color: rgba(255, 255, 255, 0.2);
  }
}
.ep-item.on .check {
  background: var(--brand);
  border-color: var(--brand);
}
.check-mark {
  display: flex;
}
/* 名称(mockup .nm 原值):11.5/600 截断 */
.ep-item .nm {
  font-size: 11.5px;
  font-weight: 600;
  flex: 1;
  min-width: 0;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 置灰(mockup .ep-item.off 原值):冷却/已摘除行 */
.ep-item.off {
  opacity: 0.55;
}

/* 列表展开/收起(mockup .ep-list 原值):max-height 0→200px 过渡 0.25s */
.ep-list {
  max-height: 0;
  overflow: hidden;
  transition: max-height 0.25s ease;
}
.ep-group.open .ep-list {
  max-height: 200px;
}

/* 徽标(mockup .badge 系原值):
   outline = 优先级 P1/P2/「—」,绿描边 ghost;
   cool    = 冷却/已摘除,琥珀底字(深色只调字色)。 */
.badge {
  font-size: 10.5px;
  font-weight: 700;
  padding: 2px 7px;
  border-radius: 999px;
  flex-shrink: 0;
}
.badge.outline {
  background: transparent;
  color: #15803d;
  border: 1px solid rgba(33, 196, 93, 0.4);
  font-weight: 600;
}
@media (prefers-color-scheme: dark) {
  .badge.outline {
    color: #4ade80;
  }
}
.badge.cool {
  background: rgba(230, 162, 60, 0.13);
  color: #b97a1e;
}
@media (prefers-color-scheme: dark) {
  .badge.cool {
    color: #f0b357;
  }
}
</style>
