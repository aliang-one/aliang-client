package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/tools"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
	"github.com/gorilla/websocket"
)

// remoteWSAuthRecovery 是握手 401 时的会话自愈入口（SoftExpired 单飞刷新：
// transient 失败持续重试，刷新令牌被永久拒绝才升级 HardInvalid 清会话）。
// 包变量仅为测试注入；注册路径(register_auth_rejection)早已是同款语义，
// 握手路径 2026-10-02 起补齐 —— 此前"服务器滚动→握手401→永久离线"双实证。
var remoteWSAuthRecovery = auth.RecoverOrExpireLocalSession

// handshakeRetryDelay 握手 401 连发时的重拨退避:2s 起步指数翻倍,60s 封顶。
// 凭据刷新成功后下一次拨号即成功,连击自然归零;避免刷新持续失败期间固定
// 2s 轮询高频打 auth 端点并刷屏日志。
func handshakeRetryDelay(streak int) time.Duration {
	if streak < 1 {
		streak = 1
	}
	shift := streak - 1
	if shift > 5 {
		shift = 5
	}
	delay := 2 * time.Second << uint(shift)
	if delay > 60*time.Second || delay <= 0 {
		return 60 * time.Second
	}
	return delay
}

// agentBootReconnectGrace is how long the user agent waits for a session-owner
// push after boot before attempting the connection from persisted state.
const agentBootReconnectGrace = 15 * time.Second

// agentShouldBootFallbackReconnect decides the boot fallback: the session owner
// only forwards authority TRANSITIONS, so an agent (re)started while the
// owner's state is quiet — the normal case after an agent crash, upgrade, or
// the owner simply having run for days — used to wait in "awaiting session
// owner sync" forever and the device showed offline on the phone. With
// enabled+registered persisted state and no session event seen, the agent must
// attempt the connection itself; the server stays the judge of token validity.
func agentShouldBootFallbackReconnect(enabled, registered, sessionEventSeen bool) bool {
	return !sessionEventSeen && enabled && registered
}

// ScheduleBootReconnectFallback arms the boot fallback (user-agent runtime
// only). Safe to call multiple times; the connection itself is idempotent.
func (s *AgentService) ScheduleBootReconnectFallback() {
	if !IsUserAgentRuntime() {
		return
	}
	time.AfterFunc(agentBootReconnectGrace, func() {
		if !agentShouldBootFallbackReconnect(s.state.Enabled, s.state.Registered, s.bootSessionEventSeen.Load()) {
			return
		}
		logger.Info("[AGENT-BOOT] boot_reconnect_fallback firing (no session-owner sync received)")
		if err := s.EnsureRemoteConnection(); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] boot_reconnect_fallback failed: %v", err))
		}
	})
}

func (s *AgentService) EnsureRemoteConnection() error {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	enabled := s.state.Enabled
	registered := s.state.Registered
	authHeader := s.effectiveUserAuthorizationLocked("")
	s.mu.Unlock()
	if !registered {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=device_not_registered")
		return errors.New("device is not registered")
	}
	if strings.TrimSpace(authHeader) == "" {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=no_user_authorization")
		return errors.New("user authorization is not available")
	}
	if !enabled {
		logger.Info("[AGENT-BOOT] remote_connection skipped reason=agent_disabled")
		return errors.New("agent mode is disabled")
	}

	s.wsMu.Lock()
	if s.wsConnected || s.wsConnecting {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection already_active connected=%t connecting=%t", s.wsConnected, s.wsConnecting))
		s.wsMu.Unlock()
		return nil
	}
	s.wsConnecting = true
	s.wsMu.Unlock()

	go s.remoteConnectionLoop()
	return nil
}

func (s *AgentService) remoteConnectionLoop() {
	// loopExitRecoverable 在循环体内首次判定 recoverable 时置位，defer 直接
	// 采用、cleanup 内不得二次求值：保活并发重注册会在窗口内把 Registered
	// 翻回 true，二次求值会误走清场分支，恰好杀掉本要保留的会话且不再
	// nudge（TOCTOU，评审 2026-10-11）。
	loopExitRecoverable := false
	defer func() { s.remoteLoopExitCleanup(loopExitRecoverable) }()

	handshake401Streak := 0
	for {
		identity, shouldRun := s.remoteConnectionSnapshot()
		if !shouldRun {
			if s.shouldPreserveDisabledStatus() {
				return
			}
			if s.registrationLostRecoverable() {
				// 可恢复的注册态丢失：不写 offline（保留 server_unavailable/
				// connect_failed 等真实状态），交由会话外注册保活定时器重注册。
				loopExitRecoverable = true
				logger.Info("[AGENT-BOOT] remote_connection loop_stop reason=registration_lost_recoverable (keepalive will re-register)")
				return
			}
			logger.Info("[AGENT-BOOT] remote_connection loop_stop reason=disabled_unregistered_or_missing_auth")
			s.setRemoteConnectionState(false, "offline", "")
			return
		}

		wsURL, err := currentAgentWebSocketURL()
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection ws_url_failed error=%v", err))
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection dialing endpoint=%s", sanitizeAgentEndpoint(wsURL)))
		headers := http.Header{}
		headers.Set("Authorization", identity.authorization)
		headers.Set("X-Aliang-Device-ID", identity.deviceID)
		conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				if s.handleHandshakeUnauthorized() {
					handshake401Streak += 1
					time.Sleep(handshakeRetryDelay(handshake401Streak))
					continue
				}
				return
			}
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection dial_failed endpoint=%s error=%v", sanitizeAgentEndpoint(wsURL), err))
			s.setRemoteConnectionState(false, "connect_failed", err.Error())
			time.Sleep(2 * time.Second)
			continue
		}

		s.setActiveRemoteConnection(conn)
		s.setRemoteConnectionState(true, "online", "")
		handshake401Streak = 0
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection connected endpoint=%s", sanitizeAgentEndpoint(wsURL)))

		if err := s.runRemoteAgentSession(conn); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection session_ended status=error error=%v", err))
			if _, shouldRun := s.remoteConnectionSnapshot(); shouldRun {
				s.setRemoteConnectionState(false, "disconnected", err.Error())
			}
		} else {
			logger.Info("[AGENT-BOOT] remote_connection session_ended status=clean")
			if _, shouldRun := s.remoteConnectionSnapshot(); shouldRun {
				s.setRemoteConnectionState(false, "offline", "")
			}
		}

		s.clearActiveRemoteConnection(conn)
		_ = conn.Close()
		time.Sleep(2 * time.Second)
	}
}

// handleHandshakeUnauthorized 处理远程 WS 握手 401：走会话自愈（SoftExpired
// 单飞刷新）而非终态禁用+清凭据 —— 注册路径(register_auth_rejection)早已同款；
// 此处此前先 disable+清凭据再依赖恢复链，刷新链一旦没跑成就永久离线
// （2026-10-02 12:17/19:18 双实证）。返回 true 表示调用方应退避后重拨；
// HardInvalid 清会话后 remoteConnectionSnapshot 的 shouldRun 变 false，循环
// 自然退出。
func (s *AgentService) handleHandshakeUnauthorized() bool {
	logger.Warn("[AGENT-BOOT] remote_connection handshake_401 → session self-heal (SoftExpired refresh)")
	if IsUserAgentRuntime() {
		NotifyOwnerAuthRejected(agentAuthRejectedReasonWS)
	} else {
		agentAuthRejectedHandler()
	}
	remoteWSAuthRecovery("websocket handshake 401")
	s.setRemoteConnectionState(false, "reauthenticating", "handshake 401; session refresh in flight")
	return true
}

