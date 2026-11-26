<!-- AISection.vue — 面板③AI 加速区(spec §4.3 + §3「AI 加速关」行 + §6-4)。
     总开关行(⚡ +「AI 加速」+ 32×19 胶囊 switch)+ provider 图标行。
     图标池固定 5 家(不随数据增减):anthropic/openai/cursor 可检测 +
     gemini/grok 预留位(标「暂不支持检测」)。点亮纯检测驱动
     (summary.ai.providers[].detected),不可手动点亮;点击图标只读弹
     详情 toast。AI 加速关 → provider 行降透明 0.35 + 锁交互 + 行尾文案
     变「AI 加速已关 · 图标全灰」(§6-4)。
     ⚠️ v1 开关降级为只读展示(2026-11-27 读码定案):无 AI 专用开关端点
     (AI 规则写路径归 /api/config/customer 的 customer 配置维护——深合并
     patch(mergeCustomerPayload → deepMergeJSONObjects),技术上可逐条
     ai_rules.<key>.enble 部分 patch,仪表盘即逐家开关);且托盘快照只含
     已启用家族(ai.enabled 是派生值:Enabled=len(providers)>0)——「关」
     可全关,「开」无法还原此前启用集合(禁用家族不在快照里),半能开关
     比只读更危险,故 v1 只读降级。switch 渲染 summary 值,点击 toast
     引导去仪表盘。 -->
<script>
import { computed, inject } from 'vue';

// ---- 图标池(spec §4.3:可检测 3 家 + 预留 2 家) ----
// 可检测家数 = 计数分母(预留位不计入);未知 key(如已停用的 vscode)
// 按预留位口径处理:永不点亮,也不进图标池。
const DETECTABLE_KEYS = ['anthropic', 'openai', 'cursor'];
const RESERVED_KEYS = ['gemini', 'grok'];

// 池内默认名(服务端 label 空/条目缺失时的兜底,§4.3 图标池命名)。
const POOL_LABELS = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  cursor: 'Cursor',
  gemini: 'Gemini',
  grok: 'Grok',
};

// provider 品牌图标:简化几何近似(实现计划 §5「provider 品牌图标用
// 简化几何近似,mockup 内已有全套,实现直接取用」)——逐字抄自
// docs/superpowers/specs/tray-menu-style-mockups.html 的 I.providers 段。
// 经 v-html 渲染,内容为本文件内的字面常量,无外部输入,无 XSS 面。
const PROV_ICONS = {
  anthropic: '<svg width="15" height="15" viewBox="0 0 24 24" fill="currentColor"><path d="M12.2 4 5.6 20h3.1l1.2-3.1h6.3L17.4 20h3.1L13.9 4zM10.9 14l2-5.3 2 5.3z" transform="translate(-1.5,0)"/></svg>',
  openai: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linejoin="round"><path d="M12 3c-2 1.2-2 3.6-2 3.6S8 5.4 6.2 6.4c-.6 2.3 1.2 4 1.2 4s-2.2.6-2.4 2.9c1.8 1.6 4 .9 4 .9s-.6 2.2 1 3.6c2.2-.5 3-2.8 3-2.8s.8 2.3 3 2.8c1.6-1.4 1-3.6 1-3.6s2.2.7 4-.9c-.2-2.3-2.4-2.9-2.4-2.9s1.8-1.7 1.2-4c-1.8-1-3.8.2-3.8.2S14 4.2 12 3z" transform="scale(0.82) translate(2.6,2.6)"/></svg>',
  cursor: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 2 4 6.5v11L12 22l8-4.5v-11z"/><path d="M4 6.5 12 11l8-4.5M12 11v11"/></svg>',
  gemini: '<svg width="15" height="15" viewBox="0 0 24 24" fill="currentColor"><path d="M12 3c.6 3.8 3.2 6.4 7 7-3.8.6-6.4 3.2-7 7-.6-3.8-3.2-6.4-7-7 3.8-.6 6.4-3.2 7-7z"/></svg>',
  grok: '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M5 19 19 5M12.5 12.5 19 19"/></svg>',
};

