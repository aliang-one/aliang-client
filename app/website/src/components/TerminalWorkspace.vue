<template>
  <!--
    网页终端工作台(Task 16, plan 2026-11-21-web-terminal-phase1 §Task 16):
    固定全屏覆盖层(同 ChatPage z-[900] 惯例)。状态链四段互斥 v-if——
    未开启(开闸卡)→ 未设 PIN(PinGuard setup)→ 已锁(PinGuard unlock)
    → 已解锁(可选改 PIN 内联面板 + TerminalPane)。分支切换即组件重建,
    PinGuard 局部态(输入/倒计时)天然不复用,无需手动复位。
    dead(WS 预算耗尽/服务端 15m 闲置重锁)→ invalidateToken+refresh →
    回落解锁密码盘(spec §9「令牌过期→重新弹密码盘,不整页退出」)。
  -->
  <div class="fixed inset-0 z-[900] flex flex-col bg-slate-100 dark:bg-slate-950" data-testid="terminal-workspace">
    <!-- 头部:标题+管理钮+关闭 -->
    <div class="flex items-center justify-between border-b border-slate-200 px-4 py-2 dark:border-slate-800">
      <div class="flex items-center gap-2">
        <span class="material-symbols-outlined text-slate-400">terminal</span>
        <span class="text-sm font-medium text-slate-900 dark:text-slate-100">{{ t('term_title') }}</span>
      </div>
      <div class="flex items-center gap-2">
        <button v-if="unlocked" type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-change-pin" @click="showChange = !showChange">{{ t('vault_change_pin') }}</button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-lock" @click="onLock">{{ t('vault_lock_now') }}</button>
        <button v-if="unlocked" type="button" class="rounded border border-red-300 px-3 py-1 text-xs text-red-600 dark:border-red-800 dark:text-red-400" data-testid="terminal-killall" @click="onKillall">{{ t('vault_killall') }}</button>
        <button type="button" class="rounded border border-slate-300 px-3 py-1 text-xs text-slate-600 dark:border-slate-700 dark:text-slate-300" data-testid="terminal-close" @click="showDashboard()">{{ t('term_close') }}</button>
      </div>
    </div>

    <!-- 主体:状态链 未开启→未设PIN→已锁→已解锁终端 -->
    <div class="min-h-0 flex-1 p-3">
      <div v-if="st && !st.enabled" class="flex h-full items-center justify-center">
        <div class="max-w-md rounded-xl border border-slate-200 bg-white p-6 text-center dark:border-slate-800 dark:bg-slate-900">
          <p class="mb-2 text-sm text-slate-600 dark:text-slate-300">{{ t('vault_disabled_desc') }}</p>
          <button type="button" class="rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white" data-testid="terminal-enable" @click="onEnable">{{ t('vault_enable') }}</button>
          <p v-if="enableError" class="mt-2 text-sm text-red-600" data-testid="terminal-enable-error">{{ enableError }}</p>
        </div>
      </div>
      <PinGuard v-else-if="st && st.enabled && !st.pin_set" mode="setup" @setup-done="onPinReady" />
      <PinGuard v-else-if="st && st.enabled && st.pin_set && !unlocked" mode="unlock" @unlocked="onPinReady" />
      <template v-else-if="unlocked">
        <div v-if="showChange" class="mx-auto mb-3 max-w-sm rounded-xl border border-slate-200 bg-white p-4 dark:border-slate-800 dark:bg-slate-900" data-testid="terminal-change-panel">
          <!-- 改 PIN 内联表单:旧PIN/新PIN/提交 -->
          <input v-model="changeOld" type="password" inputmode="numeric" maxlength="4" autocomplete="off" :placeholder="t('vault_enter_pin')" class="mb-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100" data-testid="terminal-change-old" />
          <input v-model="changeNew" type="password" inputmode="numeric" maxlength="4" autocomplete="off" :placeholder="t('vault_new_pin')" class="mb-2 w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-center font-mono tracking-[0.5em] dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100" data-testid="terminal-change-new" />
          <p v-if="changeError" class="mb-2 text-sm text-red-600" data-testid="terminal-change-error">{{ changeError }}</p>
          <button type="button" class="w-full rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-white" data-testid="terminal-change-submit" @click="submitChange">{{ t('vault_set_btn') }}</button>
        </div>
        <TerminalPane :token="token" @exit="statusRefresh" @dead="onDead" />
      </template>
      <!-- agent 离线/状态拉取失败:st 为 null 时四个分支全不命中,给一句提示
           而非空白体(终审松散端#3)。loading 态显示「处理中」,失败显示通用错。 -->
      <div v-else class="flex h-full items-center justify-center text-sm text-slate-500 dark:text-slate-400" data-testid="terminal-state-pending">
        {{ wt.loading.value ? t('vault_working') : t('vault_error_generic') }}
      </div>
    </div>
  </div>
</template>

<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue';
import { useI18n } from '../i18n';
import { useNavigation } from '../composables/useNavigation';
import { useEscStack } from '../composables/useEscStack';
import { useWebTerminal } from '../composables/useWebTerminal';
import PinGuard from './terminal/PinGuard.vue';
import TerminalPane from './terminal/TerminalPane.vue';

const { t } = useI18n();
const { showDashboard } = useNavigation();
const wt = useWebTerminal();
const st = computed(() => wt.status.value);
const { token, unlocked } = wt;
const showChange = ref(false);
const enableError = ref('');
const changeOld = ref('');
const changeNew = ref('');
const changeError = ref('');
const { push: escPush, remove: escRemove } = useEscStack();
let escId = null;

onMounted(() => {
  void wt.refresh();
  // ESC 层:工作台内 Esc 退回仪表盘(spec §7.1;层由组件自管,同 ChatPage 惯例)
  escId = escPush('terminal-workspace', () => showDashboard());
});

onUnmounted(() => { if (escId !== null) escRemove(escId); });

async function onEnable() {
  enableError.value = '';
  const res = await wt.setEnabled(true);
  if (!res || !res.ok) enableError.value = t('vault_error_generic');
}

async function onPinReady() {
  await wt.refresh();
}

async function onLock() {
  await wt.lock();
}

async function onDead() {
  // 服务端闲置重锁(15m)杀终端断 WS:清本地令牌,回落解锁密码盘(spec §9
  // 「令牌过期→重新弹密码盘,不整页退出」)。改 PIN 面板若开着,一并收起防
  // 陈旧输入/陈旧错误在重新解锁后自动复现。
  wt.invalidateToken();
  showChange.value = false;
  changeOld.value = '';
  changeNew.value = '';
  changeError.value = '';
  await wt.refresh();
}

async function submitChange() {
  changeError.value = '';
  if (!/^\d{4}$/.test(changeNew.value)) { changeError.value = t('vault_bad_form'); return; }
  const res = await wt.changePin(changeOld.value, changeNew.value);
  if (res && res.ok) {
    showChange.value = false;
    changeOld.value = '';
    changeNew.value = '';
  } else if (res && res.reason === 'bad_old_pin') {
    changeError.value = t('vault_wrong_pin');
  } else if (res && res.reason === 'bad_pin_form') {
    changeError.value = t('vault_bad_form');
  } else if (res && res.reason === 'bad_token') {
    wt.invalidateToken();
    changeError.value = t('vault_error_generic');
  } else {
    changeError.value = t('vault_error_generic');
  }
}

async function onKillall() {
  if (!window.confirm(t('vault_killall_confirm'))) return;
  await wt.killall();
  wt.invalidateToken();
  await wt.refresh();
}

async function statusRefresh() {
  await wt.refresh();
}
</script>
