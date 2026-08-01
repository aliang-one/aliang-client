package services

import (
	"fmt"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// mkVibe builds a test session; smaller idx = newer (sorts first by UpdatedAt desc).
func mkVibe(id string, idx int) models.AgentVibeSession {
	return models.AgentVibeSession{ID: id, UpdatedAt: fmt.Sprintf("2026-08-01T00:00:%02dZ", idx)}
}

func TestExcludeInternalVibeSessions(t *testing.T) {
	internal := map[string]bool{"codex_internal-1": true, "claude_internal-2": true}
	in := []models.AgentVibeSession{
		mkVibe("codex_internal-1", 1),
		mkVibe("claude_internal-2", 2),
		mkVibe("codex_real-3", 3),
		mkVibe("claude_real-4", 4),
	}
	got := excludeInternalVibeSessions(in, internal)
	gotIDs := map[string]bool{}
	for _, s := range got {
		gotIDs[s.ID] = true
	}
	if gotIDs["codex_internal-1"] || gotIDs["claude_internal-2"] {
		t.Errorf("internal 未被过滤: %v", gotIDs)
	}
	if !gotIDs["codex_real-3"] || !gotIDs["claude_real-4"] {
		t.Errorf("真实 session 被误删: %v", gotIDs)
	}
}

// vibe: filter-then-truncate (internal don't occupy 200 slots); projectsSource: unfiltered top-200 (keeps internal → authorization preserved).
func TestPartitionAgentVibeSessionsFilterBeforeTruncateAndProjectsUnfiltered(t *testing.T) {
	internal := map[string]bool{"codex_planner-A": true, "claude_planner-B": true}
	// full already sorted by UpdatedAt desc: 2 internal first, then 200 real.
	full := make([]models.AgentVibeSession, 0, 202)
	full = append(full, mkVibe("codex_planner-A", 0), mkVibe("claude_planner-B", 1))
	for i := 0; i < 200; i++ {
		full = append(full, mkVibe(fmt.Sprintf("real-%d", i), 2+i))
	}

	projectsSource, vibe := partitionAgentVibeSessions(full, internal)

	// vibe: no internal, still 200 (internal didn't steal slots).
	if len(vibe) != agentVibeSummaryMaxSessions {
		t.Fatalf("vibe len = %d, want %d", len(vibe), agentVibeSummaryMaxSessions)
	}
	vibeIDs := map[string]bool{}
	for _, s := range vibe {
		vibeIDs[s.ID] = true
	}
	if vibeIDs["codex_planner-A"] || vibeIDs["claude_planner-B"] {
		t.Errorf("vibe 不应含 internal: %+v", vibeIDs)
	}
	if !vibeIDs["real-0"] || !vibeIDs["real-199"] {
		t.Errorf("vibe 应含全部 200 真实: 缺 real-0/real-199")
	}
	// projectsSource: top 200, STILL includes internal.
	if len(projectsSource) != agentVibeSummaryMaxSessions {
		t.Fatalf("projectsSource len = %d, want %d", len(projectsSource), agentVibeSummaryMaxSessions)
	}
	psIDs := map[string]bool{}
	for _, s := range projectsSource {
		psIDs[s.ID] = true
	}
	if !psIDs["codex_planner-A"] || !psIDs["claude_planner-B"] {
		t.Errorf("projectsSource 应含 internal（项目授权从未过滤派生）: %+v", psIDs)
	}
}
