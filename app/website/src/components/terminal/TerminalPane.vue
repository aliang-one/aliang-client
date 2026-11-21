<template>
  <!--
    终端单窗格（Task 15, plan 2026-11-21-web-terminal-phase1 §Task 15）：
    xterm.js 渲染 + useTerminalSocket 双向管道。挂载即建连（sessionId 恒 web-
    前缀随机段,会话与窗格同生命周期）;output/replay 字符串分片逐帧 write（WS
    消息有序,回放天然保序）;ResizeObserver 100ms 去抖后 fit+resize（防拖拽期
    PTY SIGWINCH 风暴）。状态徽标沿用轻量文案通道:closed→重连中/dead→已断开
    （并上抛 dead,工作区据此回退 PinGuard,spec §9,只抛一次）/error→服务端
    错误文案/exit→会话结束（上抛 exit）。文案键 term_* Task 16 落词表（缺失时
    t() 回落键名本身,仓内既定行为）。徽标配色不随明暗主题切换——窗格恒为纯黑
    底,固定深底浅字在两种主题下均可读。
  -->
  <div data-testid="terminal-pane" class="relative h-full w-full overflow-hidden bg-black">
    <div ref="hostEl" class="absolute inset-0"></div>
    <div
      v-if="statusText"
      data-testid="terminal-status"
      class="pointer-events-none absolute bottom-2 left-2 z-10 rounded-md bg-slate-800/95 px-2 py-1 text-xs text-amber-200"
    >{{ statusText }}</div>
  </div>
</template>

<script setup>
import { onMounted, onUnmounted, ref } from 'vue';
import { Terminal } from '@xterm/xterm';
import { FitAddon } from '@xterm/addon-fit';
import '@xterm/xterm/css/xterm.css';
import { useI18n } from '../../i18n';
import { createTerminalSocket } from '../../composables/useTerminalSocket';

const props = defineProps({
  // 一次性终端令牌（PinGuard setup/verify 换发,spec §5;经 WS 首帧 auth 上行）
  token: { type: String, required: true },
});
const emit = defineEmits(['exit', 'dead']);

const { t } = useI18n();
const hostEl = ref(null);
const statusText = ref('');

let term = null;
let fit = null;
let sock = null;
let dataSub = null;
let resizeObserver = null;
let resizeTimer = null;
// dead 闩：重连预算耗尽只上抛一次,其后 socket 已死不会再有状态,但闩掉可防
// 未来形态变化导致工作区重复回退 PinGuard
let deadEmitted = false;

onMounted(() => {
  term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, monospace',
    theme: { background: '#000000' },
    scrollback: 5000,
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  term.open(hostEl.value);
  try { fit.fit(); } catch (_) { /* 挂载瞬间容器可能零量纲(隐藏挂载)使 fit 抛错;忽略,RO 回调稍后自纠 */ }

  sock = createTerminalSocket({
    token: props.token,
    sessionId: `web-${Math.random().toString(36).slice(2, 10)}`,
    rows: term.rows,
    cols: term.cols,
    onFrame: (f) => {
      if (typeof f.data === 'string' && f.data.length) term.write(f.data);
    },
    onStatus: (s) => {
      if (s.kind === 'closed') statusText.value = t('term_reconnecting');
      else if (s.kind === 'dead') {
        statusText.value = t('term_disconnected');
        if (!deadEmitted) {
          deadEmitted = true;
          emit('dead');
        }
      } else if (s.kind === 'error') statusText.value = s.message || '';
      else if (s.kind === 'created') statusText.value = '';
    },
    onExit: () => {
      statusText.value = t('term_exit');
      emit('exit');
    },
  });

  dataSub = term.onData((data) => { if (sock) sock.input(data); });

  resizeObserver = new ResizeObserver(() => {
    if (resizeTimer) clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      resizeTimer = null;
      try {
        fit.fit();
        if (sock) sock.resize(term.rows, term.cols);
      } catch (_) { /* 宿主未量纲时 fit 抛错忽略,下次回调再试 */ }
    }, 100);
  });
  resizeObserver.observe(hostEl.value);
});

onUnmounted(() => {
  if (resizeTimer) { clearTimeout(resizeTimer); resizeTimer = null; }
  if (resizeObserver) { resizeObserver.disconnect(); resizeObserver = null; }
  if (sock) { sock.dispose(); sock = null; }
  if (dataSub) { dataSub.dispose(); dataSub = null; }
  if (term) { term.dispose(); term = null; }
});
</script>
