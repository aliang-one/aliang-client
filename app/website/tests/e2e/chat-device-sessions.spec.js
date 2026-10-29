import { expect, test } from '@playwright/test';

// 跨端会话统一阶段 2（Task 2+3 + 评审修复）：侧栏三源清单合并渲染（角标/
// 状态点/删除收敛本地草稿）+ 打开设备会话（详情转录装载、agentcore 订阅、
// 快照覆盖、切走退订、详情 404 不阻断输入）+ 审批族帧分源路由（②壳三键/
// ③活跃兜底带来源标签）+ resume 字段 created ack 后清除。

// WS 桩：ai.session.subscribe 回 window.__snapshot（用例注入；null=不回包，
// 覆盖「未订阅静默」）；create 照常回执；ai.message 回 done（复位 sending，
// 供 resume 清除用例连发两句）；socket 实例入 FakeWebSocket.instances 供
// 用例向页面注入任意帧。
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
          this.onmessage({ data: JSON.stringify({ type: 'ai.done', session_id: frame.session_id }) });
        }, 0);
      }
    }
    close() {}
  }
  FakeWebSocket.instances = [];
  window.WebSocket = FakeWebSocket;
`;

const NOW_ISO = new Date().toISOString();
const SESSIONS = [
  { id: 'conv-agentcore-1', provider: 'agentcore', title: '手机发起的会话', status: 'running', origin: 'phone', source_session_id: 'managed-xyz', project_path: '/home/alice/proj-phone', updated_at: NOW_ISO },
  { id: 'claude_native9', provider: 'claude', title: '终端 Claude 会话', status: 'idle', origin: '', project_path: '/home/alice/proj-term', updated_at: NOW_ISO },
];
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '详情用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '详情助手消息', timestamp: NOW_ISO, index: 2 },
];
const SNAPSHOT_PAYLOAD = {
  status: 'running',
  transcript: [
    { role: 'user', text: '快照用户消息' },
    { role: 'assistant', text: '快照助手回复' },
  ],
};

// detail404：详情端点回 404（必须走 openChat 的同一 route 表——Playwright
// 先注册者优先，事后 addRoute 压不过 openChat 的通配兜底）。
async function openChat(page, { detail404 = false } = {}) {
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
      await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: { sessions: SESSIONS } }) });
      return;
    }
    if (url.pathname === '/api/agent/session') {
      if (detail404) {
        await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ code: 404, msg: 'not found' }) });
        return;
      }
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { id: url.searchParams.get('id'), transcript: DETAIL_TRANSCRIPT, transcript_page: { limit: 40, count: DETAIL_TRANSCRIPT.length } } }),
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
  await expect(page.getByTestId('session-source-badge')).toHaveCount(2);
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

test('侧栏三源清单：来源角标、running 状态点、设备条目不可删除/重命名', async ({ page }) => {
  await openChat(page);
  const badges = page.getByTestId('session-source-badge');
  await expect(badges).toHaveText(['active', 'terminal']); // running > origin > id 前缀
  await expect(page.getByTestId('device-session-running-dot')).toHaveCount(1); // 仅 agentcore running 条目
  // 无本地草稿时不应有任何重命名入口（设备会话不可本端改名/删除）
  await expect(page.getByRole('button', { name: /重命名|Rename/ })).toHaveCount(0);
});

test('点开 agentcore 会话：详情转录渲染 + subscribe 帧发出 + 切走退订 + claude_ 不订阅', async ({ page }) => {
  await openChat(page);
  await page.getByText('手机发起的会话').click();
  // 详情转录渲染（复用既有气泡路径）
  await expect(page.getByText('详情用户消息')).toBeVisible();
  await expect(page.getByText('详情助手消息')).toBeVisible();
  // subscribe 帧按裸 conversation id 发出
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.subscribe' && f.session_id === 'conv-agentcore-1')
  )).toBe(true);
  // claude_ 前缀条目不订阅（无 journal 转录，详情已够）；切走 agentcore 会话
  // → unsubscribe（幂等帧，多发无害）
  await page.getByText('终端 Claude 会话').click();
  await expect(page.getByText('详情用户消息')).toBeVisible(); // claude 壳自己的详情转录
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.unsubscribe' && f.session_id === 'conv-agentcore-1')
  )).toBe(true);
  const subscribeIds = await page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.subscribe').map(f => f.session_id)
  );
  expect(subscribeIds).toEqual(['conv-agentcore-1']);
});

test('快照回包覆盖详情装载的转录（journal 权威流最新切片）', async ({ page }) => {
  await openChat(page);
  await page.evaluate(payload => { window.__snapshot = payload; }, SNAPSHOT_PAYLOAD);
  await page.getByText('手机发起的会话').click();
  // subscribe 已发出且快照到达：最终渲染 = 快照转录，详情装载被覆盖
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.subscribe' && f.session_id === 'conv-agentcore-1')
  )).toBe(true);
  await expect(page.getByText('快照用户消息')).toBeVisible();
  await expect(page.getByText('快照助手回复')).toBeVisible();
  await expect(page.getByText('详情用户消息')).toHaveCount(0);
  await expect(page.getByText('详情助手消息')).toHaveCount(0);
  // 快照 status=running → 壳状态点仍在（侧栏）
  await expect(page.getByTestId('device-session-running-dot')).toHaveCount(1);
});

test('详情 404：错误条可见，空态仍可输入，订阅照发', async ({ page }) => {
  await openChat(page, { detail404: true });
  await page.getByText('手机发起的会话').click();
  await expect(page.getByTestId('device-session-load-error')).toBeVisible();
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await expect(input).toBeEnabled();
  await input.fill('续聊草稿');
  await expect(input).toHaveValue('续聊草稿');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.subscribe' && f.session_id === 'conv-agentcore-1')
  )).toBe(true);
});

test('审批族帧②：三键命中设备壳则挂壳，不切 activeId，切回壳可见审批卡', async ({ page }) => {
  await openChat(page);
  await page.getByText('手机发起的会话').click(); // 打开 agentcore 壳
  await expect(page.getByText('详情用户消息')).toBeVisible();
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 新建本地会话并激活
  // 帧的 session_id 走 source_session_id 三键（非 aiSessionId 的 ② 路径）
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: 'managed-xyz', approval_id: 'ap-shell-1',
    kind: 'command', provider: 'agentcore', command: 'rm -rf /tmp/agent-x',
  });
  // 不切 activeId：卡挂在壳上，活跃的本地会话视图里看不到
  await expect(page.getByText('rm -rf /tmp/agent-x')).toHaveCount(0);
  // 切回壳 → 卡在壳上（审批按钮可点）
  await page.getByText('手机发起的会话').click();
  await expect(page.getByText('rm -rf /tmp/agent-x')).toBeVisible();
  await expect(page.getByRole('button', { name: /同意|Approve/ }).first()).toBeVisible();
});

test('审批族帧③：未知会话兜底挂活跃会话，卡带「来自会话」来源标签', async ({ page }) => {
  await openChat(page);
  await page.getByText('手机发起的会话').click(); // 建立 AI socket（惰性）
  await expect(page.getByText('详情用户消息')).toBeVisible();
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 活跃=本地新会话
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: 'ghost-9999999', approval_id: 'ap-ghost-1',
    kind: 'command', provider: 'claude', command: 'echo from-ghost',
  });
  // 挂活跃会话（现状 fallback）+ 来源标签（id 前 8）
  await expect(page.getByText('echo from-ghost')).toBeVisible();
  await expect(page.getByText(/来自会话 ghost-99/)).toBeVisible();
});

test('resume 字段仅首次 create：created ack 后清除，后续 create 不再携带', async ({ page }) => {
  await openChat(page);
  await page.getByText('终端 Claude 会话').click(); // 壳带 resumeSessionId='native9'
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('第一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create').length
  )).toBeGreaterThanOrEqual(1);
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.message')
  )).toBe(true); // 确认门已过
  await expect(input).toBeEnabled(); // ai.done 回包落地（入站帧不进 __aiFrames），sending 复位
  await input.fill('第二句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create').length
  )).toBeGreaterThanOrEqual(2);
  const creates = await page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create')
  );
  expect(creates[0].resume_session_id).toBe('native9'); // 首次 create 携带
  expect(creates[0].project_path).toBe('/home/alice/proj-term'); // resumeProjectPath 覆盖
  expect('resume_session_id' in creates[creates.length - 1]).toBe(false); // ack 后清除
});
