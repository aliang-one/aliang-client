// 子agent面板（Task 8, spec 2026-11-07-subagent-panel §三）：vitest 组件测试。
// 仓内无 @vue/test-utils，沿用 SubagentStatusBar.test.js 的 createApp + h 挂载
// 手法；文案期望值经 useI18n().t 动态取，不依赖测试环境 locale。条目形状 =
// utils/subagentPanel.js reducer 输出（详情来源条目额外带 result/tools）。
import { describe, it, expect } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import SubagentPanel from './SubagentPanel.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

function sub(overrides = {}) {
  return {
    taskId: 'ta_1',
    name: 'Explore',
    description: '搜索改动点',
    phase: 'progress',
    currentStep: '读文件',
    lastTool: 'Read',
    toolUses: 3,
    totalTokens: 1200,
    durationMs: 4200,
    summary: null,
    result: null,
    startedAt: null,
    endedAt: null,
    tools: [],
    ...overrides,
  };
}

async function mount(subagents, { open = true } = {}) {
  const el = document.createElement('div');
  const events = { close: 0, select: [] };
  const app = createApp({
    render: () => h(SubagentPanel, {
      open,
      subagents,
      onClose: () => { events.close += 1; },
      onSelect: (taskId) => { events.select.push(taskId); },
    }),
  });
  app.mount(el);
  await nextTick();
  return { el, events, app };
}

describe('SubagentPanel', () => {
  it('渲染列表：每行名称/描述/耗时/token 齐全，running 行带 spinner', async () => {
    const m = await mount([
      sub(),
      sub({ taskId: 'ta_2', name: 'Reviewer', description: '审查', phase: 'completed', durationMs: 8800, totalTokens: 2400 }),
    ]);
    const panel = m.el.querySelector('[data-testid="chat-subagent-panel"]');
    expect(panel).toBeTruthy();
    const rows = m.el.querySelectorAll('[data-testid="chat-subagent-panel-item"]');
    expect(rows).toHaveLength(2);
    // 名称与描述
    expect(m.el.textContent).toContain('Explore');
    expect(m.el.textContent).toContain('搜索改动点');
    expect(m.el.textContent).toContain('Reviewer');
    // 耗时（durationMs→秒）与 token 数
    expect(m.el.textContent).toContain('4.2s');
    expect(m.el.textContent).toContain('1,200');
    // running 行 spinner / 终态行无 spinner
    expect(rows[0].querySelector('[data-testid="chat-subagent-item-spinner"]')).toBeTruthy();
    expect(rows[1].querySelector('[data-testid="chat-subagent-item-spinner"]')).toBeNull();
  });

  it('打开即自动选中首条：详情在场 + emit select(首条 taskId)', async () => {
    const m = await mount([sub(), sub({ taskId: 'ta_2', name: 'Reviewer', phase: 'completed' })]);
    expect(m.el.querySelector('[data-testid="chat-subagent-detail"]')).toBeTruthy();
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-name"]').textContent).toBe('Explore');
    expect(m.events.select).toEqual(['ta_1']);
    // 桌面双栏布局在位（列表与详情同屏）
    expect(m.el.querySelector('[data-testid="chat-subagent-panel-list"]')).toBeTruthy();
  });

  it('点击第二行 → 选中切换（emit select 新 taskId，详情字段跟随）', async () => {
    const m = await mount([sub(), sub({ taskId: 'ta_2', name: 'Reviewer', phase: 'completed', description: '审查' })]);
    const rows = m.el.querySelectorAll('[data-testid="chat-subagent-panel-item"]');
    rows[1].click();
    await nextTick();
    expect(m.events.select).toEqual(['ta_1', 'ta_2']);
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-name"]').textContent).toBe('Reviewer');
  });

  it('详情刷新换数组引用：选中条目按 taskId 保持（M1 重拉后选中不丢）', async () => {
    const el = document.createElement('div');
    const events = { select: [] };
    const subagents = [sub()];
    const app = createApp({
      render: () => h(SubagentPanel, {
        open: true,
        subagents,
        onSelect: (taskId) => { events.select.push(taskId); },
      }),
    });
    app.mount(el);
    // 模拟详情重拉：同 taskId 换新引用（含补齐 result/tools）
    subagents.length = 0;
    subagents.push(sub({ result: '## 报告', tools: [{ tool: 'Bash', summary: 'npm test', isError: false, ts: '' }] }));
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(events.select).toEqual(['ta_1']); // 没有额外再发 select
    expect(el.querySelector('[data-testid="chat-subagent-detail-name"]').textContent).toBe('Explore');
    app.unmount();
  });

  it('空数组不渲染面板根（父层挂载守卫的双保险）', async () => {
    const m = await mount([]);
    expect(m.el.querySelector('[data-testid="chat-subagent-panel"]')).toBeNull();
  });

  it('open=false 不渲染', async () => {
    const m = await mount([sub()], { open: false });
    expect(m.el.querySelector('[data-testid="chat-subagent-panel"]')).toBeNull();
  });

  it('关闭按钮/移动端遮罩 → emit close；移动端返回按钮 → 取消选中（emit select null）', async () => {
    const m = await mount([sub()]);
    m.el.querySelector('[data-testid="chat-subagent-panel-close"]').click();
    expect(m.events.close).toBe(1);
    // 遮罩点击关闭（移动全屏态）
    m.el.querySelector('[data-testid="chat-subagent-panel-backdrop"]').click();
    expect(m.events.close).toBe(2);
    // 返回列表按钮：取消选中
    m.el.querySelector('[data-testid="chat-subagent-panel-back"]').click();
    await nextTick();
    expect(m.events.select).toEqual(['ta_1', null]);
    expect(m.el.querySelector('[data-testid="chat-subagent-detail"]')).toBeNull();
  });

  it('详情条目带 result 时报告区渲染 markdown；状态徽标 i18n 文案在场', async () => {
    const m = await mount([sub({ phase: 'completed', result: '**结论**', summary: '摘要' })]);
    expect(m.el.textContent).toContain(t('chat_subagentStatusCompleted'));
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-report"] strong')).toBeTruthy();
  });
});
