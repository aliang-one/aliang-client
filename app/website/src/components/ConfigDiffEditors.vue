<template>
  <div class="grid min-h-0 flex-1 grid-cols-1 overflow-hidden md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
    <!-- 左列：在用配置只读（rose 变更行装饰 + 悬停「采用此块」浮钮） -->
    <div class="flex min-h-0 min-w-0 flex-col border-b border-slate-200 md:border-b-0 md:border-r dark:border-slate-700">
      <div class="flex shrink-0 flex-wrap items-center gap-x-1.5 border-b border-slate-200 bg-slate-100/70 px-3 py-2 text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-400">
        {{ t('qs_diff_left') }}
        <span
          v-if="leftMeta"
          class="font-mono text-[10px] font-normal normal-case tracking-normal text-slate-400 dark:text-slate-500"
        >{{ leftMeta }}</span>
      </div>
      <div
        ref="leftHostEl"
        class="code-editor min-h-0 flex-1 overflow-hidden bg-slate-950 text-[12px] text-slate-100"
        :aria-label="t('qs_diff_left')"
      ></div>
    </div>

    <!-- 右列：渲染预览可编辑（emerald added/changed 行装饰 + 撤销历史） -->
    <div class="flex min-h-0 min-w-0 flex-col">
      <div class="flex shrink-0 flex-wrap items-center gap-x-1.5 border-b border-slate-200 bg-slate-100/70 px-3 py-2 text-[11px] font-semibold uppercase tracking-wide text-slate-500 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-400">
        {{ t('qs_diff_right') }}
        <span
          v-if="rightModified"
          class="rounded bg-amber-400/15 px-1.5 py-0.5 text-[10px] font-medium normal-case tracking-normal text-amber-600 dark:bg-amber-400/10 dark:text-amber-400"
        >{{ t('qs_diff_preview_modified') }}</span>
      </div>
      <div
        ref="rightHostEl"
        class="code-editor min-h-0 flex-1 overflow-hidden bg-slate-950 text-[12px] text-slate-100"
        :aria-label="t('qs_diff_right')"
      ></div>
    </div>
  </div>
</template>

<script setup>
// diff 双列 CodeMirror 6 编辑器：左=在用配置只读，右=渲染预览可编辑。
// 语法高亮按 format/defaultPath 取 quickSetupLanguageExtension；diff 染色由
// diffRowsAligned 行索引映射为 CM 行装饰（StateField + Effect），取代旧的自研
// 行渲染。单向数据流：用户输入/块采用 → emit('right-change') 由父级写
// previewEdits；props.rightContent 外部变化 → 文档 remote 替换（不回环、不进撤销栈）。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue';
import { Compartment, EditorState, StateEffect, StateField, Transaction } from '@codemirror/state';
import { syntaxHighlighting } from '@codemirror/language';
import { Decoration, EditorView, WidgetType, drawSelection, keymap, lineNumbers } from '@codemirror/view';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { oneDarkHighlightStyle } from '@codemirror/theme-one-dark';
import { diffRowsAligned, groupDiffHunks, patchLines, quickSetupLanguageExtension } from '../utils/quickSetupState';
import { useI18n } from '../i18n';

const props = defineProps({
  // 左=在用配置（旧基线，只读）；右=有效预览（新，apply 所见）
  leftContent: { type: String, default: '' },
  rightContent: { type: String, default: '' },
  rightReadonly: { type: Boolean, default: false },
  // 文件声明的 format（json/toml…）与 default_path（扩展名补充 yaml/ini/sh…）
  format: { type: String, default: '' },
  defaultPath: { type: String, default: '' },
  // 左列头部的 modified_at 标签与右列「已手动修改」徽标（展示性透传）
  leftMeta: { type: String, default: '' },
  rightModified: { type: Boolean, default: false },
});
// right-change(payload: 新内容字符串)——父级写 previewEdits（与纯渲染一致时删键）
// adopt-hunk(payload: hunk)——左列「采用此块」按钮点击（父级可审计/忽略）
const emit = defineEmits(['right-change', 'adopt-hunk']);

