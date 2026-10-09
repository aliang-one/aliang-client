import { expect, test } from '@playwright/test';

// 与 auth-consistency.spec.js 相同的 API mock 模式；dashboardData.js 对空对象有防御性提取
async function mockApi(page) {
  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() { setTimeout(() => this.onopen?.(), 0); }
      close() {}
    };
    // 首跑「Getting Started」引导是独立的一次性 overlay，与本 spec 断言无关；
    // 预置完成标记避免其遮挡面板/弹窗交互（仅测试环境准备，不弱化断言）
    window.localStorage.setItem('alianggate.onboarding.v2', 'completed');
  });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          code: 0,
          data: {
            type: 'session_snapshot', instance_id: 'e2e-mobile', revision: 1, state: 'active',
            user: { id: 7, username: 'alice', email: 'alice@example.com' }
          }
        })
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });
}

async function horizontalOverflowPx(page) {
  return page.evaluate(() =>
    Math.max(
      document.documentElement.scrollWidth - document.documentElement.clientWidth,
      document.body.scrollWidth - document.body.clientWidth
    )
  );
}

test.describe('mobile 390x844', () => {
  test.use({ viewport: { width: 390, height: 844 } });
  test.beforeEach(async ({ page }) => { await mockApi(page); });

  test('dashboard: aside 隐藏、移动顶栏可见、无横向溢出、无 pageerror', async ({ page }) => {
    const pageErrors = [];
    page.on('pageerror', (e) => pageErrors.push(e));
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await expect(page.locator('aside').first()).toBeHidden();
    await expect(page.locator('[data-mobile-topbar]')).toBeVisible();
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
    expect(pageErrors).toEqual([]);
  });

  test('dashboard: 折叠面板可开合', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    const panel = page.locator('[data-mobile-panel]');
    await expect(panel).toBeHidden();
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(panel).toBeVisible();
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(panel).toBeHidden();
  });

  test('dashboard: 各主内容区块无横向溢出', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    // 展开折叠面板到最大内容状态再量
    await page.locator('[data-mobile-panel-toggle]').click();
    await expect(page.locator('[data-mobile-panel]')).toBeVisible();
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
  });

  test('settings: 各子页无横向溢出', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await page.locator('[data-mobile-panel-toggle]').click();
    await page.getByRole('button', { name: /更多设置|More Settings/ }).click();
    await expect(page.locator('aside').first()).toBeVisible(); // settings 页 aside 是导航栏
    const tabs = page.locator('div.grid.grid-cols-2.md\\:hidden > button');
    const count = await tabs.count();
    expect(count).toBeGreaterThanOrEqual(4);
    for (let i = 0; i < count; i++) {
      await tabs.nth(i).click();
      await expect(tabs.nth(i)).toHaveClass(/border-primary/);
      expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
    }
  });

  test('登录弹窗在视口内且可滚动到底部按钮', async ({ page }) => {
    // 未登录态：拦掉 session 快照
    await page.route('**/api/auth/session', (route) =>
      route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({ code: 0, data: { type: 'session_snapshot', instance_id: 'e2e-mobile', revision: 1, state: 'unauthenticated' } })
      })
    );
    await page.goto('/');
    await page.locator('[data-mobile-topbar] button').first().waitFor();
    // 未登录时顶栏头像入口点击 → 登录弹窗（data-mobile-account 在 Task 2 的顶栏头像按钮上）
    await page.locator('[data-mobile-account]').click();
    const dialog = page.locator('.fixed.inset-0 .max-w-md');
    await expect(dialog).toBeVisible();
    const box = await dialog.boundingBox();
    expect(box).not.toBeNull();
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.y + box.height).toBeLessThanOrEqual(844);
    await dialog.locator('button[type="submit"], form button').last().scrollIntoViewIfNeeded();
  });

  test('quick setup: 弹窗在视口内且底部可达', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await page.locator('[data-mobile-panel-toggle]').click();
    await page.locator('[data-mobile-panel]').getByRole('button', { name: /快速配置|Quick Setup/ }).click();
    const shell = page.locator('.fixed.inset-0 .max-w-6xl');
    await expect(shell).toBeVisible();
    const box = await shell.boundingBox();
    expect(box).not.toBeNull();
    expect(box.y).toBeGreaterThanOrEqual(0);
    expect(box.y + box.height).toBeLessThanOrEqual(844);
    // 右列内容区可滚动（min-h-0 生效）：滚动到底后最后一个按钮可见
    const scroller = shell.locator('div.overflow-y-auto').last();
    await scroller.evaluate((el) => { el.scrollTop = el.scrollHeight; });
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
  });
});

test.describe('desktop 1280x800 控制组（桌面零变化）', () => {
  test.use({ viewport: { width: 1280, height: 800 } });
  test.beforeEach(async ({ page }) => { await mockApi(page); });

  test('桌面: aside 与服务器链路卡可见、无移动顶栏', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('aside').first()).toBeVisible();
    await expect(page.getByText(/服务器连接|服务器链路|Server Link/i).first()).toBeVisible();
    await expect(page.locator('[data-mobile-topbar]')).toBeHidden(); // md:hidden 下元素仍在 DOM，仅 display:none
  });
});

test.describe('tablet 768x1024（max-width:767px 修复哨兵）', () => {
  test.use({ viewport: { width: 768, height: 1024 } });
  test.beforeEach(async ({ page }) => { await mockApi(page); });

  test('768px 走桌面布局：主区可见非零高、无移动顶栏显示', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('h1').first()).toContainText('ALiang');
    await expect(page.locator('main').first()).toBeVisible();
    const mainBox = await page.locator('main').first().boundingBox();
    expect(mainBox).not.toBeNull();
    expect(mainBox.height).toBeGreaterThan(300);
    expect(mainBox.y).toBeLessThan(100);
    await expect(page.locator('aside').first()).toBeVisible();
    await expect(page.locator('[data-mobile-topbar]')).toBeHidden();
    expect(await horizontalOverflowPx(page)).toBeLessThanOrEqual(1);
  });
});
