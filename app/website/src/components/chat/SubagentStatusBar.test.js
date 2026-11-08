// 子agent状态入口条（Task 7, spec 2026-11-07-subagent-panel §三）：vitest 组件
// 测试。仓内无 @vue/test-utils，沿用 ChatOptionCard.test.js 的 createApp + h
// 挂载手法；文案期望值经 useI18n().t 动态取，不依赖测试环境 locale
// （ChatMarkdown.test.js 同款）。条目形状对齐 utils/subagentPanel.js 的
// makeEntry（phase 词表：started/progress 活动，completed/failed/interrupted 终态）。
import { describe, it, expect } from 'vitest';
import { createApp, h } from 'vue';
import SubagentStatusBar from './SubagentStatusBar.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

function sub(overrides = {}) {
  return {
    taskId: 'ta_1',
    name: 'Explore',
    description: '搜索',
    phase: 'progress',
    currentStep: '',
    lastTool: '',
    toolUses: 0,
    totalTokens: 0,
    durationMs: 0,
    summary: '',
    ...overrides,
  };
}

function mount(subagents) {
  const el = document.createElement('div');
  const events = [];
  const app = createApp({
    render: () => h(SubagentStatusBar, {
      subagents,
      onOpen: () => events.push('open'),
    }),
  });
  app.mount(el);
  return { el, events, app };
}

describe('SubagentStatusBar', () => {
  it('渲染：2 运行中 1 完成 → 计数与「查看详情」文案齐全，条目语义为可点按钮', () => {
    const m = mount([
      sub({ phase: 'started' }),
      sub({ phase: 'progress' }),
      sub({ phase: 'completed' }),
    ]);
    expect(m.el.textContent).toContain(t('chat_subagentEntryRunning', { n: 2 }));
    expect(m.el.textContent).toContain(t('chat_subagentEntryDone', { n: 1 }));
    expect(m.el.textContent).toContain(t('chat_subagentEntryView'));
    expect(m.el.textContent).toContain(t('chat_subagentEntry'));
    // 整条可点击：语义为 button（触控≥40px 由组件 min-h 类承载）
    const btn = m.el.querySelector('button[data-testid="chat-subagent-entry"]');
    expect(btn).toBeTruthy();
    // 运行中在场 → spinner（脉冲点）渲染
    expect(btn.querySelector('.animate-pulse')).toBeTruthy();
  });

  it('点击整条 → emit("open")', () => {
    const m = mount([sub({ phase: 'progress' })]);
    m.el.querySelector('button[data-testid="chat-subagent-entry"]').click();
    expect(m.events).toEqual(['open']);
  });

  it('全部终态 → 计数归零侧不显示运行中文案、无 spinner', () => {
    const m = mount([sub({ phase: 'completed' }), sub({ phase: 'failed' })]);
    const btn = m.el.querySelector('button[data-testid="chat-subagent-entry"]');
    expect(btn.querySelector('.animate-pulse')).toBeNull();
    expect(m.el.textContent).toContain(t('chat_subagentEntryDone', { n: 2 }));
  });

  it('空数组 → 不渲染任何内容（父层挂载守卫的双保险）', () => {
    const m = mount([]);
    expect(m.el.textContent.trim()).toBe('');
    expect(m.el.querySelector('button')).toBeNull();
  });
});
