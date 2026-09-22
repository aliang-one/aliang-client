package services

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

// External interrupt for imported Claude Code sessions (C2/C3 of the
// TUI-external-stop contract).
//
// An imported session is a conversation Claude Code spawned OUTSIDE this agent
// — the user opened `claude` in a terminal on this machine and the phone later
// imported it. The agent holds no run context for it (nothing in
// m.sessions, no cancel func), so an ai.stop cannot cancel a Go context.
// What it CAN do is find the live TUI process via the per-pid record Claude
// Code keeps in ~/.claude/sessions/<pid>.json (the same records
// loadClaudeRenameRecords reads for the busy/idle overlay) and deliver SIGINT —
// the exact signal the user's Esc keypress produces: the current turn aborts
// and the TUI stays open at its prompt. SIGKILL is never an option here: it
// would murder the interactive session the user is sitting in.

// externalInterruptFunc delivers the interrupt signal to a pid. Platform files
// install the default (unix: SIGINT via kill(2); windows/other: unsupported);
// tests swap it to capture the pid and control the outcome.
var externalInterruptFunc = defaultExternalInterrupt

// externalInterruptTargetMatches is a best-effort identity check against pid
// reuse: Claude Code prunes dead pid records "not always promptly", so a
// stale record can name a pid the OS recycled for an unrelated process.
// Platform files implement the default (unix: `ps -p <pid> -o comm=` must
// mention "claude"; windows/other cannot verify cheaply and return true).
// Verification failure is deliberately permissive (returns true) so a flaky
// probe never degrades the feature; tests swap it to simulate a mismatch.
var externalInterruptTargetMatches = defaultExternalInterruptTargetMatches

// externalInterruptDebounce windows repeated SIGINTs for the same session:
// Claude Code's double-Esc (two SIGINTs in quick succession) QUITS the whole
// TUI, and this feature's contract is "interrupt the turn, keep the TUI
// open". Repeated ai.stop deliveries (server retry after a lost reply, or a
// second tap before the button greys out) must stay idempotent.
const externalInterruptDebounce = 3 * time.Second

// externalInterruptRecent caps the debounce map; entries are pruned
// oldest-first once exceeded (native session ids are bounded in practice).
const externalInterruptRecentCap = 256

var (
	externalInterruptMu     sync.Mutex
	externalInterruptRecent = map[string]time.Time{}
)

// externalTUIBusyErrCode is the machine-readable code carried on the ai.error
// payload when the spawn precheck refuses a run (see
// agentExternalTUIBusyError).
const externalTUIBusyErrCode = "tui_busy"

// agentExternalTUIBusyError marks a Claude spawn refused because the native
// session it would --resume is currently executing a turn in its interactive
// TUI. Two writers on one native conversation would interleave transcripts and
// split the user's attention; the turn must finish (or be stopped) first.
type agentExternalTUIBusyError struct{}

func (agentExternalTUIBusyError) Error() string {
	return "TUI session is running an active turn in its terminal; retry after it finishes"
}

// ErrorCode rides on the ai.error payload so the server can surface a precise
// reason (and the phone a friendly message) instead of a raw string match.
func (agentExternalTUIBusyError) ErrorCode() string { return externalTUIBusyErrCode }

// newAgentExternalTUIBusyError returns the precheck refusal as an error value.
func newAgentExternalTUIBusyError() error { return agentExternalTUIBusyError{} }

// interruptExternalClaudeTurn delivers one SIGINT (one Esc press) to the live
// TUI process that owns nativeSessionID. Reports the signaled pid (nonzero
// only when ok=true) and whether the signal was delivered; every miss (no
// record, dead pid, signal failure — including EPERM across a user boundary
// and the windows unsupported case) degrades to ok=false so the caller falls
// back to the legacy "stopped" reply. Best-effort by design: the ai.stop
// handler must never fail because the TUI could not be interrupted.
// liveClaudeTUIRecord is the SINGLE gate both external-stop and the spawn
// precheck go through: it returns the pid record for nativeSessionID only
// when the record names a LIVE process that verifiably (best-effort) is a
// claude process. The identity check matters on BOTH consumers — signaling a
// recycled pid would interrupt an unrelated process, and treating a recycled
// pid as "TUI busy" would permanently block phone turns. Claude Code prunes
// dead pid records "not always promptly", so stale records naming recycled
// pids are expected in the wild.
func liveClaudeTUIRecord(home, nativeSessionID string) (agentRenamePidRecord, bool) {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	home = strings.TrimSpace(home)
	if nativeSessionID == "" || home == "" {
		return agentRenamePidRecord{}, false
	}
	record, found := loadClaudeRenameRecords(home)[nativeSessionID]
	if !found || record.PID <= 0 {
		return agentRenamePidRecord{}, false
	}
	if !isPidAlive(record.PID) {
		return agentRenamePidRecord{}, false
	}
	if !externalInterruptTargetMatches(record.PID) {
		logger.Info(fmt.Sprintf("ai.external: pid identity mismatch, treating as not-a-TUI home=%q native=%s pid=%d", home, nativeSessionID, record.PID))
		return agentRenamePidRecord{}, false
	}
	return record, true
}

