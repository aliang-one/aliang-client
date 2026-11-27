<!-- EndpointZone.vue — 面板④ENDPOINT 区(spec §4.4 + §6-4 + 钉选激活
     plan 2026-11-27 Task 3:radio 接真激活,取代此前「钉选递延只 toast」
     拍板)。按协议族分组(Anthropic 组 / OpenAI 组),组头常显(协议图标 +
     组名 + 当前激活 endpoint 名 + chevron),点组头展开/收起(chevron
     rotate 180°,列表 max-height 0→200px 过渡 0.25s)。列表项 radio 语义:
     14px 勾选圈 + 名称 + 徽标。
     radio 真激活(plan Task 3 语义裁定):
       ok 行(rank≥1 且非 disabled)/cooldown 行(冷却放行,不清零冷却)→
       乐观勾移 + POST /api/endpoints/activate → 成功经既有 summary 刷新
       (data.refresh,同一条轮询管道)回读确认;API 失败/回读不符 → 回滚
       勾移 + warn toast。rank=0(builtin 兜底/auto_group=0/未设优先级)与
       disabled(摘除)行本地 toast 零请求;已勾选行点击=静默空操作早退。
       ⚠️ rank 池口径 = auto_group ∧ priority>0(tray_summary.go 授 rank),而
       selectAuto 的池只看 enabled∧auto_group——priority=0 且 auto_group=1
       的行后端会接受激活、面板本地 rank 闸却拒绝,属已挂账的计划层缝隙
       (待产品对齐),【不是】与后端严格同构;只有拒绝序沿用后端
       ActivateInGroup(builtin/非池行先于摘除)。
     勾选态判定不用 current==item.name 名字匹配(组内重名行撞车全亮)——
     按行自身字段推定(rank 最小的 ok 池行;池全冷却回落 builtin=current
     名字基),见 deriveOnKey 注释;后端 current 仍是名字基,本轮不改后端。
     降级硬约束:available=false 或 groups 缺失/空数组 → 整区不渲染,无空壳、
     无「建设中」占位;sep 放区尾并随整区显隐(实现层决定,mockup 未含此
     sep;不渲染时连 sep 一起消失,不留孤线/双线)。AI 加速关(ai.enabled
     =false)→ 整区 .zone.dimmed(降透明 0.35 + 锁交互,§6-4)。 -->
<script>
import { computed, inject, ref } from 'vue';
import { activateEndpoint } from '../api';

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

// deriveOnKey(items, current) — 组内勾选行推定,返回行键(id 优先,行缺 id
// 时退回名字)。质量评审移交的注意事项落地:【不用】current==item.name 的
// 名字匹配判勾选态——组内重名行会全部点亮;优先按行自身字段推定,与后端
// current(tray_summary.go:池行里首个 ok 的 poolSorted 行)同口径:
//   1) items 中 rank>=1 且 state==='ok' 的行里 rank 最小者 = current 的池来源
//      (selectAuto「下一个会选谁」与组头名永远自洽);
//   2) 池无 ok 行(全冷却/空池)→ 后端 current 回落 builtin 名 → 按
//      name===current 找 rank0 ok 行(builtin 行全库唯一,名字兜底安全);
//   3) 都没有 → ''(无行勾选,组头「—」)。
// 取舍说明:后端 group.current 仍是名字基(本轮不改后端),名字仅在 builtin
// 回落分支使用;池分支完全走 rank 推定,重名行不互串。
// 退化限制:行缺 id 且组内同名时,池分支(与 builtin 回落分支)按名字回退
// 比较可能双亮——主路径自后端 Task 1 起 items 恒带唯一 id,此限制仅针对
// 防御降级输入,不构成正常数据下的缺陷。
export function deriveOnKey(items, current) {
  let best = null;
  for (const it of items) {
    if (it.state !== 'ok' || !(it.rank >= 1)) continue;
    if (best === null || it.rank < best.rank) best = it;
  }
  if (best !== null) return best.id || best.name;
  if (typeof current === 'string' && current !== '') {
    for (const it of items) {
      if (it.state === 'ok' && it.name === current) return it.id || it.name;
    }
  }
  return '';
}

