package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

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
// wall clock from another device), with one precedence rule on top: a
// DELIBERATE title (phone rename, /rename by the user, peer or hook) always
// outranks an automatic one (Claude Code's nameSource auto/derived/collision
// labels), regardless of timestamps. CC rewrites a live pid record's updatedAt
// on every status/classifier write while keeping the old automatic name, so a
// pure timestamp race would let an open TUI silently revert a phone rename.
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

	// agentRenameSourcePhone is the cache-entry Source recorded for renames
	// accepted over the wire. Locally observed entries carry Claude Code's own
	// pid-record nameSource instead (user/peer/hook/auto/derived/collision).
	agentRenameSourcePhone = "phone"

	// agentRenameCacheMaxEntries bounds the cache file; evictions are
	// oldest-first by updated_at.
	agentRenameCacheMaxEntries = 5000

	agentRenameCacheFilename = "rename_cache.json"
)

type agentRenameCacheEntry struct {
	Name   string `json:"name"`
	Origin string `json:"origin"`
	// Source is the origin's name provenance: Claude Code's pid-record
	// nameSource (user/peer/hook/auto/derived/collision) for observed local
	// names, or "phone" for wire renames. Empty on entries written before the
	// field existed; agentRenameEntryExplicit treats those conservatively as
	// deliberate so an upgrade never lets an auto label revert a title.
	Source      string `json:"source,omitempty"`
	UpdatedAt   string `json:"updated_at"`
	ProjectPath string `json:"project_path,omitempty"`
	// SkipMirror records that the rename-time transcript mirror was
	// deliberately skipped (codex/opencode sessions live outside
	// ~/.claude/projects), so convergePhoneRenameMirrors leaves them alone
	// too. Absent (legacy entries) = false = conservative mirror, matching
	// agentRenameMirrorsClaude's default direction.
	SkipMirror bool `json:"skip_mirror,omitempty"`
}

// agentRenameSourceExplicit reports whether a name provenance is a deliberate
// title. Claude Code's full nameSource enum is
// user|peer|hook|derived|collision|auto: only the first three are human or
// peer intent. An EMPTY source counts as explicit for backward compatibility —
// Claude Code builds before the nameSource field only wrote a pid-record name
// when the user renamed the session, and cache entries written before this
// field have no better information.
func agentRenameSourceExplicit(source string) bool {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "user", "peer", "hook", agentRenameSourcePhone:
		return true
	}
	return false
}

