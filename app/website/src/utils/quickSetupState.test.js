import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

import {
  createLatestRenderGuard,
  isBuiltInQuickSetupSoftware,
  snapshotQuickSetupFiles,
} from './quickSetupState.js';

test('latest render wins and stale responses cannot commit', () => {
  const guard = createLatestRenderGuard();
  const first = guard.begin();
  const second = guard.begin();

  assert.equal(guard.canCommit(first, false), false);
  assert.equal(guard.canCommit(second, false), true);
});

test('dirty edits prevent the current render response from committing', () => {
  const guard = createLatestRenderGuard();
  const request = guard.begin();

  assert.equal(guard.canCommit(request, true), false);
  assert.equal(guard.canCommit(request, false), true);

  guard.invalidate();
  assert.equal(guard.isCurrent(request), false);
});

test('only custom software exposes an editable path', () => {
  assert.equal(isBuiltInQuickSetupSoftware({ code: 'opencode' }), true);
  assert.equal(isBuiltInQuickSetupSoftware({ code: 'custom-tool', isCustom: true }), false);
});

test('apply snapshot preserves manual edits without sharing mutable objects', () => {
  const edited = [{ code: 'config', path: '/custom/path', content: '{"manual":true}' }];
  const snapshot = snapshotQuickSetupFiles(edited);

  assert.deepEqual(snapshot, edited);
  assert.notEqual(snapshot[0], edited[0]);
  edited[0].content = '{"overwritten":true}';
  assert.equal(snapshot[0].content, '{"manual":true}');
});

test('QuickSetupModal save path does not rerender or expose built-in paths', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCurrentVariant\(\) \{[\s\S]*?\n\}/)?.[0] || '';

  assert.ok(applyBlock, 'applyCurrentVariant function is missing');
  assert.doesNotMatch(applyBlock, /renderSelectedKey\s*\(/);
  assert.match(applyBlock, /snapshotQuickSetupFiles\(editableFiles\.value\)/);
  assert.match(component, /:readonly="isBuiltInQuickSetupSoftware\(selectedSoftwareDef\)"/);
  assert.match(component, /renderGuard\.canCommit\(requestId, filesDirty\.value\)/);
});
