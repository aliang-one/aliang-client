import { expect, test } from '@playwright/test';
import { openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// Task 9：聊天页运行中停止按钮（ai.stop）。动作槽对齐手机端交替语义——
// running 且输入空 → 停止键；running 且输入非空 → 发送键（点击走既有
// steer 分流，绝不发 stop）；非 running → 发送键。stop 帧不是终态：终态由
// 被取消 run 的 done/error 既有路径收敛，本 spec 注入 ai.done 验证键位切回。

const NOW_ISO = new Date().toISOString();
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '详情用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '详情助手消息', timestamp: NOW_ISO, index: 2 },
];

// sessions：用例专属设备清单（停止帧用例走纯本地会话=空清单；steer 并存
// 用例需要 running 带 managed_conversation_id 的壳）。aiMessageResponder
// 覆写为 'ai.run.started'（默认回 ai.done）——本地 run 保持 running 不落
// 终态，供停止键断言；subscribe 维持默认「__snapshot 空=不回包」。
async function openChat(page, { sessions = [] } = {}) {
  await openChatBase(page, {
    sessions,
    newSession: false,
    aiMessageResponder: 'ai.run.started',
    detail: { transcript: DETAIL_TRANSCRIPT },
  });
}

test('停止帧：running 态停止键可见，点击发 ai.stop（目标=steer 同构 id），ai.done 后切回发送键', async ({ page }) => {
  await openChat(page);
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 新建本地会话
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('跑起来');
  await page.keyboard.press('Enter');
  // create ack → ai.message → 桩回 ai.run.started：占位气泡 running + 输入已清空
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create').length
  )).toBeGreaterThan(0);
  const sid = await page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.session.create').session_id
  );
  // 发送后输入为空 + running → 发送键位切换为停止键
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();
  await page.getByTestId('chat-stop-button').click();
  // stop 帧目标 = steerTargetId（本地会话即 aiSessionId == create 帧的 session_id）
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.stop')
  )).toBe(true);
  const stopFrame = await page.evaluate(() => window.__aiFrames.find(f => f.type === 'ai.stop'));
  expect(stopFrame.session_id).toBe(sid);
  // stop 帧不是终态：注入 ai.done 终态（既有收敛路径）→ 停止键切回发送键
  await pushFrame(page, { type: 'ai.done', session_id: sid });
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
  await expect(page.getByRole('button', { name: /^Send$|^发送$/ })).toBeVisible();
});

test('触控端 steer 并存：running 且输入非空 → 发送键可点且发 ai.steer（非 ai.stop）；清空输入 → 停止键', async ({ page }) => {
  const sessions = [
    { id: 'conv-stop-mgd', provider: 'agentcore', title: '他端跑着的会话', status: 'running', origin: 'phone', managed_conversation_id: 'managed-run-stop', project_path: '/p/stop', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('他端跑着的会话').click();
  // 打开后输入为空 → 停止键占位（发送键让位）
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();
  await expect(page.getByRole('button', { name: /^Send$|^发送$/ })).toHaveCount(0);
  const input = page.getByPlaceholder(/输入消息|运行中|Running|Type a message/);
  await input.fill('插一句话');
  // 输入非空 → 发送键（非停止键）可见可点
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
  const sendBtn = page.getByRole('button', { name: /^Send$|^发送$/ });
  await expect(sendBtn).toBeVisible();
  await expect(sendBtn).toBeEnabled();
  await sendBtn.click();
  // 点击产物是 ai.steer（目标 = managed_conversation_id），不是 ai.stop
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.steer' && f.session_id === 'managed-run-stop')
  )).toBe(true);
  const hasStop = await page.evaluate(() => window.__aiFrames.some(f => f.type === 'ai.stop'));
  expect(hasStop).toBe(false);
  // steer 提交清空输入 → 停止键回归
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();
});
