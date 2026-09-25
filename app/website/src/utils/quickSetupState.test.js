import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  diffChangedLines,
  diffRowsAligned,
  findUnresolvedPlaceholders,
  renderComboContent,
} from './quickSetupState.js';

it('QuickSetupModal combo flow renders locally and prechecks variable values before apply', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCombo\(\) \{[\s\S]*?\n\}/)?.[0] || '';

  expect(applyBlock).not.toBe('');
  // v2 服务端渲染链（render/models）必须零引用
  expect(component).not.toMatch(/renderQuickSetup|getQuickSetupModels/);
  // apply 内容 = renderComboContent(模板, 变量) 逐文件产物，路径/格式/种类取自 software 声明
  expect(applyBlock).toMatch(/renderComboContent\(file\.content, vars\)/);
  expect(applyBlock).toMatch(/declared\.default_path/);
  // 值感知预检：渲染残留占位符 + 变量空值双重拦截
  expect(component).toMatch(/findUnresolvedPlaceholders\(renderComboContent\(content, vars\)\)/);
  expect(component).toMatch(/String\(vars\[name\] \?\? ''\)\.trim\(\)/);
});

it('QuickSetupModal diff view compares rendered preview with live config from config-state', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCombo\(\) \{[\s\S]*?\n\}/)?.[0] || '';

  // git 风格 split diff：diffRowsAligned 对齐行（左=在用配置，右=渲染预览），按文件 code 对齐
  expect(component).toMatch(/diffRowsAligned/);
  expect(component).not.toMatch(/diffChangedLines/);
  expect(component).toMatch(/activeLiveFile/);
  expect(component).toMatch(/file\?\.code === activeFileCode\.value/);
  // Modal 自取 config-state（loadCatalog 后 / 切 agent / apply 成功 / 手动刷新按钮）
  expect(component).toMatch(/fetchConfigState/);
  expect(component).toMatch(/refreshLiveFiles/);
  expect(component).toMatch(/qs_state_refresh/);
  // apply 不再传 combo_id（快照特性已回退）；成功后重取 config-state 让右列对齐磁盘
  expect(applyBlock).not.toMatch(/combo_id/);
  expect(applyBlock).toMatch(/refreshLiveFiles\(\)/);
  // 快照语义零残留（applied 快照 / applied_at 时间戳 / 乐观更新）
  expect(component).not.toMatch(/appliedContent|applied_at|applied:|appliedSnapshot/);
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

describe('diffRowsAligned', () => {
	it('returns all-same rows for identical content', () => {
		const rows = diffRowsAligned('a\nb\nc', 'a\nb\nc');
		expect(rows).toEqual([
			{ left: 'a', right: 'a', type: 'same' },
			{ left: 'b', right: 'b', type: 'same' },
			{ left: 'c', right: 'c', type: 'same' },
		]);
	});
	it('marks pure additions with left=null rows after the shared prefix', () => {
		expect(diffRowsAligned('a', 'a\nb\nc')).toEqual([
			{ left: 'a', right: 'a', type: 'same' },
			{ left: null, right: 'b', type: 'added' },
			{ left: null, right: 'c', type: 'added' },
		]);
	});
	it('marks pure deletions with right=null rows after the shared prefix', () => {
		expect(diffRowsAligned('a\nb\nc', 'a')).toEqual([
			{ left: 'a', right: 'a', type: 'same' },
			{ left: 'b', right: null, type: 'removed' },
			{ left: 'c', right: null, type: 'removed' },
		]);
	});
	it('aligns a middle rewrite as one paired changed row between same rows', () => {
		expect(diffRowsAligned('x=1\ny=2\nz=3', 'x=1\ny=9\nz=3')).toEqual([
			{ left: 'x=1', right: 'x=1', type: 'same' },
			{ left: 'y=2', right: 'y=9', type: 'changed' },
			{ left: 'z=3', right: 'z=3', type: 'same' },
		]);
	});
	it('aligns repeated lines without over-matching (trailing duplicate removed)', () => {
		expect(diffRowsAligned('a\na', 'a')).toEqual([
			{ left: 'a', right: 'a', type: 'same' },
			{ left: 'a', right: null, type: 'removed' },
		]);
	});
	it('pairs unequal removed/added runs and keeps the leftover unpaired', () => {
		expect(diffRowsAligned('k1\nk2\nkeep', 'v1\nkeep')).toEqual([
			{ left: 'k1', right: 'v1', type: 'changed' },
			{ left: 'k2', right: null, type: 'removed' },
			{ left: 'keep', right: 'keep', type: 'same' },
		]);
	});
	it('degrades to all removed then all added beyond the 800-line LCS bound', () => {
		const left = Array.from({ length: 801 }, (_, i) => `old-${i}`).join('\n');
		const right = Array.from({ length: 801 }, (_, i) => `new-${i}`).join('\n');
		const rows = diffRowsAligned(left, right);
		expect(rows).toHaveLength(1602);
		expect(rows[0]).toEqual({ left: 'old-0', right: null, type: 'removed' });
		expect(rows[800]).toEqual({ left: 'old-800', right: null, type: 'removed' });
		expect(rows[801]).toEqual({ left: null, right: 'new-0', type: 'added' });
		expect(rows.every((row, i) => (i < 801 ? row.type === 'removed' : row.type === 'added'))).toBe(true);
	});
	it('still runs bounded LCS at exactly the 800-line limit', () => {
		const left = Array.from({ length: 800 }, (_, i) => `old-${i}`).join('\n');
		const right = Array.from({ length: 800 }, (_, i) => `new-${i}`).join('\n');
		const rows = diffRowsAligned(left, right);
		// 800 行全无交集：LCS 正常跑完（不退化），成对合并为 800 条 changed 行
		expect(rows).toHaveLength(800);
		expect(rows.every((row) => row.type === 'changed')).toBe(true);
		expect(rows[0]).toEqual({ left: 'old-0', right: 'new-0', type: 'changed' });
	});
	it('defends nullish input as empty content', () => {
		expect(diffRowsAligned(null, undefined)).toEqual([{ left: '', right: '', type: 'same' }]);
		expect(diffRowsAligned(null, 'a')).toEqual([{ left: '', right: 'a', type: 'changed' }]);
	});
});

describe('diffChangedLines', () => {
	it('marks nothing when contents are identical', () => {
		const { leftFlags, rightFlags } = diffChangedLines('a\nb\nc', 'a\nb\nc');
		expect(leftFlags).toEqual([false, false, false]);
		expect(rightFlags).toEqual([false, false, false]);
	});
	it('flags lines that exist on only one side', () => {
		const { leftFlags, rightFlags } = diffChangedLines('a\nb\nc', 'a\nb');
		expect(leftFlags).toEqual([false, false, true]);
		expect(rightFlags).toEqual([false, false]);
	});
	it('flags changed line content on both sides', () => {
		const { leftFlags, rightFlags } = diffChangedLines('x=1\ny=2', 'x=1\ny=3');
		expect(leftFlags).toEqual([false, true]);
		expect(rightFlags).toEqual([false, true]);
	});
	it('treats duplicate lines as a multiset (one of two left "a" flagged)', () => {
		const { leftFlags, rightFlags } = diffChangedLines('a\na', 'a');
		expect(leftFlags.filter(Boolean)).toHaveLength(1);
		expect(leftFlags).toHaveLength(2);
		expect(rightFlags).toEqual([false]);
	});
	it('defends nullish input as empty content', () => {
		const { leftFlags, rightFlags } = diffChangedLines(null, undefined);
		expect(leftFlags).toEqual([false]);
		expect(rightFlags).toEqual([false]);
	});
});