// agentRenameSlugSanitizer mirrors Claude Code's cwd→slug normalization.
var agentRenameSlugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// agentRenameLooksLikeDerivedSlug reports whether name is Claude Code's
// placeholder "<project-dir-slug>-<2 alnum>" (observed: plan-maker-96,
// alianggate-99, aliangboard-5e, tmp-4f).
//
// These are CC's own fallback labels for sessions it has no real title for
// (nameSource=derived). The shape check deliberately does NOT require a live
// pid record: cache entries written before the Source field existed carry no
// provenance and their CC process is long gone, yet the old "empty source =
// deliberate" rule kept reporting them as user renames — which stamped
// title_updated_at on PhoneServer and permanently disabled Auto Session Title.
// The predicate is intentionally isomorphic to the server's
// looksLikeClaudeDerivedSessionName so both sides reach the same verdict.
func agentRenameLooksLikeDerivedSlug(name, projectPath string) bool {
	name = strings.TrimSpace(name)
	projectPath = strings.TrimSpace(projectPath)
	if name == "" || projectPath == "" {
		return false
	}
	// 与服务端一致：先剥掉尾部分隔符，否则 "/x/aliangboard/" 会切出空 basename。
	base := strings.TrimRight(projectPath, `/\`)
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	slug := strings.Trim(agentRenameSlugSanitizer.ReplaceAllString(strings.ToLower(base), "-"), "-")
	if slug == "" {
		return false
	}
	lower := strings.ToLower(name)
	prefix := slug + "-"
	if !strings.HasPrefix(lower, prefix) {
		return false
	}
	tail := lower[len(prefix):]
	if len(tail) != 2 {
		return false
	}
	for _, r := range tail {
		if (r < '0' || r > '9') && (r < 'a' || r > 'z') {
			return false
		}
	}
	return true
}

// agentRenameEntryExplicit classifies a whole cache entry: a phone rename is
// always deliberate, otherwise the recorded provenance decides. Legacy entries
// with no provenance fall back to the slug shape check above before assuming
// the name was a deliberate rename.
func agentRenameEntryExplicit(entry agentRenameCacheEntry) bool {
	if entry.Origin == agentRenameOriginPhone {
		return true
	}
	if source := strings.TrimSpace(entry.Source); source != "" {
		return agentRenameSourceExplicit(source)
	}
	if agentRenameLooksLikeDerivedSlug(entry.Name, entry.ProjectPath) {
		return false
	}
	return agentRenameSourceExplicit("")
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
	// A UNIQUE temp file per save: two concurrent writers sharing a fixed
	// "<path>.tmp" can truncate each other's partial write and rename the
	// interleaved result into place (the mutex below narrows but does not
	// remove stray direct-save callers such as tests).
	tmpFile, err := os.CreateTemp(filepath.Dir(path), ".rename_cache-*")
	if err != nil {
		return err
	}
	tmp := tmpFile.Name()
	if _, err := tmpFile.Write(raw); err != nil {
		tmpFile.Close()
		os.Remove(tmp)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// agentRenameCacheMu serializes every read-modify-write cycle on the rename
// cache. Writers live on different goroutines — handleRemoteAIRename on the
// WS read loop, applyClaudeRenameNames' observed/purged merge on the inventory
// ticker and HTTP handlers — and each save rewrites the WHOLE file, so two
// unsynchronized cycles interleave as load{A}/load{B}/save{A}/save{B}: the
// loser's entry (typically the phone rename, which has no local re-source) is
// lost permanently.
var agentRenameCacheMu sync.Mutex

// updateAgentRenameCache runs one whole read-modify-write cycle under
// agentRenameCacheMu and persists only when mutate reports a change, so
// steady-state scans never rewrite the file.
func updateAgentRenameCache(mutate func(entries map[string]agentRenameCacheEntry) bool) error {
	agentRenameCacheMu.Lock()
	defer agentRenameCacheMu.Unlock()
	entries := loadAgentRenameCache()
	if !mutate(entries) {
		return nil
	}
	return saveAgentRenameCache(entries)
}

// mergeAgentRenameEntry applies the precedence described at the top of the
// file: a deliberate title beats an automatic one regardless of timestamps;
// within the same class the strictly newer updatedAt wins, and on equal
// timestamps the incumbent stays so repeated scans never churn the file.
func mergeAgentRenameEntry(entries map[string]agentRenameCacheEntry, sid string, incoming agentRenameCacheEntry) {
	sid = strings.TrimSpace(sid)
	incoming.Name = strings.TrimSpace(incoming.Name)
	incoming.ProjectPath = strings.TrimSpace(incoming.ProjectPath)
	if sid == "" || incoming.Name == "" {
		return
	}
	if existing, ok := entries[sid]; ok {
		existingExplicit := agentRenameEntryExplicit(existing)
		incomingExplicit := agentRenameEntryExplicit(incoming)
		switch {
		case !incomingExplicit && existingExplicit:
			// An auto/derived/collision label may never overwrite a deliberate
			// title — CC bumps those labels' updatedAt on every status write.
			return
		case incomingExplicit && !existingExplicit:
			// A deliberate rename always wins over a previously cached
			// automatic label, even with an older clock stamp.
		default:
			existingTS := renameTimestamp(existing.UpdatedAt)
			incomingTS := renameTimestamp(incoming.UpdatedAt)
			if existingTS.After(incomingTS) || existing.UpdatedAt == incoming.UpdatedAt {
				return
			}
		}
	}
	entries[sid] = incoming
}

func renameTimestamp(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// normalizeAgentRenameTitle mirrors Claude Code's own /rename normalization
// (pi(): strip [\x00-\x1f\x7f-\x9f]; whitespace runs fold to one space like
// CC's fork-title path; cap at 200 code points). PhoneServer has normalized
// PATCH titles since 2026-10, but older servers and other wire sources may
// still send raw titles — without this the SAME title lands in different
// shapes depending on whether it was renamed in the TUI or on the phone, and
// a title containing \n/ESC renders as multi-line/escape garbage in the
// /resume list, which CC's own renames never produce.
func normalizeAgentRenameTitle(raw string) string {
	var out strings.Builder
	pendingSpace := false
	written := 0
	for _, r := range raw {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue // control chars are stripped outright, like CC's T1
		}
		if unicode.IsSpace(r) {
			pendingSpace = true
			continue
		}
		if written >= 200 {
			break
		}
		if pendingSpace && out.Len() > 0 {
			out.WriteRune(' ')
			written++
		}
		pendingSpace = false
		out.WriteRune(r)
		written++
	}
	return strings.TrimSpace(out.String())
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
	title := normalizeAgentRenameTitle(remoteString(msg, "title"))
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
	mirrorsClaude := agentRenameMirrorsClaude(remoteString(msg, "provider"), remoteString(msg, "tool"))
	// One serialized read-modify-write: the inventory ticker and HTTP handlers
	// merge observed renames on other goroutines, and an interleaved whole-file
	// save here used to drop this phone entry permanently (no local re-source).
	persistErr := updateAgentRenameCache(func(entries map[string]agentRenameCacheEntry) bool {
		before, existed := entries[nativeID]
		mergeAgentRenameEntry(entries, nativeID, agentRenameCacheEntry{
			Name:        title,
			Origin:      agentRenameOriginPhone,
			Source:      agentRenameSourcePhone,
			UpdatedAt:   now,
			ProjectPath: strings.TrimSpace(remoteString(msg, "project_path")),
			SkipMirror:  !mirrorsClaude,
		})
		return !existed || entries[nativeID] != before
	})
	if persistErr != nil {
		logger.Warn("[AGENT-RENAME] phone_rename_persist_failed sid=" + nativeID + " error=" + persistErr.Error())
	}
	// Mirror the rename into Claude Code's own transcript so the desktop TUI
	// /resume list shows the phone-chosen name too (its list builder takes the
	// LAST custom-title line per session). Claude-only: codex/opencode
	// transcripts live outside ~/.claude/projects, so mirroring them would be
	// a wasted walk plus a guaranteed misleading transcript_not_found warning.
	// Absent/auto still mirror for backward compatibility with servers that
	// omit provider (same direction as guardExternalTUIClaudeSpawn).
	// Best-effort only: the rename cache above remains the phone-side source
	// of truth, so a failure here is logged and never fails the rename or its
	// ack.
	if mirrorsClaude {
		if home := agentHome(); home != "" {
			if err := appendClaudeCustomTitleLine(filepath.Join(home, ".claude", "projects"), nativeID, title); err != nil {
				if errors.Is(err, errClaudeTranscriptNotFound) {
					logger.Warn("[AGENT-RENAME] transcript_not_found sid=" + nativeID)
				} else {
					logger.Warn("[AGENT-RENAME] transcript_custom_title_failed sid=" + nativeID + " error=" + err.Error())
				}
			}
		}
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

// errClaudeTranscriptNotFound reports that no Claude Code transcript file
// exists on this machine for the renamed session (e.g. a conversation
// imported from another device, or a session id this mirror refuses). It is
// log-only: the agent's rename cache remains the phone-side source of truth
// either way.
var errClaudeTranscriptNotFound = errors.New("claude transcript not found")

// claudeSessionIDPattern is Claude Code's own session-id guard: it refuses to
// write a custom title for anything that is not a strict UUID. Mirroring a
// non-UUID id could glob-match an unrelated transcript, so this mirror
// refuses the same way.
var claudeSessionIDPattern = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// agentRenameMirrorsClaude reports whether a rename event refers to a
// Claude-family session. The provider field is optional on the wire, so only
// an explicit non-Claude provider/tool skips the transcript mirror; absent
// and auto stay conservative and mirror anyway (a mirror failure is
// log-only, so the conservative direction costs nothing).
func agentRenameMirrorsClaude(provider, tool string) bool {
	for _, raw := range []string{provider, tool} {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "codex", "opencode", "agentcore":
			// agentcore 的 native id 形如 agentcore-1792954925284702588-1，
			// 不是 UUID，claudeSessionIDPattern 那一关永远过不了——照 Claude
			// 镜像只会稳定产出一条 transcript_not_found 噪音日志。
			return false
		case "claude", "claudecode", "auto":
			return true
		}
	}
	return true // absent: conservative mirror
}

// claudeCustomTitleRecord is the exact record shape Claude Code writes for
// /rename. Field order matches the native writer so the appended line is
// byte-identical in shape (the parser itself is order-insensitive).
type claudeCustomTitleRecord struct {
	Type        string `json:"type"`
	CustomTitle string `json:"customTitle"`
	SessionID   string `json:"sessionId"`
}

// findAgentTranscriptFile locates <name> under the Claude projects root.
// Claude Code stores transcripts at a fixed two-level layout
// (<root>/<sanitized-project>/<name>), so a per-directory stat probe resolves
// the typical case without a full walk; the bounded recency walk stays as the
// fallback for unexpected nesting (on a huge projects tree a budget
// truncation degrades to not-found rather than a wrong-file write).
func findAgentTranscriptFile(root, name string) string {
	if entries, err := os.ReadDir(root); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(root, entry.Name(), name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	matches := findRecentAgentFiles(root, name, agentVibeDetailCandidateFileLimit)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

// appendClaudeCustomTitleLine mirrors a conversation rename into Claude
// Code's own storage so the desktop TUI /resume list shows the same name as
// the phone. Claude Code records /rename titles in TWO places: one
// {"type":"custom-title",...} line appended to the session's JSONL (its list
// builder takes the LAST such line per session, so "updating the title" is
// implemented strictly as an append, never a rewrite) and a per-session
// sidecar file <transcript dir>/<sessionId>/custom-title.json. The sidecar
// matters because the list builder resolves titles from the transcript's
// first/last 64KB only: once a long turn pushes the custom-title line out of
// the tail window, the sidecar is the only thing keeping the phone title
// visible — Claude Code's own /rename writes both (gB → writeSessionTitleSidecar),
// so the mirror writes both too.
//
// Data-safety contract (the transcript is the user's conversation history):
//   - the file is opened write-only with O_APPEND, never read-modify-written
//     and never truncated — existing bytes cannot be altered;
//   - O_CREATE is never set on the transcript: a missing transcript is
//     reported via errClaudeTranscriptNotFound instead of fabricating a bare
//     custom-title file, which would surface as an empty ghost session in
//     /resume. The sidecar (a separate file Claude Code owns the shape of)
//     IS created when missing, but only after a transcript was found;
//   - if the file does not end in a newline (a torn write by some earlier
//     process), a separator newline is prepended so the existing partial
//     line is not glued to the new record. With a concurrent writer (a live
//     claude session) that check can be stale; the worst outcome is one
//     blank line, which every line-oriented parser on both sides skips;
//   - the record is serialized via json.Encoder with HTML escaping off, so
//     it is byte-identical in shape to claude's own lines; newlines/quotes
//     stay escaped, guaranteeing exactly ONE physical line;
//   - a true I/O error mid-write can leave a torn partial record; this
//     function deliberately does not roll back (truncating would itself be a
//     rewrite) — claude's own /rename has no torn-tail protection either;
//   - root follows the default ~/.claude/projects convention; a custom
//     CLAUDE_CONFIG_DIR environment is not resolved (consistent with the
//     rest of this agent) and shows up as transcript_not_found.
func appendClaudeCustomTitleLine(root, nativeSessionID, title string) error {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	// Normalize on write too (not just at wire ingest): cache entries written
	// before normalization existed, and any non-PATCH wire source, must not
	// leak raw control characters into the transcript mirror.
	title = normalizeAgentRenameTitle(title)
	if strings.TrimSpace(root) == "" || nativeSessionID == "" || title == "" {
		return nil // nothing meaningful to mirror
	}
	if !claudeSessionIDPattern.MatchString(nativeSessionID) {
		return errClaudeTranscriptNotFound
	}
	path := findAgentTranscriptFile(root, nativeSessionID+".jsonl")
	if path == "" {
		return errClaudeTranscriptNotFound
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(claudeCustomTitleRecord{
		Type:        "custom-title",
		CustomTitle: title,
		SessionID:   nativeSessionID,
	}); err != nil {
		return err
	}
	payload := buf.Bytes() // Encoder already terminates the record with '\n'
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		if last, err := readLastByte(path); err == nil && last != '\n' {
			payload = append([]byte{'\n'}, payload...)
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := writeClaudeTitleSidecar(path, nativeSessionID, title); err != nil {
		return err
	}
	return nil
}

// writeClaudeTitleSidecar writes Claude Code's own per-session title sidecar
// (<transcript dir>/<sessionId>/custom-title.json, content {"customTitle":...}
// with no trailing newline, dir 0700 / file 0600 — byte-shaped after CC's
// writeSessionTitleSidecar). CC's /resume title resolution is
// tail-64KB → sidecar → head-64KB, so this file is what keeps the title
// visible after the custom-title JSONL line is evicted from the tail window.
func writeClaudeTitleSidecar(transcriptPath, nativeSessionID, title string) error {
	nativeSessionID = strings.TrimSpace(nativeSessionID)
	title = strings.TrimSpace(title)
	if nativeSessionID == "" || title == "" {
		return nil
	}
	dir := filepath.Join(filepath.Dir(transcriptPath), nativeSessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(claudeTitleSidecarRecord{CustomTitle: title})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "custom-title.json"), raw, 0o600)
}

// claudeTitleSidecarRecord is the sidecar's exact on-disk shape.
type claudeTitleSidecarRecord struct {
	CustomTitle string `json:"customTitle"`
}

// claudeTitleTailWindow mirrors Claude Code's transcript read window (pS=65536):
// the /resume list builder only considers custom-title lines inside the
// transcript's last 64KB, so convergence must judge "what will the desktop
// show" over the same window, not over the whole file.
const claudeTitleTailWindow = 64 * 1024

// lastClaudeCustomTitleInTail returns the customTitle of the LAST
// custom-title line inside the transcript's trailing 64KB window — exactly
// what Claude Code's list builder will resolve for this session. ok=false
// means the window holds no custom-title line (evicted, or never renamed).
func lastClaudeCustomTitleInTail(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() == 0 {
		return "", false
	}
	window := int64(claudeTitleTailWindow)
	if window > info.Size() {
		window = info.Size()
	}
	buf := make([]byte, window)
	if _, err := f.ReadAt(buf, info.Size()-window); err != nil {
		return "", false
	}
	last, found := "", false
	for _, line := range bytes.Split(buf, []byte("\n")) {
		if !bytes.Contains(line, []byte(`"type":"custom-title"`)) {
			continue
		}
		var record claudeCustomTitleRecord
		if err := json.Unmarshal(line, &record); err == nil && record.CustomTitle != "" {
			last, found = record.CustomTitle, true
		}
	}
	return last, found
}

// convergePhoneRenameMirrors repairs the DESKTOP mirror of phone renames.
// handleRemoteAIRename appends the custom-title line + sidecar once, but a
// live Claude Code process keeps re-appending ITS in-memory (old) title every
// ~32KB of writes, and on exit re-appends whatever old title still sits in
// the tail window — so after the phone line is evicted from the last 64KB,
// the desktop list can settle back on the old title indefinitely. This pass
// walks the phone-origin entries of the durable cache and, once no live
// process owns the session's tail, re-appends the phone title (and refreshes
// the sidecar) whenever the tail window no longer resolves to it. Local
// /rename titles need no such repair: they originate from Claude Code itself
// and it keeps mirroring them.
func convergePhoneRenameMirrors(home string, records map[string]agentRenamePidRecord) {
	home = strings.TrimSpace(home)
	if home == "" {
		return
	}
	root := filepath.Join(home, ".claude", "projects")
	for sid, entry := range loadAgentRenameCache() {
		if entry.Origin != agentRenameOriginPhone || strings.TrimSpace(entry.Name) == "" {
			continue
		}
		if entry.SkipMirror {
			continue // codex/opencode sessions: the mirror was skipped at rename time
		}
		// A live TUI owns the tail right now (it re-appends its in-memory
		// title on every ~32KB); re-appending under it would start a ping-pong
		// of alternating title lines. Wait for the process to exit.
		if record, ok := records[sid]; ok && isPidAlive(record.PID) {
			continue
		}
		if !claudeSessionIDPattern.MatchString(sid) {
			continue
		}
		transcript := findAgentTranscriptFile(root, sid+".jsonl")
		if transcript == "" {
			continue
		}
		if tail, ok := lastClaudeCustomTitleInTail(transcript); ok && tail == normalizeAgentRenameTitle(entry.Name) {
			continue
		}
		if err := appendClaudeCustomTitleLine(root, sid, entry.Name); err != nil {
			logger.Warn("[AGENT-RENAME] converge_mirror_failed sid=" + sid + " error=" + err.Error())
		}
	}
}

func readLastByte(path string) (byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, info.Size()-1); err != nil {
		return 0, err
	}
	return buf[0], nil
}
