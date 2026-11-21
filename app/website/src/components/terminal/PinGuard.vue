<template>
  <!--
    保险箱密码盘（Task 14, plan 2026-11-21-web-terminal-phase1 §Task 14）：
    setup（首次设 PIN，双输入）/ unlock（解锁，单输入）两态展示组件。状态机由
    useWebTerminal 单例驱动；锁定失败 reason='locked' 起 1s 倒计时，倒计时中
    输入与提交禁用，归零/卸载清 ticker。文案键 Task 16 落 i18n 词表（缺失时
    t() 回落键名本身，仓内既定行为）。
  -->
  <div class="flex h-full w-full items-center justify-center bg-slate-50 dark:bg-slate-950">
    <div class="w-full max-w-sm rounded-xl border border-slate-200 bg-white p-6 shadow-sm dark:border-slate-700 dark:bg-slate-900">
      <h2 class="mb-1 text-lg font-semibold text-slate-900 dark:text-slate-100">{{ title }}</h2>
      <p v-if="subtitle" class="mb-4 text-sm text-slate-500 dark:text-slate-400">{{ subtitle }}</p>

      <!-- 锁定倒计时横幅：lockRemaining>0 期间输入/提交同步禁用 -->
      <div
        v-if="lockRemaining > 0"
        data-testid="vault-lock-banner"
        class="mb-4 rounded-lg bg-red-50 px-4 py-3 text-sm text-red-700 dark:bg-red-950/50 dark:text-red-300"
      >
        {{ t('vault_locked') }}<span class="font-mono">{{ lockRemaining }}</span>{{ t('vault_seconds_unit') }}
      </div>

      <label class="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">{{ pinLabel }}</label>
      <input
        ref="pinInput"
        v-model="pin"
        type="password"
        inputmode="numeric"
        maxlength="4"
        autocomplete="off"
        :disabled="lockRemaining > 0 || busy"
        data-testid="vault-pin-input"
        class="mb-3 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono text-xl tracking-[0.5em] shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        @keydown.enter="submit"
      />
      <template v-if="mode === 'setup'">
        <label class="mb-1 block text-xs font-medium text-slate-500 dark:text-slate-400">{{ t('vault_confirm_pin') }}</label>
        <input
          v-model="pinConfirm"
          type="password"
          inputmode="numeric"
          maxlength="4"
          autocomplete="off"
          :disabled="busy"
          data-testid="vault-pin-confirm-input"
          class="mb-3 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono text-xl tracking-[0.5em] shadow-sm transition focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/20 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          @keydown.enter="submit"
        />
      </template>

      <p v-if="error" class="mb-3 text-sm text-red-600 dark:text-red-400" data-testid="vault-error">{{ error }}</p>

      <button
        type="button"
        :disabled="busy || lockRemaining > 0"
        data-testid="vault-submit"
        class="w-full rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white transition hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-60"
        @click="submit"
      >
        {{ busy ? t('vault_working') : submitLabel }}
      </button>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { useI18n } from '../../i18n';
import { useWebTerminal } from '../../composables/useWebTerminal';

const props = defineProps({
  // 'setup' | 'unlock'
  mode: { type: String, required: true },
});
const emit = defineEmits(['unlocked', 'setup-done']);

const { t } = useI18n();
const wt = useWebTerminal();
const pin = ref('');
const pinConfirm = ref('');
const error = ref('');
const busy = ref(false);
const lockRemaining = ref(0);
const pinInput = ref(null);
let ticker = null;

const title = computed(() => (props.mode === 'setup' ? t('vault_set_pin_title') : t('vault_unlock_title')));
const subtitle = computed(() => (props.mode === 'setup' ? t('vault_set_pin_desc') : ''));
const pinLabel = computed(() => (props.mode === 'setup' ? t('vault_new_pin') : t('vault_enter_pin')));
const submitLabel = computed(() => (props.mode === 'setup' ? t('vault_set_btn') : t('vault_unlock_btn')));

function startCountdown(seconds) {
  if (!seconds || seconds <= 0) return;
  lockRemaining.value = seconds;
  if (ticker) clearInterval(ticker);
  ticker = setInterval(() => {
    lockRemaining.value = Math.max(0, lockRemaining.value - 1);
    if (lockRemaining.value <= 0) {
      clearInterval(ticker);
      ticker = null;
    }
  }, 1000);
}

async function submit() {
  // 双提交闸：按钮 disabled 之外，Enter 键仍可触发——busy/锁定中直接忽略。
  if (busy.value || lockRemaining.value > 0) return;
  error.value = '';
  if (props.mode === 'setup' && pin.value !== pinConfirm.value) {
    error.value = t('vault_mismatch');
    return;
  }
  busy.value = true;
  try {
    const res = props.mode === 'setup' ? await wt.setup(pin.value) : await wt.verify(pin.value);
    if (res.ok) {
      pin.value = '';
      pinConfirm.value = '';
      emit(props.mode === 'setup' ? 'setup-done' : 'unlocked');
      return;
    }
    if (res.reason === 'locked') startCountdown(res.lock_remaining_seconds || 0);
    else if (res.reason === 'bad_pin') error.value = `${t('vault_wrong_pin')}（${t('vault_attempts_left')} ${res.attempts_left ?? 0}）`;
    else if (res.reason === 'mismatch') error.value = t('vault_mismatch');
    else if (res.reason === 'bad_pin_form') error.value = t('vault_bad_form');
    else if (res.reason === 'disabled') error.value = t('vault_disabled');
    else if (res.reason === 'too_fast') error.value = t('vault_too_fast');
    else error.value = t('vault_error_generic');
  } finally {
    busy.value = false;
  }
}

onMounted(() => { pinInput.value?.focus?.(); });
onUnmounted(() => { if (ticker) clearInterval(ticker); });
</script>
