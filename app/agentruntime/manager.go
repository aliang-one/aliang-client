package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

const (
	capabilityPath = "/api/agent/status"
	protocolPath   = "/api/agent/protocol"
)

// ensureStartedMu 保证 EnsureStarted 在同一进程内串行执行，避免 auth-success 事件、
// 冷启动以及未来的 watchdog 并发调用时重复 spawn user-agent。跨进程唯一性由 user-agent
// 自身的端口监听（56433）兜底，后续 pidfile flock 会再补一层内核级互斥。
var ensureStartedMu sync.Mutex

func init() {
	// 将 ensureAgentAfterAuth 注入 services 的 auth-success 钩子，规避 services →
	// agentruntime 的循环依赖。所有包 init 跑完后，hook 才会在首次登录事件中被调用。
	services.EnsureAgentAfterAuthHook = ensureAgentAfterAuth
}

// ensureAgentAfterAuth 是 auth-success 钩子的真实实现。
//
// core（root daemon）在此跳过 spawn：user-agent 必须以登录用户身份运行，才能读到
// 正确的家目录、Claude/Codex 凭证与 ~/.aliang 状态。core 是 root，spawn 出的子进程
// 会继承 root 身份，家目录与凭证全部错位；更糟的是它会与 .app 之后 spawn 的用户态
// agent 共用 ~/.aliang/agent（HOME 继承），抢 56433 端口和 flock，形成 root+mac 双
// agent 混战。所以 user-agent 的 spawn 必须交给用户态 .app——它的冷启动 EnsureStarted
// （companion.go）+ watchdog（companion_watchdog.go）负责拉起并看护。
//
// core 跳过 spawn 不影响 token 下发：core 仍会经 agentSyncDispatch 把 token 转发给
// 已在跑的 user-agent（POST 127.0.0.1:56433/api/agent/sync）。
func ensureAgentAfterAuth() error {
	if os.Geteuid() == 0 {
		logger.Info("[AGENT-BOOT] ensure_agent_after_auth skipped reason=root_daemon (user-agent must run as login user; spawn left to .app)")
		return nil
	}
	return EnsureStarted()
}

// EnsureStarted ensures the local user-agent process is running and ready.
// It is safe to call concurrently or repeatedly: an in-process mutex serializes
// callers, and the existing-runtime fast path short-circuits when 56433 already
// answers with a matching protocol.
func EnsureStarted() error {
	ensureStartedMu.Lock()
	defer ensureStartedMu.Unlock()
	logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started begin local_agent_url=%s capability_path=%s", services.UserAgentBaseURL(), capabilityPath))
	if SupportsCurrentAgentAPI(700 * time.Millisecond) {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started existing_runtime_healthy local_agent_url=%s", services.UserAgentBaseURL()))
		if err := requestUserAgentStartupSync("ensure_started_existing"); err == nil {
			return nil
		} else if !services.CanUseAdminConsoleAgentRegistration() || !shouldReplaceExistingRuntimeAfterSyncError(err) {
			return nil
		}
		logger.Warn("[AGENT-BOOT] ensure_started replacing_existing_runtime_after_startup_sync_failure")
		stopStaleUserAgentIfNeeded(700 * time.Millisecond)
	} else {
		stopStaleUserAgentIfNeeded(700 * time.Millisecond)
	}

	cmd, err := newAgentProcessCommand()
	if err != nil {
		return err
	}
	// The rotating log writer backs the child's stdout/stderr via an os/exec
	// pipe+copy goroutine that runs for the agent's lifetime, so it must NOT be
	// closed here — closing it would cut the captured agent.log off the moment
	// EnsureStarted returns. It is reclaimed once the child exits.

	logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started launching_user_agent command=%q args=%v", cmd.Path, cmd.Args))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start user agent process: %w", err)
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started user_agent_process_started pid=%d", cmd.Process.Pid))
	go func() {
		if err := cmd.Wait(); err != nil {
			logger.Warn(fmt.Sprintf("User agent process exited: %v", err))
		}
	}()

	attempts, err := waitForCurrentAgentAPI(7*time.Second, 700*time.Millisecond)
	if err == nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started user_agent_ready local_agent_url=%s readiness_attempts=%d", services.UserAgentBaseURL(), attempts))
		return nil
	}
	logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started capability_check final_failed path=%s attempts=%d error=%v", protocolPath, attempts, err))
	return err
}

