package services

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestResolveEnvDuration covers the env-to-duration resolution used by the
// approval-timeout and hard-ceiling knobs.
func TestResolveEnvDuration(t *testing.T) {
	const key = "ALIANG_TEST_RESOLVE_DURATION"
	cases := []struct {
		name string
		env  string
		def  time.Duration
		want time.Duration
	}{
		{"unset returns default", "", 24 * time.Hour, 24 * time.Hour},
		{"blank returns default", "   ", 24 * time.Hour, 24 * time.Hour},
		{"valid minutes parsed", "30m", 24 * time.Hour, 30 * time.Minute},
		{"valid hours parsed", "24h", 10 * time.Minute, 24 * time.Hour},
		{"valid compound duration", "1h30m", time.Hour, 90 * time.Minute},
		{"unparseable returns default", "garbage", 48 * time.Hour, 48 * time.Hour},
		{"zero returns default", "0s", 24 * time.Hour, 24 * time.Hour},
		{"negative returns default", "-5m", 24 * time.Hour, 24 * time.Hour},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(key, test.env)
			if got := resolveEnvDuration(key, test.def); got != test.want {
				t.Fatalf("resolveEnvDuration(%q, %s) with env %q = %s, want %s", key, test.def, test.env, got, test.want)
			}
		})
	}
}

func TestAgentAITimeoutDefaults(t *testing.T) {
	if value := os.Getenv("ALIANG_AI_APPROVAL_TIMEOUT"); value != "" {
		t.Skipf("ALIANG_AI_APPROVAL_TIMEOUT=%s set at start; skipping default assertion", value)
	}
	if value := os.Getenv("ALIANG_AI_HARD_CEILING"); value != "" {
		t.Skipf("ALIANG_AI_HARD_CEILING=%s set at start; skipping default assertion", value)
	}
	if agentAIApprovalTimeout != 24*time.Hour {
		t.Fatalf("default agentAIApprovalTimeout = %s, want 24h", agentAIApprovalTimeout)
	}
	if agentAIHardCeiling != 48*time.Hour {
		t.Fatalf("default agentAIHardCeiling = %s, want 48h", agentAIHardCeiling)
	}
	if agentAIHardCeiling <= agentAIApprovalTimeout {
		t.Fatalf("agentAIHardCeiling (%s) must exceed agentAIApprovalTimeout (%s)", agentAIHardCeiling, agentAIApprovalTimeout)
	}
}

func TestResolveAgentAuthorizedCWDConfinesExecution(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	setAgentAuthorizedExecutionDirectoriesCache([]string{root})
	t.Cleanup(func() { setAgentAuthorizedExecutionDirectoriesCache(nil) })

	resolved, err := resolveAgentAuthorizedCWD(child, "working directory")
	want, cleanErr := cleanExistingAgentDirectory(child)
	if cleanErr != nil {
		t.Fatal(cleanErr)
	}
	if err != nil || resolved != want {
		t.Fatalf("authorized child = %q, %v", resolved, err)
	}
	outside := t.TempDir()
	if _, err := resolveAgentAuthorizedCWD(outside, "working directory"); err == nil {
		t.Fatal("outside working directory should be rejected")
	}

	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := resolveAgentAuthorizedCWD(link, "working directory"); err == nil {
			t.Fatal("symlink escaping the authorized root should be rejected")
		}
	}
}
