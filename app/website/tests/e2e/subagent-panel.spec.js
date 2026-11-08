import { expect, test } from '@playwright/test';
import { openChat as openChatBase, pushFrame } from './helpers/chatStub.js';

// 子agent面板 e2e（spec 2026-11-07-subagent-panel §三）：agentcore 会话底部入口条
// （子agent + 运行中/已完成计数）→ 点击开面板（列表可见）→ 条目切换选中 →
// 详情三段（状态头/工具时间线/完整报告）→ ai.subagent progress 帧实时刷新
// 运行中条目的 detail 行（currentStep/lastTool/toolUses）。
//
// 数据面全部走 route 拦截（禁写真实 agent home）：详情端点 /api/agent/session
// 预置 subagents[]（completed 带 tools[]+result 完整报告；started 运行中），
// WS 实时侧经 helpers/chatStub 的 pushFrame 注入 ai.subagent 帧。
//
// 注意本 spec 断言中文文案：test.use({ locale }) 驱动 i18n 的
// navigator.language 判定（loadLocale：zh* → zh），与既有 spec 的「无头
// Chrome=en-US → 英文双语正则」是两种合法口径，这里按任务要求走中文档。

test.use({ locale: 'zh-CN' });

const NOW_ISO = new Date().toISOString();
const SESSIONS = [
  { id: 'conv-sub-1', provider: 'agentcore', title: '子agent母会话', status: 'running', origin: 'phone', project_path: '/home/alice/proj-sub', updated_at: NOW_ISO },
];
const DETAIL_TRANSCRIPT = [
  { id: 'm1', role: 'user', content: '母会话用户消息', timestamp: NOW_ISO, index: 1 },
  { id: 'm2', role: 'assistant', content: '母会话助手消息', timestamp: NOW_ISO, index: 2 },
];

// 详情 API（AgentVibeSubagent 蛇形键 + status 词表）→ 前端归一（status→phase）。
// 首条 completed 置顶：面板打开自动选中首条 → 初始详情即终态条目（时间线+报告）。
const SUBAGENT_DONE = {
  task_id: 'ta_done',
  name: 'explore-agent',
  description: '检索仓库结构',
  status: 'completed',
  total_tokens: 1520,
  duration_ms: 4200,
  result: '已定位 12 个关键文件，核心入口在 main.go。',
  tools: [
    { tool: 'Bash', summary: 'ls -R src', is_error: false, ts: NOW_ISO },
    { tool: 'Grep', summary: 'grep -n main', is_error: false, ts: NOW_ISO },
    { tool: 'Bash', summary: 'rm -rf /tmp/scratch', is_error: true, ts: NOW_ISO },
  ],
};
// 运行中条目：currentStep/lastTool/toolUses 是 WS progress 帧的专有字段（详情
// 归一置 null/0），初始 detail 行只露状态头；progress 帧到达后才刷新出三行。
// shell 装载与面板重拉两版 total_tokens 不同（320→480）：480 落列表行即
// 「面板打开触发的详情重拉已入库」的确定性锚点，此后再注入 progress 帧防
// 整体替换入库把帧聚合态冲掉（ingestDetailSubagents 是 store[sid] 整换）。
const SUBAGENT_RUNNING_SHELL = {
  task_id: 'ta_run',
  name: 'research-agent',
  description: '并行调研实现方案',
  status: 'started',
  total_tokens: 320,
};
const SUBAGENT_RUNNING_REFETCH = { ...SUBAGENT_RUNNING_SHELL, total_tokens: 480 };

async function openChat(page, { detailLog = [] } = {}) {
  await openChatBase(page, { sessions: SESSIONS, newSession: false, detail: { transcript: DETAIL_TRANSCRIPT } });
  // 详情端点重载（必须在 openChat 之后注册）：Playwright 对同一 URL 的多路由
  // 按「最近注册者优先」裁决，晚注册的这条压过 chatStub 的 **/api/** 通配兜底，
  // 拿到 /api/agent/session 全部请求。响应同时带转录与 subagents[]（会话级
  // 字段，服务端每页同值——首装与面板重拉同源）。shell 装载（带 limit=100）
  // 与面板重拉（无 limit）按请求形态区分响应。
  await page.route('**/api/agent/session*', async (route) => {
    const url = new URL(route.request().url());
    detailLog.push({ limit: url.searchParams.get('limit'), before: url.searchParams.get('before') });
    const running = url.searchParams.get('limit')
      ? SUBAGENT_RUNNING_SHELL
      : SUBAGENT_RUNNING_REFETCH;
    await route.fulfill({
      contentType: 'application/json',
      body: JSON.stringify({
        code: 0,
        data: {
          id: url.searchParams.get('id'),
          transcript: DETAIL_TRANSCRIPT,
          transcript_page: { limit: 40, count: DETAIL_TRANSCRIPT.length },
          subagents: [SUBAGENT_DONE, running],
        },
      }),
    });
  });
  await expect(page.getByTestId('session-source-badge')).toHaveCount(1);
}

