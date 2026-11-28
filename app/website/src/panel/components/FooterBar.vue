<!-- FooterBar.vue — 面板⑥底部动作行(spec §4.6 + §6-8 + 无宿主降级修订)。
     左下版本号(10.5px muted tabular-nums,来自 summary.version,缺失「—」);
     右侧三键等宽:仪表盘 / 设备互访 / 退出。前两键灰字 hover 浮底;退出键
     danger 类 hover 变红(危险语义,mockup .f-btn.danger 原值)。
     宿主模式(isHosted=true):仪表盘/设备互访 → postMessageHost
     {type:'open', kind:'dashboard'|'peer'};退出 → 内联二次确认条(选定案,
     340px 窄面板模态显重,确认/取消占原按钮位最轻)→ postMessageHost
     {type:'quit'}(§6-8 破坏性动作确认兜底)。
     无宿主模式(M1 浏览器直开 panel.html):仪表盘/设备互访 → 同窗导航到
     仪表盘根页 window.location.href='/';退出键隐藏,原位置 muted 说明
     「浏览器面板暂不支持退出」。
     宿主探测口径:用 isHosted()(零副作用)而非 postMessageHost——后者
     探测即真发一条消息污染宿主通道;两者同族(认同一组宿主通道,api.js
     同文件),main.js 的 hosted 门控已用 isHosted 先例。open 投递返回值即
     送达语义:返回 false(宿主对象被改坏等通道异常)→ 同窗导航兜底,动作
     不断链。quit 投递失败无浏览器侧兜底语义,保持无操作。
     样式偏差自述:mockup .foot 的 border-top 刻意不抄——本仓区尾 sep 架构
     (④/⑤ 的 sep 随区显隐)已在那条线位置画了分隔线,再带 border-top 会
     双线;其余数值(padding/gap/字号/颜色/hover)原值照抄。 -->
<script>
import { computed, inject, ref } from 'vue';
import { isHosted, postMessageHost } from '../api';

// 底部三键图标:逐字抄自 docs/superpowers/specs/tray-menu-style-mockups.html
// 的 I.dashboard/I.phone/I.quit 段。经 v-html 渲染,内容为本文件字面常量,
// 无外部输入,无 XSS 面。
const DASH_ICON =
  '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="4" width="18" height="14" rx="2"/><path d="M3 9h18M8 21h8"/></svg>';
const PEER_ICON =
  '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="3" width="14" height="18" rx="3"/><path d="M10 18h4"/></svg>';
const QUIT_ICON =
  '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9"/></svg>';

// 组件本体:渲染 + 动作派发(宿主消息 / 同窗导航),零 fetch(本行不读
// 聚合数据以外的任何接口;版本号来自已注入的 summary)。
export default {
  name: 'FooterBar',
  setup() {
    // 数据层(纪律 M-4):经 App.vue 根壳 inject,绝不开第二条轮询。
    const data = inject('panelData');

    // 宿主探测:面板生命周期内宿主形态不变,setup 取一次即可。
    const hosted = isHosted();

    // 版本号:缺失/空/非字符串 → 「—」(与 EndpointZone 的 g-current 兜底
    // 同口径;首帧 summary=null 时底行照常渲染、动作可用)。
    const versionText = computed(() => {
      const s = data.summary.value;
      return s && typeof s.version === 'string' && s.version ? s.version : '—';
    });

    // 退出二次确认(§6-8):内联确认条态,true 时整行切成「确认退出?+确认/取消」。
    const confirming = ref(false);

    // navigateRoot — 无宿主兜底:同窗导航到仪表盘根页(面板在 /panel.html,
    // 根页即仪表盘;面板内不做页内跳转的 §6-9 约束由宿主模式的消息通道承揽)。
    function navigateRoot() {
      window.location.href = '/';
    }

    // openExternal(kind) — 仪表盘/设备互访两键共用:宿主在 → 投 open 消息,
    // 投递返回值即送达语义,返回 false(宿主对象被改坏等通道异常)→ 同窗
    // 导航兜底,动作不断链;无宿主 → 不碰宿主通道,直接同窗导航。
    // 宿主侧 open 分派成功即 exit(0) 正常关闭面板(2026-11-28 用户走查
    // 「点仪表盘开了浏览器,面板不消失」:此前壳 open 后驻留,指望浏览器
    // 抢焦点走失焦链,macOS Accessory 进程的 NonactivatingPanel 永不 resignKey,
    // Windows 冷启动前台切换延迟期间面板悬在前面)——页面无需也不能自己
    // 关自己,关闭归壳。
    function openExternal(kind) {
      if (hosted) {
        if (postMessageHost({ type: 'open', kind })) return;
      }
      navigateRoot();
    }

    // onQuit / onConfirmQuit / onCancelQuit — 退出三步(仅宿主模式可达:
    // 无宿主时退出键不渲染,确认条无从进入)。
    function onQuit() {
      confirming.value = true;
    }
    function onConfirmQuit() {
      postMessageHost({ type: 'quit' });
      // 投递失败(通道坏)也无浏览器侧兜底语义——收条回正常行,不再误触。
      confirming.value = false;
    }
    function onCancelQuit() {
      confirming.value = false;
    }

    return { hosted, versionText, confirming, DASH_ICON, PEER_ICON, QUIT_ICON, openExternal, onQuit, onConfirmQuit, onCancelQuit };
  },
};
</script>

