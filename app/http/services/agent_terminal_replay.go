package services

import (
	"fmt"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
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
// — carries final:true. attachToken, when non-empty, is echoed verbatim as
// "attach_token" on EVERY frame (final included) so the server can route this
// replay to the requesting web viewer only; empty keeps the legacy frames
// byte-identical (old server / REST / phone paths). gate, when non-nil (a live
// session's outputGate), is held across the snapshot and every send so replay
// frames never interleave with live terminal.output frames; tombstones have no
// live writers and pass nil. Callers take the session reference under m.mu and
// release it before calling, keeping the lock order one-way (m.mu -> outputGate).
func (m *agentTerminalManager) sendReplay(sessionID string, ring *terminalRingBuffer, status string, gate *sync.Mutex, attachToken string, writeJSON agentTerminalWriter) {
	if writeJSON == nil || ring == nil {
		return
	}
	var gateWait time.Duration
	if gate != nil {
		gateStart := time.Now()
		gate.Lock()
		gateWait = time.Since(gateStart)
		defer gate.Unlock()
	}

	start := time.Now()
	snap := ring.snapshot()
	totalBytes := 0
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
		frame := map[string]interface{}{
			"type":       models.AgentEventTerminalReplay,
			"session_id": sessionID,
			"encoding":   "text",
			"data":       string(snap[off:end]),
			"seq":        seq,
			"final":      end >= len(snap),
			"status":     status,
			"truncated":  false,
		}
		if attachToken != "" {
			frame["attach_token"] = attachToken
		}
		_ = writeJSON(frame)
		totalBytes += end - off
		seq++
		off = end
	}
	// Content-free summary (frames/bytes/durations only): the observable
	// footprint of one attach backfill, and the number that used to stall the
	// whole read loop before replay moved off it.
	logger.Info(fmt.Sprintf("[AGENT-BOOT] terminal_replay frames=%d bytes=%d duration_ms=%d gate_wait_ms=%d",
		seq, totalBytes, time.Since(start).Milliseconds(), gateWait.Milliseconds()))
}
