package services

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// TestAnnounceSessionsListsLiveOnly pins the announce contract: after
// registration the agent broadcasts the set of LIVE terminal sessions only —
// tombstones stay out of the frame (the server reconciles against this list and
// must never resurrect a dead session), and nil map entries are skipped.
func TestAnnounceSessionsListsLiveOnly(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	startedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	lastActiveAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	live := newAttachTestSession("t-live-1", newTerminalRingBuffer(4096))
	m.mu.Lock()
	live.startedAt = startedAt
	live.lastActiveAt = lastActiveAt
	m.sessions["t-live-1"] = live
	m.sessions["t-live-nil"] = nil                 // must be skipped, not marshalled as null
	m.history["t-live-1"] = &agentTerminalHistory{ // a tombstone shadowing a live id must not win
		shell: "/bin/tombstone",
		ring:  newTerminalRingBuffer(64),
	}
	m.history["t-dead"] = &agentTerminalHistory{ // exited sessions are never announced
		shell:    "/bin/exited",
		cwd:      "/tmp/dead",
		rows:     1,
		cols:     2,
		exitedAt: time.Now(),
		ring:     newTerminalRingBuffer(64),
	}
	m.mu.Unlock()

	m.announceSessions(write)

	frames := coll.ofTypes(models.AgentEventTerminalSessions)
	if len(frames) != 1 {
		t.Fatalf("announce frames = %d, want exactly 1, payloads=%v", len(frames), coll.snapshot())
	}
	raw, err := json.Marshal(frames[0])
	if err != nil {
		t.Fatalf("marshal announce frame: %v", err)
	}
	var decoded struct {
		Sessions []struct {
			SessionID    string `json:"session_id"`
			Shell        string `json:"shell"`
			Cwd          string `json:"cwd"`
			Rows         int    `json:"rows"`
			Cols         int    `json:"cols"`
			StartedAt    string `json:"started_at"`
			LastActiveAt string `json:"last_active_at"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal announce frame %s: %v", raw, err)
	}
	if len(decoded.Sessions) != 1 {
		t.Fatalf("announced sessions = %d (%s), want exactly the 1 live session", len(decoded.Sessions), raw)
	}
	got := decoded.Sessions[0]
	if got.SessionID != "t-live-1" {
		t.Fatalf("announced session_id = %q, want t-live-1", got.SessionID)
	}
	if got.Shell != live.shell || got.Cwd != live.cwd {
		t.Fatalf("announced shell/cwd = %q/%q, want %q/%q", got.Shell, got.Cwd, live.shell, live.cwd)
	}
	if got.Rows != 24 || got.Cols != 80 {
		t.Fatalf("announced rows/cols = %d/%d, want 24/80", got.Rows, got.Cols)
	}
	if want := startedAt.UTC().Format(time.RFC3339); got.StartedAt != want {
		t.Fatalf("announced started_at = %q, want %q", got.StartedAt, want)
	}
	if want := lastActiveAt.UTC().Format(time.RFC3339); got.LastActiveAt != want {
		t.Fatalf("announced last_active_at = %q, want %q", got.LastActiveAt, want)
	}
	if strings.Contains(string(raw), "t-dead") || strings.Contains(string(raw), "tombstone") {
		t.Fatalf("tombstones must not leak into the announce frame: %s", raw)
	}
}

// TestAnnounceSessionsEmptyStillEmits pins the empty-set behavior: a fresh
// device with no terminals still emits one terminal.sessions frame carrying an
// empty JSON array (`[]`, never null) so the server can converge its stale
// records on every registration.
func TestAnnounceSessionsEmptyStillEmits(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	m.announceSessions(write)

	frames := coll.ofTypes(models.AgentEventTerminalSessions)
	if len(frames) != 1 {
		t.Fatalf("announce frames = %d, want exactly 1 even with no sessions", len(frames))
	}
	raw, err := json.Marshal(frames[0])
	if err != nil {
		t.Fatalf("marshal announce frame: %v", err)
	}
	if !strings.Contains(string(raw), `"sessions":[]`) {
		t.Fatalf("empty announce must carry a JSON empty array (not null), got %s", raw)
	}
}

// TestCapabilityIncludesTerminalReplay pins the capability declaration: a server
// gated on terminal_replay must see it advertised before it may trust the
// sessions broadcast / replay protocol.
func TestCapabilityIncludesTerminalReplay(t *testing.T) {
	caps := agentCapabilities()
	for _, cap := range caps {
		if cap == "terminal_replay" {
			return
		}
	}
	t.Fatalf("capabilities %v missing terminal_replay", caps)
}
