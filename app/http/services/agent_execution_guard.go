package services

import (
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/tccguard"
)

const (
	agentMaxTerminalSessions     = 4
	agentTerminalInputLimitBytes = 64 * 1024
	// Terminal output flood protection. A hard cumulative cap killed legitimately
	// long-running continuous commands (watch / top / tail -f), so the rate
	// window is now the only blind kill: a stream stops when its sustained rate
	// over the sliding window exceeds the flood threshold (runaway commands such
	// as `yes` or `cat /dev/urandom` are stopped within seconds). Cumulative
	// volume is governed by the quota challenge state machine instead
	// (agentTerminalQuota* vars below), not by a blind kill. watch-paced output
	// (~1-5 KB/s) never trips the rate limit, so it streams indefinitely up to
	// the idle timeout (agentTerminalAttachedIdle / agentTerminalDetachedIdle in
	// agent_terminal.go — both ACTIVITY clocks as of the 2026-10-22 fix).
	agentTerminalOutputRateWindow = 5 * time.Second
	agentTerminalOutputRateBytes  = 8 * 1024 * 1024

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
	// hard ceiling (agentAIHardCeiling) is a runaway backstop for ENGINE-ACTIVE
	// time: ticks spent awaiting a human decision, a tool result, or a subagent
	// are excluded (each has its own bound), so it carries no ordering
	// constraint against agentAIApprovalTimeout. A run/session ending or
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
// human decision before being cancelled (→ denied). Env-configurable:
//
//	ALIANG_AI_APPROVAL_TIMEOUT  (time.ParseDuration form, e.g. "24h", "30m"). Default 24h.
//
// agentAIHardCeiling is the runaway backstop for ENGINE-ACTIVE time: the
// watchdog accumulates elapsed time tick by tick and deliberately does NOT
// count ticks spent in a legitimate waiting state (awaiting a human approval,
// a pending tool result, or an active subagent). Each waiting state has its
// own bound (agentAIApprovalTimeout / agentAIToolWaitCeiling /
// agentAISubagentWaitCeiling), so a long approval, a 3h subagent batch or a
// 10min tool call can never be clipped by this ceiling — only time actually
// spent running the engine counts.
//
// Default 0 (disabled) since 2026-10-28: engine-active time is HOW LONG
// HEALTHY WORK TAKES, not a failure signal — a single message can legitimately
// drive an overnight run. Runaway protection lives in the liveness family
// (idle window / no-progress window / tool-wait & subagent ceilings / the
// stale-run sweeper), all of which fire on "no progress" instead of "too
// long". The ceiling remains as explicit opt-in for operators who want a
// hard spend bound:
//
//	ALIANG_AI_HARD_CEILING  (time.ParseDuration form). Default 0 = off;
//	any positive value enables it.
//
// Both resolve once at process start; blank/unparseable values fall back to
// the default (see resolveEnvDurationAllowZero, which is unit-tested).
var (
	agentAIApprovalTimeout = resolveEnvDuration("ALIANG_AI_APPROVAL_TIMEOUT", 24*time.Hour)
	agentAIHardCeiling     = resolveEnvDurationAllowZero("ALIANG_AI_HARD_CEILING", 0)

	// agentAINoProgressWindow bounds how long a run may keep EMITTING output
	// without producing meaningful progress (assistant text/thinking, a file
	// change, or a completed Codex work item). Raw output keeps the idle
	// watchdog fed, so a degenerate model loop (observed: a model emitting
	// filler `echo xN` tool calls every ~40s for 11+ hours) is invisible to
	// every other guard — only this progress-quality check stops it. Env:
	//
	//	ALIANG_AI_NO_PROGRESS_WINDOW  (time.ParseDuration, e.g. "45m"). Default 1h.
	//
	// Explicitly set it to "0s" to disable the check. Blank/unparseable/negative
	// values fall back to the default (see resolveEnvDurationAllowZero).
	agentAINoProgressWindow = resolveEnvDurationAllowZero("ALIANG_AI_NO_PROGRESS_WINDOW", 60*time.Minute)

	// agentAIToolWaitCeiling bounds how long a CONTIGUOUS tool/subagent wait
	// (approval-pending time excluded) may run before the run is cancelled as
	// hung. It is the only guard that fires during such a wait: the idle and
	// no-progress watchdogs pause while a tool result is pending, and the
	// ai.run.progress heartbeat keeps feeding the server-side stale-run
	// sweeper — without this bound a hung subagent keeps the phone showing
	// "running" until the stale-run sweeper reclaims it. Env:
	//
	//	ALIANG_AI_TOOL_WAIT_CEILING  (time.ParseDuration, e.g. "90m"). Default 1h.
	//
	// Explicitly set it to "0s" to disable. Blank/unparseable/negative values
	// fall back to the default (see resolveEnvDurationAllowZero).
	// 置 0 等于放弃该等待状态的唯一本地上限：硬顶只累加引擎活跃拍，等待期间
	// 的拍不计入，不会兜住这段等待；审批等待的拍同样不计入。
	// 无需再与 agentAIHardCeiling 比较大小：硬顶只累加引擎活跃时长，工具等待
	// 的拍不计入，2h 硬顶永远不会在这条等待里先触发（它会由本上限或 stale
	// 扫描器收敛）。审批等待的拍同样不计入本计时（挂起即停表），由
	// agentAIApprovalTimeout 管辖。
	agentAIToolWaitCeiling = resolveEnvDurationAllowZero("ALIANG_AI_TOOL_WAIT_CEILING", time.Hour)

	// agentAISubagentWaitCeiling bounds one CONTIGUOUS stretch with >=1 active
	// subagent, regardless of progress. task_progress resets the 1h tool-wait
	// clock above (progress = not hung), but not this one — so a subagent that
	// keeps emitting progress forever is still bounded. Env:
	//
	//	ALIANG_AI_SUBAGENT_WAIT_CEILING  (time.ParseDuration, e.g. "90m"). Default 6h.
	//
	// Explicitly "0s" disables. No ordering constraint against
	// agentAIHardCeiling any more — the hard ceiling counts only engine-active
	// ticks, so subagent-wait time is never clipped by it. Active-subagent
	// time also pauses the idle/no-progress guards (idlePaused), so without
	// this bound a marquee subagent would pin the run indefinitely.
	// 置 0 等于放弃该等待状态的唯一本地上限：硬顶只累加引擎活跃拍，等待期间
	// 的拍不计入，不会兜住这段等待；审批等待的拍同样不计入硬顶。
	agentAISubagentWaitCeiling = resolveEnvDurationAllowZero("ALIANG_AI_SUBAGENT_WAIT_CEILING", 6*time.Hour)
)

// Terminal output quota challenge knobs (terminal.quota.* protocol, see
// agent_terminal.go for the state machine that consumes them). Per spec §5.2
// there is NO pause/resume gate: when a terminal stream's cumulative output
// crosses agentTerminalQuotaCheckpointBytes the agent emits
// terminal.quota.challenge_required and output KEEPS STREAMING; the server's
// terminal.quota.resolved (granted) reply only clears the pending challenge —
// it does not resume anything. An unanswered challenge kills the session once
// output reaches the next checkpoint (kill_at_bytes, the checkpoint beyond
// the challenge point); agentTerminalQuotaMaxBytes separately hard-kills
// regardless of pending state.
// agentTerminalQuotaMinInterval throttles how often a challenge may fire so a
// noisy stream cannot spam the user. All three are package vars resolved once
// at process start; tests shrink them by injecting policy values through the
// quota-policy constructor rather than rewriting these vars.
//
//	ALIANG_TERMINAL_QUOTA_CHECKPOINT_MB           MiB per challenge checkpoint. Default 128.
//	ALIANG_TERMINAL_QUOTA_MAX_MB                  hard cumulative cap in MiB; explicit "0" disables the whole quota mechanism. Default 512.
//	ALIANG_TERMINAL_QUOTA_CHALLENGE_MIN_INTERVAL  minimum spacing between challenges (time.ParseDuration form). Default 30m.
//	ALIANG_TERMINAL_ATTACHED_IDLE                 attached-session activity idle before reap (time.ParseDuration form). Default 24h. See agent_terminal.go.
//	ALIANG_TERMINAL_DETACHED_IDLE                 detached-session activity idle before reap (time.ParseDuration form). Default 30m. See agent_terminal.go.
//
// Invalid values fall back to the default (see envMiB / envMiBAllowZero /
// resolveEnvDuration). For MAX_MB an explicit "0" is the documented off
// switch; every other invalid input (blank, unparseable, negative, or above
// the overflow guard) falls back to the default.
var (
	agentTerminalQuotaCheckpointBytes = envMiB("ALIANG_TERMINAL_QUOTA_CHECKPOINT_MB", 128)
	agentTerminalQuotaMaxBytes        = envMiBAllowZero("ALIANG_TERMINAL_QUOTA_MAX_MB", 512)
	agentTerminalQuotaMinInterval     = resolveEnvDuration("ALIANG_TERMINAL_QUOTA_CHALLENGE_MIN_INTERVAL", 30*time.Minute)
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

// resolveEnvDurationAllowZero is resolveEnvDuration but accepts an explicit
// zero ("0" / "0s") as a meaningful value — the documented way to disable a
// window-based guard. Unset/blank/unparseable/negative still fall back to def.
func resolveEnvDurationAllowZero(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return def
	}
	return d
}

// envMiB reads an env key holding a MiB count and returns the corresponding
// byte value (MiB × 1024 × 1024). def is the fallback, likewise expressed in
// MiB. Unset, blank, unparseable, non-positive, or overflow-range values (a
// MiB count whose byte conversion would not fit in int64, i.e. ≥ 8 TiB — a
// silently negative cap would kill a session on its first byte) use def.
func envMiB(key string, def int64) int64 {
	mib := def
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 && v <= math.MaxInt64/(1024*1024) {
			mib = v
		}
	}
	return mib * 1024 * 1024
}