// aiSectionModel(ai) — AI 加速区内容纯派生(spec §4.3 + §6-4)。
//   ai:parseSummary 归一后的 ai 子源(TrayAISummary,可 null——服务端
//   单源降级/error 态下无数据)。返回
//   { enabled, tiles, litCount, detectableCount, hint }:
//     enabled         switch on 态(仅真值 true 为开,缺省视为关)
//     tiles           图标池 5 家的渲染模型{key,label,live,reserved,title,detail}
//     litCount        已点亮家数(=开态下 detected 的可检测家族;关态恒 0)
//     detectableCount 分母=可检测家数 3(预留位不计入)
//     hint            行尾文案:开=「检测到 N / 3」;关=固定降级文案(§6-4)
// 防御纪律(spec §4.3 数据防御):providers 缺失/null/坏类型 → 全灰 +
// 计数 0/3;未知 key 按预留位口径不点亮;条目坏类型逐条跳过不炸。
// 纯函数:不读全局、不改入参,坏输入走容错分支而非抛错。
export function aiSectionModel(ai) {
  const obj =
    ai !== null && ai !== undefined && typeof ai === 'object' && !Array.isArray(ai) ? ai : {};
  const enabled = obj.enabled === true;
  const rawList = Array.isArray(obj.providers) ? obj.providers : [];

  // 条目归一:key 统一 trim+小写(与服务端 NormalizeAIProviderKey 同口径,
  // provider/config/ai_rules_normalize.go:ToLower+TrimSpace);重复 key 取
  // 首条;label/detected/detail 只认契约类型。
  const byKey = new Map();
  for (const p of rawList) {
    if (p === null || p === undefined || typeof p !== 'object') continue;
    const key = typeof p.key === 'string' ? p.key.trim().toLowerCase() : '';
    if (!key || byKey.has(key)) continue;
    byKey.set(key, {
      label: typeof p.label === 'string' ? p.label : '',
      detected: p.detected === true,
      detail: typeof p.detail === 'string' ? p.detail : '',
    });
  }

  // 图标池固定 5 家:可检测 3 家按「开关开 且 detected」点亮(关态整行已
  // 降透明+行尾「图标全灰」,点亮会自相矛盾,故 live 加 enabled 闸);
  // 预留 2 家永不点亮(无检测通道,哪怕数据里混入 detected=true 条目)。
  const tiles = [];
  for (const key of DETECTABLE_KEYS) {
    const entry = byKey.get(key) || null;
    const label = entry && entry.label ? entry.label : POOL_LABELS[key];
    const live = enabled && entry !== null && entry.detected;
    tiles.push({
      key,
      label,
      live,
      reserved: false,
      title: entry && entry.detail ? `${label} · ${entry.detail}` : label,
      detail: entry ? entry.detail : '',
    });
  }
  for (const key of RESERVED_KEYS) {
    const entry = byKey.get(key) || null;
    const label = entry && entry.label ? entry.label : POOL_LABELS[key];
    tiles.push({
      key,
      label,
      live: false, // 预留位永不点亮(§4.3「暂不支持检测」)
      reserved: true,
      // 未来服务端 preset 纳入 gemini/grok 并带真实流量 detail → title 跟随
      // 真实详情(否则点击弹真实 detail 与 title「暂不支持检测」矛盾);
      // 纯占位(无数据条目/detail 空)保持「暂不支持检测」标记。
      title: entry && entry.detail ? `${label} · ${entry.detail}` : `${label} · 暂不支持检测`,
      detail: entry ? entry.detail : '',
    });
  }

  const litCount = tiles.filter((t) => t.live).length;
  const hint = enabled
    ? `检测到 ${litCount} / ${DETECTABLE_KEYS.length}`
    : 'AI 加速已关 · 图标全灰';
  return { enabled, tiles, litCount, detectableCount: DETECTABLE_KEYS.length, hint };
}

// 组件本体:只渲染派生结果 + 只读交互(toast),不发起任何写路径。
export default {
  name: 'AISection',
  setup() {
    // 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询。
    const data = inject('panelData');
    const toast = inject('panelToast');
    const model = computed(() =>
      aiSectionModel(data.summary.value ? data.summary.value.ai : null)
    );

    // onSwitch — 只读降级(降级原因见文件头注释):switch 只渲染 summary
    // 回读值,点击不翻转、不落盘,toast 引导去仪表盘。两态(开/关)同文案
    // ——点击都是「这里动不了」的统一引导。
    function onSwitch() {
      toast.show('请到仪表盘开关 AI 加速', 'warn');
    }

    // onProv — 图标点击:只读弹检测详情(spec §6-5「点亮由检测驱动;点击
    // 查详情」)。AI 加速关时行已锁(dimmed + pointer-events:none),
    // handler 再闸一道(jsdom/键盘路径绕得开 CSS 锁,双保险),点击无响应。
    // detail 空(启用规则但尚无流量/预留位无数据)→「暂无详情」。
    function onProv(tile) {
      if (!model.value.enabled) return;
      const msg = tile.detail ? `${tile.label}：${tile.detail}` : `${tile.label}：暂无详情`;
      toast.show(msg);
    }

    return { model, PROV_ICONS, onSwitch, onProv };
  },
};
</script>

