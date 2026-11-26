// 会话信息抽屉组件测试（spec 2026-11-26-chat-info-drawer-design）：滑出范式
// （常驻挂载+translate 切换）、五区块渲染门（有数据画表/无数据显示空态/失败
// 区块仅错误态渲染）、行点击上抛 open-subagent-panel。直接 mount 组件，事件
// 经 onXxx 捕获，文案断言走 en（jsdom navigator.language 默认 en-US）。
import { describe, it, expect } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import ChatInfoDrawer from './ChatInfoDrawer.vue';

const SUBAGENTS = [
  { taskId: 'ta_1', name: 'Explore', description: null, phase: 'progress', currentStep: '搜索文件', lastTool: null, toolUses: 3, totalTokens: 1234, durationMs: 5200, summary: null },
  { taskId: 'ta_2', name: 'Reviewer', description: '审查改动', phase: 'completed', currentStep: null, lastTool: null, toolUses: 1, totalTokens: 99999, durationMs: 65000, summary: null },
];

const RUNNING_DIAG = {
  turns: 4, contextTokens: 51234, contextWindow: 200000,
  dupStreak: 1, errStreak: 0, truncStreak: 2, elapsedMs: 61320,
  stopReason: 'end_turn', terminal: false, reason: null, status: null, error: null,
  filesTouchedCount: 3, gitChangedCount: 1,
};

async function mountDrawer(props) {
  const events = [];
  const el = document.createElement('div');
  const app = createApp({
    render: () => h(ChatInfoDrawer, {
      open: false, subagents: [], diagnostics: null, running: false,
      ...props,
      onClose: () => events.push('close'),
      onOpenSubagentPanel: () => events.push('panel'),
    }),
  });
  app.mount(el);
  await nextTick();
  return { el, events, app };
}

describe('ChatInfoDrawer 滑出范式', () => {
  it('关闭态：面板常驻挂载但位移屏外（translate-x-full+aria-hidden），无遮罩', async () => {
    const { el } = await mountDrawer({ open: false });
    const aside = el.querySelector('[data-testid="chat-info-drawer"]');
    expect(aside).toBeTruthy(); // 常驻挂载（滑出动画的前提）
    expect(aside.className).toContain('translate-x-full');
    expect(aside.className).toContain('transition-transform');
    expect(aside.getAttribute('aria-hidden')).toBe('true');
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeNull();
  });

  it('打开态：translate-x-0 + 遮罩出现；点关闭按钮/遮罩 emit close', async () => {
    const { el, events } = await mountDrawer({ open: true });
    const aside = el.querySelector('[data-testid="chat-info-drawer"]');
    expect(aside.className).toContain('translate-x-0');
    expect(aside.className).not.toContain('translate-x-full');
    expect(aside.getAttribute('aria-hidden')).toBe('false');
    expect(el.querySelector('[data-testid="chat-info-drawer-backdrop"]')).toBeTruthy();
    el.querySelector('[data-testid="chat-info-drawer-backdrop"]').click();
    el.querySelector('[data-testid="chat-info-drawer-close"]').click();
    await nextTick();
    expect(events).toEqual(['close', 'close']);
  });
});

describe('ChatInfoDrawer 五区块渲染门', () => {
  it('子agent行渲染（图标+名称+步骤+耗时/tokens），行点击与「查看详情」都上抛 open-subagent-panel', async () => {
    const { el, events } = await mountDrawer({ open: true, subagents: SUBAGENTS });
    const rows = el.querySelectorAll('[data-testid="chat-info-drawer-subagent-item"]');
    expect(rows.length).toBe(2);
    expect(el.textContent).toContain('Explore');
    expect(el.textContent).toContain('搜索文件');
    expect(el.textContent).toContain('5.2s');
    expect(el.textContent).toContain('1m5s'); // 终态条目 durationMs 折算
    rows[0].click();
    el.querySelector('[data-testid="chat-info-drawer-subagent-detail"]').click();
    await nextTick();
    expect(events).toEqual(['panel', 'panel']);
  });

  it('无子agent活动时空态文案、无「查看详情」按钮', async () => {
    const { el } = await mountDrawer({ open: true, subagents: [] });
    expect(el.querySelector('[data-testid="chat-info-drawer-subagent-item"]')).toBeNull();
    expect(el.querySelector('[data-testid="chat-info-drawer-subagent-detail"]')).toBeNull();
    expect(el.textContent).toContain('No subagent activity this run');
  });

  it('上下文/运行状态/守卫三区块：有诊断快照时按数据画表', async () => {
    const { el } = await mountDrawer({ open: true, diagnostics: RUNNING_DIAG });
    expect(el.querySelector('[data-testid="chat-info-drawer-context"]')).toBeTruthy();
    expect(el.textContent).toContain('51,234');
    expect(el.textContent).toContain('/ 200,000');
    expect(el.textContent).toContain('148,766'); // 剩余可用
    expect(el.textContent).toContain('1m1s'); // elapsed 61320ms 折算
    expect(el.textContent).toContain('end_turn');
    expect(el.querySelector('[data-testid="chat-info-drawer-guards"]')).toBeTruthy();
    expect(el.textContent).toContain('1 / 3');
    expect(el.textContent).toContain('0 / 5');
    expect(el.textContent).toContain('2 / 2');
    expect(el.querySelector('[data-testid="chat-info-drawer-error"]')).toBeNull(); // 运行中无失败区块
  });

  it('无诊断快照：空态文案，不画守卫表', async () => {
    const { el } = await mountDrawer({ open: true, diagnostics: null });
    expect(el.textContent).toContain('No usage data yet');
    expect(el.textContent).toContain('No run data yet');
    expect(el.querySelector('[data-testid="chat-info-drawer-guards"]')).toBeNull();
    expect(el.querySelector('[data-testid="chat-info-drawer-error"]')).toBeNull();
  });

  it('失败态（journal end 终态）：失败区块渲染真因+错误文案', async () => {
    const { el } = await mountDrawer({
      open: true,
      diagnostics: { ...RUNNING_DIAG, terminal: true, status: 'error', reason: 'err_streak_overflow', error: '上游连续失败' },
    });
    const err = el.querySelector('[data-testid="chat-info-drawer-error"]');
    expect(err).toBeTruthy();
    expect(el.textContent).toContain('err_streak_overflow');
    expect(el.textContent).toContain('上游连续失败');
  });

  it('成功终态（status=success 无 reason/error）不渲染失败区块', async () => {
    const { el } = await mountDrawer({
      open: true,
      diagnostics: { ...RUNNING_DIAG, terminal: true, status: 'success' },
    });
    expect(el.querySelector('[data-testid="chat-info-drawer-error"]')).toBeNull();
  });
});

describe('ChatInfoDrawer running 徽标', () => {
  it('会话运行中时头部显示 running 徽标（en 文案 chat_statusRunning）', async () => {
    const { el } = await mountDrawer({ open: true, running: true });
    expect(el.textContent).toContain('In conversation');
  });
});
