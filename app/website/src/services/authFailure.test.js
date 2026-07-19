import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

import {
  authenticationFailureMessage,
  handleAuthenticationFailure,
  isAuthenticationFailure
} from './authFailure.js';

const currentDir = dirname(fileURLToPath(import.meta.url));

test('HTTP 401 always invalidates authentication before the caller throws', () => {
  const envelope = { code: 1001, msg: 'Token has expired' };
  let message = '';

  assert.equal(isAuthenticationFailure(401, envelope), true);
  assert.equal(handleAuthenticationFailure(401, envelope, (value) => { message = value; }), true);
  assert.equal(message, 'Token has expired');
});

test('nested unauthenticated envelopes invalidate even with HTTP 200', () => {
  const envelope = {
    code: 0,
    data: {
      status: 'unauthenticated',
      error: 'session_expired',
      msg: 'Please log in again'
    }
  };

  assert.equal(isAuthenticationFailure(200, envelope), true);
  assert.equal(authenticationFailureMessage(envelope), 'Please log in again');
});

test('ordinary server failures do not log the user out', () => {
  let called = false;
  const envelope = { code: 500, msg: 'temporary upstream failure' };

  assert.equal(handleAuthenticationFailure(500, envelope, () => { called = true; }), false);
  assert.equal(called, false);
});

test('all authenticated service wrappers run the shared auth-failure handler', () => {
  for (const filename of [
    'agentApi.js',
    'dashboardApi.js',
    'quickSetupApi.js',
    'runApi.js',
    'softwareUpdateApi.js',
    'statusApi.js',
    'userCenterApi.js'
  ]) {
    const source = readFileSync(resolve(currentDir, filename), 'utf8');
    assert.match(source, /handleAuthenticationFailure\(response\.status, envelope, syncUnauthenticatedAuthState\)/, filename);
  }

  for (const filename of [
    '../composables/useRunStatus.js',
    '../composables/useCertStatus.js'
  ]) {
    const source = readFileSync(resolve(currentDir, filename), 'utf8');
    assert.match(source, /handleAuthenticationFailure\(/, filename);
    assert.match(source, /syncUnauthenticatedAuthState/, filename);
  }
});

test('auth store rejects stale async and startup responses after invalidation', () => {
  const source = readFileSync(resolve(currentDir, '../stores/auth.js'), 'utf8');
  assert.match(source, /let sessionEpoch = 0/);
  assert.match(source, /requestEpoch !== sessionEpoch/);
  assert.match(source, /state\.isAuthenticated && user && typeof user === 'object' && fetchSuccess/);
});
