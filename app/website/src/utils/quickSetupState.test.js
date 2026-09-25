import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  diffChangedLines,
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

it('QuickSetupModal diff view compares rendered preview with last-applied snapshot', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCombo\(\) \{[\s\S]*?\n\}/)?.[0] || '';

  // 两列 diff：diffChangedLines 标记变更行；右列取 applied 快照中该 code 的 content
  expect(component).toMatch(/diffChangedLines/);
  expect(component).toMatch(/item\?\.code === activeFileCode\.value/);
  // apply 携带 combo_id（后端持久化快照的锚点）
  expect(applyBlock).toMatch(/combo_id: combo\.id/);
  // apply 成功后乐观替换 applied/applied_at（整对象替换，不深改）
  expect(applyBlock).toMatch(/applied:/);
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
