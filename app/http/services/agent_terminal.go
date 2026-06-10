package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/cache"
)

type agentTerminalWriter func(interface{}) error

type agentTerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*agentTerminalSession
}

type agentTerminalSession struct {
	id    string
	shell string
	cwd   string
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func newAgentTerminalManager() *agentTerminalManager {
	return &agentTerminalManager{
		sessions: make(map[string]*agentTerminalSession),
	}
}

func (m *agentTerminalManager) create(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentTerminalErrorPayload("", errors.New("terminal.create missing session_id")))
		return
	}

	shell := remoteString(msg, "shell")
	if shell == "" {
		shell = defaultAgentShell()
	}
	cwd, err := resolveAgentTerminalCWD(remoteString(msg, "cwd"))
	if err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}

	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = agentTerminalEnv(shell)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}

	session := &agentTerminalSession{
		id:    sessionID,
		shell: shell,
		cwd:   cwd,
		cmd:   cmd,
		stdin: stdin,
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		existing.kill()
	}
	m.sessions[sessionID] = session
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":       "terminal.created",
		"session_id": sessionID,
		"shell":      shell,
		"cwd":        cwd,
		"pty":        false,
	})

	go m.copyTerminalOutput(sessionID, stdout, writeJSON)
	go m.copyTerminalOutput(sessionID, stderr, writeJSON)
	go m.waitTerminal(sessionID, cmd, writeJSON)
}

func (m *agentTerminalManager) write(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	data := remoteString(msg, "data")
	if sessionID == "" {
		_ = writeJSON(agentTerminalErrorPayload("", errors.New("terminal.input missing session_id")))
		return
	}

	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session not found: %s", sessionID)))
		return
	}
	if _, err := io.WriteString(session.stdin, data); err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
	}
}

func (m *agentTerminalManager) resize(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentTerminalErrorPayload("", errors.New("terminal.resize missing session_id")))
		return
	}
	if m.get(sessionID) == nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session not found: %s", sessionID)))
		return
	}

	// The initial user-agent implementation uses shell pipes for broad OS
	// compatibility. Resize becomes active when the shell backend is upgraded
	// to a native PTY on each supported platform.
	_ = remoteInt(msg, "cols", 80)
	_ = remoteInt(msg, "rows", 24)
}

func (m *agentTerminalManager) close(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentTerminalErrorPayload("", errors.New("terminal.close missing session_id")))
		return
	}

	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(map[string]interface{}{
			"type":       "terminal.exit",
			"session_id": sessionID,
			"exit_code":  0,
		})
		return
	}
	session.kill()
}

func (m *agentTerminalManager) closeAll() {
	m.mu.Lock()
	sessions := make([]*agentTerminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*agentTerminalSession)
	m.mu.Unlock()

	for _, session := range sessions {
		session.kill()
	}
}

func (m *agentTerminalManager) get(sessionID string) *agentTerminalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

func (m *agentTerminalManager) copyTerminalOutput(sessionID string, reader io.Reader, writeJSON agentTerminalWriter) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			_ = writeJSON(map[string]interface{}{
				"type":       "terminal.output",
				"session_id": sessionID,
				"encoding":   "text",
				"data":       string(buf[:n]),
			})
		}
		if err != nil {
			return
		}
	}
}

func (m *agentTerminalManager) waitTerminal(sessionID string, cmd *exec.Cmd, writeJSON agentTerminalWriter) {
	err := cmd.Wait()

	m.mu.Lock()
	session := m.sessions[sessionID]
	active := session != nil && session.cmd == cmd
	if active {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !active {
		return
	}

	if err == nil {
		_ = writeJSON(map[string]interface{}{
			"type":       "terminal.exit",
			"session_id": sessionID,
			"exit_code":  0,
		})
		return
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = writeJSON(map[string]interface{}{
			"type":       "terminal.exit",
			"session_id": sessionID,
			"exit_code":  exitErr.ExitCode(),
		})
		return
	}

	_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
}

func (s *agentTerminalSession) kill() {
	if s == nil {
		return
	}
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func agentTerminalErrorPayload(sessionID string, err error) map[string]interface{} {
	message := "terminal error"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       "terminal.error",
		"session_id": sessionID,
		"error":      message,
	}
}

func resolveAgentTerminalCWD(raw string) (string, error) {
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			return home, nil
		}
		return os.Getwd()
	}
	if expanded, err := cache.ExpandHomePath(cwd); err == nil {
		cwd = expanded
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	stat, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("working directory is not a directory: %s", abs)
	}
	return abs, nil
}

func defaultAgentShell() string {
	if runtime.GOOS == "windows" {
		if shell := strings.TrimSpace(os.Getenv("ComSpec")); shell != "" {
			return shell
		}
		if path, err := exec.LookPath("powershell.exe"); err == nil {
			return path
		}
		return "cmd.exe"
	}

	if shell := strings.TrimSpace(os.Getenv("SHELL")); shell != "" {
		if strings.HasPrefix(shell, string(os.PathSeparator)) {
			if _, err := os.Stat(shell); err == nil {
				return shell
			}
		} else if path, err := exec.LookPath(shell); err == nil {
			return path
		}
	}
	for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "sh"
}

func agentTerminalEnv(shell string) []string {
	env := os.Environ()
	env = append(env, "TERM=dumb")
	if shell != "" {
		env = append(env, "SHELL="+shell)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}

func remoteString(msg map[string]interface{}, key string) string {
	value, ok := msg[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return fmt.Sprint(v)
	}
}

func remoteInt(msg map[string]interface{}, key string, fallback int) int {
	value, ok := msg[key]
	if !ok || value == nil {
		return fallback
	}
	switch v := value.(type) {
	case int:
		return v
	case int32:
		return int(v)
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed
		}
	}
	return fallback
}
