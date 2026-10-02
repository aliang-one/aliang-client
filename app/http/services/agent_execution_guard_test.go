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

// TestEnvMiB covers the env-to-MiB byte resolution used by the terminal quota
// knobs, including the int64 overflow guard: a MiB count whose byte conversion
// would wrap negative must fall back to the default instead of producing a
// negative cap that kills a session on its first byte.
func TestEnvMiB(t *testing.T) {
	const key = "ALIANG_TEST_ENV_MIB"
	cases := []struct {
		name string
		env  string
		def  int64
		want int64
	}{
		{"unset returns default", "", 128, 128 << 20},
		{"blank returns default", "   ", 128, 128 << 20},
		{"valid value parsed to bytes", "64", 128, 64 << 20},
		{"unparseable returns default", "garbage", 512, 512 << 20},
		{"zero returns default", "0", 128, 128 << 20},
		{"negative returns default", "-5", 128, 128 << 20},
		{"max int64 overflows to default", "9223372036854775807", 512, 512 << 20},
		{"one past the overflow guard", "8796093022208", 512, 512 << 20}, // 8 TiB
		{"largest in-range value", "8796093022207", 512, 8796093022207 << 20},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(key, test.env)
			if got := envMiB(key, test.def); got != test.want {
				t.Fatalf("envMiB(%q, %d) with env %q = %d, want %d", key, test.def, test.env, got, test.want)
			}
		})
	}
}

// TestEnvMiBAllowZero is TestEnvMiB for the allow-zero variant: an explicit
// "0" is the documented off switch (0 bytes = no cap) and must survive as a
// real value; every other invalid input falls back to the default.
func TestEnvMiBAllowZero(t *testing.T) {
	const key = "ALIANG_TEST_ENV_MIB_ALLOW_ZERO"
	cases := []struct {
		name string
		env  string
		def  int64
		want int64
	}{
		{"unset returns default", "", 512, 512 << 20},
		{"blank returns default", " ", 512, 512 << 20},
		{"explicit zero disables the cap", "0", 512, 0},
		{"valid value parsed to bytes", "1", 512, 1 << 20},
		{"unparseable returns default", "12ab", 512, 512 << 20},
		{"negative returns default", "-1", 512, 512 << 20},
		{"one past the overflow guard", "8796093022208", 512, 512 << 20}, // 8 TiB
		{"max int64 overflows to default", "9223372036854775807", 512, 512 << 20},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(key, test.env)
			if got := envMiBAllowZero(key, test.def); got != test.want {
				t.Fatalf("envMiBAllowZero(%q, %d) with env %q = %d, want %d", key, test.def, test.env, got, test.want)
			}
		})
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
