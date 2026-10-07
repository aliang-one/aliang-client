package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Fixture mirroring a user's ~/.claude/settings.json: the env block is the
// canonical location of the gateway auth keys (see quick_setup migration
// fa84f42). A third, non-auth key proves the overlay copies only the two
// ANTHROPIC auth keys and nothing else.
const claudeAuthEnvSettingsFixture = `{"env":{"ANTHROPIC_AUTH_TOKEN":"settings-token","ANTHROPIC_BASE_URL":"https://settings.example","ANTHROPIC_DEFAULT_HAIKU_MODEL":"unrelated"}}`

// writeClaudeAuthEnvFixture points EffectiveAgentHome (non-root ⇒ $HOME) at a
// temp dir with an optional .claude/settings.json and returns the home path.
func writeClaudeAuthEnvFixture(t *testing.T, settingsJSON string) string {
	t.Helper()
	home := t.TempDir()
	if settingsJSON != "" {
		dir := filepath.Join(home, ".claude")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	return home
}

// forceAuthEnvMissing emulates a GUI-spawned agent process: no shell-profile
// exports, so neither auth key is present in the effective child environment.
func forceAuthEnvMissing(t *testing.T) {
	t.Helper()
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_BASE_URL", "")
}

func TestClaudeAuthEnvOverlayInjectsMissingAuthKeys(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	forceAuthEnvMissing(t)

	overlay := claudeAuthEnvOverlay()
	if overlay["ANTHROPIC_AUTH_TOKEN"] != "settings-token" {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN = %q, want the settings.json value", overlay["ANTHROPIC_AUTH_TOKEN"])
	}
	if overlay["ANTHROPIC_BASE_URL"] != "https://settings.example" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want the settings.json value", overlay["ANTHROPIC_BASE_URL"])
	}
	if _, has := overlay["ANTHROPIC_DEFAULT_HAIKU_MODEL"]; has {
		t.Fatalf("overlay must copy only the two auth keys, got %v", overlay)
	}
}

func TestClaudeAuthEnvOverlaySkipsKeysPresentInEnv(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("ANTHROPIC_BASE_URL", "")

	overlay := claudeAuthEnvOverlay()
	if _, has := overlay["ANTHROPIC_AUTH_TOKEN"]; has {
		t.Fatalf("env-provided token must win; overlay would shadow it: %v", overlay)
	}
	if overlay["ANTHROPIC_BASE_URL"] != "https://settings.example" {
		t.Fatalf("ANTHROPIC_BASE_URL = %q, want the settings.json fallback", overlay["ANTHROPIC_BASE_URL"])
	}
}

func TestClaudeAuthEnvOverlayToleratesMissingOrBrokenSettings(t *testing.T) {
	forceAuthEnvMissing(t)

	writeClaudeAuthEnvFixture(t, "") // no settings file at all
	if overlay := claudeAuthEnvOverlay(); overlay != nil {
		t.Fatalf("absent settings must yield no overlay, got %v", overlay)
	}

	writeClaudeAuthEnvFixture(t, "{not json")
	if overlay := claudeAuthEnvOverlay(); overlay != nil {
		t.Fatalf("unparseable settings must yield no overlay, got %v", overlay)
	}
}

// The auth overlay must ride the child environment (cmd.Env), never argv:
// a --settings JSON blob would expose the token in `ps`-visible command lines.
func TestWithClaudeApprovalHookInjectsAuthEnvIntoChildEnv(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	forceAuthEnvMissing(t)

	run := agentAIRun{
		sessionID:     "s-auth",
		messageID:     "m-auth",
		approvalToken: "token",
		claudePolicy:  parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": testClaudeRemotePolicy(false)}),
	}
	got := withClaudeApprovalHook(&agentAITool{path: "/bin/claude", args: []string{"--print", "prompt"}}, run)
	if got == nil {
		t.Fatal("withClaudeApprovalHook returned nil")
	}

	env := envSliceToMap(t, got.env)
	if env["ANTHROPIC_AUTH_TOKEN"] != "settings-token" {
		t.Fatalf("child env ANTHROPIC_AUTH_TOKEN = %q, want the settings.json value", env["ANTHROPIC_AUTH_TOKEN"])
	}
	if env["ANTHROPIC_BASE_URL"] != "https://settings.example" {
		t.Fatalf("child env ANTHROPIC_BASE_URL = %q, want the settings.json value", env["ANTHROPIC_BASE_URL"])
	}
	if n := countEnvKey(got.env, "ANTHROPIC_AUTH_TOKEN"); n != 1 {
		t.Fatalf("ANTHROPIC_AUTH_TOKEN appears %d times in child env, want exactly 1", n)
	}

	// Isolation flags stay, and the --settings blob must remain auth-free.
	settingsJSON := ""
	for i, arg := range got.args {
		if arg == "--settings" && i+1 < len(got.args) {
			settingsJSON = got.args[i+1]
		}
	}
	if settingsJSON == "" {
		t.Fatal("--settings blob missing from args")
	}
	var decoded struct {
		Env   map[string]string      `json:"env"`
		Hooks map[string]interface{} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(settingsJSON), &decoded); err != nil {
		t.Fatalf("unparseable --settings blob: %v (%s)", err, settingsJSON)
	}
	if len(decoded.Env) != 0 {
		t.Fatalf("auth keys must not travel in the --settings blob (argv leak): env=%v", decoded.Env)
	}
	if decoded.Hooks == nil {
		t.Fatal("approval hooks missing from --settings blob")
	}
	if !hasArgPair(got.args, "--setting-sources", "") {
		t.Fatalf("isolated --setting-sources flag lost: %v", got.args)
	}
}

func TestWithClaudeApprovalHookOmitsAuthEnvWhenEnvCarriesIt(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example")

	run := agentAIRun{
		sessionID:     "s-auth2",
		messageID:     "m-auth2",
		approvalToken: "token",
	}
	got := withClaudeApprovalHook(&agentAITool{path: "/bin/claude", args: []string{"--print", "prompt"}}, run)
	if got == nil {
		t.Fatal("withClaudeApprovalHook returned nil")
	}
	if n := countEnvKey(got.env, "ANTHROPIC_AUTH_TOKEN"); n != 0 {
		t.Fatalf("env already carries the token; overlay must not duplicate it (got %d entries)", n)
	}
}

func hasArgPair(args []string, flag, value string) bool {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return true
		}
	}
	return false
}
