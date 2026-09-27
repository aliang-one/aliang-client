import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

import {
  diffRowsAligned,
  findUnresolvedPlaceholders,
  groupDiffHunks,
  renderComboContent,
} from './quickSetupState.js';

it('QuickSetupModal combo flow renders locally and prechecks variable values before apply', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');
  const applyBlock = component.match(/async function applyCombo\(\) \{[\s\S]*?\n\}/)?.[0] || '';

  expect(applyBlock).not.toBe('');
  // v2 服务端渲染链（render/models）必须零引用
  expect(component).not.toMatch(/renderQuickSetup|getQuickSetupModels/);
  // apply 内容 = renderComboContent(最终内容, 变量) 逐文件产物（手动编辑预览优先），路径/格式/种类取自 software 声明
  expect(applyBlock).toMatch(/renderComboContent\(previewEdits\.value\[file\.code\] \?\? file\.content, vars\)/);
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

it('QuickSetupModal merge editor keeps a persistent editable preview with inline hunk adopt buttons', () => {
  const component = readFileSync(new URL('../components/QuickSetupModal.vue', import.meta.url), 'utf8');

  // 右列 textarea 常驻可编辑：:value = 有效预览（previewEdits 优先），input 即写 previewEdits
  expect(component).toMatch(/:value="effectivePreviewContent"/);
  expect(component).toMatch(/@input="onPreviewInput"/);
  const inputBlock = component.match(/function onPreviewInput\(event\) \{[\s\S]*?\n\}/)?.[0] || '';
  expect(inputBlock).not.toBe('');
  expect(inputBlock).toMatch(/previewEdits\.value = \{ \.\.\.previewEdits\.value, \[code\]: value \}/);
  // 与纯渲染一致时不留编辑记录（「预览已手动修改」徽标准确）
  expect(inputBlock).toMatch(/delete previewEdits\.value\[code\]/);

  // 两列布局：左列按 hunk 分块渲染（连续变更行整块红底），「采用此块 →」按钮内联块上，
  // 块级并入预览；无 13rem 中栏卡片列
  expect(component).toMatch(/groupDiffHunks/);
  expect(component).toMatch(/diffLeftSegments/);
  expect(component).toMatch(/qs_diff_adopt_block/);
  expect(component).not.toMatch(/qs_merge_hunk|qs_merge_adopt_left|qs_merge_no_changes/);
  expect(component).not.toMatch(/13rem/);
  expect(component).toMatch(/@click="adoptHunk\(segment\.hunk\)"/);
  const hunkBlock = component.match(/function adoptHunk\(hunk\) \{[\s\S]*?\n\}/)?.[0] || '';
  expect(hunkBlock).not.toBe('');
  // 基于当前预览内容行数组，把 [rightStart, rightStart+rightLines.length) 整块替换为 leftLines
  expect(hunkBlock).toMatch(/effectivePreviewContent\.value/);
  expect(hunkBlock).toMatch(/hunk\.rightStart/);
  expect(hunkBlock).toMatch(/hunk\.rightLines/);
  expect(hunkBlock).toMatch(/hunk\.leftLines/);
  expect(hunkBlock).toMatch(/previewEdits\.value = \{ \.\.\.previewEdits\.value, \[code\]: next \}/);
  expect(hunkBlock).toMatch(/delete previewEdits\.value\[code\]/);

  // 「编辑预览」切换退役：textarea 常驻，无 previewEditing/previewDraft 双态与离开守卫
  expect(component).not.toMatch(/previewEditing|previewDraft|confirmDiscardPreviewDraft|applyDiffLineAction/);
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
			{ left: 'a', right: 'a', type: 'same', leftIndex: 0, rightIndex: 0 },
			{ left: 'b', right: 'b', type: 'same', leftIndex: 1, rightIndex: 1 },
			{ left: 'c', right: 'c', type: 'same', leftIndex: 2, rightIndex: 2 },
		]);
	});
	it('marks pure additions with left=null rows after the shared prefix', () => {
		expect(diffRowsAligned('a', 'a\nb\nc')).toEqual([
			{ left: 'a', right: 'a', type: 'same', leftIndex: 0, rightIndex: 0 },
			{ left: null, right: 'b', type: 'added', rightIndex: 1 },
			{ left: null, right: 'c', type: 'added', rightIndex: 2 },
		]);
	});
	it('marks pure deletions with right=null rows after the shared prefix', () => {
		expect(diffRowsAligned('a\nb\nc', 'a')).toEqual([
			{ left: 'a', right: 'a', type: 'same', leftIndex: 0, rightIndex: 0 },
			{ left: 'b', right: null, type: 'removed', leftIndex: 1, rightIndex: 1 },
			{ left: 'c', right: null, type: 'removed', leftIndex: 2, rightIndex: 1 },
		]);
	});
	it('aligns a middle rewrite as one paired changed row between same rows', () => {
		expect(diffRowsAligned('x=1\ny=2\nz=3', 'x=1\ny=9\nz=3')).toEqual([
			{ left: 'x=1', right: 'x=1', type: 'same', leftIndex: 0, rightIndex: 0 },
			{ left: 'y=2', right: 'y=9', type: 'changed', leftIndex: 1, rightIndex: 1 },
			{ left: 'z=3', right: 'z=3', type: 'same', leftIndex: 2, rightIndex: 2 },
		]);
	});
	it('aligns repeated lines without over-matching (trailing duplicate removed)', () => {
		expect(diffRowsAligned('a\na', 'a')).toEqual([
			{ left: 'a', right: 'a', type: 'same', leftIndex: 0, rightIndex: 0 },
			{ left: 'a', right: null, type: 'removed', leftIndex: 1, rightIndex: 1 },
		]);
	});
	it('pairs unequal removed/added runs and keeps the leftover unpaired', () => {
		expect(diffRowsAligned('k1\nk2\nkeep', 'v1\nkeep')).toEqual([
			{ left: 'k1', right: 'v1', type: 'changed', leftIndex: 0, rightIndex: 0 },
			{ left: 'k2', right: null, type: 'removed', leftIndex: 1, rightIndex: 1 },
			{ left: 'keep', right: 'keep', type: 'same', leftIndex: 2, rightIndex: 1 },
		]);
	});
	it('degrades to all removed then all added beyond the 800-line LCS bound', () => {
		const left = Array.from({ length: 801 }, (_, i) => `old-${i}`).join('\n');
		const right = Array.from({ length: 801 }, (_, i) => `new-${i}`).join('\n');
		const rows = diffRowsAligned(left, right);
		expect(rows).toHaveLength(1602);
		expect(rows[0]).toEqual({ left: 'old-0', right: null, type: 'removed', leftIndex: 0, rightIndex: 0 });
		expect(rows[800]).toEqual({ left: 'old-800', right: null, type: 'removed', leftIndex: 800, rightIndex: 0 });
		expect(rows[801]).toEqual({ left: null, right: 'new-0', type: 'added', rightIndex: 0 });
		expect(rows.every((row, i) => (i < 801 ? row.type === 'removed' : row.type === 'added'))).toBe(true);
	});
	it('still runs bounded LCS at exactly the 800-line limit', () => {
		const left = Array.from({ length: 800 }, (_, i) => `old-${i}`).join('\n');
		const right = Array.from({ length: 800 }, (_, i) => `new-${i}`).join('\n');
		const rows = diffRowsAligned(left, right);
		// 800 行全无交集：LCS 正常跑完（不退化），成对合并为 800 条 changed 行
		expect(rows).toHaveLength(800);
		expect(rows.every((row) => row.type === 'changed')).toBe(true);
		expect(rows[0]).toEqual({ left: 'old-0', right: 'new-0', type: 'changed', leftIndex: 0, rightIndex: 0 });
	});
	it('defends nullish input as empty content', () => {
		expect(diffRowsAligned(null, undefined)).toEqual([{ left: '', right: '', type: 'same', leftIndex: 0, rightIndex: 0 }]);
		expect(diffRowsAligned(null, 'a')).toEqual([{ left: '', right: 'a', type: 'changed', leftIndex: 0, rightIndex: 0 }]);
	});
	it('gives removed rows rightIndex as the adopt-insertion position (insert before it)', () => {
		// 多集场景：left 'a\na' vs right 'a' → removed 行 rightIndex=1，
		// 在预览 index 1 前插入 left 内容即完全还原 left
		const [, removed] = diffRowsAligned('a\na', 'a');
		expect(removed).toMatchObject({ left: 'a', type: 'removed', leftIndex: 1, rightIndex: 1 });
		const lines = 'a'.split('\n');
		lines.splice(removed.rightIndex, 0, removed.left);
		expect(lines.join('\n')).toBe('a\na');
		expect(diffRowsAligned('a\na', lines.join('\n')).every((row) => row.type === 'same')).toBe(true);
	});
	it('supports adopting a whole removed block click by click (fresh indices per recompute)', () => {
		const left = 'h1\nh2\nh3\ntail';
		let preview = 'tail';
		for (const want of ['h1', 'h2', 'h3']) {
			// 每次点击后 diff 重算，行对象取自最新一次对齐（与 Vue 响应式渲染一致）
			const row = diffRowsAligned(left, preview).find((r) => r.type === 'removed' && r.left === want);
			expect(row).toBeTruthy();
			const lines = preview.split('\n');
			lines.splice(row.rightIndex, 0, row.left);
			preview = lines.join('\n');
		}
		expect(preview).toBe(left);
	});
	it('supports dropping added lines one by one via rightIndex (fresh indices per recompute)', () => {
		const left = 'a';
		let preview = 'a\nb\nc';
		for (const want of ['b', 'c']) {
			const row = diffRowsAligned(left, preview).find((r) => r.type === 'added' && r.right === want);
			expect(row).toBeTruthy();
			const lines = preview.split('\n');
			lines.splice(row.rightIndex, 1);
			preview = lines.join('\n');
		}
		expect(preview).toBe('a');
	});
	it('omits leftIndex on added rows (only rightIndex is meaningful)', () => {
		const rows = diffRowsAligned('a', 'a\nb');
		expect(rows[1]).toEqual({ left: null, right: 'b', type: 'added', rightIndex: 1 });
	});
});