const { t } = useI18n();
const leftHostEl = ref(null);
const rightHostEl = ref(null);
let leftView = null;
let rightView = null;

// —— diff 装饰管线：父级内容变化 → 重算行装饰集 → Effect 注入 StateField ——
const setDiffDecorations = StateEffect.define();
const diffDecorationsField = StateField.define({
  create: () => Decoration.none,
  update(value, tr) {
    const effect = tr.effects.find((item) => item.is(setDiffDecorations));
    return effect ? effect.value : value.map(tr.changes);
  },
  provide: (field) => EditorView.decorations.from(field),
});

// 变更块「采用此块 →」浮钮（锚定块首行右上角，悬停浮现；点击整块并入右列）
class HunkAdoptWidget extends WidgetType {
  constructor(hunk, label, onAdopt) {
    super();
    this.hunk = hunk;
    this.label = label;
    this.onAdopt = onAdopt;
  }

  eq(other) {
    return other.label === this.label
      && other.hunk.rightStart === this.hunk.rightStart
      && JSON.stringify(other.hunk.leftLines) === JSON.stringify(this.hunk.leftLines)
      && JSON.stringify(other.hunk.rightLines) === JSON.stringify(this.hunk.rightLines);
  }

  // 按钮自持 click，编辑器不得拦截
  ignoreEvent() {
    return true;
  }

  toDOM() {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'cm-diff-adopt-btn';
    button.textContent = this.label;
    button.addEventListener('click', (event) => {
      event.preventDefault();
      event.stopPropagation();
      this.onAdopt(this.hunk);
    });
    return button;
  }
}

// 深色编辑器主题：对齐旧版 12px/24px 行高与 slate-950 底；染行 rose/emerald；
// 滚动条样式对齐 .custom-scrollbar
const diffEditorTheme = EditorView.theme({
  '&': {
    height: '100%',
    backgroundColor: 'transparent',
    color: '#e2e8f0',
    fontSize: '12px',
  },
  '.cm-scroller': {
    fontFamily: "'SF Mono', 'Fira Code', 'JetBrains Mono', monospace",
    lineHeight: '24px',
    overflow: 'auto',
  },
  '.cm-content': { padding: '12px 0', caretColor: '#22d3ee' },
  '.cm-line': { padding: '0 12px', position: 'relative' },
  '.cm-gutters': {
    backgroundColor: 'rgba(15, 23, 42, 0.6)',
    border: 'none',
    color: '#475569',
  },
  '.cm-activeLine, .cm-activeLineGutter': { backgroundColor: 'transparent' },
  '&.cm-focused': { outline: 'none' },
  '.cm-selectionBackground, &.cm-focused .cm-selectionBackground': {
    backgroundColor: 'rgba(56, 189, 248, 0.25) !important',
  },
  '.cm-cursor': { borderLeftColor: '#22d3ee' },
  '.cm-scroller::-webkit-scrollbar': { width: '4px', height: '6px' },
  '.cm-scroller::-webkit-scrollbar-track': { backgroundColor: 'transparent' },
  '.cm-scroller::-webkit-scrollbar-thumb': {
    backgroundColor: 'rgba(100, 116, 139, 0.5)',
    borderRadius: '10px',
  },
  // removed/changed（在用配置将被移除/被替换）→ rose；added/changed（apply 将写入）→ emerald
  '.cm-diff-left-change': { backgroundColor: 'rgba(244, 63, 94, 0.15)', color: '#fecdd3' },
  '.cm-diff-right-change': { backgroundColor: 'rgba(16, 185, 129, 0.14)' },
  '.cm-diff-adopt-btn': {
    position: 'absolute',
    right: '6px',
    top: '1px',
    zIndex: 8,
    opacity: 0,
    transition: 'opacity 150ms',
    borderRadius: '8px',
    border: '1px solid rgba(253, 164, 175, 0.6)',
    backgroundColor: 'rgba(2, 6, 23, 0.9)',
    padding: '1px 6px',
    fontSize: '10px',
    fontWeight: '600',
    lineHeight: '16px',
    color: '#fecdd3',
  },
  '.cm-line:hover .cm-diff-adopt-btn, .cm-diff-adopt-btn:focus-visible': { opacity: 1 },
});

