import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

import {
  createLatestRenderGuard,
  isBuiltInQuickSetupSoftware,
  snapshotQuickSetupFiles,
} from './quickSetupState.js';

it('latest render wins and stale responses cannot commit', () => {
  const guard = createLatestRenderGuard();
  const first = guard.begin();
  const second = guard.begin();

	expect(guard.canCommit(first, false)).toBe(false);
	expect(guard.canCommit(second, false)).toBe(true);
});

it('dirty edits prevent the current render response from committing', () => {
  const guard = createLatestRenderGuard();
  const request = guard.begin();

	expect(guard.canCommit(request, true)).toBe(false);
	expect(guard.canCommit(request, false)).toBe(true);

  guard.invalidate();
	expect(guard.isCurrent(request)).toBe(false);
});

it('only custom software exposes an editable path', () => {
	expect(isBuiltInQuickSetupSoftware({ code: 'opencode' })).toBe(true);
	expect(isBuiltInQuickSetupSoftware({ code: 'custom-tool', isCustom: true })).toBe(false);
});

it('apply snapshot preserves manual edits without sharing mutable objects', () => {
  const edited = [{ code: 'config', path: '/custom/path', content: '{"manual":true}' }];
  const snapshot = snapshotQuickSetupFiles(edited);

	expect(snapshot).toEqual(edited);
	expect(snapshot[0]).not.toBe(edited[0]);
  edited[0].content = '{"overwritten":true}';
	expect(snapshot[0].content).toBe('{"manual":true}');
});

it('QuickSetupModal save path does not rerender or expose built-in paths', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCurrentVariant\(\) \{[\s\S]*?\n\}/)?.[0] || '';

	expect(applyBlock).not.toBe('');
	expect(applyBlock).not.toMatch(/renderSelectedKey\s*\(/);
	expect(applyBlock).toMatch(/snapshotQuickSetupFiles\(editableFiles\.value\)/);
	expect(component).toMatch(/:readonly="isBuiltInQuickSetupSoftware\(selectedSoftwareDef\)"/);
	expect(component).toMatch(/renderGuard\.canCommit\(requestId, filesDirty\.value\)/);
});