// endpointZoneModel(endpoints, ai) — ENDPOINT 区内容纯派生(spec §4.4 + 数据
// 防御契约第 8 条)。endpoints:parseSummary 归一后的 endpoints 子源
// (TrayEndpointsSummary,可 null);ai:同上(TrayAISummary,只看 enabled)。
// 返回 { available, aiOn, groups }:
//   available available=true 且 groups 为非空数组,其余一律 false → 整区不渲染
//   aiOn      AI 加速开(仅真值 true 为开,与 AISection 同口径)→ false 时整区
//             dimmed(降透明 0.35 + 锁,§6-4)
//   groups    逐组归一 {protocol,label,name,icon,current,currentText,items}
// items 逐条 {id(钉选定位键), name, rank, state, cooldownSec, on(勾选圈,按
// deriveOnKey 推定), unavailable, badgeCls, badgeText};rank 同时是组件层
// 本地拒绝闸的判据(rank<1=builtin 兜底/非池行,零请求)。不可用行
// (cooldown/disabled)照常渲染只加徽标(契约第 8 条)。
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
      const rawItems = Array.isArray(g.items) ? g.items : [];
      // 先逐条归一(坏条目跳过),再整组算一次勾选键——勾选推定要比较全部
      // 行的 rank/state,逐条边推会漏比较。
      const norm = [];
      for (const it of rawItems) {
        if (it === null || it === undefined || typeof it !== 'object' || Array.isArray(it)) continue;
        const name = typeof it.name === 'string' ? it.name : '';
        const rank = typeof it.rank === 'number' ? it.rank : 0;
        const state = typeof it.state === 'string' ? it.state : '';
        const remaining = typeof it.cooldown_remaining_sec === 'number' ? it.cooldown_remaining_sec : 0;
        norm.push({
          id: typeof it.id === 'string' ? it.id : '',
          name,
          rank,
          state,
          cooldownSec: remaining,
        });
      }
      const onKey = deriveOnKey(norm, current);
      const items = norm.map((it) => {
        const badge = normalizeBadge(it.rank, it.state, it.cooldownSec);
        return {
          id: it.id,
          name: it.name,
          rank: it.rank,
          state: it.state,
          cooldownSec: it.cooldownSec,
          // 勾选圈钉在推定行(与组头 g-current 同义,重名行不撞车,见
          // deriveOnKey);点击后的乐观迁移在组件层覆盖,不污染本纯函数。
          on: onKey !== '' && (it.id || it.name) === onKey,
          unavailable: it.state === 'cooldown' || it.state === 'disabled',
          badgeCls: badge.cls,
          badgeText: badge.text,
        };
      });
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

// confirmActivated(summary, protocol, id) — 激活回读确认:刷新后的聚合快照
// 里,目标行(按 id)仍在组内且是唯一池行(rank>=1)。判据说明:激活的持久化
// 效果 = 组内其余 auto 行 enabled=0 退清单(endpoint_service.go
// ActivateInGroup),目标行保留且 rank 收敛为组内最小;不判「目标行 on」——
// 目标行处于冷却时勾选按调度语义落在 builtin 兜底行(current 名字基回落),
// 但激活本身已生效。summary 是 parseSummary 归一后的聚合对象(endpoints 子源
// 原样透传,items 仍带 id/rank 原始键)。
function confirmActivated(summary, protocol, id) {
  if (summary === null || summary === undefined || typeof summary !== 'object') return false;
  const ep = summary.endpoints;
  if (ep === null || ep === undefined || typeof ep !== 'object' || !Array.isArray(ep.groups)) return false;
  const g = ep.groups.find((x) => x !== null && x !== undefined && typeof x === 'object' && x.protocol === protocol);
  if (!g || !Array.isArray(g.items)) return false;
  let seen = false;
  for (const it of g.items) {
    if (it === null || it === undefined || typeof it !== 'object') continue;
    if (typeof it.id === 'string' && it.id === id) {
      seen = true;
      continue;
    }
    if (typeof it.rank === 'number' && it.rank >= 1) return false; // 同组仍有别的池行 = 互斥未完成
  }
  return seen;
}