<template>
  <div class="ai-block">
    <!-- 总开关行:⚡(内联 SVG,mockup I.bolt)+「AI 加速」+ 32×19 胶囊
         switch(只读降级:点击 toast 引导仪表盘,自身 title 同文案) -->
    <div class="ai-row">
      <span class="ai-ico" aria-hidden="true"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M13 2 3 14h7l-1 8 10-12h-7z" /></svg></span>
      <span class="ai-label">AI 加速</span>
      <!-- v1 只读降级:非真开关(点击只 toast 引导),不声明 switch 语义
           (role/aria-checked 与键盘不可达不匹配),纯视觉 + title 即可 -->
      <div
        class="switch"
        :class="{ on: model.enabled }"
        data-act="ai-switch"
        title="请到仪表盘开关 AI 加速"
        @click="onSwitch"
      ></div>
    </div>

    <!-- provider 图标行:AI 关 → 整行降透明 0.35 + pointer-events 锁
         (§6-4,mockup .prov-row.dimmed 同款);点亮纯检测驱动,不可手点;
         行尾 muted 计数(mockup .hint 同款) -->
    <div class="prov-row" :class="{ dimmed: !model.enabled }">
      <div
        v-for="tile in model.tiles"
        :key="tile.key"
        class="prov"
        :class="{ live: tile.live }"
        :title="tile.title"
        :data-key="tile.key"
        @click="onProv(tile)"
      >
        <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
        <span class="prov-ico" v-html="PROV_ICONS[tile.key]"></span>
        <!-- 状态点常驻(scale(0) 隐藏),live 时弹性缩放出现(spec §5 动效) -->
        <span class="p-dot" aria-hidden="true"></span>
      </div>
      <span class="hint">{{ model.hint }}</span>
    </div>
  </div>
</template>

<style scoped>
/* 数值全部抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的
   .row(总开关行)/.prov-row/.prov/.p-dot/.switch 段原值;深色变体按
   仓内 theme.css 口径用 prefers-color-scheme 媒体查询(mockup body.dark
   等价改写)。 */

/* 总开关行(mockup .row 布局值;AI 行不 hover 变底,去 .row:hover) */
.ai-row {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 14px;
}
.ai-row svg {
  flex-shrink: 0;
}
.ai-label {
  flex: 1;
  min-width: 0;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* switch(mockup .switch 原值):32×19 圆角胶囊,品牌绿 on 态 */
.switch {
  width: 32px;
  height: 19px;
  border-radius: 999px;
  background: rgba(0, 0, 0, 0.14);
  position: relative;
  cursor: pointer;
  flex-shrink: 0;
  transition: background 0.18s;
}
@media (prefers-color-scheme: dark) {
  .switch {
    background: rgba(255, 255, 255, 0.14);
  }
}
.switch::after {
  content: '';
  position: absolute;
  top: 2px;
  left: 2px;
  width: 15px;
  height: 15px;
  border-radius: 50%;
  background: #fff;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.25);
  transition: left 0.18s;
}
.switch.on {
  background: var(--brand);
}
.switch.on::after {
  left: 15px;
}

/* provider 图标行(mockup .prov-row 原值):关态降透明 0.35 + 锁交互 */
.prov-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 8px 12px;
  transition: opacity 0.25s;
}
.prov-row.dimmed {
  opacity: 0.35;
  pointer-events: none;
}

/* 单个 provider 图标(mockup .prov 原值):30×30 圆角 9px,默认全灰 */
.prov {
  width: 30px;
  height: 30px;
  border-radius: 9px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: rgba(0, 0, 0, 0.05);
  color: #a9b8af;
  cursor: pointer;
  position: relative;
  transition: all 0.2s;
  flex-shrink: 0;
}
@media (prefers-color-scheme: dark) {
  .prov {
    background: rgba(255, 255, 255, 0.06);
    color: #5f7268;
  }
}
.prov:hover {
  transform: translateY(-1px);
  background: rgba(0, 0, 0, 0.08);
}
@media (prefers-color-scheme: dark) {
  .prov:hover {
    background: rgba(255, 255, 255, 0.1);
  }
}
/* 点亮态(mockup .prov.live 原值):深色文字 + 绿描边 inset 1.5px */
.prov.live {
  color: #17251d;
  box-shadow: inset 0 0 0 1.5px rgba(33, 196, 93, 0.55);
}
@media (prefers-color-scheme: dark) {
  .prov.live {
    color: #eef5f0;
  }
}
.prov-ico {
  display: flex;
  pointer-events: none;
}

/* 状态点(mockup .p-dot 原值):9px 绿点右下角,scale 0→1 弹性出现
   (spec §5 动效 0.2s cubic-bezier(0.34,1.56,0.64,1));常驻 scale(0)
   隐藏,点亮加 .live 才缩放出现——CSS 过渡天然承载出现/消失动画 */
.p-dot {
  position: absolute;
  right: -2px;
  bottom: -2px;
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--brand);
  border: 2px solid rgba(252, 254, 252, 0.9);
  transform: scale(0);
  transition: transform 0.2s cubic-bezier(0.34, 1.56, 0.64, 1);
  pointer-events: none;
}
@media (prefers-color-scheme: dark) {
  .p-dot {
    border-color: rgba(22, 30, 26, 0.9);
  }
}
.prov.live .p-dot {
  transform: scale(1);
}

/* 行尾计数(mockup .prov-row .hint 原值):muted 右对齐 */
.hint {
  flex: 1;
  text-align: right;
  font-size: 10.5px;
  color: #85988d;
}
@media (prefers-color-scheme: dark) {
  .hint {
    color: #6e8277;
  }
}
</style>
