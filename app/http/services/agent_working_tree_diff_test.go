package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestParseGitDiffFiles(t *testing.T) {
	const sample = "diff --git a/foo.ts b/foo.ts\n" +
		"index 111..222 100644\n" +
		"--- a/foo.ts\n" +
		"+++ b/foo.ts\n" +
		"@@ -1,2 +1,2 @@\n" +
		" keep\n" +
		"-old\n" +
		"+new\n" +
		"diff --git a/gone.ts b/gone.ts\n" +
		"deleted file mode 100644\n" +
		"--- a/gone.ts\n" +
		"+++ /dev/null\n" +
		"@@ -1,1 +0,0 @@\n" +
		"-deleted\n"
	files := parseGitDiffFiles(sample)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].relPath != "foo.ts" || files[0].status != "modified" {
		t.Errorf("file0: got %q/%q, want foo.ts/modified", files[0].relPath, files[0].status)
	}
	if files[1].relPath != "gone.ts" || files[1].status != "deleted" {
		t.Errorf("file1: got %q/%q, want gone.ts/deleted", files[1].relPath, files[1].status)
	}
	// modified chunk keeps its -old/+new lines for the viewer to parse
	if !strings.Contains(files[0].diff, "+new") || !strings.Contains(files[0].diff, "-old") {
		t.Errorf("modified diff lost its +/- lines: %q", files[0].diff)
	}
}

func TestGitDiffFilePathAndStatus(t *testing.T) {
	cases := []struct{ chunk, wantPath, wantStatus string }{
		{"diff --git a/a.ts b/a.ts\n--- a/a.ts\n+++ b/a.ts\n@@ .. @@\n x\n", "a.ts", "modified"},
		{"diff --git a/n.ts b/n.ts\nnew file mode 100644\n--- /dev/null\n+++ b/n.ts\n@@ .. @@\n+n\n", "n.ts", "added"},
		{"diff --git a/d.ts b/d.ts\ndeleted file mode 100644\n--- a/d.ts\n+++ /dev/null\n@@ .. @@\n-d\n", "d.ts", "deleted"},
	}
	for _, c := range cases {
		path, status := gitDiffFilePathAndStatus(c.chunk)
		if path != c.wantPath || status != c.wantStatus {
			t.Errorf("got %q/%q, want %q/%q", path, status, c.wantPath, c.wantStatus)
		}
	}
}

func TestSynthesizeAddedDiff(t *testing.T) {
	diff := synthesizeAddedDiff("new.ts", "a\nb\nc\n")
	if !strings.Contains(diff, "+++ b/new.ts") {
		t.Errorf("missing new-file header: %q", diff)
	}
	// three added lines
	if strings.Count(diff, "\n+a") != 1 || strings.Count(diff, "\n+b") != 1 || strings.Count(diff, "\n+c") != 1 {
		t.Errorf("expected one +a/+b/+c each, got: %q", diff)
	}
}

func TestWorkingTreeDiffEntries(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	mustWrite := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("keep.ts", "keep\n")
	mustWrite("gone.ts", "gone\n")
	run("add", ".")
	run("commit", "-m", "init")

	// working-tree changes: modify keep.ts, delete gone.ts, add untracked new.ts
	mustWrite("keep.ts", "keep\nchanged\n")
	run("rm", "--quiet", "gone.ts")
	mustWrite("new.ts", "brand\nnew\n")

	entries := workingTreeDiffEntries(dir)
	byPath := map[string]map[string]interface{}{}
	for _, e := range entries {
		byPath[e["path"].(string)] = e
	}
	keep := filepath.Join(dir, "keep.ts")
	gone := filepath.Join(dir, "gone.ts")
	newf := filepath.Join(dir, "new.ts")
	if byPath[keep]["status"] != "modified" {
		t.Errorf("keep.ts status: got %v, want modified", byPath[keep]["status"])
	}
	if byPath[gone]["status"] != "deleted" {
		t.Errorf("gone.ts status: got %v, want deleted", byPath[gone]["status"])
	}
	if byPath[newf]["status"] != "added" {
		t.Errorf("new.ts status: got %v, want added", byPath[newf]["status"])
	}
	if !strings.Contains(byPath[newf]["diff"].(string), "+brand") {
		t.Errorf("new.ts diff should include +brand: %q", byPath[newf]["diff"])
	}
	// non-git dir → empty
	if got := workingTreeDiffEntries(t.TempDir()); len(got) != 0 {
		t.Errorf("non-git dir: got %d entries, want 0", len(got))
	}
}

// TestHandleRemoteAgentMessage_RoutesWorkingTreeDiff pins the dispatch wiring:
// an inbound `file.working_tree_diff` request from the server must reach
// handleAgentDetailMessage and return a `file.working_tree_diff.result`. Before
// the fix, agent_remote_ws.go's detail dispatch case omitted this type, so the
// request was silently dropped → server 12s timeout → phone "加载改动失败".
func TestHandleRemoteAgentMessage_RoutesWorkingTreeDiff(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "keep.ts"), []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-m", "init")
	// uncommitted modification → non-empty working-tree diff
	if err := os.WriteFile(filepath.Join(dir, "keep.ts"), []byte("keep\nchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	authorizeEnvToolDir(t, dir)

	svc := &AgentService{terminal: newAgentTerminalManager(), ai: newAgentAIManager()}
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.Registered = true
	svc.mu.Unlock()
	defer svc.ai.closeAll()

	var mu sync.Mutex
	var events []map[string]interface{}
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			return nil
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":         "file.working_tree_diff",
		"request_id":   "req_wtd_dispatch",
		"project_path": dir,
	}, writeJSON)

	result := waitForAgentEvent(t, &mu, &events, "file.working_tree_diff.result", func(e map[string]interface{}) bool {
		return e["request_id"] == "req_wtd_dispatch"
	})
	entries, _ := result["entries"].([]map[string]interface{})
	if len(entries) == 0 {
		t.Fatalf("expected non-empty entries for an uncommitted change, got 0 (result=%#v)", result)
	}
}
