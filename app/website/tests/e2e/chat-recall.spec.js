import { expect, test } from '@playwright/test';
import { establishCreatedSession, openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// Task 13：聊天消息撤回重发全链（stop → journal 截断 create → 新消息，转录级
// 真撤回）。核心帧序断言全部走 window.__aiFrames（只录出站帧）。三铁律的
// 测试面：
//   ①撤回 create 绝不带 transcript（场景 A/B 逐帧断言）；
//   ②truncate_before_message_id 只出现在撤回路径（场景 A 断言全量 create 中
//     带 truncate 键的恰好 1 帧；常规路径由 chat-settings/chatSettings.test 钉死）；
//   ③重发换新 message_id（场景 A 断言新 ai.message 帧的 message_id ≠ 被撤 id）。
//
// 场景 D 门控说明：本窗口对 claudecode 有双重过滤（loadFromStorage 只恢复
// agentcore 草稿 + 清单 isAliangCodeSession 只收 agentcore），claudecode 会话
// 无法在此窗口构造——canRecall 的 provider 门控由 chatRecall.test.js 单测钉死，
// e2e 验证同一 v-if 渲染分支的另一失配条件（存量无 messageId 的旧消息）。
// 场景 F 门控说明：设备壳会话撤回整体禁用（spec §6 定案）——壳气泡的
// messageId/provider 齐备（canRecall 本会通过），门控来自渲染层的壳禁用
// （recallEnabled=!?deviceSession），与 D 的 canRecall 失配是不同分支。

// 消息列表容器（页面上唯一 max-w-3xl 容器）：所有消息文本断言统一 scope 到
// 这里——会话标题（侧栏/顶栏）会取首条消息切片，裸 getByText 会撞严格模式。
const LIST = '.max-w-3xl';
// running 会话的占位符会切到 steer 提示（chat_steerPlaceholder）——与
// chat-stop.spec.js 同款四分支，覆盖空闲/运行两种键位态。
const INPUT = /输入消息|运行中|Running|Type a message/;
const CONFIRM = /确认重发|Resend/;

// 定位 user 气泡：getByText 命中最内层文本节点所在 div，撤回键在 group 包装层
// （父级）内——locator('..') 上取一层再 scope testid，避开多气泡同名键的严格模式冲突。
function recallButtonOf(page, text) {
  const bubble = page.locator(LIST).getByText(text, { exact: true });
  return {
    bubble,
    button: bubble.locator('..').getByTestId('chat-recall-button'),
  };
}

async function openChat(page, options = {}) {
  await openChatBase(page, { newSession: true, ...options });
}

test('场景A 空闲会话撤回全链：banner+回填→确认→truncate create 无 transcript→新 id+本地截断', async ({ page }) => {
  await openChat(page);
  await establishCreatedSession(page, '第一句');
  // 第二条消息（成为撤回目标）
  await page.getByPlaceholder(INPUT).fill('第二句原文');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0); // run 已收敛
  const targetId = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.message')[1].message_id);
  expect(targetId).toBeTruthy();

  // hover 第二条 user 气泡 → 撤回键出现 → 点击进编辑态
  const { bubble, button } = recallButtonOf(page, '第二句原文');
  await bubble.hover();
  await expect(button).toBeVisible();
  await button.click();

  // banner 可见 + 输入框回填原文
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await expect(page.getByPlaceholder(INPUT)).toHaveValue('第二句原文');

  // 修改文本 → 确认重发
  await page.getByPlaceholder(INPUT).fill('第二句改后');
  await page.getByRole('button', { name: CONFIRM }).click();

  // 撤回 create 到达（唯一一帧带 truncate 键 = 只出现在撤回路径）且重发的
  // ai.message 已发出（2 条初始 + 1 条重发）——轮询消掉 ack→ai.message 竞态
  await expect.poll(async () => page.evaluate(() => {
    const msgs = window.__aiFrames.filter((f) => f.type === 'ai.message');
    const truncateCreates = window.__aiFrames
      .filter((f) => f.type === 'ai.session.create' && f.truncate_before_message_id);
    return msgs.length >= 3 && truncateCreates.length === 1;
  })).toBe(true);

  const frames = await page.evaluate(() => window.__aiFrames);
  const creates = frames.filter((f) => f.type === 'ai.session.create');
  const lastCreate = creates[creates.length - 1];
  expect(lastCreate.truncate_before_message_id).toBe(targetId);
  expect(lastCreate).not.toHaveProperty('transcript'); // 铁律：绝不喂 transcript
  expect(lastCreate).not.toHaveProperty('resume_session_id');

  // 新 ai.message：在 truncate create 之后发出，且换新 message_id
  const types = frames.map((f) => f.type);
  const lastMsgIdx = types.lastIndexOf('ai.message');
  const lastCreateIdx = types.lastIndexOf('ai.session.create');
  expect(lastMsgIdx).toBeGreaterThan(lastCreateIdx);
  const resent = frames.filter((f) => f.type === 'ai.message').slice(-1)[0];
  expect(resent.message_id).not.toBe(targetId);
  expect(resent.content).toBe('第二句改后');

  // 本地截断生效（DOM）：改后文本可见、被撤原文与其后内容消失、更早消息仍在
  await expect(page.locator(LIST).getByText('第二句改后')).toBeVisible();
  await expect(page.locator(LIST).getByText('第二句原文', { exact: true })).toHaveCount(0);
  await expect(page.locator(LIST).getByText('第一句', { exact: true })).toBeVisible();
  // 发送成功后编辑态退场
  await expect(page.getByTestId('chat-recall-banner')).toHaveCount(0);
});

