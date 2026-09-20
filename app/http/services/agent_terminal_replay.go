package services

import (
	"sync"

	"aliang.one/nursorgate/app/http/models"
)

// agentTerminalReplayChunkBytes caps each terminal.replay frame so a large
// scrollback is delivered as a sequence of 64KiB chunks instead of one huge
// WebSocket frame.
const agentTerminalReplayChunkBytes = 64 * 1024

const (
	terminalReplayStatusLive   = "live"   // ring belongs to a still-running session
	terminalReplayStatusExited = "exited" // ring belongs to a tombstoned session
)

// sendReplay streams a ring snapshot to the client as terminal.replay frames:
// {session_id, encoding:"text", data, seq, final, status, truncated:false}.
// seq counts from 0; the last frame — and, for an empty ring, the single frame
// — carries final:true. gate, when non-nil (a live session's outputGate), is
// held across the snapshot and every send so replay frames never interleave
// with live terminal.output frames; tombstones have no live writers and pass
// nil. Callers take the session reference under m.mu and release it before
// calling, keeping the lock order one-way (m.mu -> outputGate).
func (m *agentTerminalManager) sendReplay(sessionID string, ring *terminalRingBuffer, status string, gate *sync.Mutex, writeJSON agentTerminalWriter) {
	if writeJSON == nil || ring == nil {
		return
	}
	if gate != nil {
		gate.Lock()
		defer gate.Unlock()
	}

	snap := ring.snapshot()
	seq := 0
	for off := 0; off < len(snap) || seq == 0; {
		end := off + agentTerminalReplayChunkBytes
		if end > len(snap) {
			end = len(snap)
		} else if safe := off + utf8SafePrefix(snap[off:end]); safe > off {
			// Align interior chunk ends to a rune boundary so json.Marshal
			// cannot corrupt a straddling multi-byte rune into U+FFFD — the
			// same invariant terminalOutputEncoder enforces on the live output
			// path. safe == off is impossible here: the slice is a full
			// agentTerminalReplayChunkBytes (>= 4 bytes), so progress holds.
			// The final chunk is emitted as-is — a genuinely truncated
			// snapshot tail has no continuation to align with.
			end = safe
		}
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventTerminalReplay,
			"session_id": sessionID,
			"encoding":   "text",
			"data":       string(snap[off:end]),
			"seq":        seq,
			"final":      end >= len(snap),
			"status":     status,
			"truncated":  false,
		})
		seq++
		off = end
	}
}
