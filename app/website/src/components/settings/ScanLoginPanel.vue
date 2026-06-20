<template>
  <div class="mx-auto max-w-sm space-y-4 py-4">
    <div class="rounded-xl border border-slate-200 bg-slate-50/80 p-5 text-center dark:border-slate-800 dark:bg-slate-900/50">
      <div class="flex flex-col items-center gap-3">
        <div class="flex size-12 items-center justify-center rounded-full bg-primary/10">
          <span class="material-symbols-outlined text-2xl text-primary">qr_code_2</span>
        </div>
        <div>
          <p class="text-sm font-semibold text-slate-900 dark:text-white">{{ t('scan_title') }}</p>
          <p class="mt-1 text-xs text-slate-500 dark:text-slate-400">{{ t('scan_desc') }}</p>
        </div>
      </div>
    </div>

    <!-- 二维码区 -->
    <div class="flex flex-col items-center gap-3">
      <div
        class="relative flex size-60 items-center justify-center rounded-xl border border-slate-200 bg-white p-3 dark:border-slate-800 dark:bg-slate-900"
        :class="{ 'opacity-40': transitioning }"
      >
        <span
          v-if="phase === 'loading' || !qrDataUrl"
          class="inline-block size-6 border-2 border-slate-300 border-t-primary rounded-full animate-spin"
        ></span>
        <img v-else :src="qrDataUrl" alt="QR Code" class="size-full rounded" />

        <!-- 过期/取消遮罩 -->
        <div
          v-if="phase === 'expired' || phase === 'denied'"
          class="absolute inset-0 flex flex-col items-center justify-center gap-2 rounded-xl bg-white/90 dark:bg-slate-900/90"
        >
          <span class="material-symbols-outlined text-2xl text-slate-400">refresh</span>
          <span class="text-xs text-slate-500 dark:text-slate-400">{{ phaseLabel }}</span>
        </div>
      </div>

      <p class="text-center text-xs text-slate-500 dark:text-slate-400">
        <span v-if="phase === 'pending' && remainingSeconds > 0">{{ t('scan_expiresIn', { seconds: remainingSeconds }) }}</span>
        <span v-else-if="phase === 'scanned'" class="text-emerald-600 dark:text-emerald-400">{{ phaseLabel }}</span>
        <span v-else>{{ phaseLabel }}</span>
      </p>
    </div>

    <div
      v-if="errorMessage"
      class="flex items-center gap-2 rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-700 dark:border-rose-900 dark:bg-rose-950/30 dark:text-rose-300"
    >
      <span class="material-symbols-outlined text-sm">error</span>
      {{ errorMessage }}
    </div>

    <button
      type="button"
      class="inline-flex h-10 w-full items-center justify-center gap-2 rounded-lg border border-slate-200 px-4 text-sm font-semibold text-slate-700 transition hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-60 dark:border-slate-700 dark:text-slate-100 dark:hover:bg-slate-800"
      :disabled="transitioning"
      @click="startScan"
    >
      <span class="material-symbols-outlined text-[18px]">refresh</span>
      {{ t('scan_refresh') }}
    </button>

    <p class="text-center text-[11px] text-slate-400">{{ t('scan_hint') }}</p>
  </div>
</template>

<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue';
import QRCode from 'qrcode';
import { scanInit, scanStatus } from '../../services/authApi';
import { useAuthStore } from '../../stores/auth';
import { useI18n } from '../../i18n';

const emit = defineEmits(['success', 'error']);

const { t } = useI18n();
const { completeScanLogin, loginError } = useAuthStore();

// phase: loading | pending | scanned | success | expired | denied | error
const phase = ref('loading');
const qrDataUrl = ref('');
const errorMessage = ref('');
const remainingSeconds = ref(0);

let deviceCode = '';
let pollIntervalSec = 2;
let expiresAt = 0;
let pollTimer = null;
let countdownTimer = null;
let restartTimer = null;
let stopped = false; // 卸载后不再 setState

const transitioning = computed(() => phase.value === 'loading' || phase.value === 'expired' || phase.value === 'denied');

