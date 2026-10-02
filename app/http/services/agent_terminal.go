package services

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
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

// agentTerminalHistoryCap bounds the tombstone history of exited sessions;
// beyond the cap the oldest tombstone (by exitedAt) is evicted on insert.
const agentTerminalHistoryCap = 8

// agentTerminalRingDefaultBytes is the scrollback capacity allocated per live
// terminal session (overridable via ALIANG_TERMINAL_RING_BYTES, floor 64KiB).
const agentTerminalRingDefaultBytes = 2 * 1024 * 1024

// Terminal lifecycle knobs. Both durations resolve once at process start; an
// unset/blank/unparseable/non-positive env value falls back to the default
// (see resolveEnvDuration, which is unit-tested).
//
//	ALIANG_TERMINAL_DETACHED_IDLE  (time.ParseDuration form, e.g. "30m").
//	                               How long a DETACHED session (its client
//	                               disconnected, PTY deliberately kept alive)
//	                               may sit without user input before it is
//	                               reaped. Timed from the last INPUT only:
//	                               output does not extend a detached
//	                               session's life. Default 30m.
//
//	ALIANG_TERMINAL_HISTORY_TTL    (time.ParseDuration form, e.g. "24h").
//	                               How long an exited session's tombstone
//	                               (metadata + output ring, up to ~2 MiB per
//	                               ring) stays replayable before the sweeper
//	                               drops it. Default 24h.
var (
	agentTerminalDetachedIdle = newDurationAtom(resolveEnvDuration("ALIANG_TERMINAL_DETACHED_IDLE", 30*time.Minute))
	agentTerminalHistoryTTL   = resolveEnvDuration("ALIANG_TERMINAL_HISTORY_TTL", 24*time.Hour)

	// agentTerminalIdleWatchInterval is how often each session's idle watcher
	// re-evaluates the reap rules. Package var so tests can shrink it. Stored as
	// an atomicDuration: tests rewrite it while live watchers still read it.
	agentTerminalIdleWatchInterval = newDurationAtom(time.Minute)
)

// agentTerminalHistorySweepInterval is how often the manager-level sweeper
// drops tombstones past agentTerminalHistoryTTL.
const agentTerminalHistorySweepInterval = time.Minute

// agentTerminalHistory is a tombstone of an exited terminal session: enough
// metadata to describe the dead session and the output ring so a reconnecting
// client can still be served a final replay without spawning a new shell.
type agentTerminalHistory struct {
	shell     string
	cwd       string
	rows      int
	cols      int
	startedAt time.Time
	exitedAt  time.Time
	exitCode  int
	ring      *terminalRingBuffer
}

// agentTerminalProcessStarter starts the shell process for a terminal session
// and returns the session wiring plus its output readers. It is a manager field
// (defaulting to startAgentTerminalProcess) so tests can fake shell startup.
type agentTerminalProcessStarter func(sessionID string, shell string, cwd string, rows int, cols int) (*agentTerminalSession, []io.Reader, error)

type agentTerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*agentTerminalSession
	history  map[string]*agentTerminalHistory

	startProcess agentTerminalProcessStarter
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
	startedAt    time.Time
	lastActiveAt time.Time
	lastInputAt  time.Time

	// killClaimed dedups kill frames across concurrent output-path kills
	// (guarded by the manager mutex): a terminal.quota.resolved denied verdict
	// can land between the copy loop's meter record and the kill it then
	// performs, and exactly one of the racing paths may frame and kill. It is
	// never reset — once a session is on its way out, later kills stay silent.
	killClaimed bool

	rows int
	cols int

	// ring keeps the raw output scrollback for reconnect replay. outputGate
	// must cover both ring pushes and live terminal.output writes so a replay
	// never interleaves with fresh output frames. detachedAt != zero marks the
	// moment the session lost its attached client (zero = attached).
	ring       *terminalRingBuffer
	outputGate sync.Mutex
	detachedAt time.Time
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
	m := &agentTerminalManager{
		sessions:     make(map[string]*agentTerminalSession),
		history:      make(map[string]*agentTerminalHistory),
		startProcess: startAgentTerminalProcess,
	}
	// Tombstone TTL sweeper: runs for the life of the process (there is no
	// shutdown signal because the manager dies with it), freeing each expired
	// tombstone's output ring (~2 MiB a piece).
	go m.sweepHistoryLoop()
	return m
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

	rows := normalizeTerminalDimension(remoteInt(msg, "rows", 24), 24)
	cols := normalizeTerminalDimension(remoteInt(msg, "cols", 80), 80)

	// attach:true prefers re-attaching to existing state (a live session, then
	// a tombstone) over spawning a fresh shell. When neither exists the request
	// falls through to the regular fresh-create path below, and a create
	// without attach keeps the exact pre-attach behavior (already-exists error).
	if remoteBool(msg, "attach", false) {
		if m.attachLive(sessionID, rows, cols, writeJSON) {
			return
		}
		if m.attachHistory(sessionID, writeJSON) {
			return
		}
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

	session, readers, err := m.startProcess(sessionID, shell, cwd, rows, cols)
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
		"resumed":    false,
	})

	for _, reader := range readers {
		go m.copyTerminalOutput(sessionID, reader, writeJSON)
	}
	go m.waitTerminal(sessionID, session.token, writeJSON)
	go m.watchTerminalIdle(sessionID, session.token, writeJSON)
}

