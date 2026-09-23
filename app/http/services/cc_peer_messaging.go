package services

import (
	"bufio"
	crand "crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// loadClaudePeerToken reads the peer auth token for a pid from
// ~/.claude/sessions/<pid>.<sha>.key (CC >= 2.1.224). Read on demand ONLY —
// never in the periodic scan. Missing/unreadable is not fatal: macOS/Linux
// accept token-less frames (spike-verified, see plan 附录 A), so callers
// degrade gracefully.
//
// (filepath.Glob returns sorted entries, so files[0] is deterministic;
// exactly one .key per pid in practice)
func loadClaudePeerToken(home string, pid int) string {
	if home = strings.TrimSpace(home); home == "" || pid <= 0 {
		return ""
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".*.key"))
	if err != nil || len(files) == 0 {
		return ""
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		return ""
	}
	var row struct {
		PeerToken string `json:"peerToken"`
	}
	if err := json.Unmarshal(raw, &row); err != nil {
		return ""
	}
	return strings.TrimSpace(row.PeerToken)
}

// claudePeerKeyModTime returns the mtime of the pid's session key file — the
// closest cheap proxy for when that TUI process started (CC writes the .key
// at startup), used as the "TUI opened after the last transcript write?" gate
// input. Zero time when absent/unknown (callers treat zero as "no evidence").
func claudePeerKeyModTime(home string, pid int) time.Time {
	if home = strings.TrimSpace(home); home == "" || pid <= 0 {
		return time.Time{}
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "sessions", strconv.Itoa(pid)+".*.key"))
	if err != nil || len(files) == 0 {
		return time.Time{}
	}
	info, err := os.Stat(files[0])
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ccPeerSessionJSONL resolves the transcript jsonl for a native session id
// under ~/.claude/projects/*/ (session ids are unique across project dirs; if
// a stale copy lingers in two, the newest mtime wins). Empty when absent.
func ccPeerSessionJSONL(home, nativeSessionID string) string {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if home = strings.TrimSpace(home); home == "" || nativeSessionID == "" {
		return ""
	}
	files, err := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", nativeSessionID+".jsonl"))
	if err != nil || len(files) == 0 {
		return ""
	}
	best, bestMod := files[0], time.Time{}
	for _, f := range files {
		if info, err := os.Stat(f); err == nil && (bestMod.IsZero() || info.ModTime().After(bestMod)) {
			best, bestMod = f, info.ModTime()
		}
	}
	return best
}

// 帧形态经 Task 0 spike 实测校准(计划附录 A):auth 帧字段名是 peerToken(mac 实测可省);
// user 帧实测被接受的形态带 msgV/msg_id/priority。
type ccPeerAuthFrame struct {
	Type      string `json:"type"`
	PeerToken string `json:"peerToken"`
}

