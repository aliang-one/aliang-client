// toast.test.js — useToast 队列/堆叠/超时自动消失/语义色归一。
// useToast 从 Toast.vue 命名导出(模块级单例,面板多组件共用同一队列)。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { useToast } from './components/Toast.vue';

describe('useToast', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    useToast().clear(); // 单例队列在用例间隔离
  });
  afterEach(() => {
    useToast().clear();
    vi.useRealTimers();
  });

  it('show 入队一条:含自增 id、msg 与默认 success 语义', () => {
    const toast = useToast();
    toast.show('已启动');
    expect(toast.toasts.value.length).toBe(1);
    expect(toast.toasts.value[0].msg).toBe('已启动');
    expect(toast.toasts.value[0].kind).toBe('success');
    expect(toast.toasts.value[0].id).toBeGreaterThan(0);
  });

  it('队列堆叠:连发三条同屏共存且 id 唯一', () => {
    const toast = useToast();
    toast.show('一');
    toast.show('二', 'warn');
    toast.show('三');
    expect(toast.toasts.value.length).toBe(3);
    const ids = toast.toasts.value.map((t) => t.id);
    expect(new Set(ids).size).toBe(3);
    expect(toast.toasts.value[1].kind).toBe('warn');
  });

  it('超时 3s 自动消失', () => {
    const toast = useToast();
    toast.show('闪现');
    expect(toast.toasts.value.length).toBe(1);
    vi.advanceTimersByTime(2999);
    expect(toast.toasts.value.length).toBe(1);
    vi.advanceTimersByTime(1);
    expect(toast.toasts.value.length).toBe(0);
  });

  it('错峰超时:先发的先消失,后发的多留 1s', () => {
    const toast = useToast();
    toast.show('先');
    vi.advanceTimersByTime(1000);
    toast.show('后');
    vi.advanceTimersByTime(2000); // 距「先」3s
    expect(toast.toasts.value.map((t) => t.msg)).toEqual(['后']);
    vi.advanceTimersByTime(1000); // 距「后」3s
    expect(toast.toasts.value.length).toBe(0);
  });

  it('语义色归一:warn 保留,未知 kind 归 success', () => {
    const toast = useToast();
    toast.show('警告', 'warn');
    toast.show('怪kind', 'bogus');
    expect(toast.toasts.value[0].kind).toBe('warn');
    expect(toast.toasts.value[1].kind).toBe('success');
  });

  it('clear 立即清空且不再触发超时残留', () => {
    const toast = useToast();
    toast.show('甲');
    toast.show('乙');
    toast.clear();
    expect(toast.toasts.value.length).toBe(0);
    vi.advanceTimersByTime(5000);
    expect(toast.toasts.value.length).toBe(0);
  });

  it('超限挤旧:上限 4 条,第 5 条入队时最旧一条被挤出且其超时被回收', () => {
    const toast = useToast();
    toast.show('旧1');
    toast.show('旧2');
    toast.show('旧3');
    toast.show('旧4');
    toast.show('新5');
    expect(toast.toasts.value.length).toBe(4);
    expect(toast.toasts.value.map((t) => t.msg)).toEqual(['旧2', '旧3', '旧4', '新5']);
    // 被挤出的「旧1」句柄已回收:推到其原超时点不会引发异常或队列抖动,
    // 剩余 4 条按各自计划消失(2s 后旧2/旧3/旧4 还在,新5 也在)。
    vi.advanceTimersByTime(2000);
    expect(toast.toasts.value.length).toBe(4);
    vi.advanceTimersByTime(1000); // 满 3s:旧2-新5 全部到期消失
    expect(toast.toasts.value.length).toBe(0);
  });
});
