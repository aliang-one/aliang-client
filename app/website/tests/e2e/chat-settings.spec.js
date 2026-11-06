import { expect, test } from '@playwright/test';
import { establishCreatedSession, openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// 阶段 2：清单条目带 id/provider/title/status/origin（三源同列表渲染）；
// 三条重复 project_path 保留——设置条「最近项目」按路径去重语义不破。
const SESSIONS = [
  { id: 'sess-settings-b', provider: 'agentcore', title: '清单会话乙', status: 'idle', origin: 'phone', project_path: '/home/alice/proj-b' },
  { id: 'sess-settings-a1', provider: 'agentcore', title: '清单会话甲一', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
  { id: 'sess-settings-a2', provider: 'agentcore', title: '清单会话甲二', status: 'idle', origin: '', project_path: '/home/alice/proj-a' },
];

// createResponder='failCreate'：ai.session.create 的回执可经 window.__failCreate
// （用例 page.evaluate 随时翻转）变为 ai.error（模拟目录不存在），默认照常回执。
// 额外 options（如 integrations）透传给 helper 的 installChatStub。
async function openChat(page, options = {}) {
  await openChatBase(page, { sessions: SESSIONS, createResponder: 'failCreate', ...options });
}

test('设置条渲染 + 最近项目去重 + 改模型后 create 帧带 model', async ({ page }) => {
  await openChat(page);
  // 打开目录面板：最近项目去重为 2 项
  await page.getByTestId('chat-settings-directory').click();
  await expect(page.getByText('/home/alice/proj-b')).toBeVisible();
  const projectButtons = page.locator('button', { hasText: '/home/alice/proj-' });
  await expect(projectButtons).toHaveCount(2);
  await page.getByText('/home/alice/proj-b').click();
  // 改模型为 glm-5.2(composer 工具行 chip,2026-11-05 迁移)
  await page.getByTestId('chat-composer-model').click();
  await page.getByRole('button', { name: 'glm-5.2' }).click();
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
  // 改 effort → 追加一次 create(composer chip)
  await page.getByTestId('chat-composer-effort').click();
  await page.getByRole('button', { name: 'high', exact: true }).click();
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create' && f.effort === 'high').length)).toBeGreaterThan(0);
});

// 「下轮生效」角标（Task 12）：effort/model 经 applySessionSettings 落值即挂
// conv.pendingConfig；ai.run.started 路由回该会话（新回合开始）即清。前置建会
// 话+收敛锚已迁 chatStub.establishCreatedSession（正锚版，Task 13）。

test('改 effort 落「下轮生效」角标，ai.run.started 后清除', async ({ page }) => {
  await openChat(page);
  const sid = await establishCreatedSession(page);
  await page.getByTestId('chat-composer-effort').click();
  await page.getByRole('button', { name: 'high', exact: true }).click();
  const chip = page.getByTestId('chat-effort-pending');
  await expect(chip).toBeVisible();
  await expect(chip).toHaveText(/下轮生效|Next turn/);
  // 注入该会话的 ai.run.started（新回合开始）→ 角标消失
  await pushFrame(page, { type: 'ai.run.started', session_id: sid });
  await expect(chip).toHaveCount(0);
});

test('改模型同样落「下轮生效」角标，ai.run.started 后清除', async ({ page }) => {
  await openChat(page);
  const sid = await establishCreatedSession(page);
  await page.getByTestId('chat-composer-model').click();
  await page.getByRole('button', { name: 'glm-5.2' }).click();
  const chip = page.getByTestId('chat-effort-pending');
  await expect(chip).toBeVisible();
  await expect(chip).toHaveText(/下轮生效|Next turn/);
  await pushFrame(page, { type: 'ai.run.started', session_id: sid });
  await expect(chip).toHaveCount(0);
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

// --- 「更多」输出预算 + 会话级 MCP/技能开关（master 并行线移植面） ---

test('输出预算面板：留空不下发，输入 clamp 后随 create 帧下发', async ({ page }) => {
  await openChat(page);
  // 默认（未显式设置）不携带 max_tokens/context_window 键
  await establishCreatedSession(page, '首句');
  let create = await page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create').slice(-1)[0]);
  expect('max_tokens' in create).toBe(false);
  expect('context_window' in create).toBe(false);
  // 展开面板：默认桩 integrations 为空 → MCP/技能面板空态可见（不渲染假开关）
  await page.getByTestId('chat-settings-more').click();
  await expect(page.getByTestId('chat-settings-more-panel')).toBeVisible();
  await expect(page.getByTestId('chat-settings-mcp-panel')).toContainText(/未配置|No MCP servers/);
  await expect(page.getByTestId('chat-settings-skills-panel')).toContainText(/未配置|No skills/);
  // 低于下限的输入被 clamp 到与后端同区间（1024 / 100000）。
  // commit 挂在 @change（blur/Enter 语义），fill() 只派发 input——须补 blur。
  const mt = page.getByTestId('chat-settings-max-tokens');
  await mt.fill('999');
  await mt.blur();
  await expect(mt).toHaveValue('1024');
  const cw = page.getByTestId('chat-settings-context-window');
  await cw.fill('50000');
  await cw.blur();
  await expect(cw).toHaveValue('100000');
  // 下一条消息的 create 帧携带 clamp 后的预算
  await establishCreatedSession(page, '次句');
  create = await page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create').slice(-1)[0]);
  expect(create.max_tokens).toBe(1024);
  expect(create.context_window).toBe(100000);
});

test('会话级 MCP/技能开关：取消勾选随 create 帧整体下发 disabled 列表', async ({ page }) => {
  await openChat(page, {
    integrations: { mcp_servers: [{ name: 'weather' }], skills: [{ name: 'pdf' }] },
  });
  await establishCreatedSession(page, '首句');
  await page.getByTestId('chat-settings-more').click();
  await expect(page.getByTestId('chat-mcp-weather')).toBeVisible();
  await expect(page.getByTestId('chat-skill-pdf')).toBeVisible();
  // 取消勾选 → 整份「已关闭清单」写回会话并立即 resendCreate（已建会话）
  await page.getByTestId('chat-mcp-weather').click();
  await page.getByTestId('chat-skill-pdf').click();
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.session.create'
      && Array.isArray(f.disabled_mcp_servers) && f.disabled_mcp_servers.includes('weather'))
  )).toBe(true);
  // 下一条消息的 create 帧同时携带两份 disabled 列表
  await establishCreatedSession(page, '次句');
  const create = await page.evaluate(() => window.__aiFrames.filter(f => f.type === 'ai.session.create').slice(-1)[0]);
  expect(create.disabled_mcp_servers).toEqual(['weather']);
  expect(create.disabled_skills).toEqual(['pdf']);
});

// 孤儿属性守卫:ChatPage 模板一旦被打坏(标签截断),残缺事件属性会以
// 裸文本渲染在页面上——这里廉价断言正文不含模板源码特征(2026-11-05 事故)。
test('composer 区域无孤儿模板文本(标签截断哨兵)', async ({ page }) => {
  await openChat(page);
  const body = await page.locator('body').innerText();
  expect(body).not.toContain('@recall-confirm');
  expect(body).not.toContain('@slash-apply');
  expect(body).not.toContain('@apply-settings');
  await expect(page.getByTestId('chat-approval-mode-chip')).toBeVisible();
});

test('审批模式 chip:切放过所有走确认,create 载荷带 approval_mode', async ({ page }) => {
  // bypass 切换有 window.confirm 确认——e2e 自动接受
  page.on('dialog', (d) => d.accept());
  await openChat(page);
  const sid = await establishCreatedSession(page);
  await page.getByTestId('chat-approval-mode-chip').click();
  await page.getByRole('button', { name: /放过所有|Allow all/ }).click();
  // 下轮生效角标亮起(approvalMode 挂 pendingConfig)
  const chip = page.getByTestId('chat-effort-pending');
  await expect(chip).toBeVisible();
  // 触发新回合的 create(重发),载荷带 approval_mode
  await page.getByPlaceholder(/输入消息|Type a message/).fill('试试');
  await page.keyboard.press('Enter');
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.some((f) => f.type === 'ai.session.create' && f.approval_mode === 'bypass_all'))).toBe(true);
  await pushFrame(page, { type: 'ai.run.started', session_id: sid });
  await expect(page.getByTestId('chat-effort-pending')).toHaveCount(0);
});
