package services

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// agentVibeDigestInterval is how often the remote connection re-hashes the
// cheap parts of the session inventory to detect changes worth pushing.
const agentVibeDigestInterval = 10 * time.Second

// agentVibeDigest fingerprints everything that changes the phone-visible
// session list without paying for the transcript walk: index titles, pid
// rename records (with liveness, which drives the running status) and the
// durable rename cache. A digest change means the next hello would differ, so
// the remote loop pushes one immediately instead of waiting for the minute
// backstop — turning title/status sync from a 60s worst case into ~10s.
// Returns "" when there is nothing to fingerprint (no Claude home / no
// sessions), which callers treat as "nothing to compare yet".
func agentVibeDigest(home string) string {
	home = strings.TrimSpace(home)
	if home == "" {
		return ""
	}
	type tuple struct {
		id    string
		title string
		ts    string
		extra string
	}
	var tuples []tuple

	root := filepath.Join(home, ".claude", "projects")
	for _, indexPath := range listAllProjectIndexFiles(root) {
		raw, err := os.ReadFile(indexPath)
		if err != nil {
			continue
		}
		var index struct {
			Entries []struct {
				SessionID   string `json:"sessionId"`
				FirstPrompt string `json:"firstPrompt"`
				Summary     string `json:"summary"`
				CustomTitle string `json:"customTitle"`
				Modified    string `json:"modified"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(raw, &index); err != nil {
			continue
		}
		for _, entry := range index.Entries {
			if entry.SessionID == "" {
				continue
			}
			tuples = append(tuples, tuple{
				id:    "claude_" + entry.SessionID,
				title: firstNonEmpty(entry.CustomTitle, entry.Summary, entry.FirstPrompt),
				ts:    entry.Modified,
			})
		}
	}

	for sid, record := range loadClaudeRenameRecords(home) {
		// No timestamp here on purpose: Claude Code rewrites live pid records
		// (updatedAt) every few seconds during activity. Only the wire-visible
		// state — name, liveness and busy/idle status — may flip the digest;
		// an idle→busy transition is exactly what the phone must see within
		// one tick, while pure updatedAt churn must not push.
		alive := isPidAlive(record.PID)
		tuples = append(tuples, tuple{
			id:    "pid_" + sid,
			title: record.Name,
			extra: aliveLiveMark(alive) + ":" + record.Status,
		})
	}

	for sid, entry := range loadAgentRenameCache() {
		tuples = append(tuples, tuple{
			id:    "cache_" + sid,
			title: entry.Name,
			ts:    entry.UpdatedAt,
		})
	}

	// Codex TUI conversations do not update the Claude indexes above. Fingerprint
	// the recent rollout files by path, mtime, and size so an append from the
	// desktop TUI triggers the same near-real-time hello refresh.
	for _, root := range []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
	} {
		for _, sessionPath := range findRecentCodexSessionFiles(root, agentVibeSessionFileScanLimit) {
			info, err := os.Stat(sessionPath)
			if err != nil {
				continue
			}
			tuples = append(tuples, tuple{
				id:    "codex_file_" + sessionPath,
				ts:    info.ModTime().UTC().Format(time.RFC3339Nano),
				extra: strconv.FormatInt(info.Size(), 10),
			})
		}
	}

	// Claude 索引由 CC 懒更新(生产曾 2026-08-24 起停更),索引指纹盖不住
	// jsonl 追加——TUI 新消息若无 pid 状态翻转,原只能等 60s 周期兜底。
	// 对采集范围内(与游走通道同 top-N)的转录文件做 path+mtime+size 指纹,
	// 让 10s tick 直接感知正文落地(2026-10-18 审计 S1 延迟缺口)。
	claudeFiles, _, _ := findRecentAgentFilesWithStats(
		filepath.Join(home, ".claude", "projects"), "*.jsonl", agentVibeSessionFileScanLimit)
	for _, sessionPath := range claudeFiles {
		if strings.Contains(sessionPath, string(filepath.Separator)+"subagents"+string(filepath.Separator)) {
			continue
		}
		info, err := os.Stat(sessionPath)
		if err != nil {
			continue
		}
		tuples = append(tuples, tuple{
			id:    "claude_file_" + sessionPath,
			ts:    info.ModTime().UTC().Format(time.RFC3339Nano),
			extra: strconv.FormatInt(info.Size(), 10),
		})
	}

	if len(tuples) == 0 {
		return ""
	}
	sort.Slice(tuples, func(i, j int) bool {
		if tuples[i].id != tuples[j].id {
			return tuples[i].id < tuples[j].id
		}
		if tuples[i].title != tuples[j].title {
			return tuples[i].title < tuples[j].title
		}
		return tuples[i].ts < tuples[j].ts
	})
	h := sha1.New()
	for _, t := range tuples {
		_, _ = h.Write([]byte(t.id + "\x00" + t.title + "\x00" + t.ts + "\x00" + t.extra + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func aliveLiveMark(alive bool) string {
	if alive {
		return "live"
	}
	return "dead"
}
