<!-- Toast.vue — 面板轻提示:队列堆叠 + 3s 自动消失 + 成功/警告两语义色。
     useToast() 从本文件命名导出(模块级单例):启停按钮/endpoint 单选/provider
     详情等后续组件(App 承接的 Task 8-11 交互)共用同一个队列与同一个
     <Toast/> 挂载点,接口约定 toast.show(msg, kind)。 -->
<script>
import { ref } from 'vue';

// 单条 toast 存活时长(spec §5「不要长时动画」口径下的轻提示预算)。
const TOAST_TTL_MS = 3000;
// 语义色只认两档:警告归 warn,其余一律归 success(容错,不炸未知输入)。
const KIND_WARN = 'warn';

// 模块级单例状态:跨组件共享一个队列。
const toasts = ref([]);
let seq = 0;
const timers = new Map(); // id → setTimeout 句柄(clear 时统一回收)

function dismiss(id) {
  const idx = toasts.value.findIndex((t) => t.id === id);
  if (idx !== -1) toasts.value.splice(idx, 1);
  const timer = timers.get(id);
  if (timer !== undefined) {
    clearTimeout(timer);
    timers.delete(id);
  }
}

export function useToast() {
  function show(msg, kind = 'success') {
    const id = ++seq;
    toasts.value.push({
      id,
      msg: String(msg),
      kind: kind === KIND_WARN ? KIND_WARN : 'success',
    });
    timers.set(
      id,
      setTimeout(() => dismiss(id), TOAST_TTL_MS)
    );
    return id;
  }

  // clear:立即清空队列并回收所有未触发的超时(测试隔离/面板收起时用)。
  function clear() {
    for (const timer of timers.values()) clearTimeout(timer);
    timers.clear();
    toasts.value.splice(0, toasts.value.length);
  }

  return { toasts, show, clear };
}

// 组件本体:只渲染队列,不含业务。
export default {
  name: 'PanelToast',
  setup() {
    const { toasts: items } = useToast();
    return { items };
  },
};
</script>

<template>
  <div class="toast-wrap" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="t in items" :key="t.id" class="toast" :class="`toast-${t.kind}`">
        <span class="toast-dot" aria-hidden="true"></span>
        <span class="toast-msg">{{ t.msg }}</span>
      </div>
    </TransitionGroup>
  </div>
</template>