const phaseLabel = computed(() => {
  switch (phase.value) {
    case 'loading':
      return t('scan_loading');
    case 'scanned':
      return t('scan_scanned');
    case 'expired':
      return t('scan_expired');
    case 'denied':
      return t('scan_denied');
    case 'success':
      return t('scan_success');
    case 'error':
      return t('scan_failed');
    default:
      return t('scan_pending');
  }
});

async function startScan() {
  clearTimers();
  errorMessage.value = '';
  phase.value = 'loading';
  qrDataUrl.value = '';

  let init;
  try {
    init = await scanInit();
  } catch (error) {
    if (stopped) return;
    phase.value = 'error';
    errorMessage.value = error instanceof Error ? error.message : t('scan_failed');
    emit('error');
    scheduleRestart();
    return;
  }
  if (stopped) return;

  const data = init.data || {};
  deviceCode = data.device_code || '';
  if (!deviceCode || !data.qr_payload) {
    phase.value = 'error';
    errorMessage.value = init.message || t('scan_failed');
    scheduleRestart();
    return;
  }

  try {
    qrDataUrl.value = await QRCode.toDataURL(data.qr_payload, { width: 240, margin: 1, errorCorrectionLevel: 'M' });
  } catch (error) {
    if (stopped) return;
    phase.value = 'error';
    errorMessage.value = error instanceof Error ? error.message : t('scan_failed');
    emit('error');
    return;
  }
  if (stopped) return;

  pollIntervalSec = Number(data.interval) > 0 ? Number(data.interval) : 2;
  expiresAt = Date.now() + Math.max(1, Number(data.expires_in) || 300) * 1000;
  phase.value = 'pending';
  updateCountdown();
  pollTimer = setInterval(pollOnce, pollIntervalSec * 1000);
  countdownTimer = setInterval(updateCountdown, 1000);
}

async function pollOnce() {
  if (stopped || !deviceCode) return;

  let res;
  try {
    res = await scanStatus(deviceCode);
  } catch (error) {
    // 网络抖动：不打断轮询，仅静默（下一次重试）
    return;
  }
  if (stopped) return;

  const status = res.data?.status;
  if (status === 'authorized') {
    clearTimers();
    const ok = await completeScanLogin({
      sessionToken: res.data.session_token,
      refreshToken: res.data.refresh_token
    });
    if (stopped) return;
    if (ok) {
      phase.value = 'success';
      emit('success');
    } else {
      phase.value = 'error';
      errorMessage.value = loginError.value || t('scan_failed');
      emit('error');
    }
    return;
  }

  if (status === 'scanned') {
    phase.value = 'scanned';
    return;
  }

  if (status === 'denied') {
    phase.value = 'denied';
    clearTimers();
    scheduleRestart();
    return;
  }

  if (status === 'expired') {
    phase.value = 'expired';
    clearTimers();
    scheduleRestart();
    return;
  }

  // pending 或未知：继续轮询
  if (phase.value !== 'scanned') {
    phase.value = 'pending';
  }
}

function updateCountdown() {
  const remaining = Math.max(0, Math.ceil((expiresAt - Date.now()) / 1000));
  remainingSeconds.value = remaining;
  if (remaining <= 0 && (phase.value === 'pending' || phase.value === 'scanned')) {
    // 本地倒计时到点：交给下一次轮询拿服务端 expired；提前切遮罩更友好
    phase.value = 'expired';
    clearTimers();
    scheduleRestart();
  }
}

function scheduleRestart() {
  clearTimers();
  if (stopped) return;
  restartTimer = setTimeout(() => {
    if (!stopped) startScan();
  }, 1500);
}

function clearTimers() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null; }
  if (countdownTimer) { clearInterval(countdownTimer); countdownTimer = null; }
  if (restartTimer) { clearTimeout(restartTimer); restartTimer = null; }
}

onMounted(() => {
  startScan();
});

onBeforeUnmount(() => {
  stopped = true;
  clearTimers();
});
</script>
