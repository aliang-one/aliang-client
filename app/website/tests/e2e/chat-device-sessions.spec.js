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
// 先注册者优先，事后 addRoute 压不过 openChat 的通配兜底）。sessions：用例
// 专属清单（分流用例需要 running 带.managed_conversation_id、裸 id running、
// idle agentcore 等形态，不动共享默认清单）。detailPages：按调用顺序弹出的
// 详情分页 mock（rows + transcript_page），验证分页拉全（评审 must-fix）；
// detailLog：可选数组，按序记录每次详情请求的 {limit, before}。
async function openChat(page, { detail404 = false, sessions = SESSIONS, detailPages = null, detailLog = null } = {}) {
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
      if (detail404) {
        await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ code: 404, msg: 'not found' }) });
        return;
      }
      if (detailPages) {
        if (detailLog) detailLog.push({ limit: url.searchParams.get('limit'), before: url.searchParams.get('before') });
        const nextPage = detailPages.length ? detailPages.shift() : { rows: [], page: { has_more: false } };
        await route.fulfill({
          contentType: 'application/json',
          body: JSON.stringify({ code: 0, data: { id: url.searchParams.get('id'), transcript: nextPage.rows, transcript_page: nextPage.page } }),
        });
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
  // 角标数随用例清单走（与 sessionSourceBadge 同一条优先级规则的镜像实现：
  // running > origin > id 前缀）；默认双条清单维持 2。
  const badgeCount = sessions.filter((s) => {
    if (!s || typeof s !== 'object') return false;
    if (s.status === 'running') return true;
    if (s.origin === 'phone' || s.origin === 'dashboard') return true;
    return typeof s.id === 'string' && (s.id.startsWith('claude_') || s.id.startsWith('codex_'));
  }).length;
  await expect(page.getByTestId('session-source-badge')).toHaveCount(badgeCount);
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
  // i18n 后角标渲染 chat_source_* 文案（无头 Chrome=en-US → 英文），双语文本匹配
  await expect(badges).toHaveText([/Running|进行中/, /Terminal|终端/]); // running > origin > id 前缀
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
  // running agentcore 裸 id → 占位符切插话语义（评审折叠项：回退 entry.id 可插话）
  const input = page.getByPlaceholder(/输入消息|运行中|Running|Type a message/);
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
  await expect(page.getByText(/来自会话 ghost-99|From session ghost-99/)).toBeVisible();
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

// --- Task 4：续聊发送分流（steer）+ agentcore 上下文喂入 ---

test('running 设备壳（managed_conversation_id）提交 → ai.steer 目标正确，queued ack 渲染提示条', async ({ page }) => {
  const sessions = [
    { id: 'conv-mgd', provider: 'agentcore', title: '他端跑着的会话', status: 'running', origin: 'phone', managed_conversation_id: 'managed-run-9', project_path: '/p/mgd', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('他端跑着的会话').click();
  // running+可插话 → 占位符切到插话语义（chat_steerPlaceholder，双语文本匹配）
  const input = page.getByPlaceholder(/输入消息|运行中|Running|Type a message/);
  await expect(input).toHaveAttribute('placeholder', /运行中，发送将作为插话|Running — your message will be sent as a steer/);
  await input.fill('插一句话');
  await page.keyboard.press('Enter');
  // steer 帧目标 = managed_conversation_id（真正的在跑回合）
  let steer = null;
  await expect.poll(async () => {
    steer = await page.evaluate(() =>
      window.__aiFrames.find(f => f.type === 'ai.steer') || null
    );
    return steer ? steer.session_id : '';
  }).toBe('managed-run-9');
  expect(steer.content).toBe('插一句话');
  // 分流绝不走原链：零 create / 零 ai.message / 零 user 气泡（轻量条承载）
  const types = await page.evaluate(() => window.__aiFrames.map(f => f.type));
  expect(types).not.toContain('ai.session.create');
  expect(types).not.toContain('ai.message');
  await expect(page.getByTestId('chat-steer-item')).toContainText('插一句话');
  // queued ack → 状态文案（message_id 回读自帧，串起 ack 匹配链路）
  await pushFrame(page, {
    type: 'ai.steer.ack', session_id: 'managed-run-9', message_id: steer.message_id, result: 'queued',
  });
  await expect(page.getByTestId('chat-steer-status')).toHaveText(/已受理|Queued/);
});

test('running agentcore 裸 id 无 managed id：steer 回退 entry.id，不误阻断', async ({ page }) => {
  const sessions = [
    { id: 'conv-bare', provider: 'agentcore', title: '裸 id 运行', status: 'running', origin: 'phone', project_path: '/p/bare', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('裸 id 运行').click();
  // 无阻断提示：entry.id ==托管通道会话 id（订阅/续聊同源），可直连 steer
  await expect(page.getByTestId('chat-steer-blocked-hint')).toHaveCount(0);
  const input = page.getByPlaceholder(/输入消息|运行中|Running|Type a message/);
  await input.fill('裸 id 插话');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.steer' && f.session_id === 'conv-bare')
  )).toBe(true);
});

test('running claude_ 壳无 managed id：内联提示常驻、提交零帧、输入保留', async ({ page }) => {
  const sessions = [
    { id: 'claude_nobind', provider: 'claude', title: '无绑定终端运行', status: 'running', origin: '', project_path: '/p/nb', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('无绑定终端运行').click();
  await expect(page.getByTestId('chat-steer-blocked-hint')).toBeVisible();
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('试试插话');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-steer-blocked-hint')).toBeVisible();
  await expect(input).toHaveValue('试试插话'); // 不清输入（没有可去的去处）
  // 零帧（subscribe 是打开壳时的订阅，不算提交产物）
  const types = await page.evaluate(() =>
    window.__aiFrames.map(f => f.type).filter(t => t !== 'ai.session.subscribe')
  );
  expect(types).toEqual([]);
});

test('idle agentcore 壳首条续聊：create 帧带 transcript，created ack 后不再携带', async ({ page }) => {
  const sessions = [
    { id: 'conv-idle', provider: 'agentcore', title: '空闲手机会话', status: 'idle', origin: 'phone', source_session_id: 'managed-idle-1', project_path: '/home/alice/proj-phone', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  await page.getByText('空闲手机会话').click();
  await expect(page.getByText('详情用户消息')).toBeVisible();
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('接着聊');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create').length
  )).toBeGreaterThanOrEqual(1);
  const creates = await page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create')
  );
  // transcript 行形态 = remoteAgentAIHistory 消费的 {role, content}
  expect(creates[0].transcript).toEqual([
    { role: 'user', content: '详情用户消息' },
    { role: 'assistant', content: '详情助手消息' },
  ]);
  expect('resume_session_id' in creates[0]).toBe(false); // 裸 id 壳不走 resume
  await expect(input).toBeEnabled(); // done 回包落地，sending 复位
  await input.fill('再来一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create').length
  )).toBeGreaterThanOrEqual(2);
  const later = await page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.session.create')
  );
  expect('transcript' in later[later.length - 1]).toBe(false); // ack 后一次性消费
});

test('journal 超详情默认尾巴：详情分页拉全，create 帧的 transcript 为全量（评审 must-fix）', async ({ page }) => {
  // 首页 = 最新 40 行（m41..m80，模拟默认尾巴形态），has_more → before=m41
  // 续拉更早 40 行（m1..m40）；create 喂入必须 80 行全量且时间正序。
  const oldest = Array.from({ length: 40 }, (_, i) => ({
    id: `m${i + 1}`, role: i % 2 ? 'assistant' : 'user', content: `旧消息${i + 1}`, timestamp: NOW_ISO, index: i + 1,
  }));
  const newest = Array.from({ length: 40 }, (_, i) => ({
    id: `m${i + 41}`, role: i % 2 ? 'assistant' : 'user', content: `新消息${i + 41}`, timestamp: NOW_ISO, index: i + 41,
  }));
  const detailPages = [
    { rows: newest, page: { limit: 100, count: 40, has_more: true, next_before_message_id: 'm41' } },
    { rows: oldest, page: { limit: 100, count: 80, has_more: false, next_before_message_id: '' } },
  ];
  const detailLog = [];
  const sessions = [
    { id: 'conv-big', provider: 'agentcore', title: '长会话', status: 'idle', origin: 'phone', project_path: '/p/big', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions, detailPages, detailLog });
  await page.getByText('长会话').click();
  await expect(page.getByText('旧消息1', { exact: true })).toBeVisible(); // 全量渲染（含最旧行）
  // 续拉请求带 limit=100 + before=首页最旧行 id
  await expect.poll(async () => detailLog.length).toBe(2);
  expect(detailLog[0]).toEqual({ limit: '100', before: null });
  expect(detailLog[1]).toEqual({ limit: '100', before: 'm41' });
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('续一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.create')
  )).toBe(true);
  const create = await page.evaluate(() => window.__aiFrames.find(f => f.type === 'ai.session.create'));
  expect(create.transcript.length).toBe(80); // 全量而非 40 行尾巴
  expect(create.transcript[0].content).toBe('旧消息1'); // 每页新行前插 → 时间正序
  expect(create.transcript[79].content).toBe('新消息80');
});

