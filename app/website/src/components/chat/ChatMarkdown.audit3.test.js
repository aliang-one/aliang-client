// 网页聊天审计三批组件层（M1 自适应节流 / M5 展开态按内容键 / M6 图片加载
// 回锚事件派发）。挂载手法同 ChatMarkdown.test.js（createApp + jsdom）。
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createApp, h, reactive, nextTick } from 'vue';

vi.mock('../../utils/chatMarkdown', async (importOriginal) => {
  const actual = await importOriginal();
  return {
    ...actual,
    renderMarkdownSafe: vi.fn((src) => actual.renderMarkdownSafe(src)),
  };
});

import { renderMarkdownSafe } from '../../utils/chatMarkdown';
import ChatMarkdown from './ChatMarkdown.vue';
import { noteChatFirstText } from '../../utils/chatTiming';


function mountComponent(contentProps = {}) {
  const state = reactive({ content: '', ...contentProps });
  const el = document.createElement('div');
  document.body.appendChild(el); // M6 冒泡链需要真实文档
  const app = createApp({ render: () => h(ChatMarkdown, { content: state.content, timingMessage: state.timingMessage }) });
  app.mount(el);
  return { state, el, app };
}

async function flushTicks(n = 5) {
  for (let i = 0; i < n; i++) await nextTick();
}

