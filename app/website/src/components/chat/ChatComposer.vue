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
      <!-- 动作槽（Task 9，对齐手机端交替语义）：非 running → 发送键；running
           且输入非空 → 发送键（点击走既有 steer 分流——emit('send') 不变，
           label 沿用 插话中/发送，disabled 沿用 sendDisabled）；running 且输入
           空 → 停止键（emit('stop') 接 ai.stop，目标 id 与 steer 同构）。
           Task 8 占位的「running 即整键禁用」死路径已删——触控端 running 时
           两个键位至少有一个可点。 -->
      <button
        v-if="running && !hasInput"
        type="button"
        data-testid="chat-stop-button"
        class="min-h-[40px] flex shrink-0 items-center gap-1 rounded-lg bg-slate-200 px-4 text-sm font-semibold text-slate-500 transition hover:bg-slate-300 md:min-h-0 dark:bg-slate-700 dark:text-slate-400 dark:hover:bg-slate-600"
        @click="emit('stop')"
      >
        <span class="material-symbols-outlined text-base leading-none">stop_circle</span>
        <span>{{ t('chat_stop') }}</span>
      </button>
      <button
        v-else
        type="button"
        class="min-h-[40px] shrink-0 rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90 md:min-h-0"
        :disabled="disabled"
        @click="emit('send')"
      >{{ label }}</button>
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
  // running（会话级并集判定：本地流式气泡 OR 设备壳清单/快照 running）→
  // 动作槽进入 发送/停止 交替语义（Task 9；门控源与 steer 分流同源）。
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
  // running 且输入空时停止键的点击出口（Task 9）：ChatPage 接 stopRun 发 ai.stop。
  'stop',
  'slash-apply',
  'slash-index',
  'slash-close',
  'slash-input',
]);

const { t } = useI18n();

// 输入是否「实义非空」：空白按空判定——与 sendMessage 的 trim 守卫同口径，
// 别让「看似可发、点了无操作」的死发送键占住停止键位。
const hasInput = computed(() => props.modelValue.trim() !== '');

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
// 卸载时以 null 落值，两侧同步清空）。元素交接即 autosize 的确切可用时机：
// 挂载初值（恢复草稿等）在此同步一次高度。
let inputEl = null;
function bindInputEl(el) {
  inputEl = el;
  props.setInputEl(el);
  if (el) autosize();
}

function onInput() {
  autosize();
  emit('slash-input');
}

// 发送后（或任意清空）复位单行；外部赋值（e2e fill/applySlash 补全）走
// 同一 watcher 同步高度。挂载初值的高度同步由 bindInputEl 交接回调承担
// （元素可用的确切时机）——不设 immediate：setup 期模板 ref 未绑定，
// immediate 只会撞 autosize 空守卫空转。
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