// attachLive re-attaches to a live session: resize first (so a TUI redraws via
// SIGWINCH), then replay the scrollback, then confirm with
// terminal.created{resumed:true}. It reports whether the attach happened.
func (m *agentTerminalManager) attachLive(sessionID string, rows, cols int, writeJSON agentTerminalWriter) bool {
	m.mu.Lock()
	session := m.sessions[sessionID]
	if session != nil {
		// Re-attached: clear the detach stamp so the detached-input reaper no
		// longer applies, and re-arm both idle clocks — the attach itself
		// proves a live user. A session that sat detached and fully silent
		// carries stale lastActiveAt/lastInputAt; without the refresh the very
		// next watchTerminalIdle tick (≤1min) would kill the shell the user
		// just attached to, defeating the reconnect-attach promise.
		now := time.Now()
		session.detachedAt = time.Time{}
		session.lastActiveAt = now
		session.lastInputAt = now
	}
	m.mu.Unlock()
	if session == nil {
		return false
	}
	if session.resizer != nil {
		_ = session.resizer(rows, cols)
	}
	m.sendReplay(sessionID, session.ring, terminalReplayStatusLive, &session.outputGate, writeJSON)
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventTerminalCreated,
		"session_id": sessionID,
		"shell":      session.shell,
		"cwd":        session.cwd,
		"pty":        session.isPTY,
		"rows":       rows,
		"cols":       cols,
		"resumed":    true,
	})
	return true
}

// attachHistory serves a tombstone of an exited session: replay its ring with
// status "exited" and confirm with terminal.created{resumed:false, exited:true}
// built from the tombstone metadata — without spawning a new shell. It reports
// whether a tombstone was served.
func (m *agentTerminalManager) attachHistory(sessionID string, writeJSON agentTerminalWriter) bool {
	m.mu.Lock()
	tomb := m.history[sessionID]
	m.mu.Unlock()
	if tomb == nil {
		return false
	}
	// A tombstone ring has no live writers, so no output gate is needed.
	m.sendReplay(sessionID, tomb.ring, terminalReplayStatusExited, nil, writeJSON)
	_ = writeJSON(map[string]interface{}{
		"type":       models.AgentEventTerminalCreated,
		"session_id": sessionID,
		"shell":      tomb.shell,
		"cwd":        tomb.cwd,
		"rows":       tomb.rows,
		"cols":       tomb.cols,
		"started_at": tomb.startedAt.UTC().Format(time.RFC3339),
		"resumed":    false,
		"exited":     true,
	})
	return true
}

// recordTerminalHistoryLocked stores a tombstone and evicts the oldest entries
// (by exitedAt) beyond agentTerminalHistoryCap. Callers must hold m.mu.
func (m *agentTerminalManager) recordTerminalHistoryLocked(sessionID string, tomb *agentTerminalHistory) {
	if tomb == nil {
		return
	}
	if m.history == nil {
		m.history = make(map[string]*agentTerminalHistory)
	}
	m.history[sessionID] = tomb
	for len(m.history) > agentTerminalHistoryCap {
		oldestID := ""
		var oldestAt time.Time
		for id, h := range m.history {
			if oldestID == "" || h.exitedAt.Before(oldestAt) {
				oldestID, oldestAt = id, h.exitedAt
			}
		}
		if oldestID == "" {
			break
		}
		delete(m.history, oldestID)
	}
}

