<template>
  <!-- Input -->
  <div class="border-t border-slate-200 p-4 dark:border-slate-700">
    <!-- steer 阻断提示（Task 4）：壳 running 且无可用 steer 目标
         （claude_/codex_ 壳无 managed id；agentcore 裸 id 已回退 entry.id），
         插话不可能（不发帧、绝不引导重发为普通消息）——常驻说明而非提交后报错。 -->
    <div
      v-if="steerBlocked"
      data-testid="chat-steer-blocked-hint"
      class="mb-2 rounded-lg border border-slate-200 bg-slate-100 px-3 py-2 text-xs text-slate-500 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-300"
    >
      {{ t('chat_steerBlocked') }}
    </div>
    <div class="flex items-end gap-2">
      <div class="relative min-w-0 flex-1">
        <div
          v-if="slashOpen && slashMatches.length"
          class="absolute bottom-full left-0 z-20 mb-2 max-h-60 w-full overflow-auto rounded-lg border border-slate-200 bg-white shadow-lg dark:border-slate-600 dark:bg-slate-800"
        >
          <button
            v-for="(c, i) in slashMatches"
            :key="c.name"
            type="button"
            class="flex w-full items-baseline gap-2 px-3 py-2 text-left text-sm transition hover:bg-primary/10"
            :class="i === slashIndex ? 'bg-primary/10' : ''"
            @click="emit('slash-apply', c)"
            @mouseenter="emit('slash-index', i)"
          >
            <span class="font-mono font-medium text-primary">/{{ c.name }}</span>
            <span v-if="c.arg_hint" class="font-mono text-xs text-slate-400">{{ c.arg_hint }}</span>
            <span class="truncate text-xs text-slate-500 dark:text-slate-400">{{ c.description }}</span>
          </button>
        </div>
        <!-- 胶囊容器 + 自增高 textarea（≤6 行 = 144px）。focus 环挂在容器
             focus-within 上；触控定式 min-h-[40px] md:min-h-0 保留在 textarea
             与发送键上。 -->
        <div
          class="flex items-end rounded-2xl border border-slate-200 bg-white transition focus-within:border-primary dark:border-slate-600 dark:bg-slate-800"
        >
          <textarea
            :ref="bindInputEl"
            v-model="inputModel"
            rows="1"
            :placeholder="placeholder"
            :disabled="disabled"
            class="min-h-[40px] w-full resize-none bg-transparent px-3 py-2 text-sm text-slate-700 outline-none placeholder:text-slate-400 dark:text-slate-100 dark:placeholder:text-slate-500 md:min-h-0"
            @input="onInput"
            @keydown="onInputKeydown"
            @blur="closeSlashDeferred"
          ></textarea>
        </div>
      </div>
      <button
        type="button"
        class="min-h-[40px] shrink-0 rounded-lg px-4 text-sm font-semibold transition md:min-h-0"
        :class="running
          ? 'flex items-center gap-1 bg-slate-200 text-slate-500 dark:bg-slate-700 dark:text-slate-400'
          : 'bg-primary text-white hover:bg-primary/90'"
        :disabled="disabled || running"
        @click="emit('send')"
      >
        <!-- running 键位切换停止键（Task 8 仅 UI 占位与禁用态视觉，点击行为
             Task 9 接线）：disabled 兜底保证占位期点击无副作用。 -->
        <template v-if="running">
          <span class="material-symbols-outlined text-base leading-none">stop_circle</span>
          <span>{{ t('chat_stop') }}</span>
        </template>
        <template v-else>{{ label }}</template>
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed, watch } from 'vue';
import { useI18n } from '../../i18n';
import { isComposingEvent } from '../../composables/useAliangChat';

// 自增高上限：144px ≈ 6 行（行高 20px + 上下 padding）。
const TEXTAREA_MAX_HEIGHT = 144;

