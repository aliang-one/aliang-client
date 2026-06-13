package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"github.com/creack/pty"
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
	input io.WriteCloser
	pty   *os.File
	isPTY bool

	outputBytes  int
	lastActiveAt time.Time
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

	shell, err := resolveAgentShell(remoteString(msg, "shell"))
	if err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}
	cwd, err := resolveAgentTerminalCWD(remoteString(msg, "cwd"))
	if err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		m.mu.Unlock()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session already exists: %s", sessionID)))
		return
	}
	if len(m.sessions) >= agentMaxTerminalSessions {
		m.mu.Unlock()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session limit reached: %d", agentMaxTerminalSessions)))
		return
	}
	m.mu.Unlock()

	rows := normalizeTerminalDimension(remoteInt(msg, "rows", 24), 24)
	cols := normalizeTerminalDimension(remoteInt(msg, "cols", 80), 80)
	session, readers, err := startAgentTerminalProcess(sessionID, shell, cwd, rows, cols)
	if err != nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
		return
	}

	m.mu.Lock()
	if existing := m.sessions[sessionID]; existing != nil {
		m.mu.Unlock()
		session.kill()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session already exists: %s", sessionID)))
		return
	}
	if len(m.sessions) >= agentMaxTerminalSessions {
		m.mu.Unlock()
		session.kill()
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session limit reached: %d", agentMaxTerminalSessions)))
		return
	}
	session.lastActiveAt = time.Now()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventTerminalCreated,
		"session_id": sessionID,
		"shell":      shell,
		"cwd":        cwd,
		"pty":        session.isPTY,
		"rows":       rows,
		"cols":       cols,
	})

	for _, reader := range readers {
		go m.copyTerminalOutput(sessionID, reader, writeJSON)
	}
	go m.waitTerminal(sessionID, session.cmd, writeJSON)
	go m.watchTerminalIdle(sessionID, session.cmd, writeJSON)
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
	if len(data) > agentTerminalInputLimitBytes {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal.input exceeds %d bytes", agentTerminalInputLimitBytes)))
		return
	}

	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session not found: %s", sessionID)))
		return
	}
	m.touch(sessionID)
	if _, err := io.WriteString(session.input, data); err != nil {
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
	session := m.get(sessionID)
	if session == nil {
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session not found: %s", sessionID)))
		return
	}
	m.touch(sessionID)

	cols := normalizeTerminalDimension(remoteInt(msg, "cols", 80), 80)
	rows := normalizeTerminalDimension(remoteInt(msg, "rows", 24), 24)
	if session.isPTY && session.pty != nil {
		if err := pty.Setsize(session.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
			_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
			return
		}
	}

	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventTerminalResized,
		"session_id": sessionID,
		"rows":       rows,
		"cols":       cols,
		"pty":        session.isPTY,
	})
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
			"type":       models.AgentEventTerminalExit,
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

func (m *agentTerminalManager) touch(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil {
		session.lastActiveAt = time.Now()
	}
}

func (m *agentTerminalManager) reserveTerminalOutput(sessionID string, n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return false
	}
	if session.outputBytes+n > agentTerminalOutputLimitBytes {
		return false
	}
	session.outputBytes += n
	session.lastActiveAt = time.Now()
	return true
}

func (m *agentTerminalManager) copyTerminalOutput(sessionID string, reader io.Reader, writeJSON agentTerminalWriter) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if !m.reserveTerminalOutput(sessionID, n) {
				_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal output exceeded %d bytes", agentTerminalOutputLimitBytes)))
				if session := m.get(sessionID); session != nil {
					session.kill()
				}
				return
			}
			_ = writeJSON(map[string]interface{}{
				"type":       models.AgentEventTerminalOutput,
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

func (m *agentTerminalManager) watchTerminalIdle(sessionID string, cmd *exec.Cmd, writeJSON agentTerminalWriter) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		session := m.sessions[sessionID]
		if session == nil || session.cmd != cmd {
			m.mu.Unlock()
			return
		}
		expired := time.Since(session.lastActiveAt) >= agentTerminalIdleTimeout
		m.mu.Unlock()
		if !expired {
			continue
		}
		_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf("terminal session idle timeout after %s", agentTerminalIdleTimeout)))
		session.kill()
		return
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
	if session.pty != nil {
		_ = session.pty.Close()
	}

	if err == nil {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventTerminalExit,
			"session_id": sessionID,
			"exit_code":  0,
		})
		return
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventTerminalExit,
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
	if s.input != nil {
		_ = s.input.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func startAgentTerminalProcess(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
	cmd := newAgentShellCommand(shell, cwd)
	ptyFile, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err == nil {
		return &agentTerminalSession{
			id:           sessionID,
			shell:        shell,
			cwd:          cwd,
			cmd:          cmd,
			input:        ptyFile,
			pty:          ptyFile,
			isPTY:        true,
			lastActiveAt: time.Now(),
		}, []io.Reader{ptyFile}, nil
	}
	if !errors.Is(err, pty.ErrUnsupported) {
		return nil, nil, err
	}

	cmd = newAgentShellCommand(shell, cwd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, nil, err
	}
	return &agentTerminalSession{
		id:           sessionID,
		shell:        shell,
		cwd:          cwd,
		cmd:          cmd,
		input:        stdin,
		isPTY:        false,
		lastActiveAt: time.Now(),
	}, []io.Reader{stdout, stderr}, nil
}

func newAgentShellCommand(shell string, cwd string) *exec.Cmd {
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = agentTerminalEnv(shell)
	return cmd
}

func normalizeTerminalDimension(value int, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	if value < 2 {
		return 2
	}
	if value > 500 {
		return 500
	}
	return value
}

func agentTerminalErrorPayload(sessionID string, err error) map[string]interface{} {
	message := "terminal error"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       models.AgentEventTerminalError,
		"session_id": sessionID,
		"error":      message,
	}
}

func resolveAgentTerminalCWD(raw string) (string, error) {
	return resolveAgentAuthorizedCWD(raw, "working directory")
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

func agentNativePTYSupported() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "dragonfly", "netbsd", "openbsd", "solaris", "zos":
		return true
	default:
		return false
	}
}

func agentTerminalEnv(shell string) []string {
	env := os.Environ()
	env = append(env, "TERM=xterm-256color")
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
