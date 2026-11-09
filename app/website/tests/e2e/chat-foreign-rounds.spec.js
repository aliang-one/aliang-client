import { test, expect } from '@playwright/test';
import { openChat, pushFrame, establishCreatedSession } from './helpers/chatStub';

// 2026-11-09 vm22 事故 e2e 复现（计划 docs/superpowers/plans/2026-11-09-web-
// local-foreign-turn-render.md）：手机端向网页正在使用的同一 journal 会话发问
// （user 行 msg_* id，run 帧经 fanOut 扇出、提问行只随快照走）后，网页必须：
// ①外来提问气泡可见（journal 权威补建）；②各回合答案独立成泡、旧连体修复；
// ③活体外来回合 delta 进新泡（不续旧泡、不整段丢弃）。
// 桩语义：subscribe 仅在 window.__snapshot 非空时回快照；pushFrame 注入 fanOut 帧。

const LIST = '.max-w-3xl';

test.describe('跨端回合渲染（本地会话）', () => {
  test('快照补建：外来提问可见，锚回合修复为 journal 文本，答案逐回合分泡', async ({ page }) => {
    await openChat(page, { newSession: true });
    const sid = await establishCreatedSession(page, '问1');
    // 本端气泡的 messageId 即 journal user 行 id（ai.message 帧同源）
    const mid = await page.evaluate(() =>
      window.__aiFrames.find((f) => f.type === 'ai.message').message_id);
    // 手机两问 + 各自回答（快照读侧折叠口径：user/assistant 行）
    await page.evaluate((firstMid) => {
      window.__snapshot = {
        status: 'idle',
        transcript: [
          { role: 'user', message_id: firstMid, text: '问1', ts: '2026-11-09T03:40:00Z' },
          { role: 'assistant', text: '答1全文', ts: '2026-11-09T03:40:05Z' },
          { role: 'user', message_id: 'msg_p3', text: '上边是几点', ts: '2026-11-09T03:44:34Z' },
          { role: 'assistant', text: '电子钟 14:43', ts: '2026-11-09T03:44:53Z' },
          { role: 'user', message_id: 'msg_p4', text: '为什么网络断了', ts: '2026-11-09T03:45:54Z' },
          { role: 'assistant', text: '排查结论：11:31 整机重启', ts: '2026-11-09T03:49:38Z' },
        ],
      };
    }, mid);
    // 外来回合启动帧（fanOut 形态）→ 本端命中补订阅 → 桩回快照
    await pushFrame(page, { type: 'ai.run.started', session_id: sid });
    await expect.poll(async () => page.evaluate((s) =>
      window.__aiFrames.filter((f) => f.type === 'ai.session.subscribe' && f.session_id === s).length, sid),
    ).toBeGreaterThan(0);
    // ①外来提问气泡可见
    await expect(page.locator(LIST).getByText('上边是几点', { exact: true })).toBeVisible();
    await expect(page.locator(LIST).getByText('为什么网络断了', { exact: true })).toBeVisible();
    // ②答案逐回合独立成泡
    await expect(page.locator(LIST).getByText('电子钟 14:43', { exact: true })).toBeVisible();
    await expect(page.locator(LIST).getByText('排查结论：11:31 整机重启', { exact: true })).toBeVisible();
    // 锚回合修复为 journal 全文（旧代码会把外来文本拼进最后一个气泡且提问隐形）
    await expect(page.locator(LIST).getByText('答1全文', { exact: true })).toBeVisible();
    // 回合数不变（无重复补建）
    await expect(page.locator(LIST).getByText('上边是几点', { exact: true })).toHaveCount(1);
    await expect(page.locator(LIST).getByText('电子钟 14:43', { exact: true })).toHaveCount(1);
  });

  test('活体外来回合：delta 进新泡承接（旧实现整段丢弃），终态后留存', async ({ page }) => {
    await openChat(page, { newSession: true });
    const sid = await establishCreatedSession(page, '问1');
    // 无快照（订阅回包未回/为空）——纯帧流形态：run.started + delta + done
    await pushFrame(page, { type: 'ai.run.started', session_id: sid });
    await pushFrame(page, { type: 'ai.delta', session_id: sid, delta: '外来答案第一段' });
    await pushFrame(page, { type: 'ai.delta', session_id: sid, delta: '，第二段' });
    // 新泡承接（旧代码：末泡 done → ensureAssistantBubble null → delta 整段丢弃）
    await expect(page.locator(LIST).getByText('外来答案第一段，第二段', { exact: true })).toBeVisible();
    await pushFrame(page, { type: 'ai.done', session_id: sid });
    // 终态后留存（running→done 不清内容）
    await expect(page.locator(LIST).getByText('外来答案第一段，第二段', { exact: true })).toBeVisible();
    // 旧回合文本不受新回合增量污染（挤一泡阴性锚：本端气泡内容恒为空占位+答1
    // 修复前是空——这里断言外来文本只出现一次，即未写入任何其他泡）
    await expect(page.locator(LIST).getByText('外来答案第一段，第二段', { exact: true })).toHaveCount(1);
  });
});