func (s *AgentService) setActiveRemoteConnection(conn *websocket.Conn) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.wsConn = conn
	s.wsConnected = true
}

func (s *AgentService) clearActiveRemoteConnection(conn *websocket.Conn) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsConn == conn {
		s.wsConn = nil
	}
	s.wsConnected = false
}

// setCurrentRemoteWriter publishes the live remote-connection writer so that
// AI runs (which outlive a single socket) keep streaming after a reconnect.
// Pass nil to detach (writes will fail until the next connection publishes one).
func (s *AgentService) setCurrentRemoteWriter(w agentTerminalWriter) {
	if w == nil {
		s.remoteWriter.Store(nil)
		return
	}
	s.remoteWriter.Store(&w)
}

func (s *AgentService) clearCurrentRemoteWriter() {
	s.remoteWriter.Store(nil)
}

// currentRemoteWriter returns the live remote writer, or nil when disconnected.
func (s *AgentService) currentRemoteWriter() agentTerminalWriter {
	p := s.remoteWriter.Load()
	if p == nil {
		return nil
	}
	return *p
}

// armRegistrationGate closes the gate so outbound business writes block until
// markRegistrationReady opens it. Called once per connection at the start of
// runRemoteAgentSession. The connect-time agent.hello bypasses the gate (it is
// the message that causes registration); every other write goes through the
// published writer and therefore waits.
func (s *AgentService) armRegistrationGate() {
	ch := make(chan struct{})
	s.regMu.Lock()
	s.registrationGate = ch
	s.regMu.Unlock()
}

// markRegistrationReady opens the gate, releasing any blocked business writes.
// Called from the agent.registered handler. Idempotent: a nil/already-open gate
// is a no-op, so periodic re-hellos that re-ACK registration do nothing.
func (s *AgentService) markRegistrationReady() {
	s.regMu.Lock()
	ch := s.registrationGate
	s.registrationGate = nil // nil ⇒ gate open / not armed
	s.regMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// waitForRegistration blocks until the current connection is registered (gate
// opened) or the deadline elapses. Returns true if registered. A nil/absent
// gate means "not armed" (open) — used by tests and any non-remote-WS caller.
func (s *AgentService) waitForRegistration(timeout time.Duration) bool {
	s.regMu.Lock()
	ch := s.registrationGate
	s.regMu.Unlock()
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *AgentService) forceDisconnectRemote(reason string) {
	s.wsMu.Lock()
	conn := s.wsConn
	s.wsConn = nil
	s.wsConnected = false
	s.wsMu.Unlock()

	if conn != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection force_close reason=%s", normalizeAgentDisableReason(reason)))
		_ = conn.Close()
	}
	if s.tunnel != nil {
		s.tunnel.Stop()
	}
	s.terminal.closeAll()
	// Converge in-flight runs to a terminal BEFORE the sessions are dropped: the
	// terminal is journaled first, so it survives this teardown (and even a crash)
	// and is replayed on the next agent.registered. Without it the server keeps
	// the run live and its stale sweeper later paints a false timed_out card.
	s.ai.closeAllWithReason("connection_force_closed:" + normalizeAgentDisableReason(reason))
}

func (s *AgentService) shouldPreserveDisabledStatus() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch normalizeAgentDisableReason(s.state.LastSyncStatus) {
	case "disabled", "logout", "auth_expired", "refresh_invalid", "soft_expiry_timeout", "revoked", "device_unbound":
		return true
	default:
		return false
	}
}

// remoteLoopExitCleanup 在连接循环终态退出时执行。recoverable 必须传循环体
// 内的首次判定结果（单一求值点，见 remoteConnectionLoop 注释）。不可恢复终态
// （disabled / 无凭据 / 进程级关闭）按既有语义清空终端 PTY 与 AI 会话；可恢复
// 的注册态丢失例外——keepalive 将重注册并由新循环接管，会话必须像瞬断一样
// 存活（与 markAllDetached 的断线保留语义一致；2026-10-08 事故形态曾在此连带
// 全杀终端与 AI 会话）。必须在 wsConnecting 清零之后 nudge：此刻
// EnsureRemoteConnection 的幂等守卫已放行，保活触发的注册成功能立刻重新拉起
// 连接循环。
func (s *AgentService) remoteLoopExitCleanup(recoverable bool) {
	if recoverable {
		logger.Info("[AGENT-BOOT] remote_connection loop_exit recoverable_registration_lost skipping_session_teardown (keepalive will re-register)")
	} else {
		// The remote connection is being torn down for good (disabled / no
		// authorization / process-level shutdown of the loop): clean up the AI
		// sessions and terminal PTYs that runRemoteAgentSession deliberately
		// keeps alive across transient reconnects.
		s.terminal.closeAll()
		s.ai.closeAllWithReason("connection_loop_exit")
	}
	s.wsMu.Lock()
	s.wsConnecting = false
	s.wsMu.Unlock()
	if recoverable {
		logger.Info("[AGENT-BOOT] remote_connection loop_exit recoverable_registration_lost nudging_keepalive")
		nudgeAgentRegistrationKeepalive("ws_loop_exit_registration_lost")
	}
}

// registrationLostRecoverable 是循环终态退出里的可恢复子场景：enabled、握有
// 凭据、曾有 device_id（=曾注册过），但内存注册态已丢。这正是瞬时注册失败
// 或 2026-10-08 事故残留（Registered=false 落盘）的形态；粘性禁用态
// （logout/revoked/device_unbound 等，均 Enabled=false，由
// shouldPreserveDisabledStatus 先行拦截）与凭据缺失态不在此列。
func (s *AgentService) registrationLostRecoverable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	// device_id_conflict 是服务端断言的绑定冲突（换账号解绑才可解），
	// 不属于"网络瞬态丢注册"，重试只会空转——留给 watchdog 提醒用户处理。
	return s.state.Enabled && authHeader != "" &&
		strings.TrimSpace(s.state.DeviceID) != "" && !s.state.Registered &&
		normalizeAgentDisableReason(s.state.LastSyncStatus) != "device_id_conflict"
}

// atomicDuration is a race-safe time.Duration knob. Tests rewrite these
// tuning package vars while the goroutines they configure (ping/heartbeat
// loops, idle watchers) are still running and reading them — plain vars there
// are a guaranteed -race report, atomics make the shrink/restore benign.
type atomicDuration struct{ v atomic.Int64 }

func (d *atomicDuration) Store(v time.Duration) { d.v.Store(int64(v)) }
func (d *atomicDuration) Load() time.Duration   { return time.Duration(d.v.Load()) }

func newDurationAtom(v time.Duration) *atomicDuration {
	a := &atomicDuration{}
	a.Store(v)
	return a
}

