import { expect, test } from '@playwright/test';
import { installChatStub, pushFrame } from './helpers/chatStub.js';

// 后台静默运行（2026-11-04）：窗口关闭 ≠ run 中断——run 在 agent 侧照跑，
// journal/会话态是真值源。本 spec 钉六条行为：
//   ①重进页对账：恢复的 running 气泡向订阅快照收敛（idle→done+内容回填）；
//   ②快照 running：气泡保持 running + journal 前缀延伸 + 后续 delta 续流；
//   ③纯草稿（无 aiSessionId）不发订阅帧（无可订阅对象）；
//   ④关窗主动 close：绝不画「连接已中断」假中断（旧实现关窗即污染气泡）；
//   ⑤刷新丢占位恢复（2026-11-04 用户实测主形态）：占位气泡未落盘的会话
//     （本地末尾只有 user 气泡）照样订阅，快照 running 补建运行态；
//   ⑥历史丢答案修复：快照 idle + journal 锚定回答 → 补建 done 气泡。
// 设计定调（用户 2026-11-04）：会话状态权威在 Go 侧（journal+会话态），
// localStorage 只是视图缓存——对账绝不以缓存形态为触发前提。
// 会话种子直接写 localStorage（loadFromStorage 的 agentcore 本地草稿过滤口径：
// 无 deviceSession + provider 归一为 agentcore）。

const SEED_CONV_ID = 'seedconv1';
const SEED_SESSION_ID = 'local-seedconv1';

function seedConversation({
  bubbleContent = '',
  bubbleStatus = 'running',
  withAssistant = true,
  withAiSessionId = true,
} = {}) {
  const now = 1730000000000;
  const messages = [
    { role: 'user', content: '跑一个长任务', messageId: 'm-user-1', provider: 'agentcore', ts: now },
  ];
  if (withAssistant) {
    messages.push({ role: 'assistant', content: bubbleContent, status: bubbleStatus, ts: now });
  }
  const conv = {
    id: SEED_CONV_ID,
    title: '后台续跑种子会话',
    provider: 'agentcore',
    model: 'glm-5.2',
    effort: '',
    maxTokens: 0,
    contextWindow: 0,
    disabledMcpServers: [],
    disabledSkills: [],
    aiSessionCreated: true,
    createdAt: now,
    updatedAt: now,
    messages,
  };
  if (withAiSessionId) conv.aiSessionId = SEED_SESSION_ID;
  return { conversations: [conv], activeConversationId: SEED_CONV_ID };
}

// seedAndOpen：会话种子（localStorage）+ 可选挂载期快照。快照 init 脚本必须
// 在 installChatStub（内部把 __snapshot 置 null）之后注册——同队列按序执行，
// 后者覆盖前者；挂载期的 resubscribeRestoredRuns 读的就是这里的值。
async function seedAndOpen(page, seedOptions, snapshot = undefined, sessions = []) {
  await page.addInitScript((seed) => {
    localStorage.setItem('aliang-chat-data', JSON.stringify(seed));
  }, seedConversation(seedOptions));
  await installChatStub(page, { sessions });
  if (snapshot !== undefined) {
    await page.addInitScript((s) => { window.__snapshot = s; }, snapshot);
  }
  await page.goto('/');
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible();
  await page.getByTestId('aliang-chat-entry-sidebar').click();
}

test('重进页对账：快照 idle → 气泡落 done + journal 内容回填 + 订阅帧发出', async ({ page }) => {
  await seedAndOpen(
    page,
    { bubbleContent: '', bubbleStatus: 'running' },
    {
      status: 'idle',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '任务已全部完成。', message_id: 'm-asst-1' },
      ],
    },
  );

  // 订阅帧发出（对账通道建立）
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.subscribe' && f.session_id === 'local-seedconv1').length
  )).toBeGreaterThanOrEqual(1);

  // 气泡收敛：journal 尾行回填、终态 done（停止键退场、无「中断」错误文案）
  await expect(page.getByText('任务已全部完成。')).toBeVisible();
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
  await expect(page.getByText(/连接.*中断|interrupted/i)).toHaveCount(0);
});

test('快照 running：气泡保持 running + 前缀延伸 + 后续 delta 续流', async ({ page }) => {
  await seedAndOpen(
    page,
    { bubbleContent: '正在执行：', bubbleStatus: 'running' },
    {
      status: 'running',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '正在执行：步骤一已完成，', message_id: 'm-asst-1' },
      ],
    },
  );

  // journal 前缀延伸（「正在执行：」是快照尾行的前缀 → 顶到快照值）
  await expect(page.getByText('正在执行：步骤一已完成，')).toBeVisible();
  // 仍是 running（停止键在场），后续 delta 经订阅续流
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();
  await pushFrame(page, { type: 'ai.delta', session_id: 'local-seedconv1', delta: '步骤二进行中…' });
  await expect(page.getByText('正在执行：步骤一已完成，步骤二进行中…')).toBeVisible();
});

