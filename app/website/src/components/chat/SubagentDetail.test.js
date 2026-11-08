// 子agent单条详情（Task 8, spec 2026-11-07-subagent-panel §三）：vitest 组件
// 测试。沿用 SubagentStatusBar.test.js 的 createApp + h 挂载手法；文案经
// useI18n().t 动态取。条目形状 = reducer 条目（详情来源补 result/tools）。
import { beforeEach, describe, it, expect, vi } from 'vitest';
import { createApp, h, nextTick } from 'vue';
import SubagentDetail from './SubagentDetail.vue';
import { useI18n } from '../../i18n';

const { t } = useI18n();

function entry(overrides = {}) {
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
    tools: [
      { tool: 'Bash', summary: 'npm test', isError: false, ts: '' },
      { tool: 'Edit', summary: '写入失败', isError: true, ts: '' },
    ],
    ...overrides,
  };
}

function mount(subagent) {
  const el = document.createElement('div');
  const events = { back: 0, copied: 0 };
  const app = createApp({
    render: () => h(SubagentDetail, {
      subagent,
      onBack: () => { events.back += 1; },
    }),
  });
  app.mount(el);
  return { el, events, app };
}

describe('SubagentDetail', () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it('状态头：名称/描述/状态徽标/当前步骤/最近工具/工具次数/耗时/token 字段齐全', () => {
    const m = mount(entry());
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-name"]').textContent).toBe('Explore');
    expect(m.el.textContent).toContain('搜索改动点');
    const badge = m.el.querySelector('[data-testid="chat-subagent-detail-status"]');
    expect(badge.textContent).toContain(t('chat_subagentStatusRunning'));
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-step"]').textContent).toContain('读文件');
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-lasttool"]').textContent).toContain('Read');
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-tooluses"]').textContent).toContain('3');
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-duration"]').textContent).toContain('4.2s');
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-tokens"]').textContent).toContain('1,200');
  });

  it('failed 徽标文案与 failed 色调；completed 显示已完成', () => {
    const failed = mount(entry({ phase: 'failed', summary: '上游失联' }));
    const badge = failed.el.querySelector('[data-testid="chat-subagent-detail-status"]');
    expect(badge.textContent).toContain(t('chat_subagentStatusFailed'));
    expect(badge.className).toContain('text-red');
    const done = mount(entry({ phase: 'completed', summary: '全绿' }));
    expect(done.el.querySelector('[data-testid="chat-subagent-detail-status"]').textContent)
      .toContain(t('chat_subagentStatusCompleted'));
  });

  it('工具时间线：条目按序渲染工具名+摘要，is_error 行标红并带独立 testid', () => {
    const m = mount(entry());
    const items = m.el.querySelectorAll('[data-testid="chat-subagent-detail-tool-item"]');
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain('Bash');
    expect(items[0].textContent).toContain('npm test');
    expect(items[0].querySelector('[data-testid="chat-subagent-detail-tool-error"]')).toBeNull();
    const errRow = items[1].querySelector('[data-testid="chat-subagent-detail-tool-error"]');
    expect(errRow).toBeTruthy();
    expect(errRow.className).toContain('text-red');
    expect(items[1].textContent).toContain('写入失败');
  });

  it('tools 缺失（WS 实时条目）→ 时间线区显示占位文案', () => {
    const m = mount(entry({ tools: [] }));
    expect(m.el.textContent).toContain(t('chat_subagentNoTools'));
  });

  it('运行中条目：报告区显示占位文案（无 result/summary）', () => {
    const m = mount(entry());
    expect(m.el.textContent).toContain(t('chat_subagentReportPending'));
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-report"]')).toBeNull();
    expect(m.el.querySelector('[data-testid="chat-subagent-detail-copy"]')).toBeNull();
  });

  it('终态条目：result 优先渲染 markdown；缺 result 用 summary 兜底', () => {
    const withResult = mount(entry({ phase: 'completed', result: '## 结论\n**全绿**', summary: '摘要文本' }));
    const report = withResult.el.querySelector('[data-testid="chat-subagent-detail-report"]');
    expect(report.querySelector('h2').textContent).toBe('结论');
    expect(report.querySelector('strong').textContent).toBe('全绿');
    // result 优先，summary 不双份
    expect(withResult.el.textContent).not.toContain('摘要文本');
    const summaryOnly = mount(entry({ phase: 'completed', result: '', summary: '摘要文本' }));
    expect(summaryOnly.el.querySelector('[data-testid="chat-subagent-detail-report"]').textContent)
      .toContain('摘要文本');
  });

  it('复制按钮：写入剪贴板报告全文并给成功反馈（chat_subagentCopied）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
    const m = mount(entry({ phase: 'completed', result: '**结论**\n正文段落', summary: '摘要' }));
    const copy = m.el.querySelector('[data-testid="chat-subagent-detail-copy"]');
    copy.click();
    await Promise.resolve();
    await Promise.resolve();
    await nextTick();
    expect(writeText).toHaveBeenCalledTimes(1);
    expect(writeText.mock.calls[0][0]).toBe('**结论**\n正文段落');
    expect(copy.textContent).toContain(t('chat_subagentCopied'));
  });
});