// Remote-WS liveness tuning. The connection rides over a NAT tunnel, so a
// dead/half-open socket can persist undetected: without an enforced deadline
// the agent believes itself online while PhoneServer sees no traffic (and vice
// versa). These keep the connection honest. Package vars so tests can shrink
// them to exercise the dead-peer path quickly.
var (
	agentRemoteHeartbeatInterval = newDurationAtom(10 * time.Second)
	agentRemotePingInterval      = newDurationAtom(30 * time.Second)
	agentRemoteReadWindow        = newDurationAtom(90 * time.Second)
	agentRemoteWriteTimeout      = newDurationAtom(10 * time.Second)
	// 旧工具臂(file.list/file.read/git.status/env.info)分发入口。包级变量
	// 便于测试注入 panic 假实现(agentToolsRegistry 同款注入模式),钉"旧臂
	// panic 不得杀进程"——两臂绕过 default 臂的 RunWithGuards,进程内存活
	// 只能靠分发处自身的 recover 护栏。
	dispatchDetailWithAI = handleAgentDetailMessageWithAI
	dispatchEnvTools     = handleAgentEnvToolsMessage
	// agentRemoteRegistrationWait bounds how long a business write blocks for the
	// server to ACK agent.registered before failing. Registration normally
	// completes within a single RTT; this is a safety net for slow networks. On
	// timeout the write fails and existing per-call retry (e.g.
	// writeAgentAIApprovalRequest's 5×/1s loop) recovers once registration lands.
	// Package var so tests can shrink it.
	agentRemoteRegistrationWait = newDurationAtom(5 * time.Second)
	// agentRemoteTeardownGrace bounds how long runRemoteAgentSession waits for its
	// ping/heartbeat goroutines to leave the stack before returning. They only
	// need to observe close(done); the grace exists so a goroutine parked inside a
	// socket write can never delay loop exit indefinitely.
	agentRemoteTeardownGrace = newDurationAtom(2 * time.Second)
	// agentRemoteMaxFrameBytes 是远端 WS 单帧读上限（gorilla 连接级，一次设置
	// 即对整条连接生效）。此前双方均无帧限（gorilla 默认不限），对端/中间层
	// 异常可把进程内存灌爆。8MiB 与 PhoneServer /ws/agent 的 maxPayload 对齐
	// ——peer.* 阶段 1 帧族（stdout/stderr 各 256KiB）远小于该值，取服务端
	// 上限而不是 1MiB 是为了让既有大帧（如 terminal.replay 批量回放）不被
	// 客户端读限先行掐断。
	agentRemoteMaxFrameBytes int64 = 8 << 20
)

func (s *AgentService) runRemoteAgentSession(conn *websocket.Conn) error {
	// Write deadline: a stuck write (peer stopped reading / NAT blackhole) must
	// fail fast instead of blocking forever while holding writeMu.
	var writeMu sync.Mutex
	rawWriter := func(payload interface{}) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(agentRemoteWriteTimeout.Load()))
		return conn.WriteJSON(payload)
	}
	// Arm the registration gate, then publish a writer that waits for the gate
	// before each write. Business messages (notably ai.approval.request from an
	// in-flight AI turn) must never reach the server before it has ACKed
	// agent.registered — otherwise they hit the agent_not_registered guard and
	// are silently dropped, leaving the approval waiter blocked for up to 24h
	// while heartbeats keep the session looking alive (the vibecoding "stuck on
	// thinking" race). agent.hello below bypasses the gate via rawWriter (it is
	// what triggers registration); the gate opens in the agent.registered handler.
	s.armRegistrationGate()
	publishedWriter := func(payload interface{}) error {
		if !s.waitForRegistration(agentRemoteRegistrationWait.Load()) {
			return errors.New("remote agent not registered within deadline")
		}
		return rawWriter(payload)
	}
	s.setCurrentRemoteWriter(publishedWriter)
	defer s.clearCurrentRemoteWriter()
	// writeJSON is an indirection: it always writes to the *current* live writer,
	// not the closure captured here, so a reconnect reattaches in-flight runs.
	writeJSON := func(payload interface{}) error {
		w := s.currentRemoteWriter()
		if w == nil {
			return errors.New("remote agent writer unavailable (disconnected)")
		}
		return w(payload)
	}

	// 远端常驻 socket：写函数是间接层（重连换道），注册一次进程内有效。
	// 本函数每条连接跑一次——重连必然重入，断线死亡的 socket 在此复活。
	s.ai.ensureRemoteAISocket(writeJSON)

	// Read liveness: enforce a read deadline that is refreshed by inbound pongs
	// (and any application message). If the peer goes silent the deadline fires
	// -> ReadJSON errors -> the session ends and remoteConnectionLoop reconnects,
	// instead of hanging on a dead socket.
	resetReadDeadline := func() {
		_ = conn.SetReadDeadline(time.Now().Add(agentRemoteReadWindow.Load()))
	}
	resetReadDeadline()
	// 单帧读上限（spec §5.1/§8.4）：连接级一次设置，超限帧让 ReadJSON 立即
	// 报错断连重连，而不是把内存交给对端任意挥霍。
	conn.SetReadLimit(agentRemoteMaxFrameBytes)
	conn.SetPongHandler(func(string) error {
		resetReadDeadline()
		return nil
	})

	if err := s.sendAgentHello(rawWriter, "connect"); err != nil {
		return err
	}

	defer s.terminal.markAllDetached()
	// NOTE: AI sessions AND terminal PTYs are intentionally NOT closed here. A
	// transient WS disconnect (conn read error) must not kill locally-running
	// processes: AI CLIs survive and re-stream over the next connection via
	// currentRemoteWriter(); terminal shells stay alive detached and their
	// scrollback is replayed from the output ring when the client re-attaches.
	// A detached session is only reaped later if it goes input-idle (see
	// watchTerminalIdle). True shutdown paths (remoteConnectionLoop exit,
	// forceDisconnectRemote, remote_terminal_enabled turned off) still close
	// AI sessions and kill terminals explicitly — except that a loop exit
	// caused by a recoverable registration loss skips the teardown entirely:
	// keepalive re-registers and a fresh loop takes over, so sessions survive
	// like a transient disconnect (see remoteLoopExitCleanup).
	//
	// terminal replay + approval sync used to run here, before the read loop.
	// They are business writes that must wait for registration, so they now run
	// in the agent.registered handler below once the gate opens.

	done := make(chan struct{})
	// background tracks the ping/heartbeat goroutines. close(done) only *signals*
	// them; a goroutine already parked inside a socket write (or inside
	// waitForRegistration) keeps running after this function returns — straight
	// into a connection the caller is closing. That is the use-after-teardown
	// shape which surfaced as an unexplained SIGBUS with a garbage PC inside the
	// heartbeat select below (2026-10-21). Wait for them, bounded by a grace so a
	// stuck write can never block loop exit.
	var background sync.WaitGroup
	defer func() {
		close(done)
		finished := make(chan struct{})
		go func() {
			background.Wait()
			close(finished)
		}()
		select {
		case <-finished:
		case <-time.After(agentRemoteTeardownGrace.Load()):
			logger.Warn("[AGENT-BOOT] remote_connection teardown_grace_expired waiting for ping/heartbeat goroutines")
		}
	}()

	// Ping goroutine, independent of the application ticker below: liveness
	// probes must never be starved by (or starve) heartbeats/inventory writes.
	// WriteControl is safe to call concurrently with WriteJSON and carries its
	// own deadline, so it does not contend on writeMu.
	background.Add(1)
	go func() {
		defer background.Done()
		pingTicker := time.NewTicker(agentRemotePingInterval.Load())
		defer pingTicker.Stop()
		for {
			select {
			case <-pingTicker.C:
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(agentRemoteWriteTimeout.Load())); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	background.Add(1)
	go func() {
		defer background.Done()
		heartbeatTicker := time.NewTicker(agentRemoteHeartbeatInterval.Load())
		defer heartbeatTicker.Stop()
		inventoryTicker := time.NewTicker(time.Minute)
		defer inventoryTicker.Stop()
		// Periodically re-surface still-pending approvals: a delivery retry nudge
		// and a dialogue-liveness heartbeat for the server.
		approvalSyncTicker := time.NewTicker(time.Minute)
		defer approvalSyncTicker.Stop()
		// Change-driven inventory: re-hash the cheap session fingerprint every
		// few seconds and push a hello on a change, throttled to
		// agentVibeDigestHelloMinInterval: an active machine flips the
		// fingerprint on nearly every tick (live jsonl appends), which produced
		// 3367 hellos/day on the 2026-10-29 production Mac and drove the
		// server's detail-refresh fan-out into PG deadlocks. Consecutive
		// changes within the window coalesce into one hello carrying the latest
		// fingerprint; other hello reasons (connect/periodic/rename) bypass the
		// gate and refresh its baseline via sendAgentHello.
		digestTicker := time.NewTicker(agentVibeDigestInterval)
		defer digestTicker.Stop()
		for {
			select {
			case <-heartbeatTicker.C:
				_ = writeJSON(map[string]interface{}{
					"type":      models.AgentEventHeartbeat,
					"device_id": s.currentDeviceID(),
					"ts":        time.Now().UnixMilli(),
					"load":      collectAgentLoadSnapshot(),
					// 热层网络身份(spec §4.3)：接口地址随插拔变化，2s TTL 缓存。
					"net": collectAgentNetSnapshot(),
					// 工具注册表 rev：服务端比对发现漂移即重拉 tools.list。
					"agent_tools_rev": agentToolRegistryRev,
				})
			case <-digestTicker.C:
				// The first observed fingerprint only records the baseline — the
				// connect hello already carried that state. Log-only on failure
				// (see the periodic hello case): a failed push must not kill
				// the stream.
				digest := agentVibeDigest(agentHome())
				if digest == "" {
					continue
				}
				if s.remoteHello.baseline(digest) {
					continue
				}
				if s.remoteHello.unchanged(digest) {
					continue
				}
				if ok, firstDefer := s.remoteHello.allow(digest, time.Now(), agentVibeDigestHelloMinInterval); !ok {
					if firstDefer {
						// 窗口内连续变更合批为一次 hello,只打一条去重日志。
						logger.Info("[AGENT-BOOT] remote_connection digest_hello_deferred min_interval=" + agentVibeDigestHelloMinInterval.String())
					}
					continue
				}
				if err := s.sendAgentHello(writeJSON, "vibe_digest_change"); err != nil {
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection digest_hello_failed error=%v", err))
				}
				// 成功路径的基线/时间窗刷新在 sendAgentHello→noteSent 内完成;
				// 失败时不刷新基线,下个 tick 会带着最新指纹重试放行。
			case <-inventoryTicker.C:
				// Log-only on failure: a periodic hello failure must NOT tear down
				// the heartbeat/liveness stream. Returning here (the old behavior)
				// silently starved PhoneServer of heartbeats and tripped its own
				// liveness timer, dropping an otherwise-healthy connection.
				if err := s.sendAgentHello(writeJSON, "periodic"); err != nil {
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection periodic_hello_failed error=%v", err))
				}
			case <-s.sessionRefreshSig:
				// Token was refreshed (new exp). Push the current access_token so
				// PhoneServer advances this device's userTokenExp without a reconnect.
				accessToken := s.currentAccessToken()
				if accessToken == "" {
					continue
				}
				if err := writeJSON(map[string]interface{}{
					"type":         "agent.session.refresh",
					"access_token": accessToken,
				}); err != nil {
					// Log-only (see inventory): don't kill the heartbeat stream.
					logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection session_refresh_send_failed error=%v", err))
				}
			case <-approvalSyncTicker.C:
				s.ai.emitApprovalSync(writeJSON)
			case <-done:
				return
			}
		}
	}()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		// Any inbound traffic confirms the peer is alive; refresh the deadline so
		// a chatty-but-pong-less peer isn't misclassified as dead.
		resetReadDeadline()
		s.handleRemoteAgentMessage(msg, writeJSON)
	}
}