test('纯草稿（无 aiSessionId）：不发订阅帧', async ({ page }) => {
  await seedAndOpen(page, { bubbleContent: '已完成', bubbleStatus: 'done', withAiSessionId: false });
  await expect(page.getByText('已完成')).toBeVisible();
  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.subscribe').length
  )).toBe(0);
});

test('刷新丢占位恢复：本地缺 assistant 气泡 → 快照 running 补建运行态', async ({ page }) => {
  // 用户实测主形态（2026-11-04）：占位气泡未落盘,刷新后本地末尾只有 user
  // 气泡——对账不得以「缓存有 running 气泡」为前提,订阅照发,快照 running
  // → 补建 running 气泡(内容取锚定 runText),delta 续流落进它。
  await seedAndOpen(
    page,
    { withAssistant: false },
    {
      status: 'running',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '正在执行：步骤一已完成，', message_id: 'm-asst-1' },
      ],
    },
  );

  await expect.poll(async () => page.evaluate(() =>
    window.__aiFrames.filter((f) => f.type === 'ai.session.subscribe' && f.session_id === 'local-seedconv1').length
  )).toBeGreaterThanOrEqual(1);
  await expect(page.getByText('正在执行：步骤一已完成，')).toBeVisible();
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();
  await pushFrame(page, { type: 'ai.delta', session_id: 'local-seedconv1', delta: '步骤二进行中…' });
  await expect(page.getByText('正在执行：步骤一已完成，步骤二进行中…')).toBeVisible();
});

test('历史丢答案修复：本地缺气泡 → 快照 idle + journal 回答补建 done 气泡', async ({ page }) => {
  // 修复前 RefreshWindow 丢掉的答案,重进页后由快照(journal 权威)补建——
  // 用户现有浏览器里已损坏的会话即由此自愈。hasAssistantAfter 守卫保证
  // 已完成会话不被重复补气泡。
  await seedAndOpen(
    page,
    { withAssistant: false },
    {
      status: 'idle',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '丢失的回答已找回。', message_id: 'm-asst-1' },
      ],
    },
  );
  await expect(page.getByText('丢失的回答已找回。')).toBeVisible();
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
});

test('本地会话同时在设备清单：本地对账优先于壳分支（路由劫持回归锚）', async ({ page }) => {
  // 生产形态：journal 三源扫描会把 local-* 会话也列进 /api/agent/sessions。
  // 壳查找若先命中同名 entry，快照被壳分支吃掉、本地气泡永不收敛（评审 P1）。
  await seedAndOpen(
    page,
    { bubbleContent: '', bubbleStatus: 'running' },
    {
      status: 'idle',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '清单共存时本地仍收敛。', message_id: 'm-asst-1' },
      ],
    },
    [{ id: 'local-seedconv1', provider: 'agentcore', title: '后台续跑种子会话', status: 'running', origin: 'dashboard', project_path: '/home/e2e' }],
  );
  await expect(page.getByText('清单共存时本地仍收敛。')).toBeVisible();
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
});

test('关窗主动 close 不画假中断：重进后气泡仍是 running 并经快照收敛', async ({ page }) => {
  // 第一段：快照缺席（__snapshot=null）→ 恢复的 running 气泡保持 running
  await seedAndOpen(page, { bubbleContent: '', bubbleStatus: 'running' });
  await expect(page.getByTestId('chat-stop-button')).toBeVisible();

  // 把 stub 的 close 变成「触发 onclose 的真关闭」：关窗路径
  // （onUnmounted → closingIntentionally → onclose）在旧实现会把气泡画成
  // 「连接已中断」并持久化；新实现必须原样保留 running。
  await page.evaluate(() => {
    const ws = window.WebSocket.instances[window.WebSocket.instances.length - 1];
    ws.close = () => { ws.readyState = 3; ws.onclose?.({ code: 1000, reason: '' }); };
  });
  await page.getByRole('button').filter({ hasText: 'close' }).first().click();
  await expect(page.getByTestId('aliang-chat-entry-sidebar')).toBeVisible(); // 已回仪表盘

  // 第二段：重进页。此刻再挂快照（idle），重进对账把 running 收敛为 done。
  await page.evaluate(() => {
    window.__snapshot = {
      status: 'idle',
      transcript: [
        { role: 'user', text: '跑一个长任务', message_id: 'm-user-1' },
        { role: 'assistant', text: '后台任务已跑完。', message_id: 'm-asst-1' },
      ],
    };
  });
  await page.getByTestId('aliang-chat-entry-sidebar').click();

  await expect(page.getByText('后台任务已跑完。')).toBeVisible();
  await expect(page.getByTestId('chat-stop-button')).toHaveCount(0);
  await expect(page.getByText(/连接.*中断|interrupted/i)).toHaveCount(0);
});