func requestUserAgentStartupSync(reason string) error {
	if err := services.RequestUserAgentStartupSync(reason); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started sync_failed reason=%s error=%v", reason, err))
		return err
	}
	return nil
}

func shouldReplaceExistingRuntimeAfterSyncError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	// 鉴权类业务错误：换进程让新 agent 用最新持久化 token 重新注册。
	if strings.Contains(text, "login") ||
		strings.Contains(text, "authorization") ||
		strings.Contains(text, "authentication") {
		return true
	}
	// 网络层 / 进程健康错误：agent 半死（端口在但不响应 / EOF / 超时）或协议 stale。
	// 这正是 .app 重启时旧 agent 被连带杀死过程中的竞态漏 spawn 要修的场景：existing
	// 分支 sync 失败时若仅因非 auth 错误就保留，会把半死的旧 agent 当健康、永不重 spawn。
	if strings.Contains(text, "eof") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "reset by peer") ||
		strings.Contains(text, "no such host") ||
		strings.Contains(text, "deadline exceeded") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "stale protocol") {
		return true
	}
	// 其它（如 PhoneServer 返回的 5xx 业务错误）→ 保留进程，避免远程故障时反复 kill+spawn 抖动。
	return false
}

func SupportsCurrentAgentAPI(timeout time.Duration) bool {
	if err := checkCurrentAgentAPI(timeout); err != nil {
		logAgentCapabilityCheckFailure(err)
		return false
	}
	return true
}

// NeedsAuthenticatedSync detects a state that contradicts an authenticated
// core session: the Agent process is healthy, but a prior logout/session expiry
// cleared its remote registration. The caller must still ask the core to sync;
// the core remains the authority on whether a valid user session exists.
func NeedsAuthenticatedSync(timeout time.Duration) bool {
	var envelope struct {
		Code int                        `json:"code"`
		Data models.AgentStatusResponse `json:"data"`
	}
	if err := getLocalAgentJSON(timeout, capabilityPath, &envelope); err != nil || envelope.Code != 0 {
		return false
	}
	if envelope.Data.Enabled || envelope.Data.Registered {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(envelope.Data.SyncStatus)) {
	case "logout", "auth_expired":
		return true
	default:
		return false
	}
}

