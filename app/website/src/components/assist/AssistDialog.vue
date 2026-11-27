<template>
  <div
    class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4"
    data-testid="assist-dialog"
    @click.self="handleClose"
  >
    <div
      class="w-full max-w-md rounded-lg border border-slate-200 bg-white shadow-xl dark:border-slate-700 dark:bg-slate-900"
      role="dialog"
      aria-modal="true"
      :aria-label="t('assist_dialog_title')"
    >
      <header class="flex items-center justify-between border-b border-slate-100 px-5 py-3 dark:border-slate-800">
        <h3 class="text-base font-bold text-slate-800 dark:text-slate-100">{{ t('assist_dialog_title') }}</h3>
        <button
          type="button"
          class="text-slate-400 hover:text-slate-600 dark:hover:text-slate-200"
          :aria-label="t('assist_close')"
          @click="handleClose"
        >
          <span class="material-symbols-outlined text-lg">close</span>
        </button>
      </header>

      <div class="space-y-4 px-5 py-4">
        <!-- 入口 / 终态:申请表单(终态时先显示结束原因,允许重新申请) -->
        <template v-if="state === 'idle' || state === 'ended'">
          <p
            v-if="state === 'ended'"
            class="rounded border border-slate-200 bg-slate-50 px-3 py-2 text-sm text-slate-600 dark:border-slate-700 dark:bg-slate-800/40 dark:text-slate-300"
          >
            {{ t(assistEndReasonKey(endReason)) }}
          </p>

          <!-- 授权边界:任何申请前都必须始终可见 -->
          <p
            class="rounded border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-200"
            data-testid="assist-authorization-notice"
          >
            {{ t('assist_dialog_desc') }}
          </p>

          <div class="space-y-1">
            <label
              for="assist-message-input"
              class="block text-xs font-medium text-slate-500 dark:text-slate-400"
            >
              {{ t('assist_message_label') }}
            </label>
            <textarea
              id="assist-message-input"
              ref="textareaRef"
              v-model="draft"
              maxlength="200"
              rows="3"
              class="w-full resize-none rounded border border-slate-200 bg-white px-3 py-2 text-sm text-slate-800 outline-none focus:border-primary dark:border-slate-700 dark:bg-slate-950 dark:text-slate-100"
              :placeholder="t('assist_message_placeholder')"
              data-testid="assist-message-input"
            />
            <div class="text-right text-[11px] text-slate-400">{{ draft.length }}/200</div>
          </div>

          <p v-if="lastError" class="text-sm text-rose-600 dark:text-rose-400" data-testid="assist-error">
            {{ t(lastError) }}
          </p>

          <div class="flex justify-end gap-2">
            <button
              type="button"
              class="rounded border border-slate-200 px-4 py-2 text-sm font-medium text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              @click="handleClose"
            >
              {{ t('assist_close') }}
            </button>
            <button
              type="button"
              class="rounded bg-primary px-4 py-2 text-sm font-bold text-white hover:bg-primary/90 disabled:opacity-60"
              :disabled="busy"
              data-testid="assist-submit"
              @click="handleSubmit"
            >
              {{ t('assist_submit') }}
            </button>
          </div>
        </template>

        <!-- 待审批:等待管理员确认 + 取消 -->
        <template v-else-if="state === 'requested'">
          <div class="flex items-center gap-3 text-sm text-slate-700 dark:text-slate-200">
            <span class="inline-block h-2 w-2 animate-pulse rounded-full bg-primary" />
            {{ t('assist_waiting') }}
          </div>
          <p
            v-if="message"
            class="rounded border border-slate-100 bg-slate-50 px-3 py-2 text-sm text-slate-500 dark:border-slate-800 dark:bg-slate-800/40 dark:text-slate-400"
          >
            {{ message }}
          </p>
          <p v-if="lastError" class="text-sm text-rose-600 dark:text-rose-400">
            {{ t(lastError) }}
          </p>
          <div class="flex justify-end gap-2">
            <button
              type="button"
              class="rounded border border-slate-200 px-4 py-2 text-sm font-medium text-slate-600 hover:bg-slate-50 disabled:opacity-60 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800"
              :disabled="busy"
              data-testid="assist-cancel"
              @click="handleCancel"
            >
              {{ t('assist_cancel') }}
            </button>
          </div>
        </template>

        <!-- 已开始:交给观看页(Task 10 的 /assist 路由) -->
        <template v-else>
          <p class="text-sm text-slate-700 dark:text-slate-200">{{ t('assist_started') }}</p>
          <div class="flex justify-end">
            <button
              type="button"
              class="rounded bg-primary px-4 py-2 text-sm font-bold text-white hover:bg-primary/90"
              @click="handleClose"
            >
              {{ t('assist_close') }}
            </button>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue';
import { useI18n } from '../../i18n';
import { useAssist, assistEndReasonKey } from '../../composables/useAssist';
import { useEscLayer } from '../../composables/useEscStack';

const emit = defineEmits(['close', 'watch']);
const { t } = useI18n();

const {
  state,
  endReason,
  message,
  lastError,
  busy,
  refresh,
  request,
  cancel,
  startPolling,
  stopPolling,
  resetTransient,
} = useAssist();

const draft = ref('');
const textareaRef = ref(null);

// 状态机推进:requested 态开 2s 轮询等审批;一旦 active 交由父级导航到
// 观看页(本组件保持 dumb,不持有路由)。刻意不用 immediate:true——
// 持久单例的陈旧 active/ended 会在 setup 期同步闪关/显示旧原因,初始渲染
// 改由 onMounted 的 resetTransient + refresh 驱动。
watch(state, (next) => {
  if (next === 'requested') {
    startPolling();
  } else {
    stopPolling();
  }
  if (next === 'active') {
    emit('watch');
  }
});

onMounted(() => {
  // 打开即复位瞬态(保留 gates),再对齐服务端真源(可能已有进行中的会话)。
  resetTransient();
  refresh();
  nextTick(() => {
    if (textareaRef.value && typeof textareaRef.value.focus === 'function') {
      textareaRef.value.focus();
    }
  });
});

onUnmounted(() => {
  stopPolling();
});

// Esc 关闭(house 约定:v-if 挂载期恒 active)。
useEscLayer('assist-dialog', handleClose, computed(() => true));

async function handleSubmit() {
  const ok = await request(draft.value.trim());
  if (ok) draft.value = '';
}

async function handleCancel() {
  await cancel();
}

function handleClose() {
  stopPolling();
  emit('close');
}
</script>
