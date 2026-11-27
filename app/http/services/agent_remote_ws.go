package services

import (
	"context"
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
	"aliang.one/nursorgate/app/tunnel"
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

// newAgentRemoteWSDialer 构造远端 WS 拨号器。显式书写 DefaultDialer 的两条
// 默认不变量(ProxyFromEnvironment + 正握手超时)防静默回退:Proxy 丢了会在
// 仅代理可达出口的环境造成「REST 注册成功 / WS 永久重连失败」不对称;超时
// 归零则握手挂死时 dial goroutine 无界。事实核对(2026-11-22 评审纠正):
// gorilla v1.5.3 DefaultDialer 本就是 {Proxy, 45s},当日 2h+ dialing 静默
// 并非拨号缺陷,而是 WS goroutine 停在被焊死的 s.mu 上(锁纪律修复根治,
// 见 agent_channel_lock_reentry_test.go);本拨号器是钉死不变量的文档化锚点。
func newAgentRemoteWSDialer(handshakeTimeout time.Duration) *websocket.Dialer {
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: handshakeTimeout,
	}
}

var agentRemoteWSDialer = newAgentRemoteWSDialer(45 * time.Second)

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
		// 显式拨号器(不变量钉死见 newAgentRemoteWSDialer):当日 09:02:49
		// 第 5 次 dialing 后 2h+ 无 connected/dial_failed 的真因是 WS goroutine
		// 停在被焊死的 s.mu 上(锁纪律修复根治),非拨号器缺陷。
		conn, resp, err := agentRemoteWSDialer.Dial(wsURL, headers)
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

		if err := s.runRemoteAgentSession(conn, identity.account); err != nil {
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
	// 远程协助 P1(Task 4):连接被强制拆除=协助腿随之中断,活跃会话按
	// device_restart 收口(spec §5/§14 坑 3)。此处的上行 assist.end 预期
	// 就是失败的(writer 随连接一起失效)——本调用的目标是本地状态收敛
	// (落定终态,重连后不复活),投递不是目标;云端由 device_offline/
	// admin 侧超时自愈(spec §5 明文该语义)。
	s.assistEndForConnectionDown()
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
		// 远程协助 P1(Task 4):不可恢复循环终态(进程关闭/禁用兜底)与
		// forceDisconnectRemote 同族——会话按 device_restart 收口,重连后
		// 云端拉取终态即知,勿复活(spec §14 坑 3)。上行 assist.end 预期
		// 失败(writer 已随连接拆除),目标同 forceDisconnectRemote:状态
		// 收敛而非投递,云端 device_offline/admin_timeout 自愈。可恢复分支
		// 刻意不动:瞬断丢帧由云侧语义兜底,本地会话跨重连存活(与 AI/
		// 终端腿同语义)。
		s.assistEndForConnectionDown()
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

func (s *AgentService) runRemoteAgentSession(conn *websocket.Conn, accounts ...string) error {
	// sessionCtx 是本条 WS 会话的生命周期：会话退出（断线/重连/停用）即取消，
	// 让经注册表派发的工具调用（peer_exec 这类真实远端操作）不再 Background
	// 孤儿到底（2026-11-05 审计盲点 4）。
	account := s.operationAccount()
	if len(accounts) > 0 {
		account = accounts[0]
	}
	sessionCtx, sessionCancel := context.WithCancel(context.WithValue(context.Background(), operationConnectionAccount{}, account))
	defer sessionCancel()
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
		if frame, ok := payload.(map[string]interface{}); ok {
			kind := remoteString(frame, "type")
			if (kind == "operation.progress" || kind == "operation.reconcile") &&
				(account == "" || account != s.operationAccount()) {
				return errors.New("remote progress account changed")
			}
		}
		if !s.waitForRegistration(agentRemoteRegistrationWait.Load()) {
			return errors.New("remote agent not registered within deadline")
		}
		return rawWriter(payload)
	}
	s.setCurrentRemoteWriter(publishedWriter)
	defer s.clearCurrentRemoteWriter()
	defer func() {
		sessionCancel()
		s.operations.setConnected(false)
	}()
	defer s.progress.supported.Store(false)
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
	// 重连补订阅（2026-11-12 视频会话事故）：远端 socket 重建后订阅集为空，
	// 服务端重发（agent.registered）只看其 interest 注册表（30 分钟 TTL）——
	// 在飞 run 的会话若不在册就会静默失联。按本机活跃 run 权威补订；门关着
	// 也没关系，订阅集在内存里，门开后扇出照常投递。
	s.ai.resubscribeRemoteAISessionsForActiveRuns()

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
				// 内存态变更(create 纳管 create-only 会话)即使磁盘指纹不变也要
				// 放行一次 hello;标记被 30s 门 defer 掉就等 60s 周期兜底,不重试。
				if !consumeAgentVibeMemorySnapshotDirty() && s.remoteHello.unchanged(digest) {
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

	// Only this connection can renew its watches or emit their notifications.
	// Disk lookup and writes stay off the reader and heartbeat goroutines.
	var transcriptWatches chan map[string]interface{}
	defer sessionCancel()

	for {
		var msg map[string]interface{}
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		// Any inbound traffic confirms the peer is alive; refresh the deadline so
		// a chatty-but-pong-less peer isn't misclassified as dead.
		resetReadDeadline()
		if remoteString(msg, "type") == models.AgentEventAITranscriptWatch {
			if !s.remoteControlAllowed() || account == "" || account != s.operationAccount() {
				continue
			}
			if transcriptWatches == nil {
				transcriptWatches = make(chan map[string]interface{}, maxAgentTranscriptWatches)
				frames := transcriptWatches
				safeGo("agent_transcript_watches", func() {
					runAgentTranscriptWatches(sessionCtx, frames, func(sessionID string) error {
						if sessionCtx.Err() != nil || account != s.operationAccount() || !s.remoteControlAllowed() {
							return context.Canceled
						}
						return publishedWriter(map[string]interface{}{
							"type": models.AgentEventAITranscriptChanged, "session_id": sessionID,
						})
					})
				})
			}
			select {
			case transcriptWatches <- msg:
			default:
			}
			continue
		}
		s.handleRemoteAgentMessage(sessionCtx, msg, writeJSON)
	}
}

func (s *AgentService) sendAgentHello(writeJSON func(interface{}) error, reason string) error {
	// 增量协议三步(plan→apply→commit)必须串行:并发 reason 交叉簿记会把段
	// 指纹/会话基线记到对方的帧上。写路径本就按连接串行,这里显式加序。
	defer s.helloDelta.beginSequence()()
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
	framePlan := s.helloDelta.plan(reason, hello)
	wire := s.helloDelta.applyPlan(hello, framePlan)
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection sending_hello reason=%s sync_mode=%s device_id=%s platform=%s projects=%d vibe_sessions=%d dirs=%d session_changes=%d session_removed=%d%s",
		strings.TrimSpace(reason),
		framePlan.mode,
		wire["device_id"],
		wire["platform"],
		projectCount,
		vibeSessionCount,
		dirCount,
		len(framePlan.sessionChanges),
		len(framePlan.sessionRemoved),
		framePlanLogSuffix(framePlan),
	))
	if err := writeJSON(wire); err != nil {
		return err
	}
	// 基线从全量构建结果推进(full 帧线==全量;delta 只吸收变化段;heartbeat 不动),
	// 失败路径不进 commit=基线不动,下个 tick 重试(与 gate 既有范式一致)。
	s.helloDelta.commit(&framePlan, hello)
	s.remoteHello.noteSent(sentDigest, time.Now())
	logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection hello_sent reason=%s sync_mode=%s", strings.TrimSpace(reason), framePlan.mode))
	return nil
}