test('快照只覆盖展示流，deviceTranscript 保持详情版（喂入即全部上下文）', async ({ page }) => {
  const sessions = [
    { id: 'conv-snap', provider: 'agentcore', title: '快照覆盖会话', status: 'idle', origin: 'phone', project_path: '/p/snap', updated_at: NOW_ISO },
  ];
  await openChat(page, { sessions });
  // 快照切片只有 1 行（有损），详情转录有 2 行
  await page.evaluate(payload => { window.__snapshot = payload; }, {
    status: 'idle',
    transcript: [{ role: 'user', text: '快照用户消息' }],
  });
  await page.getByText('快照覆盖会话').click();
  // 展示流被快照覆盖（评审折叠项 b：职责分离后的既有行为）
  await expect(page.getByText('快照用户消息')).toBeVisible();
  await expect(page.getByText('详情用户消息')).toHaveCount(0);
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('续一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.create')
  )).toBe(true);
  const create = await page.evaluate(() => window.__aiFrames.find(f => f.type === 'ai.session.create'));
  // 喂入 = 详情版本（快照切片不得顶掉 deviceTranscript）
  expect(create.transcript).toEqual([
    { role: 'user', content: '详情用户消息' },
    { role: 'assistant', content: '详情助手消息' },
  ]);
});

