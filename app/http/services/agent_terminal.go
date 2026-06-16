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
	"unicode/utf8"

	"aliang.one/nursorgate/app/http/models"
)

// errPTYUnsupported is returned by startAgentPTY when the current platform cannot
// allocate a real pseudo-terminal. The caller falls back to plain stdin/stdout
// pipes so terminals still work, albeit without PTY semantics (no TUI, no
// resize, no signal handling via the controlling terminal).
var errPTYUnsupported = errors.New("pty not supported on this platform; falling back to pipes")

type agentTerminalWriter func(interface{}) error

type agentTerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*agentTerminalSession
}

type agentTerminalSession struct {
	id    string
	shell string
	cwd   string

	input   io.WriteCloser
	isPTY   bool
	resizer func(rows, cols int) error
	waiter  func() (int, error) // returns process exit code (and a non-exit error, if any)
	killer  func() error
	closer  func() error

	meter        *outputMeter
	token        *struct{}
	lastActiveAt time.Time
}

// agentTerminalHandle is the platform-supplied wiring for a started shell. The
// Unix backend fills it from creack/pty, the Windows backend from ConPTY, and
// the shared pipe fallback fills it from os/exec pipes.
type agentTerminalHandle struct {
	input   io.WriteCloser
	readers []io.Reader
	resizer func(rows, cols int) error // nil when resize is unsupported (pipe fallback)
	wait    func() (int, error)
	kill    func() error
	close   func() error
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
	go m.waitTerminal(sessionID, session.token, writeJSON)
	go m.watchTerminalIdle(sessionID, session.token, writeJSON)
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
	if session.resizer != nil {
		if err := session.resizer(rows, cols); err != nil {
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

// acceptTerminalOutput records n bytes of output for the session, refreshing its
// idle timer, and reports whether the stream has tripped the flood limiter (in
// which case the caller should terminate the session). Continuous, human-paced
// output such as `watch` never trips it; only runaway floods do.
func (m *agentTerminalManager) acceptTerminalOutput(sessionID string, n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return false
	}
	session.lastActiveAt = time.Now()
	if session.meter == nil {
		return false
	}
	return session.meter.add(n, time.Now())
}

func (m *agentTerminalManager) copyTerminalOutput(sessionID string, reader io.Reader, writeJSON agentTerminalWriter) {
	enc := newTerminalOutputEncoder()
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			if m.acceptTerminalOutput(sessionID, n) {
				_ = writeJSON(agentTerminalErrorPayload(sessionID, fmt.Errorf(
					"terminal output flood limit exceeded (max %d bytes per %s, lifetime cap %d bytes)",
					agentTerminalOutputRateBytes, agentTerminalOutputRateWindow, agentTerminalOutputCapBytes)))
				if session := m.get(sessionID); session != nil {
					session.kill()
				}
				return
			}
			if chunk := enc.push(buf[:n]); len(chunk) > 0 {
				_ = writeJSON(map[string]interface{}{
					"type":       models.AgentEventTerminalOutput,
					"session_id": sessionID,
					"encoding":   "text",
					"data":       string(chunk),
				})
			}
		}
		if err != nil {
			return
		}
	}
}

func (m *agentTerminalManager) watchTerminalIdle(sessionID string, token *struct{}, writeJSON agentTerminalWriter) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		session := m.sessions[sessionID]
		if session == nil || session.token != token {
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

func (m *agentTerminalManager) waitTerminal(sessionID string, token *struct{}, writeJSON agentTerminalWriter) {
	session := m.get(sessionID)
	if session == nil || session.waiter == nil {
		return
	}
	exitCode, err := session.waiter()

	m.mu.Lock()
	current := m.sessions[sessionID]
	active := current != nil && current.token == token
	if active {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()
	if !active {
		return
	}
	if session.closer != nil {
		_ = session.closer()
	}

	if err == nil {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventTerminalExit,
			"session_id": sessionID,
			"exit_code":  exitCode,
		})
		return
	}

	_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
}

func (s *agentTerminalSession) kill() {
	if s == nil {
		return
	}
	if s.killer != nil {
		_ = s.killer()
	}
	if s.input != nil {
		_ = s.input.Close()
	}
}

// startAgentTerminalProcess starts a shell for a terminal session. It prefers a
// real PTY (platform-specific startAgentPTY) and falls back to plain pipes when
// the platform has no PTY support, so terminals work everywhere.
func startAgentTerminalProcess(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error) {
	if handle, err := startAgentPTY(shell, cwd, rows, cols); err == nil {
		return newAgentTerminalSession(sessionID, shell, cwd, handle, true), handle.readers, nil
	} else if !errors.Is(err, errPTYUnsupported) {
		return nil, nil, err
	}

	handle, err := startAgentTerminalPipes(shell, cwd)
	if err != nil {
		return nil, nil, err
	}
	return newAgentTerminalSession(sessionID, shell, cwd, handle, false), handle.readers, nil
}

