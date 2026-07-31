import { expect, test } from '@playwright/test';

test('a session GET that exceeds five seconds does not clear the active user', async ({ page }) => {
  let delaySessionRead = false;
  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() {
        setTimeout(() => this.onopen?.(), 0);
      }
      close() {}
    };
  });
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === '/api/auth/session') {
      if (delaySessionRead) await new Promise((resolve) => setTimeout(resolve, 5500));
      await route.fulfill({
        contentType: 'application/json',
        body: JSON.stringify({
          code: 0,
          data: {
            type: 'session_snapshot',
            instance_id: 'e2e-backend',
            revision: delaySessionRead ? 2 : 1,
            state: 'active',
            user: { id: 7, username: 'alice', email: 'alice@example.com' }
          }
        })
      });
      return;
    }
    await route.fulfill({ contentType: 'application/json', body: JSON.stringify({ code: 0, data: {} }) });
  });

  await page.goto('/');
  await expect.poll(() => page.evaluate(async () => {
    const { useAuthStore } = await import('/src/stores/auth.js');
    return useAuthStore().isAuthenticated.value;
  })).toBe(true);

  delaySessionRead = true;
  await page.evaluate(async () => {
    const { restoreAuthSession } = await import('/src/stores/auth.js');
    await restoreAuthSession();
  });

  const result = await page.evaluate(async () => {
    const { useAuthStore } = await import('/src/stores/auth.js');
    const store = useAuthStore();
    return {
      authenticated: store.isAuthenticated.value,
      username: store.user.value?.username,
      status: store.status.value
    };
  });
  expect(result).toEqual({
    authenticated: true,
    username: 'alice',
    status: 'transport_unavailable'
  });
});

test('a delayed older snapshot cannot revive a terminal session', async ({ page }) => {
  await page.goto('/');
  const result = await page.evaluate(async () => {
    const { applySessionSnapshot, useAuthStore } = await import('/src/stores/auth.js');
    applySessionSnapshot({
      type: 'session_snapshot', instance_id: 'race-backend', revision: 1, state: 'active',
      user: { username: 'alice' }
    });
    applySessionSnapshot({
      type: 'session_snapshot', instance_id: 'race-backend', revision: 3, state: 'hard_invalid'
    });
    const accepted = applySessionSnapshot({
      type: 'session_snapshot', instance_id: 'race-backend', revision: 2, state: 'active',
      user: { username: 'stale' }
    });
    const store = useAuthStore();
    return { accepted, authenticated: store.isAuthenticated.value, user: store.user.value };
  });
  expect(result).toEqual({ accepted: false, authenticated: false, user: null });
});
