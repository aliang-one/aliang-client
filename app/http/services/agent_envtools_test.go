package services

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// authorizeEnvToolDir registers a temp dir as an authorized project dir so the
// cwd gate (resolveAgentProjectPath -> agentAuthorizedExecutionDirectories) lets it through.
func authorizeEnvToolDir(t *testing.T, dir string) {
	t.Helper()
	setAgentAuthorizedExecutionDirectoriesCache([]string{dir})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })
}

func skipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func TestAgentGitStatusPayload_OnRepo(t *testing.T) {
	skipIfNoGit(t)
	dir := t.TempDir()
	authorizeEnvToolDir(t, dir)

	// Initialize a repo, create a commit so HEAD resolves to a real branch.
	if out, err := exec.Command("git", "-C", dir, "init", "--initial-branch=main").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	if err := os.WriteFile(dir+"/hello.txt", []byte("hi"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "hello.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	// git commit needs an identity; set a local one for the test repo.
	if out, err := exec.Command("git", "-C", dir, "config", "user.email", "test@example.com").CombinedOutput(); err != nil {
		t.Fatalf("git config user.email failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config user.name failed: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "commit", "-m", "seed").CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %v\n%s", err, out)
	}
	// Now add a second (uncommitted) file so `status --short` is non-empty.
	if err := os.WriteFile(dir+"/world.txt", []byte("yo"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	msg := map[string]interface{}{"request_id": "r1", "cwd": dir}
	result := agentGitStatusPayload(msg)

	if got := result["type"]; got != models.AgentEventGitStatusResult {
		t.Fatalf("type = %v, want %s", got, models.AgentEventGitStatusResult)
	}
	if got := result["request_id"]; got != "r1" {
		t.Fatalf("request_id = %v, want r1", got)
	}
	if isRepo, _ := result["is_repo"].(bool); !isRepo {
		t.Fatalf("is_repo = false, want true")
	}
	branch, _ := result["branch"].(string)
	if strings.TrimSpace(branch) == "" {
		t.Fatalf("branch is empty, want non-empty (got %#v)", result["branch"])
	}
	if branch != "main" {
		t.Logf("note: branch=%q (some git versions default differently)", branch)
	}
	// status should mention the untracked file.
	status, _ := result["status"].(string)
	if !strings.Contains(status, "world.txt") {
		t.Fatalf("status does not mention world.txt: %q", status)
	}
}

func TestAgentGitStatusPayload_NonRepo(t *testing.T) {
	dir := t.TempDir()
	authorizeEnvToolDir(t, dir)

	msg := map[string]interface{}{"request_id": "r2", "cwd": dir}
	result := agentGitStatusPayload(msg)

	if got := result["type"]; got != models.AgentEventGitStatusResult {
		t.Fatalf("type = %v, want %s", got, models.AgentEventGitStatusResult)
	}
	if isRepo, _ := result["is_repo"].(bool); isRepo {
		t.Fatalf("is_repo = true on a plain temp dir, want false")
	}
	if status, _ := result["status"].(string); status != "" {
		t.Fatalf("status = %q on non-repo, want empty", status)
	}
}

func TestAgentGitStatusPayload_UnauthorizedCwd(t *testing.T) {
	// A path that does not exist fails at cleanExistingAgentDirectory before any
	// authorization-cache lookup, deterministically yielding the per-type error
	// regardless of device sync state.
	missing := "/definitely/not/a/real/project/path/xyz-12345"

	msg := map[string]interface{}{"request_id": "r3", "cwd": missing}
	result := agentGitStatusPayload(msg)

	if got := result["type"]; got != models.AgentEventGitStatusError {
		t.Fatalf("type = %v, want %s", got, models.AgentEventGitStatusError)
	}
	if got := result["request_id"]; got != "r3" {
		t.Fatalf("request_id = %v, want r3", got)
	}
	if errStr, _ := result["error"].(string); errStr == "" {
		t.Fatalf("error is empty, want non-empty authorization error")
	}
}

func TestAgentEnvInfoPayload_WhitelistedKeysOnly(t *testing.T) {
	dir := t.TempDir()
	authorizeEnvToolDir(t, dir)

	msg := map[string]interface{}{"request_id": "r4", "cwd": dir}
	result := agentEnvInfoPayload(msg)

	if got := result["type"]; got != models.AgentEventEnvInfoResult {
		t.Fatalf("type = %v, want %s", got, models.AgentEventEnvInfoResult)
	}
	if got := result["request_id"]; got != "r4" {
		t.Fatalf("request_id = %v, want r4", got)
	}
	if osName, _ := result["os"].(string); osName == "" {
		t.Fatalf("os is empty, want non-empty")
	}
	if osName, _ := result["os"].(string); osName != runtime.GOOS {
		t.Fatalf("os = %q, want %q", osName, runtime.GOOS)
	}

	// Security invariant: no env dump may leak. Only whitelisted keys allowed.
	allowed := map[string]bool{
		"type":         true,
		"request_id":   true,
		"os":           true,
		"arch":         true,
		"shell":        true,
		"user":         true,
		"versions":     true,
		"generated_at": true,
	}
	for key := range result {
		if !allowed[key] {
			t.Fatalf("env.info result leaked unexpected key %q (full map: %#v)", key, result)
		}
	}
	// versions must be a map of whitelisted tool probes, never an env dump.
	versions, ok := result["versions"].(map[string]string)
	if !ok {
		t.Fatalf("versions is %T, want map[string]string", result["versions"])
	}
	for _, banned := range []string{"PATH", "HOME", "SECRET", "TOKEN", "PASSWORD"} {
		for vk, vv := range versions {
			if strings.Contains(strings.ToUpper(vk), banned) || strings.Contains(strings.ToUpper(vv), banned) {
				t.Fatalf("versions leaked env-like value %q=%q", vk, vv)
			}
		}
	}
}

func TestTruncateLines(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"empty", "", 5, ""},
		{"under_limit", "a\nb", 5, "a\nb"},
		{"exact_limit", "a\nb\nc", 3, "a\nb\nc"},
		{"trailing_newline_kept_when_under", "a\nb\n", 5, "a\nb\n"},
		{"truncate", "a\nb\nc\nd\ne\nf", 3, "a\nb\nc\n... (truncated)"},
		{"zero_max", "a\nb", 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := truncateLines(c.in, c.max); got != c.want {
				t.Errorf("truncateLines(%q,%d) = %q, want %q", c.in, c.max, got, c.want)
			}
		})
	}
}

func TestVersionProbe_MissingBinary(t *testing.T) {
	got := versionProbe("definitely-not-a-real-binary-xyz", "--version")
	if got != "" {
		t.Fatalf("versionProbe on missing binary = %q, want empty string", got)
	}
}
