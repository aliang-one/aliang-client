import { expect, test } from '@playwright/test';
import { openChat as openChatBase, pushFrame, establishCreatedSession } from './helpers/chatStub.js';

// Task 14：ai.option 两步作答交互卡 + 静态围栏卡防双份。
// 用例①交互卡两步作答：ai.option.request（2 项 / multi:false / allow_custom:true）
// → 卡渲染且两项可见 → 点选项 A 仅本地高亮（未发包，提交键从禁用解除）→ 填
// 自定义文本 → 提交 → __aiFrames 含 ai.option.response{option_id/selected/
// custom_text} → 卡折叠「已选择」终态；随后 ai.option.cancelled → 卡消失 +
// 系统提示条（chat-option-cancelled-note）。
// 用例②静态卡 + 防双份：assistant 输出含 ```aliang-options 围栏（ai.delta
// 注入）→ 静态只读卡渲染且围栏原文不露出（正文永远 strip）→ 同 option_id 的
// ai.option.request 到达 → 静态卡隐藏（交互卡在场）。
// 注意无头 Chrome locale=en-US，UI 文案断言一律双语音正则；选项文案来自帧内
// 数据（中文原文），可直接断言。

function optionRequestFrame(sid, optionId) {
  return {
    type: 'ai.option.request',
    session_id: sid,
    option_id: optionId,
    title: '部署方案',
    summary: '请选择部署方式，选择后我会继续执行。',
    v: 2,
    options: [
      { id: 'a', label: '方案 A：蓝绿部署', description: '先起新实例再切流量' },
      { id: 'b', label: '方案 B：直接替换', description: '停旧起新，最快但中断服务' },
    ],
    allow_custom: true,
    multi: false,
    provider: 'agentcore',
  };
}

test('交互卡两步作答：点选仅高亮不发帧，提交发 ai.option.response，折叠终态，cancelled 清卡', async ({ page }) => {
  // 默认回 ai.done：establishCreatedSession 需要终态收敛锚（停止键退场）
  await openChatBase(page);
  const sid = await establishCreatedSession(page);
  await pushFrame(page, optionRequestFrame(sid, 'op1'));

  const card = page.getByTestId('chat-option-card');
  await expect(card).toBeVisible();
  await expect(card).toContainText('方案 A：蓝绿部署');
  await expect(card).toContainText('方案 B：直接替换');

  // 两步作答第一步：未选任何项时提交禁用
  const submit = page.getByTestId('chat-option-submit');
  await expect(submit).toBeDisabled();

  // 点选项 A：仅本地高亮（aria-checked 互斥），尚未发任何作答帧
  await card.getByText('方案 A：蓝绿部署').click();
  await expect(card.locator('[role="radio"]').nth(0)).toHaveAttribute('aria-checked', 'true');
  await expect(card.locator('[role="radio"]').nth(1)).toHaveAttribute('aria-checked', 'false');
  expect(await page.evaluate(() =>
    window.__aiFrames.some(f => f.type === 'ai.option.response')
  )).toBe(false);
  await expect(submit).toBeEnabled();

  // 可选自定义文本随帧一起发走
  await page.getByPlaceholder(/自定义回复|Custom reply/).fill('注意灰度发布');
  await submit.click();

  await expect.poll(() => page.evaluate(() =>
    window.__aiFrames.filter(f => f.type === 'ai.option.response').length
  )).toBe(1);
  const resp = await page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.option.response')
  );
  expect(resp.option_id).toBe('op1');
  expect(resp.session_id).toBe(sid);
  expect(resp.selected).toEqual(['a']);
  expect(resp.custom_text).toBe('注意灰度发布');

  // 折叠终态：已选择 + 选项行退场
  await expect(card).toContainText(/已选择|Selected/);
  await expect(card.locator('[role="radio"]')).toHaveCount(0);

  // cancelled：卡消失 + 系统提示条（镜像审批取消的 run.notice 式通栏）
  await pushFrame(page, {
    type: 'ai.option.cancelled',
    session_id: sid,
    option_ids: ['op1'],
    reason: 'session_closed',
  });
  await expect(page.getByTestId('chat-option-card')).toHaveCount(0);
  const note = page.getByTestId('chat-option-cancelled-note');
  await expect(note).toBeVisible();
  await expect(note).toContainText(/方案选择已失效|Option selection expired/);
});

test('静态围栏卡 + 防双份：围栏渲染只读卡且原文不露出，同 option_id 交互卡到达后静态卡隐藏', async ({ page }) => {
  // run 保持 running：ai.delta 需要承接气泡（ai.done 后迟到 delta 会被丢弃）
  await openChatBase(page, { aiMessageResponder: 'ai.run.started' });
  const input = page.getByPlaceholder(/输入消息|Type a message/);
  await input.fill('给我一个部署方案');
  await page.keyboard.press('Enter');
  await expect.poll(() => page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.session.create')?.session_id || ''
  )).not.toBe('');
  const sid = await page.evaluate(() =>
    window.__aiFrames.find(f => f.type === 'ai.session.create').session_id
  );

  // assistant 输出含 aliang-options 围栏 → 静态只读卡 + 正文剔除围栏原文
  const block = {
    option_id: 'op2',
    title: '上线方案',
    summary: '两个候选方案',
    options: [
      { id: 'a', label: '滚动升级' },
      { id: 'b', label: '全量切换' },
    ],
  };
  await pushFrame(page, {
    type: 'ai.delta',
    session_id: sid,
    delta: '给你两个方案：\n\n```aliang-options\n' + JSON.stringify(block) + '\n```\n',
  });

  const staticCard = page.getByTestId('chat-option-static');
  await expect(staticCard).toBeVisible();
  await expect(staticCard).toContainText('滚动升级');
  await expect(staticCard).toContainText('全量切换');
  // 防双份正确流：正文永远 strip 围栏——正文文字在场，围栏代码块/原文永不露出
  await expect(page.getByText('给你两个方案')).toBeVisible();
  await expect(page.locator('code.language-aliang-options')).toHaveCount(0);

  // 同 option_id 交互请求到达：静态卡隐藏，交互卡在场
  await pushFrame(page, optionRequestFrame(sid, 'op2'));
  await expect(page.getByTestId('chat-option-static')).toHaveCount(0);
  await expect(page.getByTestId('chat-option-card')).toBeVisible();
});
