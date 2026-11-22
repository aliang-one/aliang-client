<template>
  <!--
    终端单窗格（Task 15, plan 2026-11-21-web-terminal-phase1 §Task 15）：
    xterm.js 渲染 + useTerminalSocket 双向管道。挂载即建连（sessionId 恒 web-
    前缀随机段,会话与窗格同生命周期）;output/replay 字符串分片逐帧 write（WS
    消息有序,回放天然保序）;ResizeObserver 100ms 去抖后 fit+resize（防拖拽期
    PTY SIGWINCH 风暴）。状态徽标沿用轻量文案通道:closed→重连中/dead→已断开
    （并上抛 dead,工作区据此回退 PinGuard,spec §9,只抛一次）/error→服务端
    错误文案+本会话错误自动重建(全新随机 ID,退避 1.5s/4s 上限 2 次,吸收
    刷新恢复壳撞后端会话上限的窗口期)/exit→会话结束（上抛 exit）。文案键
    term_* Task 16 落词表（缺失时
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
let rebuildTimer = null;
let sessionId = '';
// dead 闩：重连预算耗尽只上抛一次,其后 socket 已死不会再有状态,但闩掉可防
// 未来形态变化导致工作区重复回退 PinGuard
let deadEmitted = false;
// create 失败自动重建(2026-11-22 真机 not found 风暴兜底):刷新恢复壳的并发
// create 可能撞后端 agentMaxTerminalSessions(全局 4 坑)——旧实现任由错误
// 徽标一闪而过,socket 保持 open,用户一敲键 input 打死 ID 报
// "terminal session not found"。现在:error 归属本会话时自动以全新随机 ID
// 重建至多 2 次(退避 1.5s/4s,配合后端网页腿孤儿 10s 延迟回收把坑腾出来);
// 仍失败则停,错误文案保留由用户手动关窗重开。全局错误(re-locked 等,
// session_id 为空)不重建——重建只会再撞闸。
let rebuilds = 0;
const REBUILD_DELAYS_MS = [1500, 4000];

function newSessionId() {
  return `web-${Math.random().toString(36).slice(2, 10)}`;
}

function startSession(id) {
  sessionId = id;
  sock = createTerminalSocket({
    token: props.token,
    sessionId: id,
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
      } else if (s.kind === 'error') {
        statusText.value = s.message || '';
        // 仅本会话错误(create 失败类)触发重建;全局错误不在此列
        if (s.sessionId && s.sessionId === sessionId && rebuilds < REBUILD_DELAYS_MS.length) {
          const delay = REBUILD_DELAYS_MS[rebuilds];
          rebuilds += 1;
          statusText.value = t('term_reconnecting');
          if (rebuildTimer) clearTimeout(rebuildTimer);
          rebuildTimer = setTimeout(() => {
            rebuildTimer = null;
            if (!term) return; // 窗格已卸载:dispose 链已收尾,不再重建
            if (sock) { sock.dispose(); sock = null; }
            startSession(newSessionId());
          }, delay);
        }
      } else if (s.kind === 'created') statusText.value = '';
    },
    onExit: () => {
      statusText.value = t('term_exit');
      emit('exit');
    },
  });
}

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

  startSession(newSessionId());

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
  if (rebuildTimer) { clearTimeout(rebuildTimer); rebuildTimer = null; }
  if (resizeTimer) { clearTimeout(resizeTimer); resizeTimer = null; }
  if (resizeObserver) { resizeObserver.disconnect(); resizeObserver = null; }
  if (sock) { sock.dispose(); sock = null; }
  if (dataSub) { dataSub.dispose(); dataSub = null; }
  if (term) { term.dispose(); term = null; }
});
</script>
