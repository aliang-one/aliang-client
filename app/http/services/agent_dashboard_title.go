package services

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

func (s *AgentService) renameDashboardSession(msg map[string]interface{}, writeJSON agentTerminalWriter) {
	id := remoteString(msg, "session_id")
	rawTitle := remoteString(msg, "title")
	ack := map[string]interface{}{
		"type": models.AgentEventAIRenameAck, "session_id": id,
		"request_id": remoteString(msg, "request_id"), "accepted": false,
	}
	if len(rawTitle) > 4096 || !dashboardRenameJournalExists(id) {
		_ = writeJSON(ack)
		return
	}
	title := normalizeAgentRenameTitle(rawTitle)
	if title == "" {
		_ = writeJSON(ack)
		return
	}
	var current agentRenameCacheEntry
	err := updateAgentRenameCache(func(entries map[string]agentRenameCacheEntry) bool {
		existing := entries[id]
		ts := time.Now().UTC()
		if previous := renameTimestamp(existing.UpdatedAt); !ts.After(previous) {
			ts = previous.Add(time.Nanosecond)
		}
		current = agentRenameCacheEntry{
			Name: title, Origin: agentRenameOriginLocal, Source: "user",
			UpdatedAt: ts.Format(time.RFC3339Nano), SkipMirror: true,
		}
		mergeAgentRenameEntry(entries, id, current)
		return true
	})
	if err != nil {
		logger.Warn("[AGENT-RENAME] dashboard title cache write failed: " + err.Error())
		_ = writeJSON(ack)
		return
	}
	update := dashboardSessionTitleEvent(id, current.Name, current.UpdatedAt)
	ack["accepted"] = true
	ack["title"] = current.Name
	ack["title_updated_at"] = current.UpdatedAt
	_ = writeJSON(ack)
	s.ai.broadcastLocalTitle(update)
	// Inventory already reads the durable cache. It provides recovery if the
	// current cloud connection is absent or the single-frame publish fails.
	if writer := s.currentRemoteWriter(); writer != nil {
		if err := writer(update); err != nil {
			logger.Debug("[AGENT-RENAME] dashboard title will sync with inventory: " + err.Error())
		}
	}
}

func dashboardSessionTitleEvent(id, title, stamp string) map[string]interface{} {
	return map[string]interface{}{
		"type": models.AgentEventAISessionTitleUpdated, "session_id": id,
		"title": title, "title_updated_at": stamp,
	}
}

// The dashboard shows AliangCode journals. Validate the exact header, with a
// bounded first-line read, before accepting a browser-supplied session ID.
func dashboardRenameJournalExists(id string) bool {
	root := agentCoreJournalDir()
	if root == "" || id == "" || strings.HasPrefix(id, "goal_") || agentCoreJournalFileName(id) != id+".jsonl" {
		return false
	}
	file, err := os.Open(filepath.Join(root, agentCoreJournalFileName(id)))
	if err != nil {
		return false
	}
	defer file.Close()
	line, err := bufio.NewReaderSize(file, 64*1024).ReadSlice('\n')
	if err != nil {
		return false
	}
	var header struct {
		Kind string `json:"kind"`
		ID   string `json:"session_id"`
	}
	return json.Unmarshal(line, &header) == nil && header.Kind == "session" && header.ID == id
}
