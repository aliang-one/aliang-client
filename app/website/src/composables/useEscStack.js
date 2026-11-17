import { getCurrentInstance, onUnmounted, ref, watch } from 'vue';

// ESC「返回上一层」全局层级栈(设计:docs/superpowers/specs/2026-11-18-esc-navigation-design.md)。
// 模块级单例,与 useNavigation 同模式:每个可被 Esc 关闭/返回的东西(页面、子 tab、
// 弹窗、局部编辑态)在激活时压栈、失活时移除;全局唯一 keydown 监听取栈顶派发。
// 谁后激活谁在顶,优先级不靠约定、靠入栈顺序。
const stack = ref([]);
let installed = false; // once 守卫,防 HMR/dev 双监听

function installDispatcher() {
  if (installed || typeof window === 'undefined') return;
  installed = true;
  window.addEventListener('keydown', (e) => {
    if (e.key !== 'Escape') return;
    // IME 组合期 Esc 属输入法内部语义(候选窗消隐等),不拦截、透传给 IME
    // (保留 ChatPage recall 分支 isComposingEvent 检查的同款语义)
    if (e.isComposing || e.keyCode === 229) return;
    const top = stack.value[stack.value.length - 1];
    if (!top) return; // 栈空不拦截,交浏览器默认行为
    e.preventDefault();
    e.stopImmediatePropagation(); // 防第三方残留监听(沿用 QuickSetupModal 现有防御)
    try {
      top.handler();
    } catch (err) {
      // handler 异常不崩全局、不移除该层(下一次 Esc 仍可重试)
      console.error('[useEscStack]', top.label, err);
    }
  });
}

export function useEscStack() {
  installDispatcher();
  function push(label, handler) {
    const layer = { id: Symbol('esc-layer'), label, handler };
    stack.value.push(layer);
    return layer.id;
  }
  function remove(id) {
    const i = stack.value.findIndex((layer) => layer.id === id);
    if (i >= 0) stack.value.splice(i, 1);
  }
  return { push, remove, stack };
}

// 弹窗/局部态便捷接入:activeRef 变真压层、变假移除;返回 stop() 供手动卸载
// (组件内自动挂 onUnmounted;测试环境无组件上下文时手动调 stop)。
// handler 以激活瞬间引用为准——层激活期间需最新状态时,handler 内部读响应式 ref。
// flush 用 sync:handler 执行中改掉激活条件(如收回子 tab 后)层必须即时移除,
// 否则窗口期内下一次 Esc 会重复命中本层而非落到下层;依赖量小,同步开销可忽略。
export function useEscLayer(label, handler, activeRef) {
  const { push, remove } = useEscStack();
  let id = null;
  const stopWatch = watch(activeRef, (on) => {
    if (on && id === null) id = push(label, handler);
    else if (!on && id !== null) {
      remove(id);
      id = null;
    }
  }, { immediate: true, flush: 'sync' });
  const stop = () => {
    stopWatch();
    if (id !== null) {
      remove(id);
      id = null;
    }
  };
  if (getCurrentInstance()) onUnmounted(stop);
  return stop;
}
