import { expect, test } from '@playwright/test';
import { openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// Task 11：steer 插话融入消息时间线（user 气泡形态，对齐手机端）。三个
// 断言组：融入位置（消息流容器内、最后一条消息之后、右侧气泡形态、恰 1 处
// 渲染来源、sent → applied 状态流转）；六态抽查（not_running / 未知 result
// 中性态）；瞬态（reload 后 steer 条消失而消息保留——steerItems 不落
// localStorage）。数据层零变化：权威流仍只有 messages。

const NOW_ISO = new Date().toISOString();
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '历史用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '历史助手消息', timestamp: NOW_ISO, index: 2 },
];
const SESSIONS = [
  { id: 'conv-steer-mgd', provider: 'agentcore', title: '他端跑着的会话', status: 'running', origin: 'phone', managed_conversation_id: 'managed-steer-1', project_path: '/p/steer', updated_at: NOW_ISO },
];

// 打开 running 带 managed_conversation_id 的设备壳（driver 同 chat-stop/
// chat-device-sessions 手法）：详情转录先就位（= 已有若干历史消息）。
async function openRunningSession(page) {
  await openChatBase(page, {
    sessions: SESSIONS,
    newSession: false,
    detail: { transcript: DETAIL_TRANSCRIPT },
  });
  await page.getByText('他端跑着的会话').click();
  await expect(page.getByText('历史助手消息')).toBeVisible(); // 历史消息已装载
}

// 经输入框真实 steer 分流提交（填输入 + Enter），回读 steer 帧供 ack 匹配。
// target：精确串（.toBe）或正则（.toMatch；expect.poll 的 toBe 不吃正则）。
async function submitSteer(page, text, target) {
  const input = page.getByPlaceholder(/输入消息|运行中|Running|Type a message/);
  await input.fill(text);
  await page.keyboard.press('Enter');
  let frame = null;
  await expect.poll(async () => {
    frame = await page.evaluate(() =>
      window.__aiFrames.filter(f => f.type === 'ai.steer').slice(-1)[0] || null
    );
    return !!frame;
  }).toBe(true);
  if (typeof target === 'string') expect(frame.session_id).toBe(target);
  else if (target) expect(frame.session_id).toMatch(target);
  return frame;
}

// 消息流容器：时间线渲染根（与消息气泡同一父容器——「融入」的定位依据）。
const FLOW_SELECTOR = '.max-w-3xl';

test('融入位置：steer 条在消息流内最后一条消息之后（右侧气泡形态，恰 1 处渲染来源），sent → applied 流转', async ({ page }) => {
  await openRunningSession(page);
  const steer = await submitSteer(page, '第一句插话', 'managed-steer-1');
  // 恰 1 处渲染来源：流外独立区块已删，双份渲染必被此断言拦截
  await expect(page.getByTestId('chat-steer-item')).toHaveCount(1);
  await expect(page.getByTestId('chat-steer-item')).toContainText('第一句插话');
  // 融入位置：与历史消息同处一个时间线容器，且位于最后一条消息之后
  const pos = await page.evaluate(({ flowSelector, lastText }) => {
    const steerEl = document.querySelector('[data-testid="chat-steer-item"]');
    const lastMsg = [...document.querySelectorAll(`${flowSelector} *`)]
      .filter(el => el.children.length === 0 && el.textContent?.trim() === lastText)
      .pop();
    if (!steerEl || !lastMsg) return null;
    const steerFlow = steerEl.closest(flowSelector);
    return {
      sameFlow: !!steerFlow && steerFlow === lastMsg.closest(flowSelector),
      afterLastMessage: !!(lastMsg.compareDocumentPosition(steerEl) & Node.DOCUMENT_POSITION_FOLLOWING),
    };
  }, { flowSelector: FLOW_SELECTOR, lastText: '历史助手消息' });
  expect(pos).toEqual({ sameFlow: true, afterLastMessage: true });
  // 右侧气泡形态：ml-auto 右对齐 + border-primary/40 描边 + rounded-2xl
  // （旧流外轻量条是 border-primary/30 + rounded-lg，此三断言即形态分界）
  const cls = await page.getByTestId('chat-steer-item').getAttribute('class');
  expect(cls).toContain('ml-auto');
  expect(cls).toContain('border-primary/40');
  expect(cls).toContain('rounded-2xl');
  // 初始态「已发送」→ 注入 ack applied →「已送达」
  await expect(page.getByTestId('chat-steer-status')).toHaveText(/已发送|^Sent$/);
  await pushFrame(page, {
    type: 'ai.steer.ack', session_id: 'managed-steer-1', message_id: steer.message_id, result: 'applied',
  });
  await expect(page.getByTestId('chat-steer-status')).toHaveText(/已送达|Delivered/);
});

test('六态抽查：ack not_running → 回合已结束；未知 result → 中性「插话状态：x」（不误标失败）', async ({ page }) => {
  await openRunningSession(page);
  const s1 = await submitSteer(page, '结束态插话', 'managed-steer-1');
  await pushFrame(page, {
    type: 'ai.steer.ack', session_id: 'managed-steer-1', message_id: s1.message_id, result: 'not_running',
  });
  await expect(page.getByTestId('chat-steer-status').last()).toHaveText(/回合已结束|Turn ended/);
  const s2 = await submitSteer(page, '未知态插话', 'managed-steer-1');
  await pushFrame(page, {
    type: 'ai.steer.ack', session_id: 'managed-steer-1', message_id: s2.message_id, result: 'weird',
  });
  await expect(page.getByTestId('chat-steer-status').last()).toHaveText(/插话状态：weird|Steer status: weird/);
});

test('瞬态：reload 后 steer 条消失、消息保留（steerItems 不落 localStorage）', async ({ page }) => {
  // 本地会话 + 'ai.run.started'（run 保持 running 不落终态）：首句走原链，
  // 第二句天然分流 steer（本地目标 = aiSessionId，前缀 local-）
  await openChatBase(page, { newSession: true, aiMessageResponder: 'ai.run.started' });
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('本地保留消息');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-stop-button')).toBeVisible(); // running 就位（steer 分流前提）
  // 消息断言限定时间线容器内（侧栏标题/顶栏标题与首句同文，避免 strict 冲突）
  await expect(page.locator(`${FLOW_SELECTOR}`).getByText('本地保留消息')).toBeVisible();
  const steer = await submitSteer(page, '本地瞬态插话', /local-/);
  expect(steer.content).toBe('本地瞬态插话');
  await expect(page.getByTestId('chat-steer-item')).toHaveCount(1);
  // reload：addInitScript 桩随页重建；重进聊天页恢复活跃本地会话
  await page.reload();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
  await expect(page.locator(`${FLOW_SELECTOR}`).getByText('本地保留消息')).toBeVisible(); // 消息仍在（localStorage）
  await expect(page.getByTestId('chat-steer-item')).toHaveCount(0); // steer 条消失（瞬态）
  await expect(page.getByTestId('chat-steer-status')).toHaveCount(0);
});
