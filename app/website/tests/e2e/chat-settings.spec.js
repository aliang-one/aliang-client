import { expect, test } from '@playwright/test';
import { openChat as openChatBase } from './helpers/chatStub.js';

// 阶段 2：清单条目带 id/provider/title/status/origin（三源同列表渲染）；
// 三条重复 project_path 保留——设置条「最近项目」按路径去重语义不破。
const SESSIONS = [
  { id: 'sess-settings-b', provider: 'agentcore', title: '清单会话乙', status: 'idle', origin: 'phone', project_path: '/home/alice/proj-b' },
  { id: 'sess-settings-a1', provider: 'agentcore', title: '清单会话甲一', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
  { id: 'sess-settings-a2', provider: 'agentcore', title: '清单会话甲二', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
];

// createResponder='failCreate'：ai.session.create 的回执可经 window.__failCreate
// （用例 page.evaluate 随时翻转）变为 ai.error（模拟目录不存在），默认照常回执。
async function openChat(page) {
  await openChatBase(page, { sessions: SESSIONS, createResponder: 'failCreate' });
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
