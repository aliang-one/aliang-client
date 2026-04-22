import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const currentDir = dirname(fileURLToPath(import.meta.url));

test('critical unauthenticated auth display logic is present in frontend sources', () => {
  const authStoreSource = readFileSync(resolve(currentDir, './auth.js'), 'utf8');
  assert.match(authStoreSource, /state\.status\s*=\s*'unauthenticated'/);
  assert.match(authStoreSource, /authSessionPollIntervalMs\s*=\s*60\s*\*\s*1000/);
  assert.match(authStoreSource, /authRefreshLeadTimeMs\s*=\s*10\s*\*\s*60\s*\*\s*1000/);
  assert.match(authStoreSource, /getCurrentUserRequest/);
  assert.match(authStoreSource, /window\.setInterval/);
  assert.match(authStoreSource, /remainingMs <= authRefreshLeadTimeMs/);
  assert.match(authStoreSource, /syncUnauthenticatedAuthState\(t\('user_sessionExpired'\)\)/);

  const settingsPageSource = readFileSync(resolve(currentDir, '../components/SettingsPage.vue'), 'utf8');
  assert.match(settingsPageSource, /v-if="!isAuthenticated"/);
  assert.match(settingsPageSource, /<UserInfoSettings\s*\/>/);

  const userInfoSettingsSource = readFileSync(resolve(currentDir, '../components/settings/UserInfoSettings.vue'), 'utf8');
  assert.match(userInfoSettingsSource, /<form v-if="!isAuthenticated"/);

  const dashboardPageSource = readFileSync(resolve(currentDir, '../components/DashboardPage.vue'), 'utf8');
  assert.match(dashboardPageSource, /@click="handleShowSettings"/);
});