const languageCompartment = new Compartment();
const readonlyCompartment = new Compartment();
const adoptLabel = () => t('qs_diff_adopt_block');

function currentLanguage() {
  return quickSetupLanguageExtension(props.format, props.defaultPath) ?? [];
}

function readonlyExtensions(readonly) {
  return readonly ? [EditorView.editable.of(false), EditorState.readOnly.of(true)] : [];
}

// 右编辑器编辑/块采用路径：文档变化即上报（外部 remote 同步除外，防回环）
function onRightUpdate(update) {
  if (!update.docChanged) {
    return;
  }
  if (update.transactions.some((tr) => tr.annotation(Transaction.remote))) {
    return;
  }
  emit('right-change', update.state.doc.toString());
}

function makeState({ editable }) {
  const shared = [
    lineNumbers(),
    diffEditorTheme,
    drawSelection(),
    syntaxHighlighting(oneDarkHighlightStyle),
    languageCompartment.of(currentLanguage()),
    diffDecorationsField,
  ];
  if (!editable) {
    return EditorState.create({
      doc: String(props.leftContent ?? ''),
      extensions: [
        ...shared,
        EditorView.editable.of(false),
        EditorState.readOnly.of(true),
      ],
    });
  }
  return EditorState.create({
    doc: String(props.rightContent ?? ''),
    extensions: [
      ...shared,
      readonlyCompartment.of(readonlyExtensions(props.rightReadonly)),
      history(),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.updateListener.of(onRightUpdate),
    ],
  });
}

// props.rightContent 外部变化（adopt/restore/刷新/切 agent/变量重渲染）：remote
// 全文替换——不触发 right-change（remote 注解防回环）、不进撤销栈（须显式
// addToHistory=false：@codemirror/commands 的 history 只认 addToHistory=false、
// 不认 remote 注解，缺省注解会让外部替换成为独立历史事件，Ctrl+Z 复活替换前的
// 陈旧文档；外部替换只会以映射方式并入既有历史，Ctrl+Z 永不回滚替换本身，
// 用户先前的编辑仍可撤销）；光标按偏移就近钳制保留
function replaceDocIfChanged(view, next) {
  const text = String(next ?? '');
  if (!view || view.state.doc.toString() === text) {
    return;
  }
  const anchor = Math.min(view.state.selection.main.head, text.length);
  view.dispatch({
    changes: { from: 0, to: view.state.doc.length, insert: text },
    selection: { anchor },
    annotations: [Transaction.remote.of(true), Transaction.addToHistory.of(false)],
  });
}

