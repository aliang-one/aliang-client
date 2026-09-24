import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  createLatestRenderGuard,
  createQuickSetupModeState,
  filterInstalledQuickSetupSoftwares,
  findUnresolvedPlaceholders,
  isBuiltInQuickSetupSoftware,
  renderComboContent,
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

describe('filterInstalledQuickSetupSoftwares', () => {
	it('keeps installed built-ins and all customs, drops uninstalled built-ins', () => {
		const list = [
			{ code: 'opencode', installed: true },
			{ code: 'codex', installed: false },
			{ code: 'claude-code', installed: true },
			{ code: 'custom-abc', isCustom: true },
		];
		expect(filterInstalledQuickSetupSoftwares(list).map((s) => s.code))
			.toEqual(['opencode', 'claude-code', 'custom-abc']);
	});
	it('returns customs even when nothing is installed', () => {
		expect(filterInstalledQuickSetupSoftwares([{ code: 'codex', installed: false }, { code: 'custom-x', isCustom: true }]).map((s) => s.code)).toEqual(['custom-x']);
	});
});

describe('quickSetupMode', () => {
	it('defaults to public and toggles per software', () => {
		const state = createQuickSetupModeState();
		expect(state.modeOf('codex')).toBe('public');
		state.setMode('codex', 'local');
		expect(state.modeOf('codex')).toBe('local');
		expect(state.modeOf('opencode')).toBe('public');
	});
});

describe('renderComboContent', () => {
	it('replaces all three placeholders and does not rescan values', () => {
		const out = renderComboContent('u={{base_url}} k={{api_key}} m={{model}}', {
			base_url: 'http://127.0.0.1:56432/v1',
			api_key: 'sk-{{x}}',
			model: 'm',
		});
		expect(out).toBe('u=http://127.0.0.1:56432/v1 k=sk-{{x}} m=m');
	});
	it('handles missing variables by leaving placeholders', () => {
		expect(renderComboContent('{{base_url}}', {})).toBe('{{base_url}}');
	});
	it('tolerates whitespace inside placeholders (same as validator)', () => {
		expect(renderComboContent('{{ api_key }}', { api_key: 'sk-1' })).toBe('sk-1');
	});
	it('does not rescan replacement values even if they contain a known key token', () => {
		const out = renderComboContent('u={{base_url}}', {
			base_url: 'http://h/{{model}}',
			model: 'gpt-x',
		});
		expect(out).toBe('u=http://h/{{model}}');
	});
	it('treats nullish input and values defensively', () => {
		expect(renderComboContent(null)).toBe('');
		expect(renderComboContent('{{api_key}}', { api_key: null })).toBe('');
	});
});

describe('findUnresolvedPlaceholders', () => {
	it('lists remaining placeholders with whitespace tolerance', () => {
		expect(findUnresolvedPlaceholders('a {{api_key}} b {{ model }} c {{model}'))
			.toEqual(['api_key', 'model']);
	});
	it('returns empty for clean content', () => {
		expect(findUnresolvedPlaceholders('clean')).toEqual([]);
	});
	it('returns empty for nullish content', () => {
		expect(findUnresolvedPlaceholders(null)).toEqual([]);
	});
});
