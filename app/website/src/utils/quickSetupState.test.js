import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { EditorState } from '@codemirror/state';
import { StreamLanguage, syntaxTree } from '@codemirror/language';

import {
  diffRowsAligned,
  findUnresolvedPlaceholders,
  groupDiffHunks,
  patchLines,
  quickSetupLanguageExtension,
  renderComboContent,
} from './quickSetupState.js';

// 在给定语言扩展下解析 doc，返回语法树顶层节点名（区分同为 LanguageSupport 的
// json（JsonText）与 yaml（Stream），legacy 模式（toml/ini/properties/shell）直接
// 以 StreamLanguage 实例断言）
function topNodeName(extension, doc) {
  return syntaxTree(EditorState.create({ doc, extensions: [extension] })).topNode.name;
}

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
  const editors = readFileSync(new URL('../components/ConfigDiffEditors.vue', import.meta.url), 'utf8');

  // 双列 CodeMirror 编辑器：右列常驻可编辑，:right-content = 有效预览（previewEdits 优先），
  // 编辑输入经 @right-change 即写 previewEdits（单向数据流）
  expect(component).toMatch(/<ConfigDiffEditors/);
  expect(component).toMatch(/:right-content="effectivePreviewContent"/);
  expect(component).toMatch(/@right-change="onRightChange"/);
  expect(component).toMatch(/:left-content="liveContent \?\? ''"/);
  const inputBlock = component.match(/function onRightChange\(value\) \{[\s\S]*?\n\}/)?.[0] || '';
  expect(inputBlock).not.toBe('');
  expect(inputBlock).toMatch(/previewEdits\.value = \{ \.\.\.previewEdits\.value, \[code\]: next \}/);
  // 与纯渲染一致时不留编辑记录（「预览已手动修改」徽标准确）
  expect(inputBlock).toMatch(/delete previewEdits\.value\[code\]/);
  // 空态（无基线）仍保留全宽可编辑 textarea：「编辑能力永远在」
  expect(component).toMatch(/:value="effectivePreviewContent"/);
  expect(component).toMatch(/@input="onRightChange\(\$event\.target\.value\)"/);

  // 染色/变更块由 CM 行装饰取代自研行渲染（diffLeftSegments 退役），
  // 「采用此块 →」按钮由组件内 widget 浮钮承载，块级并入经 imperative patchRight
  expect(editors).toMatch(/diffRowsAligned/);
  expect(editors).toMatch(/groupDiffHunks/);
  expect(editors).toMatch(/qs_diff_adopt_block/);
  expect(editors).toMatch(/defineExpose\(\{ patchRight \}\)/);
  expect(component).not.toMatch(/diffLeftSegments|qs_merge_hunk|qs_merge_adopt_left|qs_merge_no_changes/);
  expect(component).not.toMatch(/13rem/);
  expect(component).toMatch(/@adopt-hunk="adoptHunk"/);
  const hunkBlock = component.match(/function adoptHunk\(hunk\) \{[\s\S]*?\n\}/)?.[0] || '';
  expect(hunkBlock).not.toBe('');
  // 基于 hunk 行区间（rightStart 起替换 rightLines.length 行为 leftLines）落右编辑器
  expect(hunkBlock).toMatch(/hunk\.rightStart/);
  expect(hunkBlock).toMatch(/hunk\.rightLines/);
  expect(hunkBlock).toMatch(/hunk\.leftLines/);
  expect(hunkBlock).toMatch(/patchRight\(hunk\.rightStart/);

  // 「编辑预览」切换退役：右列常驻，无 previewEditing/previewDraft 双态与离开守卫
  expect(component).not.toMatch(/previewEditing|previewDraft|confirmDiscardPreviewDraft|applyDiffLineAction/);

  // CM6 编辑器底座：只读左列 + 可撤销右列 + 语法高亮 + 装饰不回环（remote 同步不回报）
  expect(editors).toMatch(/EditorState\.readOnly\.of\(true\)/);
  expect(editors).toMatch(/historyKeymap/);
  expect(editors).toMatch(/quickSetupLanguageExtension/);
  expect(editors).toMatch(/Transaction\.remote/);
  // 外部全文替换必须双注解：history 只认 addToHistory=false（仅 remote 会复活陈旧文档）
  expect(editors).toMatch(/Transaction\.addToHistory\.of\(false\)/);
  expect(editors).toMatch(/StateField\.define/);
  // 行区间 → change-spec 映射走纯函数 patchLines（差分对拍固化在下方 describe）
  expect(editors).toMatch(/patchLines\(/);
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

describe('quickSetupLanguageExtension', () => {
	it('maps declared format json to the json language (format wins over path)', () => {
		const ext = quickSetupLanguageExtension('json', '/root/.claude/settings.yaml');
		expect(ext).not.toBeNull();
		expect(ext.language).toBeTruthy();
		expect(topNodeName(ext, '{"a": 1}')).toBe('JsonText');
	});

	it('maps declared format toml to the legacy toml StreamLanguage', () => {
		expect(quickSetupLanguageExtension('toml', '/x/no-extension')).toBeInstanceOf(StreamLanguage);
	});

	it('supplements language from path extension when format is absent', () => {
		// yaml/yml → yaml 语言（顶层节点 Stream，与 json 的 JsonText 区分）
		expect(topNodeName(quickSetupLanguageExtension('', '/root/.config/agent.yaml'), 'a: 1')).toBe('Stream');
		expect(quickSetupLanguageExtension('', '/root/.config/agent.YML')).not.toBeNull();
		// ini/conf/properties → 对应 legacy 模式
		expect(quickSetupLanguageExtension(null, '/etc/myapp/server.conf')).toBeInstanceOf(StreamLanguage);
		expect(quickSetupLanguageExtension('', '/etc/myapp/app.ini')).toBeInstanceOf(StreamLanguage);
		expect(quickSetupLanguageExtension('', '/etc/myapp/app.properties')).toBeInstanceOf(StreamLanguage);
		// sh/env → shell 模式
		expect(quickSetupLanguageExtension('', '/root/deploy.sh')).toBeInstanceOf(StreamLanguage);
		expect(quickSetupLanguageExtension('', '/root/.env')).toBeInstanceOf(StreamLanguage);
	});

	it('matches inputs case-insensitively', () => {
		expect(quickSetupLanguageExtension('JSON', '/x/a.json')).not.toBeNull();
		expect(quickSetupLanguageExtension('TOML', '/x/a.toml')).toBeInstanceOf(StreamLanguage);
	});

	it('returns null for unknown formats and extensions (plain text fallback)', () => {
		expect(quickSetupLanguageExtension('', '/x/readme.txt')).toBeNull();
		expect(quickSetupLanguageExtension('xml', '/x/config.xml')).toBeNull();
		expect(quickSetupLanguageExtension(null, null)).toBeNull();
		// 未登记的 format 值（如 yaml）回落路径匹配：/x/a.yaml 仍是 yaml 语言
		expect(topNodeName(quickSetupLanguageExtension('yaml', '/x/a.yaml'), 'a: 1')).toBe('Stream');
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

describe('patchLines', () => {
	// change-spec 应用到文本（CM dispatch 同语义：from 前 + insert + to 后）
	const apply = (text, change) => (change
		? text.slice(0, change.from) + (change.insert ?? '') + text.slice(change.to ?? change.from)
		: text);
	// Array.prototype.splice 语义的参考实现：对 lines 数组做区间替换后 join
	const spliceRef = (docText, start, removeCount, insert) => {
		const lines = docText.split('\n');
		lines.splice(start, removeCount, ...insert);
		return lines.join('\n');
	};
	// mulberry32：可复现随机源
	const rng = (seed) => () => {
		seed |= 0;
		seed = (seed + 0x6d2b79f5) | 0;
		let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
		t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
		return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
	};
	const WORDS = ['a', 'key = 1', 'x', '', '  indent', 'tail-1', 'longer line content', ''];

	// ≥200 组随机用例：合法区间直接与 splice 对拍（换行归属/行→offset 换算逐字节全等）
	it('matches Array.prototype.splice on 250 random in-range cases', () => {
		const rand = rng(20260927);
		for (let i = 0; i < 250; i += 1) {
			const lineCount = 1 + Math.floor(rand() * 8);
			const docText = Array.from({ length: lineCount }, () => WORDS[Math.floor(rand() * WORDS.length)]).join('\n');
			const start = Math.floor(rand() * (lineCount + 1));
			const removeCount = Math.floor(rand() * (lineCount - start + 1));
			const insert = Array.from({ length: Math.floor(rand() * 3) }, () => WORDS[Math.floor(rand() * WORDS.length)]);
			const change = patchLines(docText, start, removeCount, insert);
			const actual = apply(docText, change);
			const expected = spliceRef(docText, start, removeCount, insert);
			if (actual !== expected) {
				throw new Error(`case #${i} doc=${JSON.stringify(docText)} start=${start} remove=${removeCount} insert=${JSON.stringify(insert)}: ${JSON.stringify(actual)} != ${JSON.stringify(expected)}`);
			}
			expect(actual).toBe(expected);
		}
	});

	// 越界钳制用例：负 start/removeCount 归零、超界 start 钳到行数（末尾追加）、
	// removeCount 钳到剩余行数——参考实现先钳制再 splice（即 splice 的钳制前语义）
	it('clamps out-of-range start/removeCount and matches spliced reference (150 cases)', () => {
		const rand = rng(42);
		for (let i = 0; i < 150; i += 1) {
			const lineCount = 1 + Math.floor(rand() * 6);
			const docText = Array.from({ length: lineCount }, () => WORDS[Math.floor(rand() * WORDS.length)]).join('\n');
			const start = [-3, -1, 0, 1, lineCount, lineCount + 2, lineCount + 9][Math.floor(rand() * 7)];
			const removeCount = [-2, 0, 1, lineCount + 5][Math.floor(rand() * 4)];
			const insert = Array.from({ length: Math.floor(rand() * 3) }, () => WORDS[Math.floor(rand() * WORDS.length)]);
			const clampedStart = Math.min(Math.max(start, 0), lineCount);
			const clampedRemove = Math.min(Math.max(removeCount, 0), lineCount - clampedStart);
			const change = patchLines(docText, start, removeCount, insert);
			const actual = apply(docText, change);
			const expected = spliceRef(docText, clampedStart, clampedRemove, insert);
			if (actual !== expected) {
				throw new Error(`case #${i} doc=${JSON.stringify(docText)} start=${start} remove=${removeCount} insert=${JSON.stringify(insert)}: ${JSON.stringify(actual)} != ${JSON.stringify(expected)}`);
			}
			expect(actual).toBe(expected);
		}
	});

	it('handles the required deterministic scenarios (tail delete / append / multi-line replace / empty insert / single line / trailing newline)', () => {
		// 纯删尾：删到文档末尾连前导换行一起吞
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 2, 1, []))).toBe(spliceRef('a\nb\nc', 2, 1, []));
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 1, 2, []))).toBe('a');
		// 末尾追加：start = 行数（含单行文档 1 行的追加）
		expect(apply('a\nb', patchLines('a\nb', 2, 0, ['c']))).toBe('a\nb\nc');
		expect(apply('a', patchLines('a', 1, 0, ['b', 'c']))).toBe('a\nb\nc');
		expect(apply('', patchLines('', 0, 0, ['first']))).toBe('first\n');
		// 多行替换（删多插多 / 删一插多 / 删多插一）
		expect(apply('a\nb\nc\nd', patchLines('a\nb\nc\nd', 1, 2, ['x', 'y', 'z']))).toBe('a\nx\ny\nz\nd');
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 1, 1, ['B1', 'B2']))).toBe('a\nB1\nB2\nc');
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 0, 2, ['z']))).toBe('z\nc');
		// 空插入：区间空且无删除 → null（无变更免派发）
		expect(patchLines('a\nb', 0, 0, [])).toBeNull();
		expect(patchLines('a\nb', 5, 0, [])).toBeNull();
		// 行中空数组插入 = 纯删除语义
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 1, 1, []))).toBe('a\nc');
		// 单行文档：整行替换 / 清空
		expect(apply('only', patchLines('only', 0, 1, ['a', 'b']))).toBe('a\nb');
		expect(apply('only', patchLines('only', 0, 1, []))).toBe('');
		// 尾随换行文档（split 尾空行）
		expect(apply('a\nb\n', patchLines('a\nb\n', 2, 1, ['c']))).toBe('a\nb\nc');
		expect(apply('a\nb\n', patchLines('a\nb\n', 1, 2, []))).toBe('a');
		// 越界钳制定点：start<0 → 0、start>行数 → 末尾追加、removeCount<0 → 0、removeCount 超界 → 剩余全删
		expect(apply('a\nb\nc', patchLines('a\nb\nc', -5, 1, ['z']))).toBe('z\nb\nc');
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 99, 0, ['z']))).toBe('a\nb\nc\nz');
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 1, -7, ['z']))).toBe('a\nz\nb\nc');
		expect(apply('a\nb\nc', patchLines('a\nb\nc', 1, 99, []))).toBe('a');
		// nullish 容错（docText 按 '' 规范后应用）
		expect(apply('', patchLines(null, 0, 1, ['b']))).toBe('b');
		expect(apply('a\nb', patchLines('a\nb', 1, 1, [undefined, null]))).toBe('a\n\n');
	});
});