// framePlanLogSuffix 把降档动因带进日志(全量锚点的触发原因排障必需)。
func framePlanLogSuffix(plan agentHelloFramePlan) string {
	switch {
	case plan.forceReason != "":
		return " force=" + plan.forceReason
	case plan.selfHeal:
		return " force=self_heal"
	default:
		return ""
	}
}

func (s *AgentService) handleRemoteAgentMessage(ctx context.Context, msg map[string]interface{}, writeJSON func(interface{}) error) {
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
	case "operation.progress.ack":
		s.progress.acceptAck(msg)
	case "operation.updated":
		account, _ := ctx.Value(operationConnectionAccount{}).(string)
		s.applyCloudOperation(account, msg)
	case models.AgentEventRegistered:
		logger.Info(fmt.Sprintf("[AGENT-BOOT] remote_connection registered_ack server_device_id=%s", strings.TrimSpace(fmt.Sprint(msg["device_id"]))))
		// 增量协议轻对账(零新协议):agent.registered 每帧必回且携带服务端当前
		// vibe 会话数。服务端归零而基线非零 = 内存态丢失信号(PG flush 回滚类
		// 残余窗口),升格下帧 full;重启类丢失由拓扑兜底(重启必杀 socket→
		// connect 全量),这里只封尾部窗口。
		if serverSessions, ok := msg["vibe_session_count"].(float64); ok {
			s.helloDelta.reconcileRegisteredServerSessions(int(serverSessions))
		}
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
		supported, _ := msg["operation_progress_v1"].(bool)
		s.progress.supported.Store(supported)
		if account, _ := ctx.Value(operationConnectionAccount{}).(string); supported && account == s.operationAccount() {
			s.refreshOperationMirror(ctx)
			go s.flushOperationFinishes(ctx)
		}
		s.ai.replayPendingTerminals(writeJSON)
		// 子 agent 终态帧断线暂存重放（2026-11-10 P2 #4，见
		// agent_ai_subagent_journal.go）：断窗内被吞的 ai.subagent 终态帧在此
		// 补发，写成功即清账；服务端 (session,msg,task) 幂等 upsert。
		s.ai.replayPendingSubagentTerminals(writeJSON)
		// 审批请求投递 journal 重放(L1,2026-11-01):半开 socket 期间丢失的
		// ai.approval.request 原样补发;服务端 intake 以 approval_id 幂等。
		s.ai.replayPendingApprovalRequests(writeJSON)
		// 远端 socket 门开后复活（2026-10-28 评审 I2）：ensureRemoteAISocket 在
		// 连接建立即注册，但那时注册门未开——门开前的扇出会让 pump 卡在
		// publishedWriter→waitForRegistration（≤5s）后撞写超时、被 onDead 摘除，
		// 且本连接内不再有别的复活点（手机从此静默收不到审批广播）。此处幂等
		// ensure 一次：门已开，被摘/缺失的 remote socket 就地复活；活着的 no-op。
		s.ai.ensureRemoteAISocket(s.currentRemoteWriter())
		// 复活的 socket 若是新建（订阅集为空），同款补订阅（2026-11-12 事故）；
		// 活着的 no-op。
		s.ai.resubscribeRemoteAISessionsForActiveRuns()
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
		// 监听器断联暂存重放(specs/005 FR-5,Task 9)：注册成功即按序补发
		// outbox(缝内 safeGo 离线化，不堵读循环)。
		listenerOutboxReplayHook()
	case models.AgentEventHeartbeatAck:
		s.setRemoteConnectionState(true, "online", "")
		// Retry durable terminals while the socket remains healthy. A prior
		// delivery may have reached Server while its database was unavailable;
		// waiting for another reconnect would otherwise leave the run stuck.
		s.ai.replayPendingTerminals(writeJSON)
		s.ai.replayPendingApprovalRequests(writeJSON)
	case models.AgentEventAIRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		// 拒收即毒帧（2026-11-17 审计 P2）：服务端拒收具有终局性（重放永远同
		// 样被拒），seq 防回退守卫会让 rejected(seq=0) 永远留在账上——每 10s
		// 心跳全量重放、文件无界增长。带明确非 accepted result 的 ack 走强制
		// 清账止循环；无 result 字段（旧服务器）保持纯 seq 语义。
		if r := remoteString(msg, "result"); r != "" && r != "accepted" {
			s.ai.discardPendingTerminal(remoteString(msg, "run_id"), r)
		} else {
			s.ai.acknowledgePendingTerminal(
				remoteString(msg, "run_id"),
				int64(remoteInt(msg, "accepted_seq", 0)),
			)
		}
	case models.AgentEventGoalRunEventAck:
		s.setRemoteConnectionState(true, "online", "")
		if remoteString(msg, "result") == "accepted" {
			s.ai.acknowledgePendingTerminal(
				remoteString(msg, "goal_run_id"),
				int64(remoteInt(msg, "accepted_seq", 0)),
			)
		} else {
			// 拒收（PhoneServer aiRun.ts：result=rejected 附 reason，终局性）
			// ——重放无出路，清账止循环（2026-11-17 审计 P2）。
			s.ai.discardPendingTerminal(remoteString(msg, "goal_run_id"), remoteString(msg, "result"))
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
	case models.AgentEventPeerLanPushListen:
		// 上传腿 LAN 直连(P1-4,角色互换):A(目标设备)拉一次性收件 listener,
		// B 直推文件省掉 COS 上行段。绑定回包不等服务过程,经 safeGo 离线化
		// (peer.lan.listen 同款);enabled-device 门禁与 peer.lan 同族——被禁用
		// 设备不得为他人提供收件面。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_lan_push_listen", func() { s.handleAgentPeerLanPushListen(msg, writeJSON) })
	case models.AgentEventPeerLanPushListenResult:
		// A→server 的绑定回包经通用 RPC resolve(request_id 匹配)消费;到达
		// 其他侧属异常路径——静默消费防 default 臂 unsupported 噪声。
		s.setRemoteConnectionState(true, "online", "")
		logger.Info(fmt.Sprintf("[AGENT-PEER-LAN-PUSH] push_listen_result at unexpected peer request_id=%s",
			remoteString(msg, "request_id")))
	case models.AgentEventPeerPunchSession:
		// mesh UDP 打洞（Task 10）B 端信令：绑物理口 socket+STUN 判型+起
		// QUIC listener 是秒级动作（STUN 探测上限数秒），result 在绑定完成
		// 即回、服务过程由 TTL 收摊——经 safeGo 离线化，读 loop 畅通优先
		// （peer.lan.listen 同款）。enabled-device 门禁与 peer.lan 同族。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_punch_session", func() { s.handleAgentPeerPunchSession(msg, writeJSON) })
	case models.AgentEventPeerPunchCandidates:
		// 打洞候选双向交换的双端接收面：B 端按 session_id 路由到在册会话，
		// 探测 Dial 在注册表内异步起火（起火即忘）；A 端按 pending 表喂等
		// 待方。查表轻量，仍走 safeGo 防探测编排的慢路径拖 read loop。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_punch_candidates", func() { s.handleAgentPeerPunchCandidates(msg) })
	case models.AgentEventPeerPunchSessionResult:
		// B→server→A 的绑定回包：A 端按 request_id 喂 pending 等待方
		// （Task 10b）。到达 B 侧属错投——pending 查无即静默消费，防
		// default 臂 unsupported 噪声（peer.lan.listen_result 同款）。
		s.setRemoteConnectionState(true, "online", "")
		safeGo("peer_punch_session_result", func() { sharedAgentPeerPunchA().handleResultFrame(msg) })
	case models.AgentEventListenerUpsert:
		// 事件监听器镜像下行(specs/005-event-listener §2/FR-4)：server 注册表
		// 行变更推镜像。状态同步类流量(与 device.settings 同族)，不进
		// enabled-device 门控名单——被禁用设备也须保持镜像同步；读循环内联
		// 处理保持帧序(LWW 仲裁输入)。handler 自带 recover 护栏(旧工具臂
		// 同款仓规)——Task 9 钩子会跑真 watcher 生命周期代码,畸形帧只记
		// 日志、panic 就地吞掉,绝不撕断读循环。
		s.setRemoteConnectionState(true, "online", "")
		if err := handleListenerUpsertFrame(msg); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] upsert frame error=%v", err))
		}
	case models.AgentEventListenerRemove:
		// 事件监听器镜像下行同族(specs/005-event-listener §2/FR-4)：删镜像行
		// + watcher 停止钩子(钩子幂等，server 补偿重放 remove 是常态；护栏
		// 同 upsert)。
		s.setRemoteConnectionState(true, "online", "")
		if err := handleListenerRemoveFrame(msg); err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-LISTENER] remove frame error=%v", err))
		}
	case models.AgentEventListenerEventReject:
		// 事件上行拒绝回执(specs/005 §2/FR-6,Task 9)：file_tail watcher 批
		// 消费——fired/expired/deleted/paused 停 watcher，device_unregistered
		// 计数(≥5 置 error)。状态同步类流量，不进 enabled-device 门控。
		s.setRemoteConnectionState(true, "online", "")
		handleListenerEventRejectFrame(msg)
	case models.AgentEventListenerTriggered:
		// 云端触发引擎命中帧(specs/005 §2,Task 14)：server 已判定监听器命中
		// (once+recurring 同帧)，原样账号级广播给全部本机 AI-chat socket
		// (仪表盘 toast 面)。状态同步类展示流量，不进 enabled-device 门控
		// (upsert/remove 同族)；广播 enqueue 有界非阻塞，不堵 read loop；
		// server 侧 best-effort——agent 侧同样 fire-and-forget，不回执。
		s.setRemoteConnectionState(true, "online", "")
		s.ai.BroadcastListenerEvent(msg)
	// 注意：这五个旧工具事件（file.list/file.read/file.write/git.status/env.info；
	// file.write 为 D3 新增，与 file.list/file.read 同走 agent_detail 臂）由下面的
	// 显式臂直派、有意绕过 default 臂的 tools.RunWithGuards 护栏（无超时看门狗/
	// 输出上限/panic 恢复）——响应形状兼容优先。注册表里它们的 caps（TimeoutMs/
	// MaxOutputBytes）暂不对这两个臂生效。严禁新增 Event 与任何显式臂冲突的
	// 注册表工具项（会被广告但永远不会走护栏分发）。
	case models.AgentEventProjectDetail, models.AgentEventAISessionDetail, models.AgentEventFileList, models.AgentEventFileRead, models.AgentEventFileWrite, models.AgentEventSlashCommandsList, "file.working_tree_diff":
		s.setRemoteConnectionState(true, "online", "")
		// 旧臂仍绕过 RunWithGuards(响应形状兼容优先,caps 暂不生效),但 panic
		// 恢复必须兜住:裸 go 的 panic 顺 Go 语义杀全进程——read_file 是云端
		// 每回合必调的高频工具,一次 payload 解析 panic 就带走所有活 PTY。
		safeGo("agent_detail", func() { dispatchDetailWithAI(ctx, msg, writeJSON, s.ai) })
	case models.AgentEventGitStatus, models.AgentEventEnvInfo:
		s.setRemoteConnectionState(true, "online", "")
		safeGo("agent_envtools", func() { dispatchEnvTools(ctx, msg, writeJSON) })
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
	case models.AgentEventTerminalCreate, models.AgentEventTerminalInput,
		models.AgentEventTerminalResize, models.AgentEventTerminalClose:
		// 四类终端帧统一走 DispatchTerminalFrame(与网页腿共用的分派口;
		// create 异步离读循环/input·resize·close 保持 inline 的契约注释
		// 见该函数)。本腿的 enabled 闸给 remoteTerminalEnabled。
		s.setRemoteConnectionState(true, "online", "")
		// assist- 前缀帧归协助腿(Task 5):前置门控,返回 true=本帧已被协助
		// 腿处置(放行 input/resize 或拒绝),不得再走手机腿;false=无前缀帧
		// 继续下方手机腿原路径(既有行为零改动)。
		if s.handleAssistTerminalFrame(msg, writeJSON) {
			return
		}
		// 手机腿无连接 sink 身份(nil):网页孤儿回收链不涉手机腿
		s.DispatchTerminalFrame(msg, writeJSON, nil, s.remoteTerminalEnabled)
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
			// 快照先读后订（2026-11-09 审计序仲裁）：journal 读取发生在订阅前
			// ——订阅后 fanOut 才可能向本 socket 投递 delta，而快照载荷此后才
			// 入队，队列 FIFO 保证订阅后的 delta 恒在快照之后；先订后读的旧序
			// 里，已入队 delta 会被晚到的全 done 快照整体替换冲掉（吞流根因，
			// 前端 running 占位保留为兜底）。
			payload := s.ai.buildSessionSnapshotPayload(sessionID)
			// 订阅+快照入队同一 m.mu 临界区（2026-11-17 审计：旧序两步锁交接
			// 之间扇出的 delta 会先于快照入队被快照吞掉）。
			s.ai.subscribeAndEnqueueSnapshot(agentAIRemoteSocketID, sessionID, payload, writeJSON)
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
		case models.AgentEventAISubagentControl:
			// 子 agent 控制面（L3）：per-task 停止/追加指令。与 ai.stop 不同，
			// 它不动主回合——只作用在被点名的那个子 run 上。
			if !s.aiControlEnabled() {
				_ = writeJSON(map[string]interface{}{
					"type":       models.AgentEventAISubagentControlAck,
					"session_id": remoteString(msg, "session_id"),
					"task_id":    remoteString(msg, "task_id"),
					"result":     "error",
					"error":      "AI control is disabled for this device",
					"code":       "ai_control_disabled",
				})
				return
			}
			s.ai.subagentControl(msg, writeJSON)
		case models.AgentEventAITuiSync:
			s.ai.tuiSync(msg, writeJSON)
		case models.AgentEventAISessionClose:
			s.ai.close(msg, writeJSON)
		}
	case models.AgentEventAIApprovalState:
		// Server-side approval status: informational. Touch liveness;
		// delivery/retry is driven by ai.approval.ack.
		s.setRemoteConnectionState(true, "online", "")
	case models.AgentEventAIApprovalRequestAck:
		// 服务端已收货(intake 幂等 outcome 随帧):清除投递 journal 条目。
		// L1 可靠投递(2026-11-01)——半开 socket 丢帧靠重连重放补达,ack 是
		// 「必达」的终止信号。
		s.setRemoteConnectionState(true, "online", "")
		s.ai.acknowledgePendingApprovalRequest(remoteString(msg, "approval_id"))
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
	case models.AgentEventAssistApproved, models.AgentEventAssistRejected,
		models.AgentEventAssistEnd, models.AgentEventAssistState:
		// 远程协助 P1(Task 4):云端协助信令分派。OnRemoteFrame 内聚全部
		// 业务(状态机迁移+钩子+幂等吞帧),本臂保持薄分派;回帧经本连接
		// writeJSON 上行——manager 分派路径不持 writer。错误(串会话/缺
		// reason 等)转 error 帧上抛照 punch/terminal 家族语义(agent.error
		// 载荷),读循环不受影响;enabled 门已在
		// remoteAgentMessageRequiresEnabledDevice(禁用设备不接受协助)。
		s.setRemoteConnectionState(true, "online", "")
		reply, err := s.assist.OnRemoteFrame(msg)
		if err != nil {
			logger.Warn(fmt.Sprintf("[ASSIST] remote frame rejected type=%s error=%v", msgType, err))
			_ = writeJSON(map[string]interface{}{
				"type":  models.AgentEventError,
				"error": fmt.Sprintf("assist: %v", err),
			})
			return
		}
		for _, frame := range reply {
			// 回帧写失败不撕断读循环:状态迁移已提交,云端补帧/房间自愈兜底
			// (punch 信令同款姿态)。
			if err := writeJSON(frame); err != nil {
				logger.Warn(fmt.Sprintf("[ASSIST] reply frame write failed type=%s error=%v", msgType, err))
				return
			}
		}
	case models.AgentEventAssistRrEvent, models.AgentEventAssistRrSnapshot, models.AgentEventAssistNav:
		// 远程协助 P2(Task 1):rrweb 页面镜像帧族下行纯中继。帧自云端
		// (admin 观看页采集侧)来,经设备原样扇给观看端注册表——不改写载荷、
		// 不进状态机、零回帧;sid 与当前/粘性终态会话不符静默丢弃(判定与
		// 限频日志见 RelayRrFrame:纯中继不做 error 帧风暴)。enabled 门已在
		// remoteAgentMessageRequiresEnabledDevice;协助总闸关时静默丢——镜像
		// 面是协助面的一部分,总闸关=面收摊,观看注册表本就随 assist.end 清空。
		// 高频中继臂:活性触达不落盘(活性由心跳承载;全量落盘成本见
		// touchRemoteConnectionOnline/2026-11-22 锁史)。序刻意:总闸/串会话
		// 先判,被丢帧零状态触达——仅在帧真被中继后才 touch(内存态,零 IO)。
		if !s.RemoteAssistEnabled() || s.assist == nil {
			return
		}
		if s.assist.RelayRrFrame(msg, msgType) {
			s.touchRemoteConnectionOnline()
		}
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
			go tools.RunWithGuards(ctx, t, msg, writeJSON)
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
		models.AgentEventFileWrite,
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
		// mesh UDP 打洞（Task 10）：punch listener 与候选探测同族——被禁用
		// 设备不得为他人提供打洞数据面。
		models.AgentEventPeerExecRequest,
		models.AgentEventPeerLanListen,
		models.AgentEventPeerLanPushListen,
		models.AgentEventPeerPunchSession,
		models.AgentEventPeerPunchCandidates,
		// 远程协助 P1(Task 4):云端协助信令(approved/rejected/end/state)是
		// 控制面能力,与 terminal/AI 同族——禁用设备不接受协助。
		// 远程协助 P2(Task 1):rrweb 页面镜像帧族同族——镜像下行同为控制面
		// 能力(观看端页面由 admin 侧采集注入),禁用设备不接受。
		models.AgentEventAssistApproved,
		models.AgentEventAssistRejected,
		models.AgentEventAssistEnd,
		models.AgentEventAssistState,
		models.AgentEventAssistRrEvent,
		models.AgentEventAssistRrSnapshot,
		models.AgentEventAssistNav:
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
	case models.AgentEventAISubagentControl:
		s.ai.subagentControl(msg, writeJSON)
	case models.AgentEventAISessionClose:
		s.ai.close(msg, writeJSON)
	case models.AgentEventAIRename:
		s.renameDashboardSession(msg, writeJSON)
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
			// 快照先读后订 + 队列下发（2026-11-09 审计序仲裁，与远端路径同
			// 语义）：socket 不存在（半订阅）时 enqueueSessionSnapshot 回落
			// 直写，只读握手语义不变。
			payload := s.ai.buildSessionSnapshotPayload(sessionID)
			// 同远端路径：订阅+快照入队同一临界区（2026-11-17 审计）。
			s.ai.subscribeAndEnqueueSnapshot(socketID, sessionID, payload, writeJSON)
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
		// 本地 socket 入口没有 WS 会话生命周期可挂，保持无主 ctx（与历史
		// 行为一致——file.detail 类工具自身有界，无孤儿面）。
		handleAgentDetailMessageWithAI(context.Background(), msg, writeJSON, s.ai)
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
	account       string
}