test('场景B running 会话撤回：ai.stop 先于 truncate create，create 仍无 transcript', async ({ page }) => {
  await openChat(page, { aiMessageResponder: 'ai.run.started' }); // 本地 run 保持 running
  const input = page.getByPlaceholder(INPUT);
  await input.fill('跑起来别停');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-stop-button')).toBeVisible(); // running
  const first = await page.evaluate(() => window.__aiFrames.filter((f) => f.type === 'ai.message')[0]);
  const targetId = first.message_id;
  const sid = first.session_id;

  const { bubble, button } = recallButtonOf(page, '跑起来别停');
  await bubble.hover();
  await expect(button).toBeVisible();
  await button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await page.getByRole('button', { name: CONFIRM }).click();

  // stop 帧先行（终态由用例注入 ai.done 收敛——与 chat-stop 同一手法）；重发
  // ai.message 到位后再取帧，消掉 ack→ai.message 的异步竞态
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.some((f) => f.type === 'ai.stop'))).toBe(true);
  await pushFrame(page, { type: 'ai.done', session_id: sid });
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.message').length >= 2
  )).toBe(true);

  const state = await page.evaluate(() => {
    const types = window.__aiFrames.map((f) => f.type);
    const createIdx = types.lastIndexOf('ai.session.create');
    const stopIdx = types.lastIndexOf('ai.stop');
    return {
      ordered: stopIdx < createIdx,
      create: window.__aiFrames[createIdx] || null,
    };
  });
  expect(state.ordered).toBe(true);
  expect(state.create.truncate_before_message_id).toBe(targetId);
  expect(state.create).not.toHaveProperty('transcript');
  expect(state.create).not.toHaveProperty('resume_session_id');
});

test('场景C banner 取消：恢复进入前草稿，无任何新 create 帧', async ({ page }) => {
  await openChat(page);
  await establishCreatedSession(page, '第一句');
  const input = page.getByPlaceholder(INPUT);
  await input.fill('我的草稿'); // 进入编辑态前的草稿
  const { bubble, button } = recallButtonOf(page, '第一句');
  await bubble.hover();
  await button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await expect(input).toHaveValue('第一句'); // 回填原文

  const createsBefore = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.create').length);
  await page.getByTestId('chat-recall-cancel').click();
  await expect(page.getByTestId('chat-recall-banner')).toHaveCount(0);
  await expect(input).toHaveValue('我的草稿'); // 恢复进入前草稿
  const createsAfter = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.create').length);
  expect(createsAfter).toBe(createsBefore);
});