// --- Task 5：审批卡补齐 + notice ---

test('pending 审批带 expires_at：倒计时徽标显示约 23 小时', async ({ page }) => {
  await openChat(page);
  await page.getByText('手机发起的会话').click(); // session_id 走壳 aiSessionId 直连
  await expect(page.getByText('详情用户消息')).toBeVisible();
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: 'conv-agentcore-1', approval_id: 'ap-exp-1',
    kind: 'command', command: 'sleep 1', expires_at: new Date(Date.now() + 23 * 3600 * 1000).toISOString(),
  });
  await expect(page.getByTestId('approval-expires')).toHaveText(/约 23 小时|~23 h/);
});

test('ai.approval.cancelled：卡消失 + 提示条（含 reason）', async ({ page }) => {
  await openChat(page);
  await page.getByText('手机发起的会话').click();
  await expect(page.getByText('详情用户消息')).toBeVisible();
  // 未知 session_id → 审批族 ③ 兜底挂活跃会话（壳）
  await pushFrame(page, {
    type: 'ai.approval.request', session_id: 'ghost-555', approval_id: 'ap-cx-1',
    kind: 'command', command: 'echo cx',
  });
  await expect(page.getByText('echo cx')).toBeVisible();
  await pushFrame(page, {
    type: 'ai.approval.cancelled', session_id: 'ghost-555', approval_ids: ['ap-cx-1'], reason: 'run_cancelled',
  });
  await expect(page.getByText('echo cx')).toHaveCount(0); // 精确清卡
  // ③ 活跃兜底 → 系统条带「来自会话 X」前缀（评审折叠项；slice(0,8)='ghost-55'）
  await expect(page.getByTestId('chat-approval-cancelled-note'))
    .toHaveText(/来自会话 ghost-55: 审批已被取消（run_cancelled）|From session ghost-55: Approval was cancelled \(run_cancelled\)/);
});

test('ai.run.notice resume_missing：提示条可见，不阻断输入', async ({ page }) => {
  await openChat(page);
  await page.getByText('终端 Claude 会话').click(); // resume 壳——resume_missing 的真实来源
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('第一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.create')
  )).toBe(true);
  const sid = await page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.session.create').session_id
  );
  await pushFrame(page, {
    type: 'ai.run.notice', session_id: sid, message_id: 'm-1', code: 'resume_missing', detail: 'native session native9 not found',
  });
  await expect(page.getByTestId('chat-run-notice')).toHaveText(/原生会话不存在|Native session not found/);
  await expect(input).toBeEnabled(); // 信息性帧不锁输入
});