func interruptExternalClaudeTurn(home, nativeSessionID string) (pid int, ok bool) {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	home = strings.TrimSpace(home)
	record, live := liveClaudeTUIRecord(home, nativeSessionID)
	if !live {
		logger.Info(fmt.Sprintf("ai.stop.external: no live claude TUI process home=%q native=%s", home, nativeSessionID))
		return 0, false
	}
	// Debounce: within the window a repeat delivery is treated as success
	// without re-signaling (double-Esc would quit the whole TUI).
	externalInterruptMu.Lock()
	if last, seen := externalInterruptRecent[nativeSessionID]; seen && time.Since(last) < externalInterruptDebounce {
		externalInterruptMu.Unlock()
		logger.Info(fmt.Sprintf("ai.stop.external: debounced repeat within %s home=%q native=%s pid=%d", externalInterruptDebounce, home, nativeSessionID, record.PID))
		return record.PID, true
	}
	externalInterruptMu.Unlock()
	if externalInterruptFunc == nil {
		return 0, false
	}
	if err := externalInterruptFunc(record.PID); err != nil {
		logger.Info(fmt.Sprintf("ai.stop.external: signal failed home=%q native=%s pid=%d error=%v", home, nativeSessionID, record.PID, err))
		return 0, false
	}
	externalInterruptMu.Lock()
	if len(externalInterruptRecent) >= externalInterruptRecentCap {
		oldestKey := ""
		var oldest time.Time
		for key, ts := range externalInterruptRecent {
			if oldestKey == "" || ts.Before(oldest) {
				oldestKey, oldest = key, ts
			}
		}
		if oldestKey != "" {
			delete(externalInterruptRecent, oldestKey)
		}
	}
	externalInterruptRecent[nativeSessionID] = time.Now()
	externalInterruptMu.Unlock()
	logger.Info(fmt.Sprintf("ai.stop.external: SIGINT delivered (one Esc) home=%q native=%s pid=%d", home, nativeSessionID, record.PID))
	return record.PID, true
}

// externalTUIBusy reports whether the live TUI process that owns
// nativeSessionID is currently executing a turn (verified-live record &&
// status busy). Idle TUIs do NOT block: resuming an idle conversation is
// exactly the normal imported-session flow.
func externalTUIBusy(home, nativeSessionID string) bool {
	record, live := liveClaudeTUIRecord(home, nativeSessionID)
	if !live {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(record.Status), "busy")
}

// guardExternalTUIClaudeSpawn is the spawn precheck (C3): on the claude tool
// path with a known native session id, refuse the spawn while that session's
// TUI is mid-turn. Runs at the earliest abortable point — before any run state
// is mutated — so the refusal surfaces as a plain ai.error and the server can
// retry after the turn finishes. Non-claude providers and fresh (no-resume)
// spawns are never gated.
func guardExternalTUIClaudeSpawn(home, provider, resumeSessionID string) error {
	switch strings.TrimSpace(provider) {
	// "auto" is included deliberately: it is the default provider on
	// ai.message/ai.run.start and resolves to claude for imported sessions
	// (which always carry a Claude resume id). A codex-resolving auto run
	// never matches a ~/.claude/sessions pid record, so it is never gated.
	case "claude", "claudecode", "auto":
	default:
		return nil
	}
	resumeSessionID = strings.TrimSpace(resumeSessionID)
	if resumeSessionID == "" {
		return nil
	}
	if strings.TrimSpace(home) == "" {
		return nil
	}
	if externalTUIBusy(home, resumeSessionID) {
		logger.Info(fmt.Sprintf("ai.spawn.precheck: claude spawn refused, native session busy in its TUI native=%s", resumeSessionID))
		return newAgentExternalTUIBusyError()
	}
	return nil
}

// externalTUIHome resolves the Claude home for the stop/precheck paths.
// Separated from agentHome so the stop tests can pin a fake home the same way
// the rename/status tests do (HOME/USERPROFILE env).
func externalTUIHome() string {
	return agentHome()
}

// externalStopReply is the ai.status reply for an externally interrupted
// stop: "stopping" only when the interrupt signal actually went out.
func externalStopReply(sessionID string, interrupted bool) map[string]interface{} {
	status := "stopped"
	if interrupted {
		status = "stopping"
	}
	return map[string]interface{}{
		"type":       models.AgentEventAIStatus,
		"session_id": sessionID,
		"status":     status,
	}
}
