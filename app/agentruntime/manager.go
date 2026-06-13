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

func EnsureStarted() error {
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

	cmd, logFile, err := newAgentProcessCommand()
	if err != nil {
		return err
	}
	if logFile != nil {
		defer logFile.Close()
	}

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

	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		if SupportsCurrentAgentAPI(700 * time.Millisecond) {
			logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started user_agent_ready local_agent_url=%s", services.UserAgentBaseURL()))
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("user agent process did not become ready")
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
	return strings.Contains(text, "login") ||
		strings.Contains(text, "authorization") ||
		strings.Contains(text, "authentication")
}

func SupportsCurrentAgentAPI(timeout time.Duration) bool {
	protocol, err := fetchLocalAgentProtocol(timeout)
	if err != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started capability_check failed path=%s error=%v", protocolPath, err))
		return false
	}
	if strings.TrimSpace(protocol.Version) != models.AgentProtocolVersion {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] ensure_started capability_check stale_protocol got=%q want=%q", protocol.Version, models.AgentProtocolVersion))
		return false
	}
	return true
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
	if err != nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started stale_stop skipped reason=status_unavailable error=%v", err))
		return
	}
	if pid <= 0 || pid == os.Getpid() {
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

func newAgentProcessCommand() (*exec.Cmd, *os.File, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil, err
	}

	cmd := newBackgroundCommand(executable, "agent")
	cmd.Env = userAgentEnv(os.Environ())

	logFile := openAgentLogFile()
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	return cmd, logFile, nil
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

func openAgentLogFile() *os.File {
	stateDir, err := runtimepath.UserStateDir()
	if err != nil {
		return nil
	}
	dir := filepath.Join(stateDir, "agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	file, err := os.OpenFile(filepath.Join(dir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return file
}