type ccPeerUserFrame struct {
	MsgV    int    `json:"msgV"`
	MsgID   string `json:"msg_id"`
	Type    string `json:"type"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Priority string `json:"priority"`
}

// ccPeerMsgID mints a uuid4-shaped id for the user frame. Entropy failure
// falls back to a timestamp id — never fail the notice over id randomness.
func ccPeerMsgID() string {
	var b [16]byte
	if _, err := crand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func ccPeerAuthLine(token string) string {
	b, _ := json.Marshal(ccPeerAuthFrame{Type: "auth", PeerToken: token})
	return string(b)
}

func ccPeerUserLine(content string) string {
	var f ccPeerUserFrame
	f.MsgV = 1
	f.MsgID = ccPeerMsgID()
	f.Type = "user"
	f.Message.Role = "user"
	f.Message.Content = content
	f.Priority = "next"
	b, _ := json.Marshal(f)
	return string(b)
}

// Injection timeouts, named so tests can shorten them if ever needed.
const (
	ccPeerDialTimeout  = 3 * time.Second
	ccPeerWriteTimeout = 5 * time.Second
)

// ccPeerInject dials the inbox UDS and writes the auth (when known) + user
// frames, fire-and-forget. Spike-verified (Task 0, plan 附录 A): a normal
// delivery produces NO receipt frame on the injecting socket, so there is
// nothing to read — success means the frames flushed. Non-delivery outcomes
// surface elsewhere: a busy TUI queues the message (delivered after its
// turn); a held/denied inbound gate shows its approval prompt inside the TUI.
func ccPeerInject(socketPath, token, digest string) error {
	conn, err := net.DialTimeout("unix", socketPath, ccPeerDialTimeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(ccPeerWriteTimeout))
	w := bufio.NewWriter(conn)
	if token != "" {
		if _, err := w.WriteString(ccPeerAuthLine(token) + "\n"); err != nil {
			return err
		}
	}
	if _, err := w.WriteString(ccPeerUserLine(digest) + "\n"); err != nil {
		return err
	}
	return w.Flush()
}

// TUI sync coalescing (spec §3): repeated ai.tui.sync for the same native
// session inside the window collapse into ONE injection, newest digest wins.
// Injection fires ccPeerSyncCoalesceWindow after the FIRST sync of a burst,
// so the injected summary covers what landed during the window.
var ccPeerSyncCoalesceWindow = 60 * time.Second

const ccPeerSyncPendingCap = 256

// ccPeerDialInject is swapped in tests to capture frames / control outcomes.
var ccPeerDialInject = ccPeerInject

// ccPeerSyncPendingEntry is one coalescing burst awaiting its window to fire.
// (Named *Entry because the package-level map below owns the plain name.)
type ccPeerSyncPendingEntry struct {
	digest string
	timer  *time.Timer
}

var (
	ccPeerSyncMu      sync.Mutex
	ccPeerSyncPending = map[string]*ccPeerSyncPendingEntry{}
	// ccPeerSyncBaseline: jsonl size at the last successful injection per
	// native session; absent = unknown (agent restart) → gate injects.
	ccPeerSyncBaseline = map[string]int64{}
)

type ccPeerSyncGateInput struct {
	RecordLive    bool
	RecordStatus  string
	SocketPath    string
	BaselineSize  int64
	BaselineKnown bool
	JSONLSize     int64
	JSONLModTime  time.Time
	TUIStartProxy time.Time
}

// ccPeerSyncShouldInject is the pure gate (spec §4). All four spec gates:
// ① live TUI record ② idle (busy → skip, NEVER interrupt) ③ socket capability
// (messagingSocketPath present and unix-style — Windows named pipe is v2) plus
// ④ transcript-moved evidence: jsonl written after the TUI started (key-file
// mtime proxy; handles "TUI opened after the phone turn" — it already loaded
// the history) and grown since our last successful injection (baseline unknown
// after agent restart → inject; worst case one redundant notice).
func ccPeerSyncShouldInject(in ccPeerSyncGateInput) bool {
	if !in.RecordLive {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(in.RecordStatus), "idle") {
		return false
	}
	if strings.TrimSpace(in.SocketPath) == "" || !strings.HasPrefix(in.SocketPath, "/") {
		return false
	}
	if in.JSONLSize <= 0 {
		return false
	}
	if !in.TUIStartProxy.IsZero() && in.JSONLModTime.Before(in.TUIStartProxy) {
		return false
	}
	if in.BaselineKnown && in.JSONLSize <= in.BaselineSize {
		return false
	}
	return true
}

// ccPeerSyncFire runs after the coalesce window: gate, inject, log. No reply
// is written anywhere — the contract is best-effort with agent-log evidence.
func ccPeerSyncFire(home, nativeSessionID string) {
	ccPeerSyncMu.Lock()
	pending := ccPeerSyncPending[nativeSessionID]
	delete(ccPeerSyncPending, nativeSessionID)
	baseline, baselineKnown := ccPeerSyncBaseline[nativeSessionID]
	ccPeerSyncMu.Unlock()
	if pending == nil || strings.TrimSpace(pending.digest) == "" {
		return
	}
	record, live := liveClaudeTUIRecord(home, nativeSessionID)
	socketPath, tuiStart := "", time.Time{}
	if live {
		socketPath = record.MessagingSocketPath
		tuiStart = claudePeerKeyModTime(home, record.PID)
	}
	size, mod := int64(0), time.Time{}
	if jsonl := ccPeerSessionJSONL(home, nativeSessionID); jsonl != "" {
		if info, err := os.Stat(jsonl); err == nil {
			size, mod = info.Size(), info.ModTime()
		}
	}
	if !ccPeerSyncShouldInject(ccPeerSyncGateInput{
		RecordLive: live, RecordStatus: record.Status, SocketPath: socketPath,
		BaselineSize: baseline, BaselineKnown: baselineKnown,
		JSONLSize: size, JSONLModTime: mod, TUIStartProxy: tuiStart,
	}) {
		logger.Info(fmt.Sprintf("ai.tui.sync: gate dropped home=%q native=%s live=%v status=%q socket=%q", home, nativeSessionID, live, strings.TrimSpace(record.Status), socketPath))
		return
	}
	if err := ccPeerDialInject(socketPath, loadClaudePeerToken(home, record.PID), pending.digest); err != nil {
		logger.Info(fmt.Sprintf("ai.tui.sync: inject failed home=%q native=%s error=%v", home, nativeSessionID, err))
		return
	}
	ccPeerSyncMu.Lock()
	ccPeerSyncBaseline[nativeSessionID] = size
	ccPeerSyncMu.Unlock()
	logger.Info(fmt.Sprintf("ai.tui.sync: frames written home=%q native=%s bytes=%d (no receipt by design, see plan 附录 A)", home, nativeSessionID, size))
}

// tuiSync handles ai.tui.sync (server → agent, best-effort, no reply).
func (m *agentAIManager) tuiSync(msg map[string]interface{}, _ agentTerminalWriter) {
	sourceSessionID := strings.TrimSpace(remoteString(msg, "source_session_id"))
	digest := strings.TrimSpace(remoteString(msg, "digest"))
	if sourceSessionID == "" || digest == "" {
		return
	}
	home := externalTUIHome()
	ccPeerSyncMu.Lock()
	defer ccPeerSyncMu.Unlock()
	if pending := ccPeerSyncPending[sourceSessionID]; pending != nil {
		pending.digest = digest // coalesce: newest wins
		return
	}
	if len(ccPeerSyncPending) >= ccPeerSyncPendingCap {
		logger.Info("ai.tui.sync: pending cap reached, dropping")
		return
	}
	entry := &ccPeerSyncPendingEntry{digest: digest}
	entry.timer = time.AfterFunc(ccPeerSyncCoalesceWindow, func() { ccPeerSyncFire(home, sourceSessionID) })
	ccPeerSyncPending[sourceSessionID] = entry
}