const props = defineProps({
  modelValue: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
  label: { type: String, default: '' },
  placeholder: { type: String, default: '' },
  // running（活跃会话流式中）→ 发送键位切停止键占位（Task 9 接点击行为）。
  running: { type: Boolean, default: false },
  steerBlocked: { type: Boolean, default: false },
  // slash 补全状态由 useAliangChat 持有（命令缓存被帧路由写、open/index 被
  // 输入事件写），本组件经 props 读、事件回写。
  slashOpen: { type: Boolean, default: false },
  slashMatches: { type: Array, default: () => [] },
  slashIndex: { type: Number, default: 0 },
  // 输入框元素交接：createConversation/applySlash 在 nextTick 里 focus（元素
  // 在本组件渲染，焦点驱动仍在 useAliangChat，与拆分前 ref="inputRef" 等价）。
  setInputEl: { type: Function, required: true },
});

const emit = defineEmits([
  'update:modelValue',
  'send',
  'slash-apply',
  'slash-index',
  'slash-close',
  'slash-input',
]);

const { t } = useI18n();

// 经 computed 承接原生 v-model：保留 Vue 对 IME 组合期的守卫语义（组合期间
// 不回写），事件顺序与拆分前一致（v-model 先写值，@input 再做 slash 求值）。
const inputModel = computed({
  get: () => props.modelValue,
  set: (v) => emit('update:modelValue', v),
});

// 自增高：输入即按内容撑开（auto → min(scrollHeight, 144px)）；发送后
// modelValue 清空，watch 复位单行。
function autosize() {
  const el = inputEl;
  if (!el) return;
  el.style.height = 'auto';
  el.style.height = `${Math.min(el.scrollHeight, TEXTAREA_MAX_HEIGHT)}px`;
}

function resetHeight() {
  const el = inputEl;
  if (!el) return;
  el.style.height = 'auto';
}

// 函数 ref 双职：本地记录元素（自增高用），同时把元素交接给 useAliangChat
// （焦点驱动权仍在彼处，createConversation/applySlash 的 nextTick focus 不变；
// 卸载时以 null 落值，两侧同步清空）。
let inputEl = null;
function bindInputEl(el) {
  inputEl = el;
  props.setInputEl(el);
}

function onInput() {
  autosize();
  emit('slash-input');
}

// 发送后（或任意清空）复位单行；外部赋值（e2e fill/applySlash 补全）走
// 同一 watcher 同步高度。
watch(() => props.modelValue, (v) => {
  if (!v) {
    resetHeight();
    return;
  }
  autosize();
});

// Unified keydown so the popup can intercept arrows/enter/tab/escape before the
// send-on-enter binding fires.
function onInputKeydown(e) {
  // IME 组合期守卫（Task 8 新增，textarea 迁移时顺带补齐——原 input 迁移自
  // 拆分前时代的预存缺口）：组合期 Enter/Esc/方向键归属输入法（确认候选词/
  // 取消组合/选词），不发送、不 preventDefault、不触发 slash 键盘流。
  if (isComposingEvent(e)) return;
  const matches = props.slashMatches;
  if (props.slashOpen && matches.length) {
    if (e.key === 'ArrowDown') {
      emit('slash-index', (props.slashIndex + 1) % matches.length);
      e.preventDefault();
      return;
    }
    if (e.key === 'ArrowUp') {
      emit('slash-index', (props.slashIndex - 1 + matches.length) % matches.length);
      e.preventDefault();
      return;
    }
    if (e.key === 'Enter' || e.key === 'Tab') {
      emit('slash-apply', matches[props.slashIndex] || matches[0]);
      e.preventDefault();
      return;
    }
    if (e.key === 'Escape') {
      emit('slash-close');
      e.preventDefault();
      return;
    }
  }
  if (e.key === 'Enter') {
    // Shift+Enter 换行（textarea 语义），裸 Enter 发送。
    if (e.shiftKey) return;
    e.preventDefault();
    emit('send');
  }
}

// Close the popup shortly after the input loses focus so a click on a popup
// item still registers (mousedown fires before blur).
function closeSlashDeferred() {
  setTimeout(() => { emit('slash-close'); }, 150);
}
</script>
