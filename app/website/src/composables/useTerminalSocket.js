// 每会话一条终端 WS(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md §6.2)。
// 建连/超时保险丝/重连预算照抄 useAliangChat.ensureAISocket 形态(幂等 CONNECTING
// 去重 + 10s 超时 + 2 次 800ms 退避,归零只在生产性入站帧)。首帧强制 terminal.auth;
// create 恒带 attach:true——重连即续屏(manager:活会话 replay+resume/墓碑 replay)。

const TERMINAL_CONNECT_TIMEOUT_MS = 10000;
const TERMINAL_RECONNECT_LIMIT = 2;
const TERMINAL_RECONNECT_BACKOFF_MS = 800;

export function createTerminalSocket({ token, sessionId, rows, cols, onFrame, onExit, onStatus }) {
  let ws = null;
  let disposed = false;
  let reconnectAttempts = 0;
  let reconnectTimer = null;
  let connectTimer = null;

  function terminalUrl() {
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
    return `${proto}://${window.location.host}/api/agent/web-terminal/stream`;
  }

  function handleFrame(parsed) {
    if (!parsed || !parsed.type) return;
    // 生产性入站帧=通道健康证明(归零重连预算,同聊天 WS 语义;open 刻意不归零
    // ——「连上就断」的离线环境若 open 归零会退化成无限重连风暴)
    reconnectAttempts = 0;
    switch (parsed.type) {
      case 'terminal.output':
      case 'terminal.replay':
        onFrame && onFrame(parsed);
        break;
      case 'terminal.created':
        // exited:true=attach 命中墓碑(会话已死,只回放历史)——窗格据此显示
        // 会话结束并上抛 exit,不再等待活 shell 输出
        onStatus && onStatus({ kind: 'created', resumed: !!parsed.resumed, pty: !!parsed.pty, exited: !!parsed.exited });
        break;
      case 'terminal.exit':
        onStatus && onStatus({ kind: 'exit' });
        onExit && onExit(parsed);
        break;
      case 'terminal.error':
        // session_id 上抛供窗格判定错误归属:仅本会话(如 create 失败)可触发
        // 自动重建;re-locked 等全局错误帧 session_id 为空,重建只会再撞闸
        onStatus && onStatus({ kind: 'error', message: parsed.error || 'terminal error', sessionId: parsed.session_id || '' });
        break;
      default:
        break;
    }
  }

  function connect() {
    if (disposed) return;
    let sock;
    try {
      sock = new WebSocket(terminalUrl());
    } catch (e) {
      scheduleReconnect();
      return;
    }
    ws = sock;
    // 超时保险丝(属主校验:迟到 close 不得拆新连接的 timer,同聊天 WS 2026-11-20 评审)
    connectTimer = setTimeout(() => {
      if (ws === sock && sock.readyState === WebSocket.CONNECTING) {
        try { sock.close(); } catch (_) { /* 已死即跳过 */ }
      }
    }, TERMINAL_CONNECT_TIMEOUT_MS);
    sock.onopen = () => {
      if (ws !== sock) return; // 迟到 open(被 dispose/重建取代)不动作
      clearTimeout(connectTimer);
      // 先 auth 后 create:服务端首帧即鉴权(15s 服务端死线),WS 消息有序保证 auth 先到
      sock.send(JSON.stringify({ type: 'terminal.auth', token }));
      sock.send(JSON.stringify({ type: 'terminal.create', session_id: sessionId, rows, cols, attach: true }));
    };
    sock.onmessage = (ev) => {
      if (ws !== sock) return;
      let parsed = null;
      try { parsed = JSON.parse(ev.data); } catch (_) { return; }
      handleFrame(parsed);
    };
    sock.onclose = (e) => {
      if (ws !== sock) return;
      ws = null;
      if (disposed) return;
      onStatus && onStatus({ kind: 'closed', code: e.code });
      if (reconnectAttempts < TERMINAL_RECONNECT_LIMIT) {
        reconnectAttempts += 1;
        reconnectTimer = setTimeout(() => {
          reconnectTimer = null;
          if (!disposed) connect();
        }, TERMINAL_RECONNECT_BACKOFF_MS);
      } else {
        onStatus && onStatus({ kind: 'dead' });
      }
    };
    sock.onerror = () => { /* close 事件统一处理 */ };
  }

  function scheduleReconnect() {
    if (disposed) return;
    reconnectTimer = setTimeout(() => {
      reconnectTimer = null;
      if (!disposed) connect();
    }, TERMINAL_RECONNECT_BACKOFF_MS);
  }

  function send(payload) {
    if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify(payload));
  }

  connect();

  return {
    input(data) { send({ type: 'terminal.input', session_id: sessionId, data }); },
    resize(r, c) { send({ type: 'terminal.resize', session_id: sessionId, rows: r, cols: c }); },
    close() { send({ type: 'terminal.close', session_id: sessionId }); },
    dispose() {
      disposed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      if (connectTimer) clearTimeout(connectTimer);
      if (ws) { try { ws.close(); } catch (_) { /* noop */ } }
      ws = null;
    },
  };
}