// tombstoneSessionLocked converts an exiting session into a history tombstone.
// Callers must hold m.mu and have removed (or be removing) the session from
// m.sessions; exitCode is the process exit code, or -1 when unknown (killed).
func (m *agentTerminalManager) tombstoneSessionLocked(sessionID string, session *agentTerminalSession, exitCode int) {
	if session == nil {
		return
	}
	m.recordTerminalHistoryLocked(sessionID, &agentTerminalHistory{
		shell:     session.shell,
		cwd:       session.cwd,
		rows:      session.rows,
		cols:      session.cols,
		startedAt: session.startedAt,
		exitedAt:  time.Now(),
		exitCode:  exitCode,
		ring:      session.ring,
	})
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
	m.touchInput(sessionID)
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

// quotaResolved applies a terminal.quota.resolved verdict (spec §5.2 rule 5,
// §6.2): the human's answer is the only redemption for a pending challenge.
// granted clears pending silently (a challenge never blocked the stream, so
// there is nothing to resume); denied kills with the quota_denied reason. A
// seq that does not match the pending challenge is ignored, and a session that
// is already gone swallows the verdict silently — its challenge is moot.
// Lock discipline: get-then-act — fetch the session under m.mu, touch the
// meter with m.mu released; no path may hold meter.mu while taking m.mu.
func (m *agentTerminalManager) quotaResolved(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	sessionID := remoteString(msg, "session_id")
	if sessionID == "" {
		_ = writeJSON(agentTerminalErrorPayload("", errors.New("terminal.quota.resolved missing session_id")))
		return
	}
	session := m.get(sessionID)
	if session == nil || session.meter == nil {
		return
	}
	granted := remoteString(msg, "verdict") == "granted"
	if action := session.meter.resolveQuota(uint64(remoteInt(msg, "seq", 0)), granted); action.kind == quotaActionKillDenied {
		m.killTerminalSession(sessionID, errTerminalQuotaDenied, writeJSON)
	}
}

func (m *agentTerminalManager) closeAll() {
	m.mu.Lock()
	sessions := make([]*agentTerminalSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*agentTerminalSession)
	// Killed sessions never reach waitTerminal's active branch (the map is
	// cleared under the same lock), so tombstone them here; the exit code of a
	// killed process is unknown at this point (-1).
	for _, session := range sessions {
		m.tombstoneSessionLocked(session.id, session, -1)
	}
	m.mu.Unlock()

	for _, session := range sessions {
		session.kill()
	}
}

// markAllDetached stamps every live session as detached (zero = attached) at
// the moment the WebSocket dropped, WITHOUT touching the PTY processes. A
// transient agent↔server disconnect must not kill shells or AI-adjacent state:
// the sessions survive detached and are replayed from their output ring on the
// next attach. True shutdown paths (remoteConnectionLoop exit,
// forceDisconnectRemote, the remote-terminal setting being disabled) still call
// closeAll, which kills and tombstones everything.
func (m *agentTerminalManager) markAllDetached() {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, session := range m.sessions {
		if session == nil {
			continue
		}
		session.detachedAt = now
	}
}

// announceSessions broadcasts the current set of LIVE terminal sessions to the
// server as one {type:"terminal.sessions", sessions:[...]} frame, right after
// the registration ack (and again on any re-hello — the server reconciles
// idempotently against this list, using it to converge its stale records).
// Only m.sessions is announced: tombstoned (exited) sessions stay private so a
// server-side reconciliation can never resurrect a dead session. An empty set
// still emits the frame with an empty array — the convergence signal for
// "agent has no terminals". Treated as advisory (errors swallowed) like every
// other best-effort write on this connection.
func (m *agentTerminalManager) announceSessions(writeJSON agentTerminalWriter) {
	if writeJSON == nil {
		return
	}
	m.mu.Lock()
	sessions := make([]map[string]interface{}, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil {
			continue
		}
		sessions = append(sessions, map[string]interface{}{
			"session_id":     session.id,
			"shell":          session.shell,
			"cwd":            session.cwd,
			"rows":           session.rows,
			"cols":           session.cols,
			"started_at":     session.startedAt.UTC().Format(time.RFC3339),
			"last_active_at": session.lastActiveAt.UTC().Format(time.RFC3339),
		})
	}
	m.mu.Unlock()
	_ = writeJSON(map[string]interface{}{
		"type":     models.AgentEventTerminalSessions,
		"sessions": sessions,
	})
}

// reapExpiredHistory drops tombstones whose exitedAt is older than
// agentTerminalHistoryTTL, freeing their output rings, and reports how many
// were dropped.
func (m *agentTerminalManager) reapExpiredHistory(now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	dropped := 0
	for id, tomb := range m.history {
		if now.Sub(tomb.exitedAt) >= agentTerminalHistoryTTL {
			delete(m.history, id)
			dropped++
		}
	}
	return dropped
}

// sweepHistoryLoop runs the tombstone TTL sweep once a minute for the life of
// the process.
func (m *agentTerminalManager) sweepHistoryLoop() {
	ticker := time.NewTicker(agentTerminalHistorySweepInterval)
	defer ticker.Stop()
	for range ticker.C {
		m.reapExpiredHistory(time.Now())
	}
}

// terminalIdleReapReason returns a non-empty human-readable reason when the
// session should be reaped by its idle watcher, or "" while it may live.
// Detached sessions are timed from the last USER INPUT only (Board semantics:
// output does not extend a detached session's life); attached sessions keep
// the legacy any-activity idle timer.
func terminalIdleReapReason(session *agentTerminalSession) string {
	if session == nil {
		return ""
	}
	if !session.detachedAt.IsZero() {
		idle := agentTerminalDetachedIdle.Load()
		if time.Since(session.lastInputAt) >= idle {
			return fmt.Sprintf("detached terminal session reaped after %s without input", idle)
		}
		return ""
	}
	if time.Since(session.lastActiveAt) >= agentTerminalIdleTimeout {
		return fmt.Sprintf("terminal session idle timeout after %s", agentTerminalIdleTimeout)
	}
	return ""
}

func (m *agentTerminalManager) activeSessionsSnapshot() []models.AgentTerminalRuntime {
	if m == nil {
		return []models.AgentTerminalRuntime{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	sessions := make([]models.AgentTerminalRuntime, 0, len(m.sessions))
	for _, session := range m.sessions {
		if session == nil {
			continue
		}
		sessions = append(sessions, models.AgentTerminalRuntime{
			ID:           session.id,
			Shell:        session.shell,
			CWD:          session.cwd,
			PTY:          session.isPTY,
			StartedAt:    session.startedAt.UTC().Format(time.RFC3339),
			LastActiveAt: session.lastActiveAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].LastActiveAt > sessions[j].LastActiveAt
	})
	return sessions
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

// touchInput records real user input (terminal.input). It refreshes the
// attached activity timer like touch, and additionally the input clock that
// governs a detached session's life — output and resizes do not count there.
func (m *agentTerminalManager) touchInput(sessionID string) {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil {
		session.lastActiveAt = now
		session.lastInputAt = now
	}
}

// recordTerminalOutput records n bytes of output for the session, refreshing
// its idle timer, and folds the rate window and the quota challenge state
// machine into the meter's single locked evaluation (record, spec §5.2). It
// reports the rate verdict first: a rate trip means the caller kills for the
// flood — unredeemable by any ack (spec §5.3) — and must NOT perform the
// returned action. Continuous, human-paced output such as `watch` never trips
// the rate gate; only runaway floods do. Lock nesting mirrors the old
// acceptTerminalOutput: m.mu outer, meter.mu inner — never the reverse (see
// the B4 wiring notes on record).
func (m *agentTerminalManager) recordTerminalOutput(sessionID string, n int) (bool, quotaAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil {
		return false, quotaAction{}
	}
	session.lastActiveAt = time.Now()
	if session.meter == nil {
		return false, quotaAction{}
	}
	return session.meter.record(n, time.Now())
}

// claimTerminalKill atomically reserves the right to emit a terminal kill frame
// for the session and execute the kill: the first caller wins, every racing
// loser stays silent (a terminal.quota.resolved denied verdict landing between
// the copy loop's meter record and its kill — or the reverse), so the user sees
// exactly one kill frame per session. A session missing from the live map
// (reaped by waitTerminal / close / closeAll) also loses. Mirrors the
// watchTerminalIdle pattern: decide under m.mu, act outside it.
func (m *agentTerminalManager) claimTerminalKill(sessionID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	session := m.sessions[sessionID]
	if session == nil || session.killClaimed {
		return false
	}
	session.killClaimed = true
	return true
}

// killTerminalSession is the shared shape of every output-path kill (flood,
// the three quota reasons, a denied verdict): claim the kill, emit the reason
// frame, then terminate the session. Losing the claim means a concurrent path
// already framed and killed — return without a word. Killing stays outside the
// lock, matching every other kill path in this file.
func (m *agentTerminalManager) killTerminalSession(sessionID string, err error, writeJSON agentTerminalWriter) {
	if !m.claimTerminalKill(sessionID) {
		return
	}
	_ = writeJSON(agentTerminalErrorPayload(sessionID, err))
	if session := m.get(sessionID); session != nil {
		session.kill()
	}
}

func (m *agentTerminalManager) copyTerminalOutput(sessionID string, reader io.Reader, writeJSON agentTerminalWriter) {
	enc := newTerminalOutputEncoder()
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			// One locked evaluation covers the rate window AND the quota
			// challenge state machine. Rate wins (spec §5.3: a flood is not
			// redeemable by an ack), so on a rate trip the quota action is
			// dropped without being performed.
			rateTripped, action := m.recordTerminalOutput(sessionID, n)
			if rateTripped {
				// Rate-only reason: cumulative volume is no longer a blind
				// kill — it is governed by the quota challenge machine.
				m.killTerminalSession(sessionID, fmt.Errorf(
					"terminal output flood limit exceeded (max %d bytes per %s)",
					agentTerminalOutputRateBytes, agentTerminalOutputRateWindow), writeJSON)
				return
			}
			switch action.kind {
			case quotaActionIssue:
				// A challenge never blocks the stream (spec §5.2 rule 4):
				// best-effort control-plane frame — not ringed, not held by
				// the output gate — and the bytes below still flow to
				// ring+live. A failed send does not roll the state machine
				// back: output keeps accumulating and converges at the kill
				// point.
				_ = writeJSON(map[string]interface{}{
					"type":          models.AgentEventTerminalQuotaChallengeRequired,
					"session_id":    sessionID,
					"seq":           action.seq,
					"used_bytes":    action.usedBytes,
					"kill_at_bytes": action.killAtBytes,
					"max_bytes":     action.maxBytes,
				})
			case quotaActionKillUnanswered:
				m.killTerminalSession(sessionID, fmt.Errorf(
					"quota_unanswered: output quota exhausted (unanswered checkpoint %d bytes) — for long-running noisy commands use: <cmd> > log 2>&1",
					action.killAtBytes), writeJSON)
				return
			case quotaActionKillHardCap:
				m.killTerminalSession(sessionID, fmt.Errorf(
					"quota_hard_cap: output quota exhausted (hard cap %d bytes) — for long-running noisy commands use: <cmd> > log 2>&1",
					action.killAtBytes), writeJSON)
				return
			case quotaActionKillDenied:
				// record never reports denied (only resolveQuota does); kept
				// so a future meter change cannot silently fall through.
				m.killTerminalSession(sessionID, errTerminalQuotaDenied, writeJSON)
				return
			}
			if chunk := enc.push(buf[:n]); len(chunk) > 0 {
				frame := map[string]interface{}{
					"type":       models.AgentEventTerminalOutput,
					"session_id": sessionID,
					"encoding":   "text",
					"data":       string(chunk),
				}
				// Dual write: the ring is the authoritative copy of the output
				// stream, so a dead WebSocket no longer loses those bytes — the
				// next attach replays them from the ring. The gate must cover
				// push AND the live frame together (exactly what sendReplay
				// holds across snapshot+send): a chunk then lands either fully
				// in the replay snapshot or fully in the live stream, never in
				// both (seam duplication) and never in neither (seam gap).
				// Lock order: m.mu is taken and released inside m.get BEFORE
				// outputGate — never the reverse, never nested.
				if session := m.get(sessionID); session != nil && session.ring != nil {
					session.outputGate.Lock()
					session.ring.push(chunk)
					_ = writeJSON(frame)
					session.outputGate.Unlock()
				} else {
					// Session already reaped (or ring-less): keep the legacy
					// best-effort live write.
					_ = writeJSON(frame)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

func (m *agentTerminalManager) watchTerminalIdle(sessionID string, token *struct{}, writeJSON agentTerminalWriter) {
	ticker := time.NewTicker(agentTerminalIdleWatchInterval.Load())
	defer ticker.Stop()
	for range ticker.C {
		m.mu.Lock()
		session := m.sessions[sessionID]
		if session == nil || session.token != token {
			m.mu.Unlock()
			return
		}
		reason := terminalIdleReapReason(session)
		m.mu.Unlock()
		if reason == "" {
			continue
		}
		_ = writeJSON(agentTerminalErrorPayload(sessionID, errors.New(reason)))
		// The reap was decided but not yet executed. Re-verify under the lock
		// immediately before killing: a concurrent attachLive re-arms the idle
		// clocks under m.mu too, and must be able to retract a committed kill
		// — never execute a stale reap against a shell the user just attached
		// to. (The error frame may already be out in that case; it is harmless
		// next to the fresh terminal.created it is racing, and the next tick
		// sees a re-armed session.) Killing stays outside the lock, matching
		// every other kill path in this file.
		m.mu.Lock()
		session = m.sessions[sessionID]
		retracted := session == nil || session.token != token || terminalIdleReapReason(session) == ""
		m.mu.Unlock()
		if retracted {
			continue
		}
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
		// A naturally exited session becomes a tombstone so a later attach can
		// still replay its final scrollback.
		m.tombstoneSessionLocked(sessionID, session, exitCode)
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
		return newAgentTerminalSession(sessionID, shell, cwd, handle, true, rows, cols), handle.readers, nil
	} else if !errors.Is(err, errPTYUnsupported) {
		return nil, nil, err
	}

	handle, err := startAgentTerminalPipes(shell, cwd)
	if err != nil {
		return nil, nil, err
	}
	return newAgentTerminalSession(sessionID, shell, cwd, handle, false, rows, cols), handle.readers, nil
}

func newAgentTerminalSession(id string, shell string, cwd string, handle *agentTerminalHandle, isPTY bool, rows int, cols int) *agentTerminalSession {
	now := time.Now()
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
		meter:        newTerminalSessionMeter(),
		token:        new(struct{}),
		startedAt:    now,
		lastActiveAt: now,
		// A session is born attached with a full input-idle budget: a detached
		// session that never saw input is still reaped only after
		// agentTerminalDetachedIdle from creation.
		lastInputAt: now,
		rows:        rows,
		cols:        cols,
		ring:        newTerminalRingBuffer(agentTerminalRingDefaultBytes),
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
// indefinitely. Cumulative volume is handled differently per consumer: terminal
// meters attach a quota challenge state machine (quota field, spec §5.2) and
// add is a pure rate gate for them; quota-less meters (the AI output limiter)
// keep the direct capMax lifetime backstop in add.
type outputMeter struct {
	window  time.Duration
	rateMax int
	capMax  int64

	// quota is the terminal quota challenge state machine, attached at
	// construction (newTerminalOutputMeter). nil means no quota machine — add
	// then keeps the legacy lifetime-cap backstop above.
	quota *outputQuota

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
// killed because the sustained rate over the window was exceeded (or, for
// quota-less meters only, the lifetime cap). Terminal output flows through
// record instead, which folds this evaluation and the quota state machine into
// one locked call.
func (m *outputMeter) add(n int, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addLocked(n, now)
}

// addLocked is add without locking. The caller must hold m.mu.
func (m *outputMeter) addLocked(n int, now time.Time) bool {
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

	// Rate is evaluated first and cannot be redeemed by an ack (spec §5.3): an
	// instantaneous flood kills the session no matter where the quota state
	// machine stands.
	if m.rateMax > 0 {
		recent := 0
		for _, s := range m.samples {
			recent += s.bytes
		}
		if recent > m.rateMax {
			return true
		}
	}
	// The lifetime cap backstop stays only for meters without a quota state
	// machine (the AI output limiter). Terminal meters delegate cumulative
	// volume to the quota machine (record), whose maxBytes hard cap replaces
	// this blind kill (spec §5.1/§5.2).
	if m.quota == nil && m.capMax > 0 && m.total > m.capMax {
		return true
	}
	return false
}

// quotaPolicy carries the tunables of the terminal output quota challenge
// state machine (spec §3; the agentTerminalQuota* package vars in
// agent_execution_guard.go). checkpointBytes and minInterval must be positive;
// maxBytes == 0 is the documented off switch that disables the whole
// mechanism. Production values come from envMiB / resolveEnvDuration, which
// guarantee those invariants; tests inject shrunk values through this
// constructor instead of rewriting the package vars.
type quotaPolicy struct {
	checkpointBytes int64
	maxBytes        int64
	minInterval     time.Duration
}

func newQuotaPolicy(checkpointBytes, maxBytes int64, minInterval time.Duration) quotaPolicy {
	return quotaPolicy{
		checkpointBytes: checkpointBytes,
		maxBytes:        maxBytes,
		minInterval:     minInterval,
	}
}

// outputQuota is the per-session runtime state of the quota challenge machine
// (spec §5.1). It lives inside the meter so both share one lock and one
// total. All transitions are evaluated on the byte-arrival path (record)
// — there is no timer.
type outputQuota struct {
	// policy
	checkpointBytes int64         // challenge issue interval, in bytes
	maxBytes        int64         // hard cap; 0 = whole mechanism disabled
	minInterval     time.Duration // min spacing between issues (dual gate)

	// runtime state
	nextChallengeBytes int64     // next byte total at which a challenge is due; advances by checkpointBytes at each issue
	lastChallengeAt    time.Time // last issue time; zero value = never issued (any now passes the interval gate)
	pending            bool      // a challenge is awaiting a verdict
	seq                uint64    // challenge sequence number, monotonic per session
}

// quotaAction kinds. The empty kind means "no action — output keeps
// streaming": a challenge never blocks the stream, only a kill point stops it.
const (
	quotaActionIssue          = "issue"           // emit terminal.quota.challenge_required
	quotaActionKillUnanswered = "kill_unanswered" // pending challenge, output reached kill_at_bytes
	quotaActionKillHardCap    = "kill_hard_cap"   // cumulative output reached max_bytes regardless of pending
	quotaActionKillDenied     = "kill_denied"     // human rejected the challenge
)

// errTerminalQuotaDenied is the kill reason for a human-rejected challenge
// (spec §5.2 rule 5). The reason prefixes — quota_unanswered / quota_denied /
// quota_hard_cap — are a cross-repo contract: the phone humanizes terminal
// errors by matching the "quota_" prefix, so they must stay verbatim.
var errTerminalQuotaDenied = errors.New("quota_denied: output quota challenge denied by user")

// quotaAction is the verdict of one quota evaluation. The caller (terminal
// output copy loop) performs the kill / emits the challenge; the meter only
// reports.
type quotaAction struct {
	kind        string
	seq         uint64
	usedBytes   int64
	killAtBytes int64 // for issue: where an unanswered kill will fire; for byte-triggered kills: the threshold that fired
	maxBytes    int64 // carried on issue for the challenge frame's max_bytes (spec §6.1); NOT a kill point — distinct from killAtBytes
}

// newTerminalOutputMeter builds the terminal flavor of the meter: a pure rate
// gate plus the quota challenge state machine. capMax does not apply here —
// cumulative volume is governed by the quota machine, whose maxBytes hard cap
// replaces the old blind lifetime kill (spec §5.1/§5.2).
func newTerminalOutputMeter(window time.Duration, rateMax int, policy quotaPolicy) *outputMeter {
	return &outputMeter{
		window:  window,
		rateMax: rateMax,
		quota: &outputQuota{
			checkpointBytes:    policy.checkpointBytes,
			maxBytes:           policy.maxBytes,
			minInterval:        policy.minInterval,
			nextChallengeBytes: policy.checkpointBytes,
		},
	}
}

// newTerminalSessionMeter wires the env-tunable quota knobs (package vars in
// agent_execution_guard.go) into a production terminal meter.
func newTerminalSessionMeter() *outputMeter {
	return newTerminalOutputMeter(agentTerminalOutputRateWindow, agentTerminalOutputRateBytes,
		newQuotaPolicy(agentTerminalQuotaCheckpointBytes, agentTerminalQuotaMaxBytes, agentTerminalQuotaMinInterval))
}

// record is the terminal output path: in one pass under the meter's own lock
// it accumulates the chunk, evaluates the rate window, and evaluates the quota
// challenge state machine (spec §5.2), returning (rateTripped, action). The
// copy loop (B4) kills for the rate trip first and only processes the action
// otherwise — that ordering contract is guaranteed by the single-call
// structure rather than by documentation, and no interleaving can slip between
// the two evaluations. The quota evaluation itself ignores the rate verdict:
// when both fire, the caller kills for the rate (unredeemable, spec §5.3) and
// drops the action; the state machine may have advanced, which is harmless on
// a session that is about to die. On a quota-less meter (none is wired that
// way today) the second result is always the zero action.
//
// B4 wiring notes:
//   - A denied verdict (terminal.quota.resolved → resolveQuota → kill_denied)
//     can land between this call and the kill the caller then performs; the
//     copy loop must not emit a duplicate kill frame for a session it is
//     already killing.
//   - The terminal.quota.resolved handler goes m.get()-then-act (fetch the
//     session, then act on it): never acquire the manager mutex while holding
//     meter.mu.
func (m *outputMeter) record(n int, now time.Time) (bool, quotaAction) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addLocked(n, now), m.consumeQuotaLocked(now)
}

// consumeQuotaLocked evaluates the quota challenge state machine against the
// accumulated total (spec §5.2). The caller must hold m.mu.
//
// Transitions, in evaluation order (spec §5.2):
//
//  1. maxBytes == 0 → the quota mechanism is disabled entirely: never
//     challenge, never kill (pure rate gate).
//  2. total >= maxBytes → kill_hard_cap, regardless of pending state.
//  3. pending && total >= nextChallengeBytes → kill_unanswered; the kill point
//     is the checkpoint beyond the challenge point (kill_at_bytes), NOT
//     maxBytes — the two are different thresholds.
//  4. !pending && total >= nextChallengeBytes && now-lastChallengeAt >=
//     minInterval → issue: seq++, nextChallengeBytes += checkpointBytes,
//     lastChallengeAt = now, pending = true. A challenge does NOT block
//     output — the stream keeps going; only a kill point stops it. The action
//     carries maxBytes so the caller can fill the challenge frame's max_bytes
//     (spec §6.1) from the same locked evaluation.
//  5. otherwise no action.
func (m *outputMeter) consumeQuotaLocked(now time.Time) quotaAction {
	q := m.quota
	if q == nil || q.maxBytes == 0 {
		return quotaAction{}
	}
	if m.total >= q.maxBytes {
		return quotaAction{kind: quotaActionKillHardCap, seq: q.seq, usedBytes: m.total, killAtBytes: q.maxBytes}
	}
	if q.pending && m.total >= q.nextChallengeBytes {
		return quotaAction{kind: quotaActionKillUnanswered, seq: q.seq, usedBytes: m.total, killAtBytes: q.nextChallengeBytes}
	}
	if !q.pending && m.total >= q.nextChallengeBytes && now.Sub(q.lastChallengeAt) >= q.minInterval {
		q.seq++
		q.nextChallengeBytes += q.checkpointBytes
		q.lastChallengeAt = now
		q.pending = true
		return quotaAction{kind: quotaActionIssue, seq: q.seq, usedBytes: m.total, killAtBytes: q.nextChallengeBytes, maxBytes: q.maxBytes}
	}
	return quotaAction{}
}

// resolveQuota applies a terminal.quota.resolved verdict (spec §5.2 rule 5).
// A seq that does not match the pending challenge (an expired challenge) is
// ignored — state unchanged — as is any resolution while nothing is pending.
// granted only clears pending: nextChallengeBytes advanced at issue time and
// grant does not move it. denied reports kill_denied; the caller performs the
// kill.
func (m *outputMeter) resolveQuota(seq uint64, granted bool) quotaAction {
	m.mu.Lock()
	defer m.mu.Unlock()

	q := m.quota
	if q == nil || !q.pending || seq != q.seq {
		return quotaAction{}
	}
	if !granted {
		return quotaAction{kind: quotaActionKillDenied, seq: seq, usedBytes: m.total}
	}
	q.pending = false
	return quotaAction{}
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
	if home := agentHome(); home != "" {
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
