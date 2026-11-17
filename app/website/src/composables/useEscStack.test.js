import { beforeEach, describe, expect, it, vi } from 'vitest';
import { nextTick, ref } from 'vue';
import { useEscLayer, useEscStack } from './useEscStack';

// 真实键盘事件走 window dispatch(与浏览器冒泡到 window 的行为一致)。
// cancelable 必须显式开启,否则 preventDefault 是空操作、dispatchEvent 恒返回 true。
function pressEscape(overrides = {}) {
  const event = new KeyboardEvent('keydown', { key: 'Escape', cancelable: true, ...overrides });
  const notCancelled = window.dispatchEvent(event);
  return { event, notCancelled };
}

describe('useEscStack 栈顶独占派发', () => {
  beforeEach(() => {
    const { stack } = useEscStack();
    stack.value.length = 0;
  });

  it('只有栈顶层响应 Esc,下层不动', () => {
    const { push } = useEscStack();
    const bottom = vi.fn();
    const top = vi.fn();
    push('bottom', bottom);
    push('top', top);

    pressEscape();

    expect(top).toHaveBeenCalledTimes(1);
    expect(bottom).not.toHaveBeenCalled();
  });

  it('remove 栈顶后,下一层接管', () => {
    const { push, remove } = useEscStack();
    const bottom = vi.fn();
    const top = vi.fn();
    const bottomId = push('bottom', bottom);
    const topId = push('top', top);

    remove(topId);
    pressEscape();

    expect(bottom).toHaveBeenCalledTimes(1);
    expect(top).not.toHaveBeenCalled();
    expect(bottomId).toBeDefined();
  });

  it('remove 中间层不影响其余层', () => {
    const { push, remove } = useEscStack();
    const first = vi.fn();
    const second = vi.fn();
    const third = vi.fn();
    const secondId = push('first', first);
    push('second', second);
    push('third', third);

    remove(secondId);
    pressEscape();

    expect(third).toHaveBeenCalledTimes(1);
    expect(second).not.toHaveBeenCalled();
    expect(first).not.toHaveBeenCalled();
  });

  it('栈空时 Esc 不拦截(浏览器默认行为放行)', () => {
    const { stack } = useEscStack();
    expect(stack.value.length).toBe(0);

    const { notCancelled } = pressEscape();

    expect(notCancelled).toBe(true);
  });

  it('消费时 preventDefault(dispatchEvent 返回已取消)', () => {
    const { push } = useEscStack();
    push('layer', () => {});

    const { notCancelled } = pressEscape();

    expect(notCancelled).toBe(false);
  });

  it('非 Escape 键不触发任何层也不拦截', () => {
    const { push } = useEscStack();
    const handler = vi.fn();
    push('layer', handler);

    const event = new KeyboardEvent('keydown', { key: 'Enter', cancelable: true });
    const notCancelled = window.dispatchEvent(event);

    expect(handler).not.toHaveBeenCalled();
    expect(notCancelled).toBe(true);
  });

  it('IME 组合期(isComposing)Esc 不消费、层不响应', () => {
    const { push } = useEscStack();
    const handler = vi.fn();
    push('layer', handler);

    const { notCancelled } = pressEscape({ isComposing: true });

    expect(handler).not.toHaveBeenCalled();
    expect(notCancelled).toBe(true);
  });

  it('IME 组合期(keyCode 229)Esc 不消费、层不响应', () => {
    const { push } = useEscStack();
    const handler = vi.fn();
    push('layer', handler);

    const event = new KeyboardEvent('keydown', { key: 'Escape', cancelable: true });
    // jsdom 不支持 init 传 keyCode(legacy 属性),手动定义模拟真实 IME 事件
    Object.defineProperty(event, 'keyCode', { value: 229 });
    const notCancelled = window.dispatchEvent(event);

    expect(handler).not.toHaveBeenCalled();
    expect(notCancelled).toBe(true);
  });

  it('handler 抛异常被吞:console.error 上报、层保留、后续 Esc 仍响应', () => {
    const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});
    try {
      const { push } = useEscStack();
      let calls = 0;
      push('boom', () => {
        calls += 1;
        if (calls === 1) throw new Error('handler blew up');
      });

      pressEscape();
      expect(calls).toBe(1);
      expect(errorSpy).toHaveBeenCalled();

      pressEscape();
      expect(calls).toBe(2);
    } finally {
      errorSpy.mockRestore();
    }
  });

  it('同 label 重复 push 共存,后者在顶', () => {
    const { push } = useEscStack();
    const first = vi.fn();
    const second = vi.fn();
    push('rename-edit', first);
    push('rename-edit', second);

    pressEscape();

    expect(second).toHaveBeenCalledTimes(1);
    expect(first).not.toHaveBeenCalled();
  });
});

describe('useEscLayer 生命周期', () => {
  beforeEach(() => {
    const { stack } = useEscStack();
    stack.value.length = 0;
  });

  it('activeRef 变真压层、变假移除', async () => {
    const { stack } = useEscStack();
    const active = ref(false);
    const handler = vi.fn();
    useEscLayer('test-layer', handler, active);

    expect(stack.value.length).toBe(0);

    active.value = true;
    await nextTick(); // watch 回调异步 flush,与真实组件时序一致
    expect(stack.value.length).toBe(1);

    pressEscape();
    expect(handler).toHaveBeenCalledTimes(1);

    active.value = false;
    await nextTick();
    expect(stack.value.length).toBe(0);
  });

  it('创建时已为真则立即压层(immediate)', () => {
    const { stack } = useEscStack();
    const active = ref(true);
    useEscLayer('test-layer', () => {}, active);

    expect(stack.value.length).toBe(1);
  });

  it('stop() 卸载:移除层且 watch 不再响应', async () => {
    const { stack } = useEscStack();
    const active = ref(false);
    const stop = useEscLayer('test-layer', () => {}, active);

    active.value = true;
    await nextTick();
    expect(stack.value.length).toBe(1);

    stop();
    expect(stack.value.length).toBe(0);

    active.value = false;
    active.value = true;
    await nextTick();
    expect(stack.value.length).toBe(0);
  });

  it('层激活期间不会因重复赋值叠加多层', async () => {
    const { stack } = useEscStack();
    const active = ref(false);
    useEscLayer('test-layer', () => {}, active);

    active.value = true;
    active.value = true; // watch 相同值不重复触发,防御性断言
    await nextTick();
    expect(stack.value.length).toBe(1);
  });
});
