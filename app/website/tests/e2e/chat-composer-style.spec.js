import { test, expect } from '@playwright/test';
import { openChat as openChatBase } from './helpers/chatStub.js';

// 圆角直角混存修复锚(2026-11-04):composer textarea 上的浏览器层蓝色
// border/ring 由 ChatComposer scoped !important 免疫压平——计算样式必须
// 无边框、圆角 12px,历史 height:0px 残留也不再写入。
test('composer textarea immune to browser-level border/ring', async ({ page }) => {
  await openChatBase(page, { sessions: [] });
  await page.getByRole('button').filter({ hasText: 'add' }).first().click();
  const ta = page.getByPlaceholder(/输入消息|Type a message/);
  await ta.click();
  await expect.poll(async () => page.evaluate(() => {
    const el = document.querySelector('textarea');
    const cs = getComputedStyle(el);
    return {
      border: cs.border,
      radius: cs.borderRadius,
      shadow: cs.boxShadow,
      height: el.style.height,
    };
  })).toEqual({
    border: '0px none rgb(51, 65, 85)',
    radius: '12px',
    shadow: 'none',
    height: 'auto',
  });
});
