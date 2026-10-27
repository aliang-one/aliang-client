import { expect, test } from '@playwright/test';

// 注入假 WebSocket：捕获客户端发出的帧到 window.__aiFrames，并对
// ai.session.create 回 ai.session.created（可经 window.__failCreate 翻转为
// ai.error 以模拟目录不存在）。
const WS_STUB = `
  window.__aiFrames = [];
  window.__failCreate = false;
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  class FakeWebSocket {
    // 应用代码以 WebSocket.OPEN 等静态常量做就绪判定（readyState === WebSocket.OPEN），
    // stub 替换全局后若缺这些常量判定恒假、sendAI 永不发帧——必须补齐。
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
      if (frame.type === 'ai.session.create') {
        setTimeout(() => {
          if (!this.onmessage) return;
          const payload = window.__failCreate
            ? { type: 'ai.error', session_id: frame.session_id, error: 'stat /no/such/dir: no such file or directory' }
            : { type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode, project_path: frame.project_path || '/home/e2e', provider: frame.provider, model: frame.model || '', effort: frame.effort || '', state: 'idle' };
          this.onmessage({ data: JSON.stringify(payload) });
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

async function openChat(page) {
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
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { sessions: [
          { project_path: '/home/alice/proj-b' }, { project_path: '/home/alice/proj-a' }, { project_path: '/home/alice/proj-a' },
        ] } }),
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 侧栏「+」新建会话
  await expect(page.getByTestId('chat-settings-directory')).toBeVisible();
}

test('设置条渲染 + 最近项目去重 + 改模型后 create 帧带 model', async ({ page }) => {
  await openChat(page);
  // 打开目录面板：最近项目去重为 2 项
  await page.getByTestId('chat-settings-directory').click();
  await expect(page.getByText('/home/alice/proj-b')).toBeVisible();
  const projectButtons = page.locator('button', { hasText: '/home/alice/proj-' });
  await expect(projectButtons).toHaveCount(2);
  await page.getByText('/home/alice/proj-b').click();
  // 改模型为 glm-5.2
  await page.getByTestId('chat-settings-model').selectOption('glm-5.2');
  // 发消息 → 捕获的 create 帧含目录与 model
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  // 帧在 Enter 时同步入列，直接取即可
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create').length)).toBeGreaterThan(0);
  const creates = await page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create'));
  const last = creates[creates.length - 1];
  expect(last.project_path).toBe('/home/alice/proj-b');
  expect(last.model).toBe('glm-5.2');
  expect(last.effort).toBe('');
});

test('已建会话改 effort 触发第二次 create；目录锁定为只读', async ({ page }) => {
  await openChat(page);
  await page.getByPlaceholder(/输入消息|Type a message/).fill('第一句');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.some(f => f.type === 'ai.session.create'))).toBe(true);
  // 发送过 → 目录锁定（只读 span，非 button）
  await expect(page.locator('span[data-testid="chat-settings-directory"]')).toBeVisible();
  // 改 effort → 追加一次 create
  await page.getByTestId('chat-settings-effort').selectOption('high');
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create' && f.effort === 'high').length)).toBeGreaterThan(0);
});

test('create 失败内联可见且下次发送重试', async ({ page }) => {
  await openChat(page);
  await page.evaluate(() => { window.__failCreate = true; });
  // 先手输一个坏目录
  await page.getByTestId('chat-settings-directory').click();
  await page.getByPlaceholder(/agent 机器|agent machine/).fill('/no/such/dir');
  await page.getByRole('button', { name: /确定|Confirm/ }).click();
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-settings-error')).toContainText('no such file or directory');
  // 翻转 stub 标志后重发（路径本身没改，retry create 仍带 /no/such/dir——
  // 本用例断言的是重发机制与确认门，不是路径修正）：第二次 create 前不应有
  // ai.message（确认门拦住了失败那轮），且错误消失
  await page.evaluate(() => { window.__failCreate = false; });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('再来');
  await page.keyboard.press('Enter');
  await expect.poll(async () => {
    const frames = await page.evaluate(() => window.__aiFrames);
    const types = frames.map(f => f.type);
    const creates = frames.filter(f => f.type === 'ai.session.create');
    const firstCreateIdx = types.indexOf('ai.session.create');
    const retryIdx = types.lastIndexOf('ai.session.create');
    // 右边界必须钉在 retry create：重试自己的 ai.message 会在毫秒级入列，
    // 无边界的 slice 到数组尾会让断言恒假。
    const messageBeforeRetry = types.slice(firstCreateIdx, retryIdx).includes('ai.message');
    return creates.length >= 2 && creates[creates.length - 1].project_path === '/no/such/dir' && !messageBeforeRetry;
  }).toBe(true);
  await expect(page.getByTestId('chat-settings-error')).toHaveCount(0);
});