func (s *AgentService) sendAgentHello(writeJSON func(interface{}) error, reason string) error {
	// 先取指纹再构建 payload:noteSent 的基线应与本次 hello 覆盖的状态对齐。
	// 所有 reason 的成功 hello 都在此刷新 gate(单一咽喉),rename/periodic 后
	// 不会紧跟一次重复的 digest hello(见 agent_vibe_hello_gate.go)。
	sentDigest := agentVibeDigest(agentHome())
	hello := s.agentHelloPayload()
	projectCount := 0
	if projects, ok := hello["projects"].([]models.AgentProject); ok {
		projectCount = len(projects)
	}
	vibeSessionCount := 0
	if sessions, ok := hello["vibe_sessions"].([]models.AgentVibeSession); ok {
		vibeSessionCount = len(sessions)
	}
	dirCount := 0
	if dirs, ok := hello["authorized_directories"].([]string); ok {
		dirCount = len(dirs)
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection sending_hello reason=%s device_id=%s platform=%s projects=%d vibe_sessions=%d dirs=%d",
		strings.TrimSpace(reason),
		hello["device_id"],
		hello["platform"],
		projectCount,
		vibeSessionCount,
		dirCount,
	))
	if err := writeJSON(hello); err != nil {
		return err
	}
	s.remoteHello.noteSent(sentDigest, time.Now())
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection hello_sent reason=%s", strings.TrimSpace(reason)))
	return nil
}