describe('groupDiffHunks', () => {
	it('returns empty array when there is no change', () => {
		expect(groupDiffHunks(diffRowsAligned('a\nb\nc', 'a\nb\nc'))).toEqual([]);
	});
	it('returns empty array for nullish input', () => {
		expect(groupDiffHunks(null)).toEqual([]);
		expect(groupDiffHunks(undefined)).toEqual([]);
	});
	it('groups one mixed removed+added hunk with correct indices', () => {
		// 对齐产物：same(a) changed(x→p) changed(y→q) removed(z) same(b)
		expect(groupDiffHunks(diffRowsAligned('a\nx\ny\nz\nb', 'a\np\nq\nb'))).toEqual([
			{ leftStart: 1, leftLines: ['x', 'y', 'z'], rightStart: 1, rightLines: ['p', 'q'] },
		]);
	});
	it('splits multiple hunks separated by same rows', () => {
		// 两个独立改动点被 same(h/mid/tail) 分隔，各成一块
		expect(groupDiffHunks(diffRowsAligned('h\nA\nmid\nB\ntail', 'h\nA2\nmid\nB2\ntail'))).toEqual([
			{ leftStart: 1, leftLines: ['A'], rightStart: 1, rightLines: ['A2'] },
			{ leftStart: 3, leftLines: ['B'], rightStart: 3, rightLines: ['B2'] },
		]);
	});
	it('marks a pure addition hunk with empty leftLines and null leftStart', () => {
		expect(groupDiffHunks(diffRowsAligned('a', 'a\nb\nc'))).toEqual([
			{ leftStart: null, leftLines: [], rightStart: 1, rightLines: ['b', 'c'] },
		]);
	});
	it('marks a pure removal hunk with empty rightLines', () => {
		expect(groupDiffHunks(diffRowsAligned('a\nb\nc', 'a'))).toEqual([
			{ leftStart: 1, leftLines: ['b', 'c'], rightStart: 1, rightLines: [] },
		]);
	});
	it('keys pure-added hunks null so left-column blocks match by leftStart without shifting', () => {
		// 左列内联「采用此块」按 leftStart 关联变更块：纯 added hunk（leftStart=null，
		// 左列无块）不得让后续变更块错配到它（序号递增遍历会串位的场景）
		const rows = diffRowsAligned('a\nc\nd', 'a\nb\nc');
		const hunks = groupDiffHunks(rows);
		expect(hunks).toEqual([
			{ leftStart: null, leftLines: [], rightStart: 1, rightLines: ['b'] },
			{ leftStart: 2, leftLines: ['d'], rightStart: 3, rightLines: [] },
		]);
		// 块首行 leftIndex === 所属 hunk.leftStart（diffLeftSegments 的匹配键）
		const firstLeftChanged = rows.find((row) => row.type === 'removed' || row.type === 'changed');
		expect(hunks.find((h) => h.leftLines.length).leftStart).toBe(firstLeftChanged.leftIndex);
	});
	it('adopts a hunk by splicing rightStart..rightStart+rightLines back to leftLines (diff converges)', () => {
		// 块级「采用在用」对拍：按块索引替换后，diff 应收敛为全 same（多块逐块采用亦然）
		const left = 'h\nA\nmid\nB\ntail';
		let preview = 'h\nA2\nmid\nB2\ntail';
		for (let round = 0; round < 2; round += 1) {
			const [hunk] = groupDiffHunks(diffRowsAligned(left, preview));
			if (!hunk) {
				break;
			}
			const lines = preview.split('\n');
			lines.splice(hunk.rightStart, hunk.rightLines.length, ...hunk.leftLines);
			preview = lines.join('\n');
		}
		expect(preview).toBe(left);
		expect(diffRowsAligned(left, preview).every((row) => row.type === 'same')).toBe(true);
	});
});