func waitForCurrentAgentAPI(maxWait time.Duration, probeTimeout time.Duration) (int, error) {
	deadline := time.Now().Add(maxWait)
	attempts := 0
	var lastErr error
	for {
		attempts++
		if err := checkCurrentAgentAPI(probeTimeout); err == nil {
			return attempts, nil
		} else {
			lastErr = err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining > 250*time.Millisecond {
			remaining = 250 * time.Millisecond
		}
		time.Sleep(remaining)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("readiness deadline exceeded")
	}
	return attempts, fmt.Errorf("user agent process did not become ready: %w", lastErr)
}

func checkCurrentAgentAPI(timeout time.Duration) error {
	protocol, err := fetchLocalAgentProtocol(timeout)
	if err != nil {
		return err
	}
	if strings.TrimSpace(protocol.Version) != models.AgentProtocolVersion {
		return fmt.Errorf("stale protocol got=%q want=%q", protocol.Version, models.AgentProtocolVersion)
	}
	return nil
}

func logAgentCapabilityCheckFailure(err error) {
	if strings.Contains(strings.ToLower(err.Error()), "stale protocol") {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started capability_check %v", err))
		return
	}
	logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started capability_check failed path=%s error=%v", protocolPath, err))
}

func fetchLocalAgentProtocol(timeout time.Duration) (*models.AgentProtocolContract, error) {
	var envelope struct {
		Code int                          `json:"code"`
		Data models.AgentProtocolContract `json:"data"`
	}
	if err := getLocalAgentJSON(timeout, protocolPath, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("protocol endpoint returned code %d", envelope.Code)
	}
	return &envelope.Data, nil
}

func stopStaleUserAgentIfNeeded(timeout time.Duration) {
	pid, err := fetchLocalAgentPID(timeout)
	if err != nil || pid <= 0 {
		// HTTP status 拿不到（agent 半死 / 不响应 /api/agent/status）→ 从 pidfile 兜底取 pid。
		// 否则半死 agent 占着 flock 与端口，新 spawn 永远拿不到锁。
		if filePID := readAgentPIDFromFile(); filePID > 0 {
			pid = filePID
			logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop pidfile_fallback pid=%d", pid))
		}
	}
	if pid <= 0 {
		logger.Info("[AGENT-BOOT] ensure_started stale_stop skipped reason=no_pid")
		return
	}
	if pid == os.Getpid() {
		return
	}
	// 防御 pid 复用：agent 异常退出后 pidfile 可能残留，OS 把该 pid 复用给无关进程时，
	// 若不校验直接 kill 会误杀。仅当 pid 仍存活且命令行是 aliang 自身才动手。
	if !isAgentPIDAlive(pid) {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop pid_not_alive pid=%d", pid))
		return
	}
	if !isAgentProcessByComm(pid) {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop skipped reason=pid_reused_not_aliang pid=%d", pid))
		return
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop find_failed pid=%d error=%v", pid, err))
		return
	}
	logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop killing_old_user_agent pid=%d", pid))
	if err := process.Kill(); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop kill_failed pid=%d error=%v", pid, err))
		return
	}
	// 等 56433 失联（旧 agent 真正退下），为新 spawn 让出端口与 flock。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := fetchLocalAgentPID(300 * time.Millisecond); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func fetchLocalAgentPID(timeout time.Duration) (int, error) {
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Runtime *models.AgentRuntime `json:"runtime"`
		} `json:"data"`
	}
	if err := getLocalAgentJSON(timeout, capabilityPath, &envelope); err != nil {
		return 0, err
	}
	if envelope.Code != 0 {
		return 0, fmt.Errorf("status endpoint returned code %d", envelope.Code)
	}
	if envelope.Data.Runtime == nil {
		return 0, fmt.Errorf("status response missing runtime")
	}
	return envelope.Data.Runtime.PID, nil
}

func getLocalAgentJSON(timeout time.Duration, path string, target interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, services.UserAgentBaseURL()+path, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("GET %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

func newAgentProcessCommand() (*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}

	cmd := newBackgroundCommand(executable, "agent")
	cmd.Env = userAgentEnv(os.Environ())

	if writer := openAgentLogWriter(); writer != nil {
		// Pointing stdout and stderr at the same io.Writer makes os/exec merge
		// both streams onto one pipe that it copies into our rotating writer,
		// which bounds the captured agent.log (see rotatingLogWriter).
		cmd.Stdout = writer
		cmd.Stderr = writer
	}
	return cmd, nil
}

func userAgentEnv(env []string) []string {
	blocked := map[string]bool{
		"ALIANG_DATA_DIR":    true,
		"ALIANG_CACHE_DIR":   true,
		"ALIANG_LOG_DIR":     true,
		"ALIANG_SOCKET_PATH": true,
	}

	next := make([]string, 0, len(env)+1)
	for _, item := range env {
		key := item
		if idx := strings.Index(item, "="); idx >= 0 {
			key = item[:idx]
		}
		if blocked[key] {
			continue
		}
		next = append(next, item)
	}
	next = append(next, services.AgentRuntimeEnv+"=1")
	return next
}

func openAgentLogWriter() *rotatingLogWriter {
	stateDir, err := runtimepath.UserStateDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(stateDir, "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	writer, err := newRotatingLogWriter(dir, "agent.log", agentLogMaxSize, agentLogMaxBackups)
	if err != nil {
		return nil
	}
	return writer
}
