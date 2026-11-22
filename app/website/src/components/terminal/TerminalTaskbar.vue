<template>
  <!--
    底部任务栏(P2.3,计划 2026-11-22-web-terminal-phase23 §P2.3):网页终端
    多窗注册表的视图层 affordance。注册表驱动:直接消费 useTerminalWindows()
    单例(开/最小化窗集合 + 动作),父层(P2.4 TerminalWorkspace)只传 activeId
    ——任务栏本就是注册表的常驻操作面,与 PinGuard 直连 useWebTerminal 同取向;
    FloatingWindow 则反之是纯壳(props 进 events 出),两者分工见各自头注。
    排序拍板:z 序合并(最近交互居右,AliangBoard 参考语义)——注册表在
    create/focus/restore 时单调抬 zIndex、minimize 不动 z,故按 zIndex 升序
    合并 open+minimized 即「最近交互」序,活动窗恒在最右。
    交互:pill 点击 = minimized→restoreWindow / 否则 focusWindow(P2.1 拍板的
    双语义,各走一条路不留静默分支);✕ 区独立点击目标且 @click.stop(冒泡序
    下本就先于 pill 处理器执行,.stop 属防御纵深)。active 高亮仅当 activeId
    命中且该窗 open。零窗不渲染空条。文案键 win_* 已随 P2.5 落词表。
    P2.5 补齐:①尾「+」钮 emit create——父层(工作台)接 desk 建窗;栏本体
    v-if 非空,零窗场景由头部/空态新建入口覆盖(栏自隐,不留孤儿按钮)。②✕ 键
    盘可达(P2.3 评审遗留):span role=button 升 tabindex=0,Enter/Space 触发
    同一 closeWindow——保持 span 不嵌 button(pill 是 button,嵌套非法)。
    定位:absolute 贴底(inset-x-0 bottom-0)——由 P2.4 工作台的 relative 容器
    锚定,h-10 横向溢出滚动(overflow-x-auto)。
  -->
  <div
    v-if="pills.length"
    data-testid="terminal-taskbar"
    class="absolute inset-x-0 bottom-0 z-10 flex h-10 shrink-0 items-center gap-1.5 overflow-x-auto border-t border-slate-200 bg-white/95 px-2 dark:border-slate-700 dark:bg-slate-900/95"
  >
    <button
      v-for="w in pills"
      :key="w.id"
      :data-testid="`taskbar-item-${w.id}`"
      type="button"
      class="flex h-7 shrink-0 select-none items-center gap-1.5 rounded-md border px-2 text-xs transition-colors"
      :class="pillClass(w)"
      :title="w.status === 'minimized' ? t('win_restore') : w.title"
      @click="onPillClick(w)"
    >
      <!-- 状态点:open=绿 / minimized=琥珀 -->
      <span
        :data-testid="`taskbar-dot-${w.id}`"
        class="inline-block size-1.5 shrink-0 rounded-full"
        :class="w.status === 'minimized' ? 'bg-amber-400' : 'bg-emerald-500'"
      ></span>
      <span :data-testid="`taskbar-title-${w.id}`" class="max-w-[160px] truncate">{{ w.title }}</span>
      <!-- ✕ 独立点击目标:span 而非 button(button 嵌 button 非法),@click.stop 防牵连 pill 语义;
           tabindex=0 + Enter/Space(P2.3 评审遗留的键盘可达补齐) -->
      <span
        :data-testid="`taskbar-close-${w.id}`"
        role="button"
        tabindex="0"
        class="inline-flex size-4 shrink-0 items-center justify-center rounded text-[10px] leading-none text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-500/10 dark:hover:text-red-400"
        :title="t('win_close')"
        @click.stop="closeWindow(w.id)"
        @keydown.enter.prevent="closeWindow(w.id)"
        @keydown.space.prevent="closeWindow(w.id)"
      >✕</span>
    </button>
    <!-- 尾「+」新建钮(P2.5 补齐):emit create 交父层编排;随非空栏渲染 -->
    <button
      data-testid="taskbar-create"
      type="button"
      class="flex h-7 shrink-0 select-none items-center rounded-md border border-dashed border-slate-300 px-2 text-xs text-slate-500 transition-colors hover:bg-slate-100 dark:border-slate-600 dark:text-slate-400 dark:hover:bg-slate-800"
      :title="t('win_new')"
      @click="emit('create')"
    >+</button>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';
import { useTerminalWindows } from '../../composables/useTerminalWindows';

const props = defineProps({
  // 当前活动(置顶)窗 id:仅当命中且该窗 open 才高亮;空串=无活动窗
  activeId: { type: String, default: '' },
});

// 尾「+」新建钮上抛(P2.5):任务栏只报意图,建窗编排归父层(工作台)——
// 与本组件「注册表常驻操作面、不碰桌面态」的分工一致。
const emit = defineEmits(['create']);

const { t } = useI18n();
const { openWindows, minimizedWindows, restoreWindow, focusWindow, closeWindow } =
  useTerminalWindows();

// 合并序(拍板见头注):open+minimized 按 zIndex 升序 = 最近交互序。
// Array.prototype.sort 稳定,同 z(持久化恢复的 minimized 全为 0)保持表内原序。
const pills = computed(() => {
  const merged = [...openWindows.value, ...minimizedWindows.value];
  return merged.sort((a, b) => a.zIndex - b.zIndex);
});

function onPillClick(w) {
  if (w.status === 'minimized') restoreWindow(w.id);
  else focusWindow(w.id);
}

function pillClass(w) {
  if (w.id === props.activeId && w.status === 'open') {
    return 'border-primary bg-primary/10 text-primary';
  }
  if (w.status === 'minimized') {
    return 'border-transparent bg-slate-50 text-slate-500 hover:bg-slate-100 dark:bg-slate-900 dark:text-slate-400 dark:hover:bg-slate-800';
  }
  return 'border-slate-300 bg-slate-100 text-slate-700 hover:bg-slate-200 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-200 dark:hover:bg-slate-700';
}
</script>