// envMiBAllowZero is envMiB but accepts an explicit "0" as a meaningful value —
// the documented way to disable a byte-quota guard entirely (0 bytes = no
// cap). Any other invalid input — blank, unparseable, negative, or in the
// overflow range — falls back to def (MiB).
func envMiBAllowZero(key string, def int64) int64 {
	mib := def
	if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v >= 0 && v <= math.MaxInt64/(1024*1024) {
			mib = v
		}
	}
	return mib * 1024 * 1024
}

// resolveAgentAuthorizedCWD confines remote execution to project directories
// discovered and synchronized by this agent. Symlinks are resolved before the
// containment check so a path cannot escape an authorized root indirectly.
//
// Every disk-touching step (symlink resolution, stat) sits behind
// resolveAgentCWDWithConsent: on macOS a project under Desktop/Documents/
// Downloads/iCloud would otherwise raise a TCC prompt that nobody can answer
// when the request comes from the phone while the user is away. The consent
// gate probes the protected category once and remembers the grant.
func resolveAgentAuthorizedCWD(raw string, label string) (string, error) {
	cwd := strings.TrimSpace(raw)
	authorized := agentAuthorizedExecutionDirectories()
	if cwd == "" {
		if len(authorized) == 0 {
			return "", fmt.Errorf("%s is not authorized: no synchronized project directories", label)
		}
		var firstErr error
		for _, candidate := range authorized {
			resolved, err := resolveAgentCWDWithConsent(candidate)
			if err == nil {
				return resolved, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr != nil {
			return "", firstErr
		}
		return "", fmt.Errorf("%s is not authorized: no usable project directories", label)
	}
	resolved, err := resolveAgentCWDWithConsent(cwd)
	if err != nil {
		return "", err
	}
	if !agentAuthorizedDirectoryMatch(resolved, authorized) {
		return "", fmt.Errorf("%s is outside authorized project directories: %s", label, resolved)
	}
	return resolved, nil
}

// resolveAgentCWDWithConsent vets a working directory through tccguard before
// and after symbolic-link resolution. The post-resolution check matters because
// a path that looks unprotected can resolve into a protected folder (or another
// account's home) through a symlink.
func resolveAgentCWDWithConsent(raw string) (string, error) {
	if err := tccguard.GuardPath(raw); err != nil {
		return "", err
	}
	resolved, err := cleanExistingAgentDirectory(raw)
	if err != nil {
		return "", err
	}
	if err := tccguard.GuardPath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
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

// resolveAgentCWDFallbackHome resolves a remote-execution working directory like
// resolveAgentAuthorizedCWD, except that a device reporting zero synchronized
// project directories degrades the guard to an existence check instead of
// rejecting every request: the list is purely derived from session-transcript
// scans, so a genuinely fresh machine is indistinguishable from one whose
// transcripts were cleaned up (cleanupPeriodDays) or whose home drifted — on
// those the old deny-all turned every remote terminal/AI create into a
// millisecond rejection (2026-10-09 VM002 incident). Empty and ~-family cwds
// fall back to agentHome() so "~" and "~/x" land in the same tree the spawned
// shell's own ~ resolves to (agentTerminalEnv sets HOME=agentHome()).
func resolveAgentCWDFallbackHome(raw string, label string) (string, error) {
	raw = strings.TrimSpace(raw)
	authorized := agentAuthorizedExecutionDirectories()
	if len(authorized) == 0 {
		logger.Warn(fmt.Sprintf("[AGENT-BOOT] %s: no authorized project directories, guard degraded to existence-check", label))
		home := agentHome()
		if raw == "" || strings.HasPrefix(raw, "~") {
			if home == "" {
				return "", fmt.Errorf("%s is not authorized: no synchronized project directories and no resolvable home", label)
			}
			if raw == "" {
				raw = home
			} else {
				// Same Join formula as runtimepath.ExpandHome, but anchored at
				// agentHome(): under root/systemd $HOME is /root, so letting "~"
				// use agentHome() while "~/x" expanded $HOME would split one
				// input family across two trees.
				raw = filepath.Join(home, raw[1:])
			}
		}
		return resolveAgentCWDWithConsent(raw)
	}
	return resolveAgentAuthorizedCWD(raw, label)
}

func refreshAgentAuthorizedExecutionDirectories() []string {
	// singleflight 合并并发刷新：启动预热与首个请求（如 terminal.create 的
	// CWD 解析）同时撞上冷缓存时，共享同一次扫描而不是各扫一遍。
	v, _, _ := agentAuthorizedDirsRefreshGroup.Do("authorized_dirs", func() (interface{}, error) {
		snapshot := collectAgentSyncSnapshot(nil)
		return setAgentAuthorizedExecutionDirectoriesCache(snapshot.AuthorizedDirectories), nil
	})
	dirs, _ := v.([]string)
	return dirs
}

var agentAuthorizedDirsRefreshGroup singleflight.Group

// WarmAgentAuthorizedExecutionDirectories 预热授权目录缓存。authorized 列表
// 派生自会话转录扫描，缓存冷时 terminal.create 的 CWD 解析会同步跑一遍
// 全量扫描（2026-10-22 实测 Mac 冷 10.5s；旧二进制 liang-dev 30s+），期间
// shell 未 spawn、手机端建终端转圈、仪表盘 scrollback 空窗口。agent 启动时
// 调用一次，把扫描与出站连接并行掉，首个手机操作不再撞上冷窗口。
func WarmAgentAuthorizedExecutionDirectories() {
	go refreshAgentAuthorizedExecutionDirectories()
}

// setAgentAuthorizedExecutionDirectoriesCache refreshes the authorized-directory
// cache without touching the disk. This runs from background paths (connect
// hello, inventory/digest tickers, warm-up) where a stat of a project folder
// under Desktop/Documents/Downloads would raise a macOS TCC prompt while the
// user is away from the Mac (2026-10-27 privacy fix). Entries are therefore kept
// as normalized strings only; existence and symlink resolution are deferred to
// agentAuthorizedDirectoryMatch, which runs on an explicit user action
// (terminal.create) and goes through the consent gate.
func setAgentAuthorizedExecutionDirectoriesCache(rawDirs []string) []string {
	seen := make(map[string]struct{}, len(rawDirs))
	dirs := make([]string, 0, len(rawDirs))
	for _, raw := range rawDirs {
		dir, ok := normalizeAgentDirectoryString(raw)
		if !ok {
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

// normalizeAgentDirectoryString trims, expands ~, and absolutizes a configured
// directory WITHOUT any filesystem access (no stat, no EvalSymlinks).
func normalizeAgentDirectoryString(raw string) (string, bool) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", false
	}
	if expanded, err := cache.ExpandHomePath(dir); err == nil {
		dir = expanded
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	return filepath.Clean(abs), true
}

// agentAuthorizedDirectoryMatch reports whether path (already symlink-resolved)
// lies inside any authorized directory. The lexical comparison is tried first;
// when it misses (the configured entry itself contains a symlinked component,
// or is stale) each entry is re-resolved on demand — behind the TCC consent
// gate — and compared again. Only an explicit user action reaches this helper,
// so the on-demand disk access cannot fire from a background ticker.
func agentAuthorizedDirectoryMatch(path string, dirs []string) bool {
	if agentPathInsideAnyDirectory(path, dirs) {
		return true
	}
	for _, dir := range dirs {
		if err := tccguard.GuardPath(dir); err != nil {
			continue
		}
		resolved, err := cleanExistingAgentDirectory(dir)
		if err != nil {
			continue
		}
		if agentPathInsideDirectory(path, resolved) {
			return true
		}
	}
	return false
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
	case "agentcore", "agent-core", "agent_core":
		// 内置内核：不依赖任何外部 CLI，恒可用。
		return "agentcore", nil
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