// diff 行装饰：removed/changed → 左列 rose（块首行加「采用此块」浮钮），
// added/changed → 右列 emerald。行索引取自 diffRowsAligned（leftIndex/rightIndex
// 与两份文档行号一一对应）；hunk → 块首按 leftStart 匹配（纯 added hunk 无左行，
// 永不误配）。与纯渲染一致时同样染色（changed 行 = 两侧不同的行）
function rebuildDecorations() {
  if (!leftView || !rightView) {
    return;
  }
  const rows = diffRowsAligned(props.leftContent, props.rightContent);
  const hunks = groupDiffHunks(rows);
  const hunkByLeftStart = new Map();
  for (const hunk of hunks) {
    if (hunk && Number.isInteger(hunk.leftStart) && !hunkByLeftStart.has(hunk.leftStart)) {
      hunkByLeftStart.set(hunk.leftStart, hunk);
    }
  }
  const leftDoc = leftView.state.doc;
  const rightDoc = rightView.state.doc;
  const leftRanges = [];
  const rightRanges = [];
  const inRange = (doc, index) => index >= 0 && index < doc.lines;
  let inBlock = false;
  for (const row of rows) {
    const isLeftChange = row && (row.type === 'removed' || row.type === 'changed');
    if (!isLeftChange) {
      inBlock = false;
    } else if (inRange(leftDoc, row.leftIndex)) {
      const line = leftDoc.line(row.leftIndex + 1);
      if (!inBlock) {
        inBlock = true;
        const hunk = hunkByLeftStart.get(row.leftIndex);
        if (hunk) {
          leftRanges.push(Decoration.widget({
            widget: new HunkAdoptWidget(hunk, adoptLabel(), (payload) => emit('adopt-hunk', payload)),
            side: 1,
          }).range(line.from, line.from));
        }
      }
      leftRanges.push(Decoration.line({ class: 'cm-diff-left-change' }).range(line.from, line.from));
    }
    const isRightChange = row && (row.type === 'added' || row.type === 'changed');
    if (isRightChange && inRange(rightDoc, row.rightIndex)) {
      const line = rightDoc.line(row.rightIndex + 1);
      rightRanges.push(Decoration.line({ class: 'cm-diff-right-change' }).range(line.from, line.from));
    }
  }
  leftView.dispatch({ effects: setDiffDecorations.of(Decoration.set(leftRanges, true)) });
  rightView.dispatch({ effects: setDiffDecorations.of(Decoration.set(rightRanges, true)) });
}

// 左右滚动同步（scrollTop 简单联动；写入时置位防事件互激）
let scrollSyncing = false;
function syncScroll(fromView, toView) {
  if (scrollSyncing) {
    return;
  }
  scrollSyncing = true;
  toView.scrollDOM.scrollTop = fromView.scrollDOM.scrollTop;
  scrollSyncing = false;
}

let onLeftScroll = null;
let onRightScroll = null;

onMounted(() => {
  leftView = new EditorView({ state: makeState({ editable: false }), parent: leftHostEl.value });
  rightView = new EditorView({ state: makeState({ editable: true }), parent: rightHostEl.value });
  onLeftScroll = () => syncScroll(leftView, rightView);
  onRightScroll = () => syncScroll(rightView, leftView);
  leftView.scrollDOM.addEventListener('scroll', onLeftScroll, { passive: true });
  rightView.scrollDOM.addEventListener('scroll', onRightScroll, { passive: true });
  rebuildDecorations();
});

onBeforeUnmount(() => {
  if (leftView && onLeftScroll) {
    leftView.scrollDOM.removeEventListener('scroll', onLeftScroll);
  }
  if (rightView && onRightScroll) {
    rightView.scrollDOM.removeEventListener('scroll', onRightScroll);
  }
  leftView?.destroy();
  rightView?.destroy();
  leftView = null;
  rightView = null;
});

watch(
  () => [props.leftContent, props.rightContent],
  () => {
    replaceDocIfChanged(leftView, props.leftContent);
    replaceDocIfChanged(rightView, props.rightContent);
    rebuildDecorations();
  },
);
watch(
  () => [props.format, props.defaultPath],
  () => {
    const language = currentLanguage();
    leftView?.dispatch({ effects: languageCompartment.reconfigure(language) });
    rightView?.dispatch({ effects: languageCompartment.reconfigure(language) });
  },
);
watch(
  () => props.rightReadonly,
  (readonly) => {
    rightView?.dispatch({ effects: readonlyCompartment.reconfigure(readonlyExtensions(readonly)) });
  },
);

// 块级采用：把右文档 [rightStartLine, rightStartLine+removeCount) 行区间按
// split/join splice 语义替换为 insertLines——行→offset 的映射由纯函数 patchLines
// 承载（差分对拍固化），这里拿 change-spec 派发为普通事务（可撤销、经
// onRightUpdate 回报父级写 previewEdits）
function patchRight(rightStartLine, removeCount, insertLines) {
  const view = rightView;
  if (!view) {
    return;
  }
  const change = patchLines(view.state.doc.toString(), rightStartLine, removeCount, insertLines);
  if (change) {
    view.dispatch({ changes: change, userEvent: 'input.adopt' });
  }
}

defineExpose({ patchRight });
</script>