func (s *AgentService) handleRemoteAgentMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	if remoteAgentMessageRequiresEnabledDevice(msgType) && !s.remoteControlAllowed() {
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventError,
			"error": "agent mode is disabled or disconnected",
		})
		s.forceDisconnectRemote("disabled")
		return
	}
	switch msgType {
	case models.AgentEventRegistered:
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection registered_ack server_device_id=%s", strings.TrimSpace(fmt.Sprint(msg["device_id"]))))
		s.mu.Lock()
		// device_id is permanent and client-owned: ignore the id the server
		// acknowledges and re-pin our own installation identity.
		s.pinPermanentDeviceIDLocked()
		s.setRemoteConnectionStateLocked(true, "online", "")
		_ = s.saveStateLocked()
		s.mu.Unlock()
		// Registration is now server-confirmed: open the gate so business writes
		// blocked on it proceed, then run the post-registration reconciliation
		// that used to run before the read loop (and therefore before
		// registration). Both are idempotent (run_id+event_seq / approval-id
		// keyed), so periodic re-hellos re-triggering them is safe.
		s.markRegistrationReady()
		s.ai.replayPendingTerminals(writeJSON)
		// 远端 socket 门开后复活（2026-10-28 评审 I2）：ensureRemoteAISocket 在
		// 连接建立即注册，但那时注册门未开——门开前的扇出会让 pump 卡在
		// publishedWriter→waitForRegistration（≤5s）后撞写超时、被 onDead 摘除，
		// 且本连接内不再有别的复活点（手机从此静默收不到审批广播）。此处幂等
		// ensure 一次：门已开，被摘/缺失的 remote socket 就地复活；活着的 no-op。
		s.ai.ensureRemoteAISocket(s.currentRemoteWriter())
		// Tell the server which terminal sessions survived the disconnect (live
		// set, tombstones excluded); an empty set still emits so the server can
		// clear stale records. Idempotent on the server side, so re-hellos are
		// harmless.
		s.terminal.announceSessions(writeJSON)
		s.ai.emitApprovalSync(writeJSON)
		// 用量桶补推：重连后把未确认桶（dirty 集）补齐（服务端 upsert 幂等）。
		usageFlushDirtyNow(writeJSON)
		// 互传收件兜底：重连后拉一次 inbox（离线补投的主通道），顺带确保
		// 小时级定时器在跑；首次触发时清一遍陈旧 .partial（7 天对齐服务端）。
		s.kickAgentTransferInbox()
		sweepAgentTransferPartials()
	case models.AgentEventHeartbeatAck:
		s.setRemoteConnectionState(true, "online", "")
		// Retry durable terminals while the socket remains healthy. A prior
		// delivery may have reached Server while its database was unavailable;
		// waiting for another reconnect would otherwise leave the run stuck.
		s.ai.replayPendingTerminals(writeJSON)
	case models.AgentEventAIRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		s.ai.acknowledgePendingTerminal(
			remoteString(msg, "run_id"),
			int64(remoteInt(msg, "accepted_seq", 0)),
		)
	case models.AgentEventGoalRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		if remoteString(msg, "result") == "accepted" {
			s.ai.acknowledgePendingTerminal(
				remoteString(msg, "goal_run_id"),
				int64(remoteInt(msg, "accepted_seq", 0)),
			)
		}
	case models.AgentEventDeviceUnbound:
		logger.Warn("[AGENT-BOOT] remote_connection device_unbound")
		s.DisableWithReason("device_unbound")
	case models.AgentEventDeviceSettings:
		logger.Info("[AGENT-BOOT] remote_connection settings_updated")
		s.applyRemoteDeviceSettings(msg)
	case models.AgentEventInventoryRefresh:
		// Server 端「重扫电脑」轻推(手机项目会话列表下拉/按钮触发):立即
		// 收集并推送一次全量 hello,不等 10s digest tick / 60s 周期兜底。
		// 只读动作,不进 enabled-device 门控名单(与 hello 类流量一致)。
		// 失败仅记日志:一次失败的推送不得撕断读循环(与 digest 变更分支同语义)。
		s.setRemoteConnectionState(true, "online", "")
		if err := s.sendAgentHello(writeJSON, "server_requested"); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection requested_rescan_hello_failed error=%v", err))
		}
	case models.AgentEventProjectSettings:
		s.applyRemoteProjectSettings(msg)
	case models.AgentEventAIRename:
		// Phone-side conversation rename (PhoneServer agentPublish.ts). This
		// agent historically ignored the event, forcing PhoneServer to freeze
		// titles server-side; persisting it into the durable rename cache
		// closes the phone→agent direction.
		s.setRemoteConnectionState(true, "online", "")
		s.handleRemoteAIRename(msg, writeJSON)
	case models.AgentEventTunnelConfigure:
		s.setRemoteConnectionState(true, "online", "")
		s.configureTunnel(msg, writeJSON)
	case models.AgentEventFileUpload:
		// Upload runs its own single-flight goroutine inside the handler; the
		// outer go keeps the read loop free while the inner one orchestrates.
		s.setRemoteConnectionState(true, "online", "")
		go handleAgentFileUpload(msg, writeJSON)
	case models.AgentEventFileUploadCancel:
		// Not behind the enabled-device gate: a device disabled mid-upload
		// must still be able to wind the transfer down.
		s.setRemoteConnectionState(true, "online", "")
		handleAgentFileUploadCancel(msg)
	case models.AgentEventTransferUpload:
		// 设备间互传·发送：独立并发槽在 handler 内部（不动 file.upload 单飞）；
		// safeGo 保持 read loop 畅通，长传输绝不能堵在消息循环上，panic 也不许
		// 顺 Go 语义带走全进程（照 agent_detail 旧臂的教训）。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("transfer_upload", func() { s.handleAgentTransferUpload(msg, writeJSON) })
	case models.AgentEventTransferReceive:
		// 设备间互传·接收：Range 续传 + sha256 校验 + 收件目录围栏，同样离线化处理。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("transfer_receive", func() { s.handleAgentTransferReceive(msg, writeJSON) })
	case models.AgentEventTransferCancel:
		// Not behind the enabled-device gate（照 file.upload.cancel 先例）：设备
		// 中途被禁用仍必须能把在飞传输收尾、清 .partial。按 transfer_id 匹配，
		// 不匹配为 no-op。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("transfer_cancel", func() { s.handleAgentTransferCancel(msg, writeJSON) })
	case models.AgentEventPeerExecRequest:
		// 设备互访阶段 1（spec §5.2）：B 端一次性命令执行。离线化处理（不堵
		// read loop，长命令最长 600s），串行/超时/封顶/审计全在
		// handleAgentPeerExecRequest 内。经 remoteAgentMessageRequiresEnabledDevice
		// 门禁——与 terminal/AI 同族的控制面能力，禁用设备不得响应。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_exec", func() { s.handleAgentPeerExecRequest(msg, writeJSON) })
	case models.AgentEventPeerExecRejected:
		// 设备互访阶段 1（spec §5.2）：服务端对 A 侧请求的裁决回包。A 侧工具
		// 走同步 REST 编排（POST /api/peer/exec 的 4xx 即裁决），本帧是异步
		// 补发的旁证（如目标机中途 busy）——静默消费，避免落 default 臂回
		// unsupported 噪声。留一条日志供审计对账（reason 是契约必填字段）。
		s.setRemoteConnectionState(true, "online", "")
		logger.Warn(fmt.Sprintf("[AGENT-PEER] peer_exec_rejected request_id=%s reason=%s",
			remoteString(msg, "request_id"), remoteString(msg, "reason")))
	case models.AgentEventPeerLanListen:
		// 设备互访阶段 2 增量 2（spec §6）：LAN 直连文件面的 B 端信令。绑定
		// listener 是毫秒级动作（回包不等服务过程），经 safeGo 离线化——读
		// loop 畅通优先，服务过程单连接一生由 TTL+单用途自然收摊。经
		// remoteAgentMessageRequiresEnabledDevice 门禁（与 peer.exec 同族）。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_lan_listen", func() { s.handleAgentPeerLanListen(msg, writeJSON) })
	case models.AgentEventPeerLanListenResult:
		// B→server 的绑定回包经通用 RPC resolve（request_id 匹配）消费；到达
		// A 侧属异常路径——静默消费防 default 臂 unsupported 噪声。
		s.setRemoteConnectionState(true, "online", "")
		logger.Info(fmt.Sprintf("[AGENT-PEER-LAN] listen_result at unexpected peer request_id=%s",
			remoteString(msg, "request_id")))
	// 注意：这四个旧工具事件（file.list/file.read/git.status/env.info）由下面的
	// 显式臂直派、有意绕过 default 臂的 tools.RunWithGuards 护栏（无超时看门狗/
	// 输出上限/panic 恢复）——响应形状兼容优先。注册表里它们的 caps（TimeoutMs/
	// MaxOutputBytes）暂不对这两个臂生效。严禁新增 Event 与任何显式臂冲突的
	// 注册表工具项（会被广告但永远不会走护栏分发）。
	case models.AgentEventProjectDetail, models.AgentEventAISessionDetail, models.AgentEventFileList, models.AgentEventFileRead, models.AgentEventSlashCommandsList, "file.working_tree_diff":
		s.setRemoteConnectionState(true, "online", "")
		// 旧臂仍绕过 RunWithGuards(响应形状兼容优先,caps 暂不生效),但 panic
		// 恢复必须兜住:裸 go 的 panic 顺 Go 语义杀全进程——read_file 是云端
		// 每回合必调的高频工具,一次 payload 解析 panic 就带走所有活 PTY。
		safeGo("agent_detail", func() { dispatchDetailWithAI(msg, writeJSON, s.ai) })
	case models.AgentEventGitStatus, models.AgentEventEnvInfo:
		s.setRemoteConnectionState(true, "online", "")
		safeGo("agent_envtools", func() { dispatchEnvTools(msg, writeJSON) })
	case models.AgentEventToolsList:
		// 元 RPC：回显当前工具注册表（工具清单 + rev），服务端据此做能力
		// 发现与 rev 漂移检测（heartbeat 带 agent_tools_rev）。
		s.setRemoteConnectionState(true, "online", "")
		reg := agentToolRegistry()
		_ = writeJSON(map[string]interface{}{
			"type":         models.AgentEventToolsListResult,
			"request_id":   remoteString(msg, "request_id"),
			"rev":          reg.Rev(),
			"tools":        reg.Descriptors(),
			"generated_at": time.Now().UTC().Format(time.RFC3339),
		})
	case models.AgentEventGoalPlan, models.AgentEventGoalVerify:
		s.setRemoteConnectionState(true, "online", "")
		if !s.aiControlEnabled() {
			_ = writeJSON(agentGoalErrorPayload(msg, errors.New("AI control is disabled for this device")))
			return
		}
		go handleAgentGoalMessage(msg, writeJSON)
	case models.AgentEventGoalPlanAIResponse, models.AgentEventGoalPlanAIError:
		deliverGoalPlanAIResponse(msg)
	case models.AgentEventTerminalCreate:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		// create leaves the read loop entirely: an attach replay (up to
		// 2MiB of scrollback as 64KiB frames, all sent under the session's
		// outputGate) used to block every subsequent message on this
		// connection — typing during a replay stalled, AI acks stalled, and
		// the 10s write deadline amplified it. The creating registry is
		// registered SYNCHRONOUSLY here (see beginCreate) so the very next
		// inline terminal.input can wait for the create instead of erroring;
		// terminal.input/resize/close themselves stay inline — input order
		// is contractual.
		release := s.terminal.beginCreate(remoteString(msg, "session_id"))
		safeGo("terminal.create", func() {
			defer release()
			s.terminal.create(msg, writeJSON)
		})
	case models.AgentEventTerminalInput:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.write(msg, writeJSON)
	case models.AgentEventTerminalResize:
		s.setRemoteConnectionState(true, "online", "")
		if !s.remoteTerminalEnabled() {
			_ = writeJSON(agentTerminalErrorPayload(remoteString(msg, "session_id"), errors.New("remote terminal is disabled for this device")))
			return
		}
		s.terminal.resize(msg, writeJSON)
	case models.AgentEventTerminalClose:
		s.setRemoteConnectionState(true, "online", "")
		s.terminal.close(msg, writeJSON)
	case models.AgentEventTerminalQuotaResolved:
		// Deliberately NOT behind the remote-terminal toggle: a challenge
		// verdict must always be honored — dropping it would strand a pending
		// challenge and eventually kill a session the user just approved.
		s.setRemoteConnectionState(true, "online", "")
		s.terminal.quotaResolved(msg, writeJSON)
	case models.AgentEventAISessionSubscribe, models.AgentEventAISessionUnsubscribe:
		// 跨端同态（spec §4.1/阶段 3）：服务端为打开会话详情的手机代发订阅。
		// 刻意不受 aiControlEnabled 门——订阅是只读观察（快照+事件副本），
		// 关掉 AI 控制不应让已打开的详情页变瞎；与本地透传路径同语义
		//（DispatchLocalAI 同款注释）。盖远端 origin 标记：快照经 emitter
		// 写回时 origin 去重 + ack 可寻址。
		msg[agentAIOriginSocketField] = agentAIRemoteSocketID
		s.setRemoteConnectionState(true, "online", "")
		sessionID := strings.TrimSpace(remoteString(msg, "session_id"))
		if sessionID == "" {
			_ = writeJSON(agentAIErrorPayload("", "", errors.New("ai.session.subscribe missing session_id")))
			return
		}
		if msgType == models.AgentEventAISessionSubscribe {
			s.ai.subscribeAISocket(agentAIRemoteSocketID, sessionID)
			s.ai.emitSessionSnapshot(agentAIRemoteSocketID, sessionID, writeJSON)
		} else {
			s.ai.unsubscribeAISocket(agentAIRemoteSocketID, sessionID)
		}
	case models.AgentEventAISessionCreate, models.AgentEventAIMessage, models.AgentEventAIRunStart, models.AgentEventAISteer, models.AgentEventAIApprovalResponse, models.AgentEventAIOptionResponse, models.AgentEventAIStop, models.AgentEventAISessionClose, models.AgentEventAITuiSync, models.AgentEventToolAskResponse:
		// 跨端同态（spec §4.1）：入站 AI 帧盖远端来源标记。位置刻意在 case 入口、
		// aiControlEnabled 门与任何 early-return 之前——错误回包路径（含门拒绝）
		// 同样有 origin 可寻址（Task 7 审批 ack 双写）。"__" 前缀标记禁止转发。
		msg[agentAIOriginSocketField] = agentAIRemoteSocketID
		s.setRemoteConnectionState(true, "online", "")
		switch msgType {
		case models.AgentEventAISessionCreate:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), "", errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.create(msg, writeJSON)
		case models.AgentEventAIMessage:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.message(msg, writeJSON)
		case models.AgentEventAIRunStart:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.runStart(msg, writeJSON)
		case models.AgentEventAISteer:
			if !s.aiControlEnabled() {
				emitAgentAISteerAck(writeJSON, remoteString(msg, "session_id"), remoteString(msg, "message_id"), "error", "AI control is disabled for this device", "ai_control_disabled")
				return
			}
			s.ai.steer(msg, writeJSON)
		case models.AgentEventAIApprovalResponse:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.approval(msg, writeJSON)
		case models.AgentEventAIOptionResponse:
			if !s.aiControlEnabled() {
				_ = writeJSON(agentAIErrorPayload(remoteString(msg, "session_id"), remoteString(msg, "message_id"), errors.New("AI control is disabled for this device")))
				return
			}
			s.ai.optionResponse(msg, writeJSON)
		case models.AgentEventToolAskResponse:
			// 自有交互工具作答:不受 aiControlEnabled 门——run 在跑时桥必须活着
			// (关掉控制开关不该掐死一个已阻塞在 ask_user 上的回合)。
			s.ai.toolAskResponse(msg, writeJSON)
		case models.AgentEventAIStop:
			s.ai.stop(msg, writeJSON)
		case models.AgentEventAITuiSync:
			s.ai.tuiSync(msg, writeJSON)
		case models.AgentEventAISessionClose:
			s.ai.close(msg, writeJSON)
		}
	case models.AgentEventAIApprovalState, models.AgentEventAIApprovalRequestAck:
		// Server-side approval status / request-received ack: informational.
		// Touch liveness; delivery/retry is driven by ai.approval.ack.
		s.setRemoteConnectionState(true, "online", "")
	case models.AgentEventAIApprovalCancelled:
		// Server cancelled approvals (e.g. device was offline past grace): drop
		// the matching local waiters so a still-running CLI is told to give up.
		s.setRemoteConnectionState(true, "online", "")
		s.ai.cancelApprovals(msg)
	case models.AgentEventAIOptionCancelled:
		// Server cancelled an option (e.g. device offline past grace): drop the
		// local pendingOption so a later stray response is ignored.
		s.setRemoteConnectionState(true, "online", "")
		s.ai.cancelOptions(msg)
	case models.AgentEventError:
		// Server rejected something. With the registration gate, business
		// messages should never be sent before registration, but surface the
		// error instead of letting it fall through to "unsupported event type"
		// — which silently buried agent_not_registered and left approval
		// waiters blocked for 24h. No auto-recovery here: the gate prevents the
		// condition; observing agent_not_registered now signals a regression.
		errCode := strings.TrimSpace(fmt.Sprint(msg["error"]))
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] remote_connection server_rejected error=%s detail=%v", errCode, msg["message"]))
	default:
		// 工具注册表：任何注册过的工具事件统一走通用 dispatch——新工具上注册
		// 表即可，无需再动这个 switch。未注册的类型维持原有 unsupported 行为。
		if t, ok := agentToolRegistry().Get(msgType); ok {
			s.setRemoteConnectionState(true, "online", "")
			go tools.RunWithGuards(t, msg, writeJSON)
			return
		}
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventError,
			"error": fmt.Sprintf("unsupported remote agent event type: %s", msgType),
		})
	}
}