func (s *AgentService) remoteConnectionSnapshot() (remoteConnectionIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authHeader := strings.TrimSpace(s.effectiveUserAuthorizationLocked(""))
	identity := remoteConnectionIdentity{
		authorization: authHeader,
		deviceID:      strings.TrimSpace(s.state.DeviceID),
		account:       strings.TrimSpace(s.state.RegisteredUser),
	}
	return identity, authHeader != "" && identity.deviceID != "" && s.state.Enabled && s.state.Registered
}

func (s *AgentService) setRemoteConnectionState(connected bool, status string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setRemoteConnectionStateLocked(connected, status, message)
	_ = s.saveStateLocked()
}

// touchRemoteConnectionOnline 活性触达的免落盘变体(高频臂专用,rrweb 页面
// 镜像中继臂首个使用方):状态迁移语义与 setRemoteConnectionState 一致,但
// 跳过 saveStateLocked。理由:setRemoteConnectionState 每次调用都全量重写
// agent_state.json(实测 ~188µs @~1KB 状态,APFS;Windows NTFS 突发下 s.mu
// 占比更高,且 s.mu 正是 2026-11-22 锁死史之锁)——每秒几十帧的中继臂上这
// 是纯浪费,连接活性由 heartbeat.ack 臂承载,LastSeenAt 内存戳足以保鲜。
func (s *AgentService) touchRemoteConnectionOnline() {
	s.mu.Lock()
	s.setRemoteConnectionStateLocked(true, "online", "")
	s.mu.Unlock()
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

// CurrentDeviceID 本机 device_id 的只读查询(解析常驻身份文件,幂等);跨层
// 消费方入口——mesh LAN 装配(cmd/core)与 /api/mesh/status
// (handlers.MeshHandler,Task 14)。
func (s *AgentService) CurrentDeviceID() string { return s.currentDeviceID() }

// TunnelManager 暴露隧道 Manager 给 core boot 的 mesh 装配接线
// (cmd/core → WireMeshAssemblySeams,2026-11-19 0.196 登录死锁根治)。
// core 是 mesh 数据面唯一属主,user agent runtime 不得接线(见
// mesh_assembly.go 进程归属注)。
func (s *AgentService) TunnelManager() *tunnel.Manager { return s.tunnel }

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
		"operation_progress_v1",
		"ai_transcript_watch_v1",
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
