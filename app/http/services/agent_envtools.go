package services

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// agentEnvToolGitCmdTimeout bounds read-only git probes so a stuck repo never wedges the WS loop.
const agentEnvToolGitCmdTimeout = 5 * time.Second
const agentEnvToolVersionProbeTimeout = 3 * time.Second
const agentEnvToolStatusMaxLines = 40

// handleAgentEnvToolsMessage dispatches the read-only environment-inspection tools
// (git.status / env.info) the cloud orchestrator sends before generating a bash command.
// These tools never execute caller-controlled command text: only fixed read-only
// subcommands of git / node / git / python3 are probed.
func handleAgentEnvToolsMessage(msg map[string]interface{}, writeJSON func(interface{}) error) {
	switch remoteString(msg, "type") {
	case models.AgentEventGitStatus:
		_ = writeJSON(agentGitStatusPayload(msg))
	case models.AgentEventEnvInfo:
		_ = writeJSON(agentEnvInfoPayload(msg))
	}
}

// resolveEnvToolCwd resolves the request "cwd" against the device's authorized project
// directories (same gate the file.* handlers apply via resolveAgentProjectPath, just under
// the "cwd" key the server sends). On failure it returns the per-type error payload to emit.
func resolveEnvToolCwd(msg map[string]interface{}, requestID, errorType string) (string, map[string]interface{}, bool) {
	resolved, err := resolveAgentProjectPath(remoteString(msg, "cwd"))
	if err != nil {
		return "", agentEnvToolErrorPayload(errorType, requestID, err), false
	}
	return resolved, nil, true
}

func agentEnvToolErrorPayload(errorType, requestID string, err error) map[string]interface{} {
	message := "agent environment tool request failed"
	if err != nil {
		message = err.Error()
	}
	return map[string]interface{}{
		"type":       errorType,
		"request_id": requestID,
		"error":      message,
	}
}

func agentGitStatusPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	cwd, errPayload, ok := resolveEnvToolCwd(msg, requestID, models.AgentEventGitStatusError)
	if !ok {
		return errPayload
	}
	isRepo := strings.TrimSpace(gitStdoutCmd(cwd, "rev-parse", "--is-inside-work-tree")) == "true"
	branch := ""
	status := ""
	if isRepo {
		// On an unborn repo (no commits yet) HEAD is unknown; treat that as no branch
		// rather than leaking git's stderr into the result.
		branch = strings.TrimSpace(gitStdoutCmd(cwd, "rev-parse", "--abbrev-ref", "HEAD"))
		status = truncateLines(gitCmd(cwd, "status", "--short"), agentEnvToolStatusMaxLines)
	}
	return map[string]interface{}{
		"type":         models.AgentEventGitStatusResult,
		"request_id":   requestID,
		"is_repo":      isRepo,
		"branch":       branch,
		"status":       status,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
}

func agentEnvInfoPayload(msg map[string]interface{}) map[string]interface{} {
	requestID := remoteString(msg, "request_id")
	// env.info probes are global, but we still resolve cwd to confirm the device is
	// allowed to be inspected (mirrors file.* handler gating) and keep the request honest.
	if _, errPayload, ok := resolveEnvToolCwd(msg, requestID, models.AgentEventEnvInfoError); !ok {
		return errPayload
	}
	userName := os.Getenv("USER")
	if strings.TrimSpace(userName) == "" {
		if name, err := userCurrentName(); err == nil {
			userName = name
		}
	}
	versions := map[string]string{
		"node":    versionProbe("node", "--version"),
		"git":     versionProbe("git", "--version"),
		"python3": versionProbe("python3", "--version"),
	}
	return map[string]interface{}{
		"type":         models.AgentEventEnvInfoResult,
		"request_id":   requestID,
		"os":           runtime.GOOS,
		"arch":         runtime.GOARCH,
		"shell":        os.Getenv("SHELL"),
		"user":         userName,
		"versions":     versions,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}
}

// gitCmd runs a fixed read-only git subcommand scoped to cwd via `git -C cwd`.
// It never passes caller-controlled argument text. Returns combined stdout+stderr
// as a string ("" on error / timeout). Use this for `status --short` whose payload
// can legitimately span both streams.
func gitCmd(cwd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), agentEnvToolGitCmdTimeout)
	defer cancel()
	fullArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = cwd
	out, _ := cmd.CombinedOutput()
	return string(out)
}

// gitStdoutCmd is the stdout-only variant for probes whose stderr is noise on edge
// cases (e.g. `rev-parse --abbrev-ref HEAD` on an unborn repo). Never returns stderr.
func gitStdoutCmd(cwd string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), agentEnvToolGitCmdTimeout)
	defer cancel()
	fullArgs := append([]string{"-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// versionProbe runs a fixed `<name> --version` (or similar) probe with a short timeout.
// Best-effort: returns the trimmed output or "" on any error. Never panics the caller.
func versionProbe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), agentEnvToolVersionProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// truncateLines keeps the first max lines and appends a truncation marker if more were present.
func truncateLines(s string, max int) string {
	if s == "" {
		return ""
	}
	if max <= 0 {
		return ""
	}
	// Trim a single trailing newline so "a\nb\n" splits to [a,b], not [a,b,""].
	trimmed := strings.TrimRight(s, "\n")
	lines := strings.Split(trimmed, "\n")
	if len(lines) <= max {
		return s
	}
	kept := strings.Join(lines[:max], "\n")
	return kept + "\n... (truncated)"
}

// userCurrentName wraps os/user.Current().Username so it can be mocked in tests.
func userCurrentName() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", err
	}
	return current.Username, nil
}