func remoteAgentMessageRequiresEnabledDevice(msgType string) bool {
	switch msgType {
	case models.AgentEventProjectDetail,
		models.AgentEventTunnelConfigure,
		models.AgentEventAISessionDetail,
		models.AgentEventFileList,
		models.AgentEventFileRead,
		models.AgentEventFileUpload,
		models.AgentEventTransferUpload,
		models.AgentEventTransferReceive,
		"file.working_tree_diff",
		models.AgentEventSlashCommandsList,
		models.AgentEventGitStatus,
		models.AgentEventEnvInfo,
		models.AgentEventGoalPlan,
		models.AgentEventGoalVerify,
		models.AgentEventTerminalCreate,
		models.AgentEventTerminalInput,
		models.AgentEventTerminalResize,
		models.AgentEventTerminalClose,
		models.AgentEventAISessionCreate,
		models.AgentEventAIMessage,
		models.AgentEventAIRunStart,
		models.AgentEventAISteer,
		models.AgentEventAIApprovalResponse,
		models.AgentEventAIOptionResponse,
		models.AgentEventAIRename,
		models.AgentEventAIStop,
		models.AgentEventAISessionClose,
		// 设备互访阶段 1（spec §5.2）：对端发来的命令执行请求是控制面能力，
		// 与 terminal/AI 同族——禁用设备不得响应。
		// 设备互访阶段 2（spec §6）：LAN 直连 listener 同族——被禁用设备不得
		// 为他人提供文件服务。
		models.AgentEventPeerExecRequest,
		models.AgentEventPeerLanListen:
		return true
	default:
		// tools.list 是元 RPC（非注册表工具项），显式门控；注册表命中的工具事件
		// 一律视为需要 Enabled 设备（为 Phase-2 新工具兜底）。
		if msgType == models.AgentEventToolsList {
			return true
		}
		if _, ok := agentToolRegistry().Get(msgType); ok {
			return true
		}
		return false
	}
}

