package services

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	agentTranscriptWatchInterval = 250 * time.Millisecond
	agentTranscriptWatchLease    = 90 * time.Second
	maxAgentTranscriptWatches    = 8
)

var transcriptWatchID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,120}$`)

type agentTranscriptWatch struct {
	sourceID, provider string
	path               string
	expires            time.Time
	retryAfter         time.Time
	lastSent           fs.FileInfo
}

type agentTranscriptWatches struct {
	home        string
	entries     map[string]*agentTranscriptWatch
	nextResolve time.Time
}

func newAgentTranscriptWatches(home string) *agentTranscriptWatches {
	return &agentTranscriptWatches{home: home, entries: make(map[string]*agentTranscriptWatch)}
}

func (w *agentTranscriptWatches) touch(id, sourceID, provider string, now time.Time) {
	if w.home == "" || !transcriptWatchID.MatchString(id) ||
		(sourceID != "" && !transcriptWatchID.MatchString(sourceID)) {
		return
	}
	switch provider {
	case "claude", "claudecode", "codex":
		if sourceID == "" {
			return
		}
	case "agentcore":
	default:
		return
	}
	if existing := w.entries[id]; existing != nil &&
		existing.sourceID == sourceID && existing.provider == provider && now.Before(existing.expires) {
		existing.expires = now.Add(agentTranscriptWatchLease)
		return
	}
	for id, watch := range w.entries {
		if !now.Before(watch.expires) {
			delete(w.entries, id)
		}
	}
	if w.entries[id] == nil && len(w.entries) >= maxAgentTranscriptWatches {
		return
	}
	w.entries[id] = &agentTranscriptWatch{sourceID: sourceID, provider: provider, expires: now.Add(agentTranscriptWatchLease)}
}

func (w *agentTranscriptWatches) poll(now time.Time, notify func(string) error) {
	for id, watch := range w.entries {
		if !now.Before(watch.expires) {
			delete(w.entries, id)
			continue
		}
		if now.Before(watch.retryAfter) {
			continue
		}
		if watch.path == "" {
			if now.Before(w.nextResolve) {
				continue
			}
			watch.path = w.resolvePath(id, watch)
			if watch.provider != "agentcore" {
				w.nextResolve = now.Add(time.Second)
			}
			if watch.path == "" {
				watch.retryAfter = now.Add(10 * time.Second)
				continue
			}
		}
		info, err := os.Stat(watch.path)
		if err != nil || !info.Mode().IsRegular() {
			watch.path = ""
			watch.retryAfter = now.Add(time.Second)
			continue
		}
		last := watch.lastSent
		if last != nil && info.Size() == last.Size() && info.ModTime().Equal(last.ModTime()) && os.SameFile(info, last) {
			continue
		}
		// The initial signal also reconciles changes made before the lease or
		// on a prior connection. Commit the fingerprint only after a write.
		if err := notify(id); err != nil {
			watch.retryAfter = now.Add(time.Second)
			return
		}
		watch.lastSent = info
	}
}

func (w *agentTranscriptWatches) resolvePath(id string, watch *agentTranscriptWatch) string {
	switch watch.provider {
	case "agentcore":
		// Imported journals may have a different cloud id. The journal filename
		// uses the native record's session id, supplied as source_id on import.
		journalID := id
		if watch.sourceID != "" && !strings.HasPrefix(watch.sourceID, "agentcore-") {
			journalID = watch.sourceID
		}
		path := filepath.Join(w.home, ".aliang", "ai-sessions", agentCoreJournalFileName(journalID))
		if _, err := os.Stat(path); err == nil {
			return path
		}
	case "claude", "claudecode":
		return ccPeerSessionJSONL(w.home, strings.TrimPrefix(watch.sourceID, "claude_"))
	case "codex":
		suffix := "-" + strings.TrimPrefix(watch.sourceID, "codex_") + ".jsonl"
		for _, root := range []string{"sessions", "archived_sessions"} {
			paths := findRecentAgentFiles(filepath.Join(w.home, ".codex", root), "rollout-*"+suffix, 1)
			if len(paths) > 0 {
				return paths[0]
			}
		}
	}
	return ""
}

func runAgentTranscriptWatches(ctx context.Context, frames <-chan map[string]interface{}, notify func(string) error) {
	watches := newAgentTranscriptWatches(agentHome())
	ticker := time.NewTicker(agentTranscriptWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-frames:
			sessionID := remoteString(frame, "session_id")
			if frame["enabled"] == false {
				delete(watches.entries, sessionID)
				continue
			}
			watches.touch(sessionID, remoteString(frame, "source_session_id"),
				remoteString(frame, "provider"), time.Now())
		case at := <-ticker.C:
			watches.poll(at, notify)
		}
	}
}
