import { expect, test } from '@playwright/test';
import { openChat } from './helpers/chatStub.js';

const timestamp = '2026-11-25T10:00:00.000Z';
const task = { operation_id: 'op_1', title: 'Build assets', source_kind: 'ai_progress',
  phase: 'running', revision: 1, updated_at: timestamp, progress: { current: 2, total: 4, unit: 'steps' } };

test.beforeEach(async ({ page }) => {
  await openChat(page);
  await page.waitForFunction(() => window.__operationSource?.onmessage);
});

async function push(page, operation) {
  await page.evaluate(row => window.__operationSource.onmessage({
    data: JSON.stringify({ connected: true, operations: [row] }),
  }), operation);
}

test('desktop task drawer updates measured progress and restores focus on Escape', async ({ page }) => {
  await push(page, task);
  const entry = page.getByTestId('chat-operation-entry').last();
  await expect(entry).toHaveText(/1/);
  await entry.click();
  const drawer = page.getByTestId('chat-operation-panel');
  await expect(drawer).toBeVisible();
  await drawer.getByRole('button', { name: /Build assets/ }).click();
  await expect(drawer.getByText(/50%/)).toBeVisible();
  await push(page, { ...task, revision: 2, progress: { current: 3, total: 4, unit: 'steps' } });
  await expect(drawer.getByText(/75%/)).toBeVisible();
  await push(page, task);
  await expect(drawer.getByText(/75%/)).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(drawer).toHaveCount(0);
  await expect(entry).toBeFocused();
});

test('narrow task drawer shows detail and returns to the list', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await push(page, { ...task, progress: { current: 2, unit: 'steps' } });
  await page.getByTestId('chat-operation-entry').last().click();
  const drawer = page.getByTestId('chat-operation-panel');
  await expect(drawer).toBeVisible();
  await drawer.getByRole('button', { name: /Build assets/ }).click();
  await expect(drawer.getByRole('heading', { name: 'Build assets' })).toBeVisible();
  await expect(drawer.getByRole('progressbar')).toHaveCount(0);
  await drawer.getByRole('button', { name: 'Back to tasks' }).click();
  await expect(drawer.getByRole('button', { name: /Build assets/ })).toBeVisible();
});

test('task drawer advances elapsed time until the task finishes', async ({ page }) => {
  await page.clock.install({ time: new Date('2026-11-25T10:00:00Z') });
  await page.clock.pauseAt(new Date('2026-11-25T10:00:05Z'));
  const running = { ...task, started_at: timestamp, progress: undefined };
  await push(page, running);
  await page.getByTestId('chat-operation-entry').last().click();
  const drawer = page.getByTestId('chat-operation-panel');
  const taskRow = drawer.getByRole('button', { name: /Build assets/ });
  await expect(taskRow).toContainText('Elapsed 00:05');
  await taskRow.click();
  await expect(drawer.getByText('Elapsed 00:05', { exact: true }).last()).toBeVisible();

  await page.clock.fastForward(2000);
  await expect(taskRow).toContainText('Elapsed 00:07');
  await push(page, { ...running, phase: 'completed', revision: 2, finished_at: '2026-11-25T10:00:06Z' });
  await page.clock.fastForward(2000);
  await expect(taskRow).toContainText('Elapsed 00:06');
  await expect(drawer.getByText('Elapsed 00:06', { exact: true }).last()).toBeVisible();
});

test('shell activity shows the scope of Stop and keeps one terminal record', async ({ page }) => {
  const running = { ...task, title: 'Bash', kind: 'shell', session_id: 'local-shell-session',
    started_at: timestamp, progress: undefined };
  await push(page, running);
  await page.getByTestId('chat-operation-entry').last().click();
  const drawer = page.getByTestId('chat-operation-panel');
  const taskRow = drawer.getByRole('button', { name: /^Bash/ });
  await taskRow.click();
  const scope = drawer.getByText('Stop in the conversation ends the entire turn.', { exact: true });
  await expect(scope).toBeVisible();
  await expect(drawer.getByRole('progressbar')).toHaveCount(0);
  await push(page, { ...running, phase: 'cancelled', revision: 2, finished_at: timestamp });
  await expect(taskRow).toHaveCount(1);
  await expect(taskRow).toContainText(/Cancelled/i);
  await expect(scope).toHaveCount(0);
});