func (s *AgentService) remoteControlAllowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Enabled && s.state.Registered
}

// DispatchLocalAI routes a single in-process AI protocol event to the agent AI
// manager. It is the local counterpart of the AI branch in
// handleRemoteAgentMessage: the in-app chat WebSocket uses it so the web UI can
// drive the local Claude Code / Codex headless run and receive streaming
// ai.run.started / ai.delta / ai.done events without round-tripping through the
// remote agent server. writeJSON receives every event the manager emits.
// 2026-10-28 起 core 角色的仪表盘聊天改走 WS 透传（见 handlers.proxyAIStream），
// 本函数现仅由 agent 进程的 /api/agent/ai/stream 端点（56433）调用。
func (s *AgentService) DispatchLocalAI(msg map[string]interface{}, writeJSON func(interface{}) error) {
	if writeJSON == nil || msg == nil {
		return
	}
	msgType := strings.TrimSpace(fmt.Sprint(msg["type"]))
	switch msgType {
	case models.AgentEventAISessionCreate:
		s.ai.create(msg, writeJSON)
	case models.AgentEventAIMessage:
		s.ai.message(msg, writeJSON)
	case models.AgentEventAISteer:
		s.ai.steer(msg, writeJSON)
	case models.AgentEventAIApprovalResponse:
		s.ai.approval(msg, writeJSON)
	case models.AgentEventAIStop:
		s.ai.stop(msg, writeJSON)
	case models.AgentEventAISessionClose:
		s.ai.close(msg, writeJSON)
	case models.AgentEventAISessionSubscribe, models.AgentEventAISessionUnsubscribe:
		// 跨端同态（spec §4.1）：本机聊天连接订阅任意会话（手机发起的、终端
		// 导入的都行）。透传是裸泵，此端点只存在于 agent 进程。
		// 刻意不设设备控制门：订阅是只读观察（快照 + 后续扇出），不驱动任何
		// 会话状态；本端点也仅由 agent 进程自身的仪表盘回环（56433 localhost）
		// 到达，无设备控制门语境。
		// 刻意不扇出的回包（N2 记录）：steer ack 与 session.created 仍走
		// origin 写——插话方/发起方自己直接收，另一端靠快照/清单刷新可见。
		// 半订阅（评审 M4）：帧缺 __origin_socket 标记时 socketID 为空串，
		// subscribeAISocket 对未知 id no-op——什么都不订阅，但快照照发（只读
		// 握手，无害）。生产路径 HandleAIStream 恒盖章（socketID=="" 仅本地
		// socket 注册失败的极端场景），刻意不在读循环上打 Warn 防刷屏。
		socketID := strings.TrimSpace(remoteString(msg, agentAIOriginSocketField))
		sessionID := remoteString(msg, "session_id")
		if sessionID == "" {
			_ = writeJSON(agentAIErrorPayload("", "", fmt.Errorf("%s missing session_id", msgType)))
			return
		}
		if msgType == models.AgentEventAISessionSubscribe {
			s.ai.subscribeAISocket(socketID, sessionID)
			s.ai.emitSessionSnapshot(socketID, sessionID, writeJSON)
		} else {
			s.ai.unsubscribeAISocket(socketID, sessionID)
		}
	case models.AgentEventAIOptionResponse:
		s.ai.optionResponse(msg, writeJSON)
	case models.AgentEventToolAskResponse:
		// 与远端同语义：不受本地开关门（run 阻塞在 ask_user 上时必须能答）。
		s.ai.toolAskResponse(msg, writeJSON)
	case models.AgentEventSlashCommandsList:
		// Slash-command discovery shares the local chat socket so the web UI can
		// offer `/` completion. It routes through the same detail handler as the
		// remote link and is provider-aware (claude vs codex).
		handleAgentDetailMessageWithAI(msg, writeJSON, s.ai)
	default:
		_ = writeJSON(map[string]interface{}{
			"type":  models.AgentEventAIError,
			"error": fmt.Sprintf("unsupported local AI event type: %s", msgType),
		})
	}
}

