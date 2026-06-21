import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

const currentDir = dirname(fileURLToPath(import.meta.url));

test('app boots without blocking first paint on /api/auth/session', () => {
  // Mounting the app must not be gated on the session restore request —
  // awaiting it before mount() left #app empty (blank page) whenever
  // /api/auth/session was slow or hung.
  const mainSrc = readFileSync(resolve(currentDir, './main.js'), 'utf8');
  assert.doesNotMatch(mainSrc, /await\s+restoreAuthSession\s*\(\)/);
  assert.match(mainSrc, /restoreAuthSession\s*\(/);
  assert.match(mainSrc, /mount\(['"]#app['"]\)/);
});

test('session restore request has a client-side timeout', () => {
  // A fetch with no timeout hangs forever if the backend never responds.
  // The session call must abort after a bounded timeout so the UI can fall
  // back to the login view (and /api/startup/status polling can correct it).
  const authApiSrc = readFileSync(resolve(currentDir, './services/authApi.js'), 'utf8');
  assert.match(authApiSrc, /AbortController/);
  assert.match(authApiSrc, /request\('\/api\/auth\/session', \{ method: 'GET' \}, 5000\)/);
});