test('场景D 门控：不可撤回的 user 气泡 hover 菜单只有复制键', async ({ page }) => {
  // 存量无 messageId 的旧草稿（provider 门控见文件头说明，同一 canRecall 分支）
  await page.addInitScript(() => {
    localStorage.setItem('aliang-chat-data', JSON.stringify({
      conversations: [{
        id: 'legacy1', title: '旧会话', approvals: [], provider: 'agentcore',
        messages: [{ role: 'user', content: '老消息', ts: Date.now() }],
        createdAt: Date.now(), updatedAt: Date.now(),
      }],
      activeConversationId: 'legacy1',
    }));
  });
  await openChatBase(page, { newSession: false });
  const { bubble, button } = recallButtonOf(page, '老消息');
  await bubble.hover();
  await expect(bubble.locator('..').getByTestId('chat-copy-button')).toBeVisible(); // 只有复制
  await expect(button).toHaveCount(0); // 撤回键不渲染
});

test('场景F 设备壳会话撤回整体禁用：带 id 的转录行 hover 只有复制键（spec §6）', async ({ page }) => {
  // 根因：壳续聊 create 落新建 local-* journal，而壳气泡锚点 id 来自原
  // entry.id journal——truncate 未命中是 no-op 非报错（UI 假成功、上下文静默
  // 分叉），spec §6 定案壳撤回整体禁用。转录行带 id + provider=agentcore
  // （canRecall 两条件齐备），钉死门控来自壳禁用而非 canRecall 失配。
  const NOW_ISO = new Date().toISOString();
  const sessions = [
    { id: 'conv-shell', provider: 'agentcore', title: '手机壳会话', status: 'idle', origin: 'phone', project_path: '/p/shell', updated_at: NOW_ISO },
  ];
  await openChatBase(page, {
    sessions,
    newSession: false,
    detail: {
      transcript: [
        { id: 'shell-m1', role: 'user', content: '壳里用户消息', timestamp: NOW_ISO, index: 1 },
        { id: 'shell-m2', role: 'assistant', content: '壳里助手回复', timestamp: NOW_ISO, index: 2 },
      ],
    },
  });
  await page.getByText('手机壳会话').click(); // 打开设备壳（详情转录装载）
  await expect(page.locator(LIST).getByText('壳里用户消息')).toBeVisible();

  const { bubble, button } = recallButtonOf(page, '壳里用户消息');
  await bubble.hover();
  await expect(bubble.locator('..').getByTestId('chat-copy-button')).toBeVisible(); // 复制键在
  await expect(button).toHaveCount(0); // 撤回键不渲染（壳禁用，与场景D不同分支）
});

test('场景E stop 超时中止：只有 ai.stop、无 truncate create，编辑态不丢', async ({ page }) => {
  await page.addInitScript(() => { window.__CHAT_STOP_TIMEOUT_MS__ = 400; }); // 必须 app 代码运行前
  await openChatBase(page, { aiMessageResponder: 'ai.run.started' });
  await page.getByRole('button').filter({ hasText: 'add' }).first().click(); // 新建本地会话
  const input = page.getByPlaceholder(INPUT);
  await input.fill('跑起来别停');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-stop-button')).toBeVisible(); // running

  const { bubble, button } = recallButtonOf(page, '跑起来别停');
  await bubble.hover();
  await button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await page.getByRole('button', { name: CONFIRM }).click();

  // 桩对 ai.stop 不回任何终态帧 → 注入的短超时结算为 timeout → 中止撤回链
  await expect.poll(async () => page.evaluate(() => window.__aiFrames.some((f) => f.type === 'ai.stop'))).toBe(true);
  await page.waitForTimeout(700); // 越过 400ms 停止等待阈值
  const frames = await page.evaluate(() => window.__aiFrames);
  expect(frames.some((f) => f.type === 'ai.session.create' && f.truncate_before_message_id)).toBe(false);
  expect(frames.filter((f) => f.type === 'ai.stop').length).toBe(1);

  // 编辑态不丢：banner 与输入回填保留，错误文案落设置条
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await expect(input).toHaveValue('跑起来别停');
  await expect(page.getByTestId('chat-settings-error')).toContainText(/停止超时|Stop timed out/i);
});

