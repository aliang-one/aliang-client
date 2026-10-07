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

func TestClaudeApprovalHookSettingsCarriesAuthEnvFallback(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	forceAuthEnvMissing(t)

	run := agentAIRun{
		sessionID:     "s-auth",
		messageID:     "m-auth",
		approvalToken: "token",
		claudePolicy:  parseAgentAIClaudeRemotePolicy(map[string]interface{}{"claude_remote_policy": testClaudeRemotePolicy(false)}),
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPreToolUseCommand, run)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Env   map[string]string      `json:"env"`
		Hooks map[string]interface{} `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Env["ANTHROPIC_AUTH_TOKEN"] != "settings-token" || decoded.Env["ANTHROPIC_BASE_URL"] != "https://settings.example" {
		t.Fatalf("--settings blob lacks the auth env fallback: env=%s", decoded.Env)
	}
	if decoded.Hooks == nil {
		t.Fatal("auth overlay must not displace the approval hooks")
	}
}

func TestClaudeApprovalHookSettingsOmitsAuthEnvWhenEnvCarriesIt(t *testing.T) {
	writeClaudeAuthEnvFixture(t, claudeAuthEnvSettingsFixture)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://env.example")

	run := agentAIRun{
		sessionID:     "s-auth2",
		messageID:     "m-auth2",
		approvalToken: "token",
	}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPreToolUseCommand, run)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := settings["env"]; exists {
		t.Fatalf("child env already carries both auth keys; blob must not duplicate: %v", settings["env"])
	}
}
