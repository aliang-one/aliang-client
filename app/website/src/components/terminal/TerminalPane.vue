<template>
  <!--
    终端单窗格（Task 15, plan 2026-11-21-web-terminal-phase1 §Task 15）：
    xterm.js 渲染 + useTerminalSocket 双向管道。挂载即建连（sessionId=窗 id,
    2026-11-23 会话保活:刷新后同 id attach 续屏,活 shell/滚动缓冲跨刷新存活;
    会话已死则命中墓碑回放,created{exited:true} 显示会话结束）;output/replay
    字符串分片逐帧 write（WS
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
  // 会话 id=窗 id(2026-11-23 会话保活改造):窗 id 经注册表 localStorage 持久化,
  // 刷新后恢复壳重挂面板用同一 id 发 create(attach:true)——attachLive 命中即
  // replay 续屏,活 shell/滚动缓冲跨刷新存活;会话死亡(孤儿回收/PTY 退出)则
  // attachHistory 命中墓碑回放(见 created exited 分支)。此前每次挂载随机新 id,
  // 永远 attach 不上旧会话=刷新必丢内容。
  sessionId: { type: String, required: true },
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
// dead 闩：重连预算耗尽只上抛一次,其后 socket 已死不会再有状态,但闩掉可防
// 未来形态变化导致工作区重复回退 PinGuard
let deadEmitted = false;
// create 失败自动重建(2026-11-22 真机 not found 风暴兜底):error 归属本会话
// 时以同 id 重连重试至多 2 次(退避 1.5s/4s)。sessionId=窗 id 恒定(保活改造),
// 不再换新 ID——撞坑已由后端 create 的「同步回收孤儿腾坑」兜死,这里的重试
// 吸收剩余瞬态(如 agent 进程短窗不可达)。全局错误(re-locked 等,session_id
// 为空)不重建——重建只会再撞闸。仍失败则停,错误文案保留由用户手动关窗重开。
let rebuilds = 0;
const REBUILD_DELAYS_MS = [1500, 4000];

function startSession() {
  sock = createTerminalSocket({
    token: props.token,
    sessionId: props.sessionId,
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
        if (s.sessionId && s.sessionId === props.sessionId && rebuilds < REBUILD_DELAYS_MS.length) {
          const delay = REBUILD_DELAYS_MS[rebuilds];
          rebuilds += 1;
          statusText.value = t('term_reconnecting');
          if (rebuildTimer) clearTimeout(rebuildTimer);
          rebuildTimer = setTimeout(() => {
            rebuildTimer = null;
            if (!term) return; // 窗格已卸载:dispose 链已收尾,不再重建
            if (sock) { sock.dispose(); sock = null; }
            startSession();
          }, delay);
        }
      } else if (s.kind === 'created') {
        // 墓碑回放(created{resumed:false,exited:true}):attach 命中的是已退出
        // 会话的历史——历史内容已回放进 xterm,但活 shell 已死。显示会话结束
        // 徽标并上抛 exit(工作台刷新保险箱状态);用户关窗重开=新会话。
        if (s.exited) {
          statusText.value = t('term_exit');
          emit('exit');
        } else {
          statusText.value = '';
        }
      }
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

  startSession();

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