test('场景G 连续两次撤回：首次成功后入口复活，第二条 truncate create 再现（C1 回归）', async ({ page }) => {
  await openChat(page);
  await establishCreatedSession(page, '第一句');
  // 第二条消息（第一次撤回目标）
  await page.getByPlaceholder(INPUT).fill('第二句原文');
  await page.keyboard.press('Enter');
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0); // run 收敛

  // 第一次撤回：第二条 user 气泡改写重发
  const first = recallButtonOf(page, '第二句原文');
  await first.bubble.hover();
  await expect(first.button).toBeVisible();
  await first.button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await page.getByPlaceholder(INPUT).fill('第二句改后');
  await page.getByRole('button', { name: CONFIRM }).click();

  await expect.poll(async () => page.evaluate(() => {
    const msgs = window.__aiFrames.filter((f) => f.type === 'ai.message');
    const truncates = window.__aiFrames
      .filter((f) => f.type === 'ai.session.create' && f.truncate_before_message_id);
    return msgs.length >= 3 && truncates.length === 1;
  })).toBe(true);
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0); // 重发回合收敛
  const resentId = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.message').slice(-1)[0].message_id);

  // 第二次撤回当前最后一条 user 气泡（首次重发产物）。C1 缺陷时 recalling 在
  // 成功路径不复位——startRecall 守卫吞掉点击、banner 永不出现，本用例在此
  // 超时失败，即「入口永久哑火」的回归锚。
  const second = recallButtonOf(page, '第二句改后');
  await second.bubble.hover();
  await expect(second.button).toBeVisible();
  await second.button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible(); // 入口未哑火
  await expect(page.getByPlaceholder(INPUT)).toHaveValue('第二句改后'); // 回填原文
  await page.getByPlaceholder(INPUT).fill('第二句再改');
  await page.getByRole('button', { name: CONFIRM }).click();

  // 第二条 truncate create 出现 = 第二次撤回链真正走通
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames
      .filter((f) => f.type === 'ai.session.create' && f.truncate_before_message_id).length
  )).toBe(2);

  const truncates = await page.evaluate(() => window.__aiFrames
    .filter((f) => f.type === 'ai.session.create' && f.truncate_before_message_id));
  expect(truncates[1].truncate_before_message_id).toBe(resentId); // 锚点=首次重发的新 id

  // 本地截断生效 + 编辑态退场
  await expect(page.locator(LIST).getByText('第二句再改')).toBeVisible();
  await expect(page.locator(LIST).getByText('第二句改后', { exact: true })).toHaveCount(0);
  await expect(page.locator(LIST).getByText('第一句', { exact: true })).toBeVisible();
  await expect(page.getByTestId('chat-recall-banner')).toHaveCount(0);
});

test('场景H 撤回编辑态 Esc：恢复草稿退出编辑态，聊天页不关、无新 create 帧（spec §A）', async ({ page }) => {
  await openChat(page);
  await establishCreatedSession(page, '第一句');
  const input = page.getByPlaceholder(INPUT);
  await input.fill('我的草稿'); // 进入编辑态前的草稿
  const { bubble, button } = recallButtonOf(page, '第一句');
  await bubble.hover();
  await expect(button).toBeVisible();
  await button.click();
  await expect(page.getByTestId('chat-recall-banner')).toBeVisible();
  await expect(input).toHaveValue('第一句'); // 回填原文

  const createsBefore = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.create').length);

  // Esc（焦点在 textarea，经 Composer keydown 冒泡到文档级分流）→ 取消编辑态
  await page.keyboard.press('Escape');

  await expect(page.getByTestId('chat-recall-banner')).toHaveCount(0); // banner 退场
  await expect(input).toHaveValue('我的草稿'); // 恢复进入前草稿
  // 聊天页仍打开（修复前此处 Esc 会走 close() 整页关闭）
  await expect(page.getByTestId('chat-settings-directory')).toBeVisible();
  await expect(page.getByPlaceholder(INPUT)).toBeVisible();
  // 无新 create 帧
  const createsAfter = await page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.create').length);
  expect(createsAfter).toBe(createsBefore);
});
