import { expect, test } from '@playwright/test';

// mode=offline：socket 打开后立即由服务端语义 close(1011,'device agent offline')
// mode=idle：create 正常回执，但 ai.message 起的一切静默 → 看门狗（短超时注入）
const WS_STUB = (mode) => `
  window.__aiFrames = [];
  window.__mode = ${JSON.stringify(mode)};
  window.EventSource = class { constructor() { setTimeout(() => this.onopen?.(), 0); } close() {} };
  class FakeWebSocket {
    static OPEN = 1; static CONNECTING = 0; static CLOSING = 2; static CLOSED = 3;
    constructor(url) {
      this.url = url; this.readyState = 0;
      setTimeout(() => { this.readyState = 1; this.onopen?.(); }, 0);
    }
    send(data) {
      const frame = JSON.parse(data);
      window.__aiFrames.push(frame);
      // offline：不回任何回执，立即由服务端语义 close(1011)——必须死在 create
      // 回执之前，否则确认门放行 ai.message、「不回退」断言必失败。
      if (window.__mode === 'offline') {
        setTimeout(() => this.onclose?.({ code: 1011, reason: 'device agent offline' }), 0);
        return;
      }
      // idle：create 正常回执（否则确认门 5s 超时走早退分支、看门狗根本没机会
      // 武装——挂死必须发生在「已武装」之后），ai.message 起的一切静默。
      if (frame.type === 'ai.session.create') {
        setTimeout(() => this.onmessage?.({ data: JSON.stringify({
          type: 'ai.session.created', session_id: frame.session_id, mode: frame.mode,
          project_path: frame.project_path || '/home/e2e', provider: frame.provider,
          model: frame.model || '', effort: frame.effort || '', state: 'idle',
        })}), 0);
        return;
      }
      if (window.__mode === 'idle') return;
      if (frame.type === 'ai.message') {
        setTimeout(() => this.onmessage?.({ data: JSON.stringify({ type: 'ai.done', session_id: frame.session_id }) }), 0);
      }
    }
    close() {}
  }
  window.WebSocket = FakeWebSocket;
`;

// 与 chat-settings.spec.js 同范式：认证 stub + 兜底 route + onboarding 跳过 +
// 进聊天 + 新建会话；wsStub 作为参数注入（本 spec 的两种故障模式各取所需）。
async function openChat(page, { wsStub }) {
  await page.addInitScript(wsStub);
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
      // 阶段 2：清单条目带 id/provider/title/status/origin（三源同列表渲染）；
      // 三条重复 project_path 保留——设置条「最近项目」按路径去重语义不破。
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { sessions: [
          { id: 'sess-unify-b', provider: 'agentcore', title: '清单会话乙', status: 'idle', origin: 'phone', project_path: '/home/alice/proj-b' },
          { id: 'claude_unify-a1', provider: 'claude', title: '清单会话甲一', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
          { id: 'claude_unify-a2', provider: 'claude', title: '清单会话甲二', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
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

test('agent 离线：close(1011) 转成可见错误，不静默', async ({ page }) => {
  await openChat(page, { wsStub: WS_STUB('offline') });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  // locale 跟 navigator.language 走（i18n/index.js），无头 Chrome=en-US → 英文
  // 文案；与 chat-settings.spec.js 的占位符同款双语文本匹配，语义不变。
  await expect(page.locator('div', { hasText: /设备 agent 不在线|Device agent is offline/ }).first()).toBeVisible();
  // 不回退本地跑：确认门未过、ai.message 永不发出
  expect(await page.evaluate(() => window.__aiFrames.some(f => f.type === 'ai.message'))).toBe(false);
});

test('挂死看门狗：无帧超时转成可见错误', async ({ page }) => {
  await page.addInitScript(() => { window.__CHAT_IDLE_TIMEOUT_MS__ = 300; }); // 必须 app 代码运行前
  await openChat(page, { wsStub: WS_STUB('idle') });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.locator('div', { hasText: /AI 长时间无响应|AI did not respond/ }).first()).toBeVisible({ timeout: 5000 });
});
