import { expect, test } from '@playwright/test';

// Task 9：聊天页运行中停止按钮（ai.stop）。动作槽对齐手机端交替语义——
// running 且输入空 → 停止键；running 且输入非空 → 发送键（点击走既有
// steer 分流，绝不发 stop）；非 running → 发送键。stop 帧不是终态：终态由
// 被取消 run 的 done/error 既有路径收敛，本 spec 注入 ai.done 验证键位切回。

// WS 桩（照抄 chat-settings / chat-device-sessions 手法）：出站帧全量入
// window.__aiFrames；subscribe 仅在 __snapshot 非空时回快照（本 spec 保持
// null=不回包）；create 照常回执；ai.message 回 ai.run.started（而非
// ai.done）——本地 run 保持 running 不落终态，供停止键断言。
const WS_STUB = `
  window.__aiFrames = [];
  window.__snapshot = null;
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  class FakeWebSocket {
    static OPEN = 1;
    static CONNECTING = 0;
    static CLOSING = 2;
    static CLOSED = 3;
    constructor(url) {
      this.url = url;
      this.readyState = 0;
      FakeWebSocket.instances.push(this);
      setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
    }
    send(data) {
      const frame = JSON.parse(data);
      window.__aiFrames.push(frame);
      if (frame.type === 'ai.session.subscribe') {
        setTimeout(() => {
          if (!this.onmessage || !window.__snapshot) return;
          this.onmessage({ data: JSON.stringify({
            type: 'ai.session.snapshot',
            session_id: frame.session_id,
            ...window.__snapshot,
          }) });
        }, 0);
      }
      if (frame.type === 'ai.session.create') {
        setTimeout(() => {
          if (!this.onmessage) return;
          this.onmessage({ data: JSON.stringify({
            type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode,
            project_path: frame.project_path || '/home/e2e', provider: frame.provider,
            model: frame.model || '', effort: frame.effort || '', state: 'idle',
          }) });
        }, 0);
      }
      if (frame.type === 'ai.message') {
        setTimeout(() => {
          if (!this.onmessage) return;
          this.onmessage({ data: JSON.stringify({ type: 'ai.run.started', session_id: frame.session_id, message_id: frame.message_id }) });
        }, 0);
      }
    }
    close() {}
  }
  FakeWebSocket.instances = [];
  window.WebSocket = FakeWebSocket;
`;

const NOW_ISO = new Date().toISOString();
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '详情用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '详情助手消息', timestamp: NOW_ISO, index: 2 },
];

// sessions：用例专属设备清单（停止帧用例走纯本地会话=空清单；steer 并存
// 用例需要 running 带 managed_conversation_id 的壳）。
async function openChat(page, { sessions = [] } = {}) {
  await page.addInitScript(WS_STUB);
  await page.addInitScript(() => localStorage.setItem('alianggate.onboarding.v2', 'completed'));
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          code: 0,
          data: { type: 'session_snapshot', instance_id: 'e2e', revision: 1, state: 'active', user: { id: 1, username: 'alice', email: 'a@b.c' } },
        }),
      });
      return;
    }
    if (url.pathname === '/api/agent/sessions') {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: { sessions } }) });
      return;
    }
    if (url.pathname === '/api/agent/session') {
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({
        code: 0,
        data: { id: url.searchParams.get('id'), transcript: DETAIL_TRANSCRIPT, transcript_page: { limit: 40, count: DETAIL_TRANSCRIPT.length } },
      }) });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
}

// 向页面注入一帧 WS 事件（走最新一个 socket 实例的 onmessage）。socket 是
// 惰性建立的（发消息/订阅才 ensureAISocket），注入前必须确认实例存在。
async function pushFrame(page, frame) {
  await page.waitForFunction(() => window.WebSocket.instances.length > 0);
  await page.evaluate(f => {
    const ws = window.WebSocket.instances[window.WebSocket.instances.length - 1];
    ws.onmessage?.({ data: JSON.stringify(f) });
  }, frame);
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
