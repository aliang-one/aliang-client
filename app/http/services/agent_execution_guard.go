package services

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/cache"
)

const (
	agentMaxTerminalSessions     = 4
	agentTerminalInputLimitBytes = 64 * 1024
	// Terminal output flood protection. A hard cumulative cap killed legitimately
	// long-running continuous commands (watch / top / tail -f). We now stop a
	// stream only when its sustained rate over a sliding window exceeds the flood
	// threshold (runaway commands such as `yes` or `cat /dev/urandom` are stopped
	// within seconds), with a high lifetime cap as a backstop for slow leaks.
	// watch-paced output (~1-5 KB/s) never trips either limit, so it streams
	// indefinitely up to the idle timeout.
	agentTerminalOutputRateWindow = 5 * time.Second
	agentTerminalOutputRateBytes  = 8 * 1024 * 1024
	agentTerminalOutputCapBytes   = 256 * 1024 * 1024
	agentTerminalIdleTimeout      = 30 * time.Minute

	// agentAISessionResidentCap bounds how many AI session handles the agent
	// keeps resident at once. This is NOT a gate that blocks new conversations
	// — it is an LRU eviction threshold. When a new session would exceed it the
	// oldest idle (non-running) session is dropped (evictOldestIdleAISession-
	// Locked). Each session holds at most agentAIHistoryCaptureMaxBytes, so this
	// caps resident memory at roughly cap × 1 MiB. Running turns are never
	// evicted; if every resident session is mid-turn the new one is still added
	// (temporary overshoot, re-bounded once a turn settles). The only other
	// reset is closeAll() on agent↔server reconnect/restart.
	agentAISessionResidentCap = 8
	agentAIMessageLimitBytes  = 64 * 1024
	// AI output flood protection, mirroring the terminal policy: a sliding
	// window stops runaway bursts (an AI dumping megabytes per second) while
	// letting long but paced sessions run, with a high lifetime cap as a
	// backstop. This replaces the old hard 2 MiB cumulative cap that killed
	// legitimately long coding sessions after they emitted 2 MiB total.
	agentAIOutputRateWindow = 5 * time.Second
	agentAIOutputRateBytes  = 16 * 1024 * 1024
	agentAIOutputCapBytes   = 256 * 1024 * 1024

	// AI run liveness. A run is kept alive while it produces output, awaits a
	// human approval decision, or waits for Claude tool/subagent results. A run
	// that goes silent longer than agentAIIdleWindow without one of those waits is
	// stopped as idle. While awaiting a human decision the idle watchdog is
	// paused, but the approval wait itself is bounded by agentAIApprovalTimeout
	// (the env-configured var below): on expiry the request is cancelled (resolved
	// as denied) so a forgotten approval cannot pin a headless CLI forever. The
	// hard ceiling (agentAIHardCeiling) is a runaway backstop that fires from run
	// start regardless of activity; it MUST exceed agentAIApprovalTimeout or a
	// long approval wait is clipped by the ceiling first. A run/session ending or
	// the agent going offline also cancels pending approvals
	// (see agent_ai.go startAIWatchdog / clearPendingApprovalsLocked).
	agentAIIdleWindow        = 10 * time.Minute
	agentAIIdleCheckInterval = 30 * time.Second

	// agentAIRunProgressInterval is how often an active run emits ai.run.progress
	// (files touched so far + git working-tree changes) so the mobile dashboard
	// card updates live instead of only at ai.done.
	agentAIRunProgressInterval = 10 * time.Second

	// agentAIHistoryCaptureMaxBytes bounds the assistant output retained for
	// session history/replay. Client streaming is governed separately by the
	// output limiter (agentAIOutputCapBytes, which stops the run); this cap only
	// limits the in-memory buffer kept after a run, so a long/bursty run cannot
	// pin hundreds of MB in session history.
	agentAIHistoryCaptureMaxBytes = 1 << 20 // 1 MiB
)

var (
	agentAuthorizedDirsMu    sync.Mutex
	agentAuthorizedDirsCache []string
)

// agentAIApprovalTimeout bounds how long a single approval request waits for a
// human decision before being cancelled (→ denied). Env-configurable so the max
// confirmation window is tunable without a recompile:
//
//	ALIANG_AI_APPROVAL_TIMEOUT  (time.ParseDuration form, e.g. "24h", "30m"). Default 24h.
//
// agentAIHardCeiling is the runaway backstop: a run is cancelled once its total
// active time exceeds this, regardless of approval state. It MUST be greater
// than agentAIApprovalTimeout, otherwise a long approval wait is clipped by the
// ceiling first. Env-configurable:
//
//	ALIANG_AI_HARD_CEILING  (time.ParseDuration form). Default 48h (> 24h approval).
//
// Both resolve once at process start; blank/unparseable/non-positive values
// fall back to the default (see resolveEnvDuration, which is unit-tested).
var (
	agentAIApprovalTimeout = resolveEnvDuration("ALIANG_AI_APPROVAL_TIMEOUT", 24*time.Hour)
	agentAIHardCeiling     = resolveEnvDuration("ALIANG_AI_HARD_CEILING", 48*time.Hour)
)

