import { describe, expect, it, vi } from 'vitest';

// agentApi（连带 stores/auth 的 localStorage 副作用）整体 mock 掉——只测本文件
// 导出的 listener.triggered 消费纯函数，不碰 composable 装配/网络
// （useAliangChat.toolAsk.test.js 同范式）。
vi.mock('../services/agentApi', () => ({
  getAgentIntegrations: vi.fn(),
  getAgentSessionDetail: vi.fn(),
}));

import { handleListenerTriggeredEvent } from './useAliangChat';

// 注入面组装：t 走真插值语义（键名+{title} 占位由 i18n 落地，这里只验
// 参数透传），pushSystemNote/dispatchDetail 为 spy。
function makeCtx() {
  return {
    t: (key, params) => `${key}:${params?.title ?? ''}`,
    pushSystemNote: vi.fn(),
    dispatchDetail: vi.fn(),
  };
}

describe('handleListenerTriggeredEvent（listener.triggered 消费纯函数）', () => {
  it('完整载荷：轻通知文案经 t 插值 title，窗事件 detail 原样透传，返回 true', () => {
    const ctx = makeCtx();
    const ok = handleListenerTriggeredEvent(
      { type: 'listener.triggered', listener_id: 'L-9', title: '盯 build.log', summary: '新增一行 ERROR' },
      ctx,
    );
    expect(ok).toBe(true);
    expect(ctx.pushSystemNote).toHaveBeenCalledTimes(1);
    expect(ctx.pushSystemNote).toHaveBeenCalledWith('listener_triggeredToast:盯 build.log');
    expect(ctx.dispatchDetail).toHaveBeenCalledTimes(1);
    expect(ctx.dispatchDetail).toHaveBeenCalledWith({
      listener_id: 'L-9',
      title: '盯 build.log',
      summary: '新增一行 ERROR',
    });
  });

  it('畸形帧（缺 listener_id）静默丢弃：返回 false，两个注入面零调用', () => {
    const ctx = makeCtx();
    expect(handleListenerTriggeredEvent({ type: 'listener.triggered', title: 'x' }, ctx)).toBe(false);
    expect(handleListenerTriggeredEvent(null, ctx)).toBe(false);
    expect(handleListenerTriggeredEvent({ listener_id: 123 }, ctx)).toBe(false); // 非字符串 id 不认
    expect(ctx.pushSystemNote).not.toHaveBeenCalled();
    expect(ctx.dispatchDetail).not.toHaveBeenCalled();
  });

  it('title 缺失/空白：文案回退 listener_id（通知不至于露出空书名号）', () => {
    const ctx = makeCtx();
    handleListenerTriggeredEvent({ listener_id: 'L-7', summary: 's' }, ctx);
    expect(ctx.pushSystemNote).toHaveBeenCalledWith('listener_triggeredToast:L-7');
    expect(ctx.dispatchDetail).toHaveBeenCalledWith({ listener_id: 'L-7', title: 'L-7', summary: 's' });
    handleListenerTriggeredEvent({ listener_id: 'L-8', title: '   ', summary: '' }, ctx);
    expect(ctx.pushSystemNote).toHaveBeenLastCalledWith('listener_triggeredToast:L-8');
  });

  it('summary 非字符串归一空串（best-effort 展示帧不炸消费面）', () => {
    const ctx = makeCtx();
    handleListenerTriggeredEvent({ listener_id: 'L-1', title: 't', summary: 42 }, ctx);
    expect(ctx.dispatchDetail).toHaveBeenCalledWith({ listener_id: 'L-1', title: 't', summary: '' });
  });

  it('t 缺席退裸 title 文案；注入面缺席不炸（容忍最小 ctx）', () => {
    const pushSystemNote = vi.fn();
    handleListenerTriggeredEvent({ listener_id: 'L-2', title: '盯日志' }, { pushSystemNote });
    expect(pushSystemNote).toHaveBeenCalledWith('盯日志');
    expect(() => handleListenerTriggeredEvent({ listener_id: 'L-3', title: 'x' }, {})).not.toThrow();
    expect(() => handleListenerTriggeredEvent({ listener_id: 'L-4', title: 'x' })).not.toThrow();
  });
});
