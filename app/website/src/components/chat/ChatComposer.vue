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
    <div class="flex items-center gap-2">
      <div class="relative flex-1">
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
        <input
          :ref="setInputEl"
          v-model="inputModel"
          type="text"
          :placeholder="placeholder"
          :disabled="disabled"
          class="h-10 w-full rounded-lg border border-slate-200 bg-white px-3 text-sm text-slate-700 outline-none transition focus:border-primary dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
          @input="emit('slash-input')"
          @keydown="onInputKeydown"
          @blur="closeSlashDeferred"
        />
      </div>
      <button
        type="button"
        class="h-10 shrink-0 rounded-lg bg-primary px-4 text-sm font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="disabled"
        @click="emit('send')"
      >
        {{ label }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed } from 'vue';
import { useI18n } from '../../i18n';

const props = defineProps({
  modelValue: { type: String, default: '' },
  disabled: { type: Boolean, default: false },
  label: { type: String, default: '' },
  placeholder: { type: String, default: '' },
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

// Unified keydown so the popup can intercept arrows/enter/tab/escape before the
// send-on-enter binding fires.
function onInputKeydown(e) {
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