// resolveEnvDuration parses a Go duration from the env key, returning def when
// the key is unset, blank, unparseable, or non-positive.
func resolveEnvDuration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// resolveAgentAuthorizedCWD resolves a working directory for remote execution.
// Operator policy: authorized-directory confinement REMOVED — commands may run
// anywhere the agent's OS user can reach. We still validate the path is real and
// is a directory; an empty cwd falls back to the user's home (else ".").
func resolveAgentAuthorizedCWD(raw string, label string) (string, error) {
	_ = label
	cwd := strings.TrimSpace(raw)
	if cwd == "" {
		if home, err := cache.ExpandHomePath("~"); err == nil && home != "" {
			cwd = home
		} else {
			cwd = "."
		}
	}
	return cleanExistingAgentDirectory(cwd)
}

func agentAuthorizedExecutionDirectories() []string {
	agentAuthorizedDirsMu.Lock()
	if len(agentAuthorizedDirsCache) > 0 {
		dirs := append([]string(nil), agentAuthorizedDirsCache...)
		agentAuthorizedDirsMu.Unlock()
		return dirs
	}
	agentAuthorizedDirsMu.Unlock()
	return refreshAgentAuthorizedExecutionDirectories()
}

func refreshAgentAuthorizedExecutionDirectories() []string {
	snapshot := collectAgentSyncSnapshot(nil)
	return setAgentAuthorizedExecutionDirectoriesCache(snapshot.AuthorizedDirectories)
}

func setAgentAuthorizedExecutionDirectoriesCache(rawDirs []string) []string {
	seen := make(map[string]struct{}, len(rawDirs))
	dirs := make([]string, 0, len(rawDirs))
	for _, raw := range rawDirs {
		dir, err := cleanExistingAgentDirectory(raw)
		if err != nil {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	agentAuthorizedDirsMu.Lock()
	agentAuthorizedDirsCache = append([]string(nil), dirs...)
	agentAuthorizedDirsMu.Unlock()
	return append([]string(nil), dirs...)
}

func cleanExistingAgentDirectory(raw string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", errors.New("working directory is empty")
	}
	if expanded, err := cache.ExpandHomePath(dir); err == nil {
		dir = expanded
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		abs = resolved
	}
	stat, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !stat.IsDir() {
		return "", fmt.Errorf("working directory is not a directory: %s", abs)
	}
	return abs, nil
}

func agentPathInsideAnyDirectory(path string, dirs []string) bool {
	for _, dir := range dirs {
		if agentPathInsideDirectory(path, dir) {
			return true
		}
	}
	return false
}

func agentPathInsideDirectory(path string, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func resolveAgentShell(raw string) (string, error) {
	shell := strings.TrimSpace(raw)
	if shell == "" {
		shell = defaultAgentShell()
	}
	base := strings.ToLower(filepath.Base(shell))
	if !isAllowedAgentShellBase(base) {
		return "", fmt.Errorf("unsupported terminal shell: %s", shell)
	}

	var path string
	var err error
	if filepath.IsAbs(shell) || strings.ContainsAny(shell, `/\`) {
		if expanded, expandErr := cache.ExpandHomePath(shell); expandErr == nil {
			shell = expanded
		}
		path, err = filepath.Abs(shell)
		if err != nil {
			return "", err
		}
	} else {
		path, err = exec.LookPath(shell)
		if err != nil {
			return "", err
		}
	}
	if err := validateAgentExecutable(path); err != nil {
		return "", err
	}
	if !isTrustedAgentShellPath(path) {
		return "", fmt.Errorf("terminal shell is not in a trusted system location: %s", path)
	}
	return path, nil
}

func isAllowedAgentShellBase(base string) bool {
	switch base {
	case "sh", "bash", "zsh", "dash", "fish", "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return true
	default:
		return false
	}
}

func validateAgentExecutable(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		return err
	}
	if stat.IsDir() {
		return fmt.Errorf("executable path is a directory: %s", path)
	}
	if runtime.GOOS != "windows" && stat.Mode()&0o111 == 0 {
		return fmt.Errorf("executable is not runnable: %s", path)
	}
	return nil
}

func isTrustedAgentShellPath(path string) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	dir := filepath.Clean(filepath.Dir(path))
	for _, trusted := range []string{"/bin", "/usr/bin", "/usr/local/bin", "/opt/homebrew/bin"} {
		if dir == trusted {
			return true
		}
	}
	return false
}

func normalizeAgentAIProvider(raw string) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(raw))
	if provider == "" {
		return "auto", nil
	}
	switch provider {
	case "auto", "codex", "claude", "claudecode":
		return provider, nil
	case "claude-code", "claude_code":
		return "claudecode", nil
	case "opencode", "open-code", "open_code":
		return "opencode", nil
	default:
		return "", fmt.Errorf("unsupported AI provider: %s", raw)
	}
}

func truncateAgentTextBytes(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit]
}

func sortedAgentStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// appendAgentAIHistoryCapture appends text to b for session-history replay, but
// stops accumulating once b reaches agentAIHistoryCaptureMaxBytes so a long or
// bursty run cannot pin unbounded memory in the session history. Streaming to
// the client is unaffected (it happens before this capture and is governed by
// the output limiter). The caller must hold b's protecting mutex.
func appendAgentAIHistoryCapture(b *strings.Builder, text string) {
	if b == nil {
		return
	}
	if b.Len() >= agentAIHistoryCaptureMaxBytes {
		return
	}
	remaining := agentAIHistoryCaptureMaxBytes - b.Len()
	if len(text) > remaining {
		text = text[:remaining]
	}
	b.WriteString(text)
}
