import { expect, test } from '@playwright/test';
import { openChat as openChatBase } from './helpers/chatStub.js';

// 阶段 2：清单条目带 id/provider/title/status/origin（三源同列表渲染）；
// 三条重复 project_path 保留——设置条「最近项目」按路径去重语义不破。
const SESSIONS = [
  { id: 'sess-unify-b', provider: 'agentcore', title: '清单会话乙', status: 'idle', origin: 'phone', project_path: '/home/alice/proj-b' },
  { id: 'claude_unify-a1', provider: 'claude', title: '清单会话甲一', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
  { id: 'claude_unify-a2', provider: 'claude', title: '清单会话甲二', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
];

// 故障模式经 helper 的 mode 参数表达（原 WS_STUB(mode) 参数化语义保留）：
// mode=offline：任何 send 立即由服务端语义 close(1011,'device agent offline')，
//   不回任何回执——必须死在 create 回执之前，否则确认门放行 ai.message、
//   「不回退」断言必失败。
// mode=idle：create 正常回执（否则确认门 5s 超时走早退分支、看门狗根本没
//   机会武装——挂死必须发生在「已武装」之后），此后一切静默 → 看门狗
//   （短超时注入）。
async function openChat(page, { mode }) {
  await openChatBase(page, { sessions: SESSIONS, mode });
}

test('agent 离线：close(1011) 转成可见错误，不静默', async ({ page }) => {
  await openChat(page, { mode: 'offline' });
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
  await openChat(page, { mode: 'idle' });
  await page.getByPlaceholder(/输入消息|Type a message/).fill('你好');
  await page.keyboard.press('Enter');
  await expect(page.locator('div', { hasText: /AI 长时间无响应|AI did not respond/ }).first()).toBeVisible({ timeout: 5000 });
});