// 组件本体:渲染派生结果 + radio 真激活交互(乐观勾移/失败回滚/回读确认;
// api 层 activateEndpoint,App.vue 根壳经 panelData 下发 refresh)。
export default {
  name: 'EndpointZone',
  setup() {
    // 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询;refresh
    // 是 useSummary 同一管道的手动刷新,激活回读确认复用它,不新开定时器。
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

    // pending — 在途激活(全局单飞):{protocol,id}。乐观勾移经 isOn 覆盖
    // 派生勾选;成功+回读确认后由刷新后的 summary 承接,失败/未确认随
    // pending 释放回滚到派生真值。单飞而非按行并行:组内互斥下并发勾移
    // 互相踩踏(后端 last-writer-wins),乐观态无法同时成立——在途期间
    // 同行禁重复点击,其他行也一并闸住(窗口 ≤10s 超时档,常态毫秒级)。
    const pending = ref(null);

    // isOn — 勾选圈渲染值:pending 所在组整体勾移到目标行(旧行同步灭勾,
    // 「移」的语义);其余沿用派生值。pending 结束即回派生真值。
    function isOn(g, it) {
      const p = pending.value;
      if (p !== null && p.protocol === g.protocol) return p.id === it.id;
      return it.on;
    }

    // humanizeActionError — 错误 toast 人话化(与 TransportRow 同口径,本
    // 组件本地一份:面板组件自含不互引):api 层超时转译串(含「超时」)→
    // 明确提示重试;网络/HTTP 层错(TypeError / 「…失败: HTTP xxx」)→
    // 通用稍后重试;其余是服务端人话 msg(如「已摘除的行请先在仪表盘恢复」),
    // 原样上屏——它好于任何技术转译串。
    function humanizeActionError(err) {
      const msg = err && err.message ? String(err.message) : '';
      if (!msg) return '激活失败，请稍后重试';
      if (msg.includes('超时')) return '激活超时，请重试';
      if (err instanceof TypeError || /HTTP \d+/.test(msg)) return '激活失败，请稍后重试';
      return msg;
    }

    // onItem — 行点击(radio 真激活,plan 2026-11-27 Task 3 语义裁定):
    //   rank=0/缺 id 行 → 本地 warn toast 零请求。rank 池口径如实说明:rank
    //     只授给 auto_group ∧ priority>0 的行(tray_summary.go:388 的 rankOf),
    //     selectAuto 的候选池却只看 enabled∧auto_group——priority=0 且
    //     auto_group=1 的行后端 ActivateInGroup 会接受,面板这里却因 rank=0
    //     本地拒绝。这是已挂账的计划层缝隙(待产品对齐),本闸【不与后端
    //     同构】,不能把 rank 闸当成 builtin/非池闸的完整等价物;
    //   disabled 行(rank>=1 池内摘除行)→ 本地 warn toast 零请求(后端 400
    //     同文案「已摘除的行请先在仪表盘恢复」);
    //   已勾选行(it.on)→ 静默早退(见下方「A」注释:radio 空操作);
    //   其余(ok/cooldown 且 rank>=1)→ 乐观勾移 + activateEndpoint → 成功
    //     经 data.refresh()(既有轮询管道)回读确认;API 失败/回读不符 →
    //     回滚勾移 + warn toast。
    // 拒绝序沿用后端 ActivateInGroup 的判定顺序(不存在/协议 → builtin/非池
    // → 摘除),但如上所述 rank 池定义存在缝隙,别误读为严格同构。
    async function onItem(g, it) {
      if (!model.value.aiOn) return;
      if (it.rank < 1 || !it.id) {
        toast.show('builtin 为兜底行/该行不在 auto 调度池，激活无效', 'warn');
        return;
      }
      if (it.state === 'disabled') {
        toast.show('已摘除的行请先在仪表盘恢复', 'warn');
        return;
      }
      // 「A」已勾选行 = radio 空操作:点当前选中项本不该有任何副作用,但后端
      // ActivateInGroup 的互斥关停会把同组其余 enabled=1 行(含 cooldown/
      // health=disabled 行)一并 enabled=0;enabled=0 行失去懒恢复通道=永久
      // 手动停用。故静默早退(要求无 pending;在途期间点击走下方可见化提示)。
      if (pending.value === null && it.on) return;
      // 在途防重:单飞闸(见 pending 注释)。丢弃已可见化——静默吞点击会让
      // 用户以为面板失灵,统一提示「激活进行中」(pending 期间点任意行,含
      // 重复点同一行,都走这条)。
      if (pending.value !== null) {
        toast.show('激活进行中，请稍候', 'warn');
        return;
      }
      const wasCooldown = it.state === 'cooldown';
      pending.value = { protocol: g.protocol, id: it.id };
      try {
        await activateEndpoint(g.protocol, it.id);
        // 回读确认:复用既有 summary 刷新(同一轮询管道,未新开定时器)。
        // 刷新前后按引用比对识别「回读未发生」——poll 失败保留旧引用
        // (summary.js),此刻任何派生判定都建立在陈旧快照上,如实报未确认。
        const before = data.summary.value;
        await data.refresh();
        const after = data.summary.value;
        if (after === before || !confirmActivated(after, g.protocol, it.id)) {
          toast.show('激活结果未确认，请稍后查看', 'warn');
          return; // finally 释放 pending,勾选回派生值(回滚)
        }
        // 成功 toast:带组名上下文(spec §6-6「Anthropic 组 → 名」口径);
        // cooldown 行附冷却恢复预期(分钟向上取整,徽标同口径)。
        if (wasCooldown) {
          const min = Math.max(1, Math.ceil((it.cooldownSec || 0) / 60));
          toast.show(`已激活「${g.label} → ${it.name}」（冷却中，约 ${min} 分钟后恢复）`);
        } else {
          toast.show(`已激活「${g.label} → ${it.name}」`);
        }
      } catch (err) {
        toast.show(humanizeActionError(err), 'warn');
      } finally {
        pending.value = null;
      }
    }

    return { model, CHECK_SVG, isOpen, onGroupHead, onItem, isOn };
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
            :class="{ on: isOn(g, it), off: it.unavailable }"
            :data-nm="it.name"
            @click="onItem(g, it)"
          >
            <!-- 14px 勾选圈:选中=品牌绿实底白勾(推定行;点击后乐观迁移由
                 isOn 覆盖,失败回滚回推定真值) -->
            <span class="check" aria-hidden="true">
              <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
              <span v-if="isOn(g, it)" class="check-mark" v-html="CHECK_SVG"></span>
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