test('子agent面板：入口条计数→面板列表→详情时间线/报告→progress 帧实时刷新', async ({ page }) => {
  const detailLog = [];
  await openChat(page, { detailLog });

  // 打开 agentcore 壳：详情装载（含 subagents[]）→ 底部入口条出现
  await page.getByText('子agent母会话', { exact: true }).click();
  await expect(page.getByText('母会话用户消息')).toBeVisible();
  const entry = page.getByTestId('chat-subagent-entry');
  await expect(entry).toBeVisible();
  await expect(entry).toContainText('子agent');
  await expect(entry).toContainText('1 运行中'); // started 条目计数
  await expect(entry).toContainText('1 已完成'); // completed 条目计数
  await expect(entry).toContainText('查看详情');

  // 点击入口条 → 面板打开，列表可见（两子agent 名称/描述）。注意可见性断言
  // 不能落在 chat-subagent-panel 包装 div 上：它只装 fixed 定位的遮罩/抽屉，
  // 自身包围盒 0x0，Playwright 判 hidden——可见性落在列表实体上。
  await entry.click();
  await expect(page.getByTestId('chat-subagent-panel')).toHaveCount(1);
  await expect(page.getByTestId('chat-subagent-panel-list')).toBeVisible();
  const items = page.getByTestId('chat-subagent-panel-item');
  await expect(items).toHaveCount(2);
  await expect(items.nth(0)).toContainText('explore-agent');
  await expect(items.nth(0)).toContainText('检索仓库结构');
  await expect(items.nth(1)).toContainText('research-agent');
  await expect(items.nth(1)).toContainText('并行调研实现方案');

  // 打开即选中首条 → 初始详情 = completed 条目：✓态+时间线+完整报告
  await expect(page.getByTestId('chat-subagent-detail-name')).toHaveText('explore-agent');
  await expect(page.getByTestId('chat-subagent-detail-status')).toContainText('已完成');
  await expect(page.getByTestId('chat-subagent-detail-duration')).toContainText('4.2s');
  await expect(page.getByTestId('chat-subagent-detail-tokens')).toContainText('1,520');
  await expect(page.getByTestId('chat-subagent-detail-tool-item')).toHaveCount(3);
  await expect(page.getByTestId('chat-subagent-detail-tool-item').nth(0)).toContainText('Bash');
  await expect(page.getByTestId('chat-subagent-detail-tool-item').nth(1)).toContainText('Grep');
  await expect(page.getByTestId('chat-subagent-detail-tool-error')).toHaveCount(1);
  await expect(page.getByTestId('chat-subagent-detail-tool-error')).toContainText('执行失败');
  await expect(page.getByTestId('chat-subagent-detail-report')).toContainText('已定位 12 个关键文件，核心入口在 main.go。');
  // 运行中条目在列表行内带 spinner；completed 行没有
  await expect(page.getByTestId('chat-subagent-item-spinner')).toHaveCount(1);

  // 选中运行中条目：spinner 态 + 报告未生成占位（WS 实时条目无 result/tools）
  await items.nth(1).click();
  await expect(page.getByTestId('chat-subagent-detail-name')).toHaveText('research-agent');
  await expect(page.getByTestId('chat-subagent-detail-status')).toContainText('运行中');
  await expect(page.getByTestId('chat-subagent-detail-tool-item')).toHaveCount(0);
  await expect(page.getByText('暂无工具调用')).toBeVisible();
  await expect(page.getByTestId('chat-subagent-detail-report')).toHaveCount(0);
  await expect(page.getByText('运行中，报告尚未生成')).toBeVisible();

  // 面板打开触发的无 limit 详情重拉已入库（列表行 tokens 320→480）——
  // 确定性锚点：此后注入 progress 帧不会被重拉的整体替换冲掉
  await expect(items.nth(1)).toContainText('480');
  await expect.poll(async () => detailLog.filter(l => l.limit === null).length)
    .toBeGreaterThanOrEqual(1);

  // WS progress 帧实时刷新运行中条目的 detail 行（当前步骤/最近工具/工具次数）
  await pushFrame(page, {
    type: 'ai.subagent', session_id: 'conv-sub-1', task_id: 'ta_run', phase: 'progress',
    current_step: '扫描 docs 目录', last_tool: 'Read', tool_uses: 5,
  });
  await expect(page.getByTestId('chat-subagent-detail-step')).toContainText('扫描 docs 目录');
  await expect(page.getByTestId('chat-subagent-detail-lasttool')).toContainText('Read');
  await expect(page.getByTestId('chat-subagent-detail-tooluses')).toContainText('5');
  await expect(entry).toContainText('1 运行中'); // progress 仍计运行中

  // 切换回 completed 条目：报告/时间线跟随选中对象刷新
  await items.nth(0).click();
  await expect(page.getByTestId('chat-subagent-detail-name')).toHaveText('explore-agent');
  await expect(page.getByTestId('chat-subagent-detail-report')).toContainText('已定位 12 个关键文件');

  // 关闭面板退场
  await page.getByTestId('chat-subagent-panel-close').click();
  await expect(page.getByTestId('chat-subagent-panel')).toHaveCount(0);
});
