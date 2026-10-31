import { expect, test } from '@playwright/test';
import { openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// Task 10：审批等待期硬拦发送（对齐手机端 spec §C 防顶翻——审批等待期新
// 消息/插话都会顶掉待审批、把会话打翻成 failed，所有 provider 一律禁发）。
// 用例①硬拦：审批卡挂活跃会话 → activeApprovals 非空 → sendDisabled=true
// （textarea 与发送键同源 disabled——Enter 路径走 textarea keydown，禁用态
// 收不到键事件，与按钮同守卫）+ 提示条；ai.done 清卡后恢复。
// 用例②壳路由不拦：审批帧三键命中设备壳（session_id=source_session_id）时
// 卡挂壳不挂活跃会话——与 activeApprovals「仅活跃会话自身审批」口径一致，
// 本地新会话输入不受他壳审批影响。

const NOW_ISO = new Date().toISOString();
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '详情用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '详情助手回复', timestamp: NOW_ISO, index: 2 },
];

// sessions：①硬拦走纯本地会话（空清单，先发一句建立 socket + aiSessionId）；
// ②壳路由需要 source_session_id 可命中的 agentcore 壳。aiMessageResponder 维持
// 默认 'ai.done'——①的第一句需要终态复位 sending，后续 ai.done 由用例注入。
async function openChat(page, { sessions = [] } = {}) {
  await openChatBase(page, {
    sessions,
    newSession: false,
    detail: { transcript: DETAIL_TRANSCRIPT },
  });
}

test('硬拦：审批卡挂活跃会话 → 输入禁用+提示条，Enter 不发送，ai.done 清卡后恢复', async ({ page }) => {
  await openChat(page);
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 新建本地会话
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('第一句');
  await page.keyboard.press('Enter');
  // 默认桩 ai.message 即回 ai.done：sending 复位、输入恢复可用；create 帧的
  // session_id 即 conv.aiSessionId（后续审批帧按它直连命中）
  await expect(input).toBeEnabled();
  const sid = await page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.session.create').session_id
  );
  // 先备好第二句（焦点在 textarea 上）再让审批卡到来——随后禁用态必须连
  // 已聚焦输入框的 Enter 一起拦住（textarea disabled 收不到 keydown）
  await input.fill('第二句');
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: sid, approval_id: 'ap-blk-1',
    kind: 'command', provider: 'agentcore', command: 'rm -rf /tmp/blk-x',
  });
  // 硬拦生效：输入禁用 + 提示条可见（双语文案，无头 Chrome=en-US）
  await expect(input).toBeDisabled();
  const hint = page.getByTestId('chat-approval-block-hint');
  await expect(hint).toBeVisible();
  await expect(hint).toContainText(/有待审批请求，请先处理后再发送|Approval required before sending/);
  // Enter 路径同拦：禁用态下敲 Enter 绝不产生第二条 ai.message
  await page.keyboard.press('Enter');
  await page.waitForTimeout(300);
  const messageCount = await page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.message').length
  );
  expect(messageCount).toBe(1);
  // 注入 ai.done 清卡（clearApprovalsForSession 同口径）→ 输入恢复、提示条消失
  await pushFrame(page, { type: 'ai.done', session_id: sid });
  await expect(input).toBeEnabled();
  await expect(page.getByTestId('chat-approval-block-hint')).toHaveCount(0);
});

test('壳路由卡不拦：审批帧命中设备壳（session_id 三键）→ 活跃本地会话输入保持可用', async ({ page }) => {
  const sessions = [
    { id: 'conv-blk-1', provider: 'agentcore', title: '手机发起的会话', status: 'running', origin: 'phone', source_session_id: 'managed-blk', project_path: '/home/alice/proj-blk', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('手机发起的会话').click(); // 打开 agentcore 壳（惰性建立 AI socket）
  await expect(page.getByText('详情用户消息')).toBeVisible();
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 活跃=本地新会话
  // 帧的 session_id 走 source_session_id 三键命中壳（②路径），卡不挂活跃会话
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: 'managed-blk', approval_id: 'ap-blk-2',
    kind: 'command', provider: 'agentcore', command: 'rm -rf /tmp/blk-y',
  });
  // 活跃会话自身无卡：输入保持可用、无提示条（仅拦活跃会话自身审批）
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await expect(input).toBeEnabled();
  await expect(page.getByTestId('chat-approval-block-hint')).toHaveCount(0);
  // 反证帧已处理且确实路由到了壳：切回壳可见审批卡
  await page.getByText('手机发起的会话').click();
  await expect(page.getByText('rm -rf /tmp/blk-y')).toBeVisible();
});