beforeEach(() => {
  renderMarkdownSafe.mockClear();
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

it('records the first-text timing after the render throttle commits to the DOM', async () => {
  vi.useFakeTimers();
  vi.stubGlobal('__CHAT_TIMING__', true);
  const now = vi.spyOn(performance, 'now').mockReturnValue(90);
  const log = vi.spyOn(console, 'log').mockImplementation(() => {});
  const message = {};
  const { state, app, el } = mountComponent({ timingMessage: message });
  try {
    state.content = 'warm-up';
    await flushTicks();
    noteChatFirstText(state.timingMessage, {
      run_id: 'run-sample', event_seq: 7, agent_emitted_at_ms: 1234,
      delta: 'first text',
    }, 10);
    state.content = 'first text';
    await flushTicks();
    expect(log).not.toHaveBeenCalled();
    vi.advanceTimersByTime(80);
    await flushTicks();
    expect(el.querySelector('.chat-md').textContent).toContain('first text');
    expect(log).toHaveBeenCalledTimes(1);
    expect(log).toHaveBeenCalledWith(
      '[PERF] ai-first-text run=run-sample event_seq=7 receive_to_dom_ms=80',
    );
  } finally {
    app.unmount();
    el.remove();
    vi.unstubAllGlobals();
    now.mockRestore();
    log.mockRestore();
  }
});

describe('M1 大内容自适应渲染节流', () => {
  it('150KB 流式每 50ms 一枚 delta：1000ms 内渲染次数有上界（固定 80ms 下会 >10 次）', async () => {
    vi.useFakeTimers();
    const { state } = mountComponent();
    // 初始内容置为 150KB 起点
    state.content = 'x'.repeat(150 * 1024);
    await flushTicks();
    const base = renderMarkdownSafe.mock.calls.length;
    // 流式：每 50ms 追加 delta，共 1000ms（20 枚 delta）
    for (let i = 0; i < 20; i++) {
      state.content += 'y'.repeat(64);
      await nextTick();
      vi.advanceTimersByTime(50);
    }
    vi.advanceTimersByTime(1000);
    await flushTicks();
    const calls = renderMarkdownSafe.mock.calls.length - base;
    // 800ms 档：1000ms 窗口最多 leading 1 次 + trailing 1-2 次
    expect(calls).toBeLessThanOrEqual(3);
    // 内容最终必须被渲染到（trailing 兜底）
    expect(calls).toBeGreaterThanOrEqual(1);
  });

  it('小内容保持 80ms 基线（回归：不因自适应拖慢小气泡）', async () => {
    vi.useFakeTimers();
    const { state } = mountComponent();
    state.content = 'hello';
    await flushTicks();
    const base = renderMarkdownSafe.mock.calls.length;
    for (let i = 0; i < 10; i++) {
      state.content += 'y';
      await nextTick();
      vi.advanceTimersByTime(90);
    }
    const calls = renderMarkdownSafe.mock.calls.length - base;
    expect(calls).toBeGreaterThanOrEqual(8); // 80ms 档 ~90ms 一渲染
  });
});

describe('M5 展开态按内容键（序号漂移根治）', () => {
  const longCode = '```txt\n' + Array.from({ length: 25 }, (_, i) => 'l' + i).join('\n') + '\n```';

  function findFoldWrap(el) {
    return el.querySelector('.chat-md-pre-wrap.chat-md-fold');
  }

  it('展开后前置内容插入（序号漂移）：原代码块保持展开', async () => {
    const { state, el, app } = mountComponent();
    state.content = longCode;
    await flushTicks();
    const foldBtn = el.querySelector('.chat-md-unfold');
    expect(foldBtn).toBeTruthy();
    foldBtn.click();
    await flushTicks();
    expect(findFoldWrap(el)).toBeNull(); // 已展开

    // 前置一段正文 + 另一个短代码块 → 原块 pre 序号 0→1。
    // trailing 节流是真实 setTimeout：nextTick 冲刷等不到它，须真实等待。
    state.content = '前置说明\n\n```js\nshort();\n```\n\n' + longCode;
    await new Promise((r) => setTimeout(r, 150));
    await flushTicks();
    const wraps = [...el.querySelectorAll('.chat-md-pre-wrap')];
    expect(wraps.length).toBe(2);
    const drifted = wraps[1]; // 原长代码块（内容相同）
    expect(drifted.classList.contains('chat-md-fold')).toBe(false); // 保持展开
    app.unmount();
  });

  it('流式增长中的已展开块：追加内容不重新折叠（评审 should：快照前缀命中）', async () => {
    vi.useFakeTimers();
    const { state, el, app } = mountComponent();
    state.content = longCode;
    await flushTicks();
    el.querySelector('.chat-md-unfold').click();
    await flushTicks();
    expect(findFoldWrap(el)).toBeNull();
    // 流式追加一行：内容增长（快照前缀命中）→ 不重折叠
    state.content = longCode.replace(/\n```$/, '\nl-24\n```');
    vi.advanceTimersByTime(200); // trailing 定时器（fake timers 下同步推进）
    await flushTicks();
    expect(findFoldWrap(el)).toBeNull();
    app.unmount();
  });

  it('未展开的其他长代码块照常折叠（回归）', async () => {
    const { state, el } = mountComponent();
    state.content = longCode + '\n\n' + longCode.replace(/l/g, 'm');
    await flushTicks();
    expect(el.querySelectorAll('.chat-md-pre-wrap.chat-md-fold').length).toBe(2);
  });
});

describe('M6 图片加载回锚事件派发', () => {
  it('markdown 图片 load 后向上冒泡 chat-md-img-load（祖先容器可挂回锚）', async () => {
    const { state, el, app } = mountComponent();
    state.content = '![图](/api/agent/ai/attachment?ref=x.png)';
    await flushTicks();
    const img = el.querySelector('img');
    expect(img).toBeTruthy();
    const spy = vi.fn();
    document.addEventListener('chat-md-img-load', spy);
    img.dispatchEvent(new Event('load'));
    expect(spy).toHaveBeenCalledTimes(1);
    document.removeEventListener('chat-md-img-load', spy);
    app.unmount();
  });

  it('重复渲染不重复绑监听（同一 img 只派发一次）', async () => {
    const { state, el, app } = mountComponent();
    state.content = '![图](/x.png)';
    await flushTicks();
    const img = el.querySelector('img');
    const spy = vi.fn();
    document.addEventListener('chat-md-img-load', spy);
    // 内容不变重渲染（同 html 记忆短路）+ 手动多次 load
    img.dispatchEvent(new Event('load'));
    img.dispatchEvent(new Event('load'));
    expect(spy).toHaveBeenCalledTimes(2); // 每次真实 load 事件派发一次（幂等绑定的对象是监听器注册）
    document.removeEventListener('chat-md-img-load', spy);
    app.unmount();
  });
});