<template>
  <div class="foot">
    <!-- 退出确认条(宿主模式):内联占原行,确认红键 / 取消灰键 -->
    <template v-if="confirming">
      <span class="confirm-text">确认退出 Aliang？</span>
      <button class="f-btn danger" data-act="quit-yes" @click="onConfirmQuit">确认退出</button>
      <button class="f-btn" data-act="quit-no" @click="onCancelQuit">取消</button>
    </template>
    <template v-else>
      <!-- 版本号(左下):10.5px muted tabular-nums,缺失「—」 -->
      <span class="ver">{{ versionText }}</span>
      <!-- 仪表盘:宿主投 open 消息;无宿主/通道异常 → 同窗导航根页 -->
      <button class="f-btn" data-act="dashboard" @click="openExternal('dashboard')">
        <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
        <span class="f-ico" aria-hidden="true" v-html="DASH_ICON"></span>仪表盘
      </button>
      <!-- 设备互访:同上,kind=peer -->
      <button class="f-btn" data-act="peer" @click="openExternal('peer')">
        <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
        <span class="f-ico" aria-hidden="true" v-html="PEER_ICON"></span>设备互访
      </button>
      <!-- 退出:宿主模式显示(危险语义);无宿主换 muted 说明 -->
      <button v-if="hosted" class="f-btn danger" data-act="quit" @click="onQuit">
        <!-- eslint-disable-next-line vue/no-v-html —— 内容是本文件字面常量 -->
        <span class="f-ico" aria-hidden="true" v-html="QUIT_ICON"></span>退出
      </button>
      <span v-else class="f-note">浏览器面板暂不支持退出</span>
    </template>
  </div>
</template>

<style scoped>
/* 数值抄自 docs/superpowers/specs/tray-menu-style-mockups.html 的 .foot/
   .f-btn/.ver 段原值;border-top 刻意不抄(双线规避,见文件头自述);
   深色变体按仓内 theme.css 口径用 prefers-color-scheme 媒体查询。 */

/* 底部动作行(mockup .foot 原值,去 border-top) */
.foot {
  display: flex;
  align-items: center;
  padding: 8px 10px;
  gap: 2px;
  background: rgba(0, 0, 0, 0.02);
  margin-top: 6px;
}
@media (prefers-color-scheme: dark) {
  .foot {
    background: rgba(255, 255, 255, 0.02);
  }
}

/* 三键等宽(mockup .f-btn 原值):flex:1 + 灰字 + hover 浮底 */
.f-btn {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 7px 0;
  border-radius: 8px;
  font-size: 11.5px;
  font-weight: 600;
  color: #4a5d52;
  cursor: pointer;
  transition: all 0.12s;
  border: none;
  background: transparent;
  font-family: inherit;
}
@media (prefers-color-scheme: dark) {
  .f-btn {
    color: #8fa396;
  }
}
.f-btn:hover {
  background: rgba(0, 0, 0, 0.05);
}
@media (prefers-color-scheme: dark) {
  .f-btn:hover {
    background: rgba(255, 255, 255, 0.06);
  }
}
/* 退出键危险语义:hover 变红(mockup .f-btn.danger:hover 原值) */
.f-btn.danger:hover {
  color: var(--danger);
  background: rgba(229, 72, 77, 0.08);
}
.f-ico {
  display: flex;
  pointer-events: none;
}

/* 版本号(mockup .ver 原值):10.5px muted tabular-nums */
.ver {
  font-size: 10.5px;
  color: #85988d;
  padding-left: 8px;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
@media (prefers-color-scheme: dark) {
  .ver {
    color: #6e8277;
  }
}

/* 无宿主退出说明(替换退出键的第三格):10.5px muted */
.f-note {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 7px 0;
  font-size: 10.5px;
  color: #85988d;
}
@media (prefers-color-scheme: dark) {
  .f-note {
    color: #6e8277;
  }
}

/* 退出确认条文案:占版本号+余量,11.5px/600 与按钮同档 */
.confirm-text {
  flex: 1;
  min-width: 0;
  padding-left: 8px;
  font-size: 11.5px;
  font-weight: 600;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
