<template>
  <!--
    浮窗壳(P2.2,计划 2026-11-22-web-terminal-phase23):拖拽/最小化/最大化/关闭
    的纯展示壳——props 进、events 出,不 import 窗口注册表(useTerminalWindows),
    编排归 P2.4 TerminalWorkspace。emit 契约与注册表动作 1:1:minimize→
    minimizeWindow、maximize-toggle/close→父层、focus→focusWindow、move(x,y)→
    父层写回几何。开/关显隐由父层 v-show 控制(壳自身只管几何);minimized=true
    时降为紧凑条(仅头栏可见)——内容区用 v-show 藏而非 v-if:TerminalPane/
    xterm 缓冲/WS 全程保活(spec §11「切回不闪断」,绝不 v-if 内容)。
    拖拽:pointerdown 在头栏起拖 + setPointerCapture(jsdom 无此 API,守卫降级
    为元素内事件,测试等价);move 实时上抛 clampWindowPos 钳制后的坐标。
    提示词条 win_* 由 P2.5 落词表,缺失时 t() 回落键名本身(先例:TerminalPane
    的 term_* 先引用后落词)。
  -->
  <div
    data-testid="floating-window"
    class="pointer-events-auto absolute flex flex-col overflow-hidden rounded-lg border bg-white shadow-xl dark:bg-slate-900"
    :class="active ? 'border-primary ring-1 ring-primary/40' : 'border-slate-200 dark:border-slate-700'"
    :style="winStyle"
  >
    <div
      ref="headerEl"
      data-testid="floating-window-header"
      class="flex h-9 shrink-0 cursor-move select-none items-center gap-2 border-b border-slate-200/70 bg-slate-50 px-2.5 dark:border-slate-700/70 dark:bg-slate-800"
      @pointerdown="onHeaderPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
      @dblclick="onHeaderDblClick"
    >
      <span
        class="flex-1 truncate text-xs font-medium"
        :class="active ? 'text-primary' : 'text-slate-600 dark:text-slate-300'"
        :title="title"
      >{{ title }}</span>
      <!-- 双击豁免区:钮上双击不冒泡成头栏 maximize-toggle -->
      <div class="flex items-center gap-0.5" @dblclick.stop>
        <button
          data-testid="floating-minimize"
          type="button"
          class="inline-flex size-6 items-center justify-center rounded text-xs leading-none text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-700 dark:hover:text-slate-200"
          :title="t('win_minimize')"
          @pointerdown.stop
          @click="emit('minimize')"
        >—</button>
        <button
          data-testid="floating-maximize"
          type="button"
          class="inline-flex size-6 items-center justify-center rounded text-xs leading-none text-slate-400 transition hover:bg-slate-100 hover:text-slate-600 dark:hover:bg-slate-700 dark:hover:text-slate-200"
          :title="t('win_maximize')"
          @pointerdown.stop
          @click="emit('maximize-toggle')"
        >□</button>
        <button
          data-testid="floating-close"
          type="button"
          class="inline-flex size-6 items-center justify-center rounded text-xs leading-none text-slate-400 transition hover:bg-red-50 hover:text-red-500 dark:hover:bg-red-500/10 dark:hover:text-red-400"
          :title="t('win_close')"
          @pointerdown.stop
          @click="emit('close')"
        >✕</button>
      </div>
    </div>
    <!-- 内容区:恒挂载(v-show),minimized 只藏不销毁;p-0 overflow-hidden 中性
         底——终端对比度由 bg-black 兜底,槽内容(TerminalPane)自带终底色 -->
    <div
      v-show="!minimized"
      data-testid="floating-window-body"
      class="relative min-h-0 flex-1 overflow-hidden bg-black p-0"
    >
      <slot />
    </div>
  </div>
</template>

<script setup>
import { computed, onUnmounted, ref } from 'vue';
import { useI18n } from '../../i18n';
import { clampWindowPos } from './floatingWindowGeom';

const props = defineProps({
  // 窗标题(任务栏/头栏共用文案源)
  title: { type: String, default: '' },
  // 几何全由父层持有(注册表/编排层),壳只渲染——拖拽经 move 事件回写
  x: { type: Number, default: 0 },
  y: { type: Number, default: 0 },
  width: { type: Number, default: 600 },
  height: { type: Number, default: 400 },
  zIndex: { type: Number, default: 1 },
  // 当前活动窗:边框/标题高亮
  active: { type: Boolean, default: false },
  // 紧凑条模式:仅头栏可见可拖(内容 v-show 藏,不销毁)
  minimized: { type: Boolean, default: false },
});

const emit = defineEmits(['move', 'minimize', 'maximize-toggle', 'close', 'focus']);

const { t } = useI18n();
const headerEl = ref(null);

let dragging = false;
let dragOffset = { x: 0, y: 0 }; // 指针→窗左上角固定偏移,起拖时定锚
let activePointerId = null;

const winStyle = computed(() => ({
  left: props.x + 'px',
  top: props.y + 'px',
  width: props.width + 'px',
  // minimized=紧凑条:高度收成头栏自身高(auto);内容区已 display:none
  height: props.minimized ? 'auto' : props.height + 'px',
  zIndex: props.zIndex,
}));

// 起拖:按钮区 pointerdown 已 .stop,此处再 closest 兜底一次(双保险)
function onHeaderPointerDown(e) {
  if (e.target?.closest?.('button')) return;
  dragging = true;
  dragOffset = { x: e.clientX - props.x, y: e.clientY - props.y };
  activePointerId = typeof e.pointerId === 'number' ? e.pointerId : null;
  // 指针捕获:拖出窗口/视口边界后 move/up 仍派发回头栏(jsdom 无此 API,
  // 存在性守卫静默降级——事件直接命中头栏,逻辑等价)
  if (headerEl.value?.setPointerCapture && activePointerId != null) {
    try {
      headerEl.value.setPointerCapture(activePointerId);
    } catch {
      /* 指针已消失等竞态:忽略,起拖仍成立 */
    }
  }
  emit('focus');
}

// 实时 move:钳制后坐标上抛,父层写回 x/y(父层可自行节流,不影响此侧锚定)
function onPointerMove(e) {
  if (!dragging) return;
  const pos = clampWindowPos(
    e.clientX - dragOffset.x,
    e.clientY - dragOffset.y,
    props.width,
    props.height
  );
  emit('move', pos.x, pos.y);
}

function onPointerUp() {
  if (!dragging) return;
  dragging = false;
  releaseCapture();
}

// 头栏双击=切换最大化;按钮区双击已被 .stop 豁免,closest 再兜底
function onHeaderDblClick(e) {
  if (e.target?.closest?.('button')) return;
  emit('maximize-toggle');
}

function releaseCapture() {
  const el = headerEl.value;
  if (el?.hasPointerCapture && activePointerId != null && el.hasPointerCapture(activePointerId)) {
    try {
      el.releasePointerCapture(activePointerId);
    } catch {
      /* pointerup 后浏览器已自动释放:忽略 */
    }
  }
  activePointerId = null;
}

onUnmounted(() => {
  // 拖拽中卸载(父层关窗)时释放捕获,防指针事件悬挂到已卸载元素
  dragging = false;
  releaseCapture();
});
</script>
