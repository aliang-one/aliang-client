package services

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func resetAgentProjectProbeCache(t *testing.T) {
	t.Helper()
	agentProjectProbeMu.Lock()
	agentProjectProbes = make(map[string]agentProjectProbe)
	agentProjectProbeMu.Unlock()
}

// writeFakeProject creates a directory that looks like a git Go project. Any
// code that touches it will observe .git/HEAD and go.mod.
func writeFakeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Privacy regression: background inventory (connect, ~60s tick, digest change)
// must not read project folders. macOS raises "Files & Folders" prompts when a
// process touches Desktop/Documents/Downloads/iCloud, and a background root
// daemon has no user action to justify them.
func TestCollectAgentProjects_DoesNotProbeDisk(t *testing.T) {
	dir := writeFakeProject(t)
	projects := collectAgentProjects([]models.AgentVibeSession{{
		ID:          "s1",
		ProjectPath: dir,
		UpdatedAt:   "2026-10-27T00:00:00Z",
		Tool:        "claude",
	}})
	if len(projects) != 1 {
		t.Fatalf("collectAgentProjects returned %d projects, want 1", len(projects))
	}
	project := projects[0]
	if project.IsGitRepo {
		t.Error("background inventory set IsGitRepo — it read the project folder")
	}
	if project.FileCount != 0 || project.GitChangedCount != 0 {
		t.Errorf("background inventory set FileCount=%d GitChangedCount=%d, want 0/0",
			project.FileCount, project.GitChangedCount)
	}
	if project.Language != "" || project.PackageManager != "" {
		t.Errorf("background inventory set Language=%q PackageManager=%q, want empty",
			project.Language, project.PackageManager)
	}
	if project.Description != "" {
		t.Errorf("background inventory set Description=%q — it stat'ed the project path", project.Description)
	}
}

func TestUpsertActiveAgentProject_DoesNotProbeDisk(t *testing.T) {
	dir := writeFakeProject(t)
	projects := upsertActiveAgentProject(nil, models.AgentVibeSession{
		ID:          "s1",
		ProjectPath: dir,
		UpdatedAt:   "2026-10-27T00:00:00Z",
	})
	if len(projects) != 1 {
		t.Fatalf("upsertActiveAgentProject returned %d projects, want 1", len(projects))
	}
	if projects[0].IsGitRepo || projects[0].Language != "" {
		t.Errorf("active-session overlay probed disk: IsGitRepo=%v Language=%q",
			projects[0].IsGitRepo, projects[0].Language)
	}
}

// The user-initiated probe is the only disk reader; its results must replay
// through enrichAgentProject afterwards.
func TestProbeAgentProjectStats_FeedsReplay(t *testing.T) {
	resetAgentProjectProbeCache(t)
	dir := writeFakeProject(t)

	probeAgentProjectStats(dir)

	project := models.AgentProject{Path: dir}
	enrichAgentProject(&project)
	if !project.IsGitRepo {
		t.Error("replay lost IsGitRepo")
	}
	if project.Branch != "main" {
		t.Errorf("replay Branch = %q, want %q", project.Branch, "main")
	}
	if project.Language != "Go" {
		t.Errorf("replay Language = %q, want %q", project.Language, "Go")
	}
	if project.PackageManager != "go" {
		t.Errorf("replay PackageManager = %q, want %q", project.PackageManager, "go")
	}
	if project.Description == "" {
		t.Error("replay lost Description")
	}
}

// A project the user never opened has no cached probe, so enrichment must stay
// empty rather than falling back to disk.
func TestEnrichAgentProject_NoCacheNoDisk(t *testing.T) {
	resetAgentProjectProbeCache(t)
	dir := writeFakeProject(t)
	project := models.AgentProject{Path: dir}
	enrichAgentProject(&project)
	if project.IsGitRepo || project.Language != "" || project.Description != "" {
		t.Errorf("enrichAgentProject probed disk without a cached probe: %+v", project)
	}
}

func TestAgentProjectProbeCache_Bounded(t *testing.T) {
	resetAgentProjectProbeCache(t)
	base := t.TempDir()
	paths := make([]string, 0, agentProjectProbeMaxEntries+10)
	for i := 0; i < agentProjectProbeMaxEntries+10; i++ {
		path := filepath.Join(base, "proj", strconv.Itoa(i))
		paths = append(paths, path)
		probeAgentProjectStats(path)
	}

	agentProjectProbeMu.RLock()
	size := len(agentProjectProbes)
	_, oldestPresent := agentProjectProbes[paths[0]]
	_, newestPresent := agentProjectProbes[paths[len(paths)-1]]
	agentProjectProbeMu.RUnlock()

	if size != agentProjectProbeMaxEntries {
		t.Errorf("probe cache size = %d, want %d", size, agentProjectProbeMaxEntries)
	}
	if oldestPresent {
		t.Error("oldest probe entry was not evicted")
	}
	if !newestPresent {
		t.Error("newest probe entry missing")
	}
}
