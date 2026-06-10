package agentruntime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

const healthPath = "/api/agent/health"

func EnsureStarted() error {
	logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started begin local_agent_url=%s health_path=%s", services.UserAgentBaseURL(), healthPath))
	if IsHealthy(700 * time.Millisecond) {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started existing_runtime_healthy local_agent_url=%s", services.UserAgentBaseURL()))
		return nil
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
		if IsHealthy(700 * time.Millisecond) {
			logger.Info(fmt.Sprintf("[AGENT-BOOT] ensure_started user_agent_ready local_agent_url=%s", services.UserAgentBaseURL()))
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("user agent process did not become ready")
}

func IsHealthy(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, services.UserAgentBaseURL()+healthPath, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
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
