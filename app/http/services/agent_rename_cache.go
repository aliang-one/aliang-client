package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

// The rename cache is the agent's durable record of every user-set
// conversation title it has observed or accepted. Claude Code stores /rename
// titles in two volatile places — per-process ~/.claude/sessions/<pid>.json
// files and lazily-written sessions-index.json entries — and neither survives
// process pruning or a non-indexed project, which is why phone-visible titles
// used to revert to auto-derived summaries days after a rename. The cache adds
// a third, agent-owned tier: once a rename has been observed here it never
// reverts.
//
// Competition between sources is last-writer-wins on the agent clock (never a
// wall clock from another device):
//   - a live pid record participates with its updatedAt;
//   - a dead (zombie) pid record may only seed a cache entry the first time —
//     once a cache entry exists the zombie can never override it, no matter
//     which order the pid files happen to glob in;
//   - sessions-index.json customTitle remains a plain fallback without a
//     timestamp and never participates in the competition.
//
// PhoneServer publishes ai.session.rename for phone-side renames; those land
// here with origin=phone so both directions converge on this one record.

const (
	agentRenameOriginLocal = "local"
	agentRenameOriginPhone = "phone"

	// agentRenameCacheMaxEntries bounds the cache file; evictions are
	// oldest-first by updated_at.
	agentRenameCacheMaxEntries = 5000

	agentRenameCacheFilename = "rename_cache.json"
)

type agentRenameCacheEntry struct {
	Name        string `json:"name"`
	Origin      string `json:"origin"`
	UpdatedAt   string `json:"updated_at"`
	ProjectPath string `json:"project_path,omitempty"`
}

// agentRenameCachePath mirrors agentStatePath's dual-runtime layout so the
// cache is owned by whichever runtime performs collection.
func agentRenameCachePath() (string, error) {
	if IsUserAgentRuntime() {
		stateDir, err := runtimepath.UserStateDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(stateDir, "agent", agentRenameCacheFilename), nil
	}
	dir, err := cache.GetCacheSubdir("agent")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, agentRenameCacheFilename), nil
}

func loadAgentRenameCache() map[string]agentRenameCacheEntry {
	path, err := agentRenameCachePath()
	if err != nil {
		return map[string]agentRenameCacheEntry{}
	}
	entries, err := loadAgentRenameCacheFile(path)
	if err != nil {
		return map[string]agentRenameCacheEntry{}
	}
	return entries
}

func loadAgentRenameCacheFile(path string) (map[string]agentRenameCacheEntry, error) {
	entries := map[string]agentRenameCacheEntry{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return entries, nil
		}
		return entries, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return entries, nil
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		// A corrupt cache degrades to empty rather than failing collection —
		// titles then re-derive from the volatile sources and get re-cached.
		return map[string]agentRenameCacheEntry{}, nil
	}
	return entries, nil
}

func saveAgentRenameCache(entries map[string]agentRenameCacheEntry) error {
	path, err := agentRenameCachePath()
	if err != nil {
		return err
	}
	return saveAgentRenameCacheFile(path, entries)
}

func saveAgentRenameCacheFile(path string, entries map[string]agentRenameCacheEntry) error {
	if len(entries) > agentRenameCacheMaxEntries {
		type aged struct {
			sid string
			ts  time.Time
		}
		ages := make([]aged, 0, len(entries))
		for sid, entry := range entries {
			ages = append(ages, aged{sid: sid, ts: renameTimestamp(entry.UpdatedAt)})
		}
		sort.Slice(ages, func(i, j int) bool { return ages[i].ts.Before(ages[j].ts) })
		for _, victim := range ages[:len(ages)-agentRenameCacheMaxEntries] {
			delete(entries, victim.sid)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// mergeAgentRenameEntry applies last-writer-wins: a strictly newer updatedAt
// replaces the incumbent; on equal timestamps the incumbent stays so repeated
// scans never churn the file.
func mergeAgentRenameEntry(entries map[string]agentRenameCacheEntry, sid, name, origin, updatedAt, projectPath string) {
	sid = strings.TrimSpace(sid)
	name = strings.TrimSpace(name)
	if sid == "" || name == "" {
		return
	}
	if existing, ok := entries[sid]; ok {
		existingTS := renameTimestamp(existing.UpdatedAt)
		incomingTS := renameTimestamp(updatedAt)
		if existingTS.After(incomingTS) || existing.UpdatedAt == updatedAt {
			return
		}
	}
	entries[sid] = agentRenameCacheEntry{
		Name:        name,
		Origin:      origin,
		UpdatedAt:   updatedAt,
		ProjectPath: strings.TrimSpace(projectPath),
	}
}

func renameTimestamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// handleRemoteAIRename persists a phone-initiated rename. PhoneServer
// publishes ai.session.rename; this agent historically ignored it, so phone
// renames never reached local storage and PhoneServer had to freeze titles
// server-side. The rename is keyed by the native Claude session id —
// source_session_id first, then resume_session_id, then the managed
// conversation binding — because that is the key every other rename source
// competes on. The ack carries the agent-clock timestamp PhoneServer's
// latest-of-writer guard will compare against.
func (s *AgentService) handleRemoteAIRename(msg map[string]interface{}, writeJSON func(interface{}) error) {
	sessionID := remoteString(msg, "session_id")
	title := strings.TrimSpace(remoteString(msg, "title"))
	reject := func(reason string) {
		_ = writeJSON(map[string]interface{}{
			"type":       models.AgentEventAIRenameAck,
			"session_id": sessionID,
			"accepted":   false,
			"error":      reason,
		})
	}
	if title == "" {
		reject("missing title")
		return
	}
	nativeID := strings.TrimSpace(remoteString(msg, "source_session_id"))
	if nativeID == "" {
		nativeID = strings.TrimSpace(remoteString(msg, "resume_session_id"))
	}
	if nativeID == "" && s != nil && s.ai != nil {
		if binding, ok := s.ai.bindingForConversation(sessionID); ok {
			nativeID = binding.NativeSessionID
		}
	}
	if nativeID == "" {
		reject("missing source_session_id and no conversation binding")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	entries := loadAgentRenameCache()
	mergeAgentRenameEntry(entries, nativeID, title, agentRenameOriginPhone, now, remoteString(msg, "project_path"))
	if err := saveAgentRenameCache(entries); err != nil {
		logger.Warn("[AGENT-RENAME] phone_rename_persist_failed sid=" + nativeID + " error=" + err.Error())
	}
	// Push the updated inventory immediately: the rename (and the resulting
	// title_updated_at stamps) reach the phone in this round trip instead of
	// waiting for the digest tick or the minute backstop. Log-only on failure.
	if err := s.sendAgentHello(writeJSON, "rename_event"); err != nil {
		logger.Warn("[AGENT-RENAME] post_rename_hello_failed sid=" + nativeID + " error=" + err.Error())
	}
	_ = writeJSON(map[string]interface{}{
		"type":             models.AgentEventAIRenameAck,
		"session_id":       sessionID,
		"accepted":         true,
		"title_updated_at": now,
	})
}