type remoteConnectionIdentity struct {
	authorization string
	deviceID      string
}

func (s *AgentService) remoteConnectionSnapshot() (remoteConnectionIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	identity := remoteConnectionIdentity{
		authorization: authHeader,
		deviceID:      strings.TrimSpace(s.state.DeviceID),
	}
	return identity, authHeader != "" && identity.deviceID != "" && s.state.Enabled && s.state.Registered
}

func (s *AgentService) setRemoteConnectionState(connected bool, status string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setRemoteConnectionStateLocked(connected, status, message)
	_ = s.saveStateLocked()
}

func (s *AgentService) setRemoteConnectionStateLocked(connected bool, status string, message string) {
	s.state.RemoteConnected = connected
	if connected {
		s.state.Registered = true
	}
	s.state.LastSyncAt = time.Now().UTC().Format(time.RFC3339)
	s.state.LastSyncStatus = status
	s.state.LastSyncMessage = message
	s.syncRuntimeDeviceStatusLocked()
	if s.state.Device != nil {
		if connected {
			s.state.Device.Status = "online"
		} else {
			s.state.Device.Status = "offline"
		}
		s.state.Device.LastSeenAt = s.state.LastSyncAt
	}
}

func (s *AgentService) agentHelloPayload() map[string]interface{} {
	s.mu.Lock()
	s.ensureDeviceIdentityLocked()
	deviceID := s.state.DeviceID
	scanDirs := activeScanDirs(s.state.ScanDirectories, s.state.ScanDirectoriesEnabled)
	s.mu.Unlock()

	snapshot := s.collectAgentSyncSnapshotWithActiveRuns(scanDirs)
	// 画像冷层/热层(spec §4.2/§4.3)：system 是设备固定身份(OS/CPU/内存)，
	// net 是当前内网地址。服务端据此让 LLM 选目标设备、判同内网。
	systemSnap := collectAgentSystemSnapshot()
	toolVersions := make([]string, 0, len(snapshot.Tools))
	for _, tool := range snapshot.Tools {
		toolVersions = append(toolVersions, tool.ID+"@"+tool.Version)
	}
	return map[string]interface{}{
		"type":                   models.AgentEventHello,
		"protocol_version":       models.AgentProtocolVersion,
		"device_id":              deviceID,
		"device_name":            snapshot.DeviceName,
		"platform":               snapshot.Platform,
		"agent_version":          snapshot.AgentVersion,
		"host":                   snapshot.Host,
		"capabilities":           snapshot.Capabilities,
		"tools":                  snapshot.Tools,
		"subagents":              collectUserSubagents(),
		"agent_tools":            agentToolRegistry().Descriptors(),
		"agent_tools_rev":        agentToolRegistry().Rev(),
		"history":                snapshot.History,
		"projects":               snapshot.Projects,
		"vibe_sessions":          snapshot.VibeSessions,
		"authorized_directories": snapshot.AuthorizedDirectories,
		"collected_at":           snapshot.CollectedAt,
		"started_at":             time.Now().UTC().Format(time.RFC3339),
		"load":                   collectAgentLoadSnapshot(),
		"system":                 systemSnap,
		"net":                    collectAgentNetSnapshot(),
		// 冷层规范化摘要：服务端可用它识别「能力画像未变」的重复 hello
		// (spec §4.2 profile_hash)。老服务端忽略未知字段，向前兼容。
		"profile_hash": agentProfileHash(snapshot.Platform, snapshot.Capabilities, toolVersions, snapshot.AuthorizedDirectories),
	}
}

func (s *AgentService) remoteTerminalEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Enabled || !s.state.Registered {
		return false
	}
	if s.state.Device == nil {
		return true
	}
	return s.state.Device.RemoteTerminalEnabled
}

func (s *AgentService) aiControlEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.Enabled || !s.state.Registered {
		return false
	}
	if s.state.Device == nil {
		return true
	}
	return s.state.Device.AIControlEnabled
}

func (s *AgentService) currentDeviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureDeviceIdentityLocked()
	return s.state.DeviceID
}

func currentAgentWebSocketURL() (string, error) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || strings.TrimSpace(cfg.AgentBaseURL()) == "" {
		return "", errors.New("agent server is not configured")
	}
	parsed, err := url.Parse(cfg.AgentBaseURL())
	if err != nil {
		return "", err
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported agent server scheme: %s", parsed.Scheme)
	}
	if (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1") && parsed.Port() == "5174" {
		parsed.Host = parsed.Hostname() + ":4000"
	}
	parsed.Path = models.AgentWSEndpoint
	parsed.RawQuery = ""
	return parsed.String(), nil
}

func agentPlatform() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}

func agentCapabilities() []string {
	caps := []string{"terminal", "terminal_stream", "terminal_replay", "file_read", "file_diff", "command_launch", "ai_run_protocol_v2", "ai_run_start_v3", "ai_provider_binding_v1"}
	if agentNativePTYSupported() {
		caps = append(caps, "terminal_pty", "terminal_resize")
	} else {
		caps = append(caps, "terminal_pipe")
	}
	caps = append(caps, agentAICapabilities()...)
	caps = append(caps,
		"http_tunnel_v1",
		"tcp_tunnel_v1",
		"websocket_tunnel_v1",
		"goal_server_v1",
		"goal_plan_service_v1",
		"goal_report_v1",
		"goal_verify_v1",
		"workspace_fingerprint_v1",
		"file_transfer_v1",
		// 设备互访阶段 1（spec §5.1）：一次性命令执行 + fetch 编排能力位。
		// 服务端按位门控下发 peer.* 流量，老设备自动落「仅中继」兼容路径。
		"peer_v1",
		// 设备互访阶段 2 增量 2（spec §6）：LAN 协商/直连数据面。具备该位才
		// 参与 transfer.receive 的 direct_candidates 协商与 peer.lan.listen。
		"peer_path_v1",
	)
	return caps
}

func agentMessageJSON(payload interface{}) string {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
