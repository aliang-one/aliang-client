package services

import (
	"fmt"
	"strings"

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
func interruptExternalClaudeTurn(home, nativeSessionID string) (pid int, ok bool) {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return 0, false
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return 0, false
	}
	record, found := loadClaudeRenameRecords(home)[nativeSessionID]
	if !found || record.PID <= 0 {
		logger.Info(fmt.Sprintf("ai.stop.external: no live pid record home=%q native=%s", home, nativeSessionID))
		return 0, false
	}
	if !isPidAlive(record.PID) {
		logger.Info(fmt.Sprintf("ai.stop.external: pid record is stale home=%q native=%s pid=%d", home, nativeSessionID, record.PID))
		return 0, false
	}
	if externalInterruptFunc == nil {
		return 0, false
	}
	if err := externalInterruptFunc(record.PID); err != nil {
		logger.Info(fmt.Sprintf("ai.stop.external: signal failed home=%q native=%s pid=%d error=%v", home, nativeSessionID, record.PID, err))
		return 0, false
	}
	logger.Info(fmt.Sprintf("ai.stop.external: SIGINT delivered (one Esc) home=%q native=%s pid=%d", home, nativeSessionID, record.PID))
	return record.PID, true
}

// externalTUIBusy reports whether the live TUI process that owns
// nativeSessionID is currently executing a turn (record exists && pid alive
// && status busy). Idle TUIs do NOT block: resuming an idle conversation is
// exactly the normal imported-session flow.
func externalTUIBusy(home, nativeSessionID string) bool {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	if nativeSessionID == "" {
		return false
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return false
	}
	record, found := loadClaudeRenameRecords(home)[nativeSessionID]
	if !found || record.PID <= 0 || !isPidAlive(record.PID) {
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
	case "claude", "claudecode":
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