func newAgentTerminalSession(id string, shell string, cwd string, handle *agentTerminalHandle, isPTY bool) *agentTerminalSession {
	return &agentTerminalSession{
		id:           id,
		shell:        shell,
		cwd:          cwd,
		input:        handle.input,
		isPTY:        isPTY,
		resizer:      handle.resizer,
		waiter:       handle.wait,
		killer:       handle.kill,
		closer:       handle.close,
		meter:        newOutputMeter(agentTerminalOutputRateWindow, agentTerminalOutputRateBytes, int64(agentTerminalOutputCapBytes)),
		token:        new(struct{}),
		lastActiveAt: time.Now(),
	}
}

// startAgentTerminalPipes is the shared, non-PTY fallback: it runs the shell
// with piped stdin/stdout/stderr. Resize is unsupported (resizer stays nil).
func startAgentTerminalPipes(shell string, cwd string) (*agentTerminalHandle, error) {
	cmd := newAgentShellCommand(shell, cwd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	return &agentTerminalHandle{
		input:   stdin,
		readers: []io.Reader{stdout, stderr},
		wait: func() (int, error) {
			if err := cmd.Wait(); err == nil {
				return 0, nil
			} else {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					return exitErr.ExitCode(), nil
				}
				return -1, err
			}
		},
		kill: func() error {
			if cmd.Process != nil {
				return cmd.Process.Kill()
			}
			return nil
		},
	}, nil
}

func newAgentShellCommand(shell string, cwd string) *exec.Cmd {
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = agentTerminalEnv(shell)
	return cmd
}

// outputMeter bounds streamed output volume with a sliding time window. It is
// shared by terminal sessions and AI runs so both apply the same flood policy:
// stop runaway bursts quickly (e.g. `yes`, `cat /dev/urandom`, or an AI dumping
// megabytes per second) while letting continuous-but-slow streams run
// indefinitely up to a high lifetime cap.
type outputMeter struct {
	window  time.Duration
	rateMax int
	capMax  int64

	mu      sync.Mutex
	samples []outputSample
	total   int64
}

type outputSample struct {
	at    time.Time
	bytes int
}

func newOutputMeter(window time.Duration, rateMax int, capMax int64) *outputMeter {
	return &outputMeter{
		window:  window,
		rateMax: rateMax,
		capMax:  capMax,
	}
}

// add records n bytes emitted at now and reports whether the session should be
// killed because the sustained rate over the window or the lifetime cap was
// exceeded.
func (m *outputMeter) add(n int, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := now.Add(-m.window)
	drop := 0
	for drop < len(m.samples) && m.samples[drop].at.Before(cutoff) {
		drop++
	}
	if drop > 0 {
		m.samples = m.samples[drop:]
	}
	m.samples = append(m.samples, outputSample{at: now, bytes: n})
	m.total += int64(n)

	if m.capMax > 0 && m.total > m.capMax {
		return true
	}
	if m.rateMax > 0 {
		recent := 0
		for _, s := range m.samples {
			recent += s.bytes
		}
		if recent > m.rateMax {
			return true
		}
	}
	return false
}

// terminalOutputEncoder buffers an incomplete trailing UTF-8 sequence so that
// multi-byte runes split across PTY reads are not split across WebSocket frames
// (json.Marshal would otherwise corrupt them into U+FFFD). Genuinely invalid
// bytes are emitted as-is.
type terminalOutputEncoder struct {
	carry []byte
}

func newTerminalOutputEncoder() *terminalOutputEncoder {
	return &terminalOutputEncoder{}
}

// push consumes incoming bytes and returns a chunk that ends on a complete UTF-8
// rune boundary. Any incomplete trailing sequence is held until the next push.
func (e *terminalOutputEncoder) push(in []byte) []byte {
	var merged []byte
	if len(e.carry) > 0 {
		merged = make([]byte, 0, len(e.carry)+len(in))
		merged = append(merged, e.carry...)
		merged = append(merged, in...)
		e.carry = nil
	} else {
		merged = in
	}

	safe := utf8SafePrefix(merged)
	if safe < len(merged) {
		tail := merged[safe:]
		e.carry = make([]byte, len(tail))
		copy(e.carry, tail)
	}
	if safe == 0 {
		return nil
	}
	return merged[:safe]
}

// utf8SafePrefix returns the length of the longest prefix of b that does not end
// inside an incomplete multi-byte UTF-8 sequence. Invalid bytes are kept (they
// will become U+FFFD on marshal) and only a genuinely truncated tail is excluded.
func utf8SafePrefix(b []byte) int {
	i := 0
	for i < len(b) {
		if !utf8.FullRune(b[i:]) {
			return i
		}
		if r, size := utf8.DecodeRune(b[i:]); r == utf8.RuneError {
			i++ // invalid start byte: keep it, advance one
		} else {
			i += size
		}
	}
	return i
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
