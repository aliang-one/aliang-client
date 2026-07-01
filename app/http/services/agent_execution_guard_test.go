package services

import (
	"os"
	"testing"
	"time"
)

// TestResolveEnvDuration covers the env→duration resolution used by the
// approval-timeout / hard-ceiling knobs. It targets the pure helper directly so
// the package-level vars (resolved once at process start) don't need re-init.
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

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(key, c.env)
			got := resolveEnvDuration(key, c.def)
			if got != c.want {
				t.Fatalf("resolveEnvDuration(%q, def=%s) with env=%q = %s, want %s",
					key, c.def, c.env, got, c.want)
			}
		})
	}
}

// TestAgentAITimeoutDefaults locks in the shipped fallback values so a future
// change is conscious. The package vars resolve once at process start, so an
// operator running tests with an override is skipped rather than failed.
func TestAgentAITimeoutDefaults(t *testing.T) {
	if v := os.Getenv("ALIANG_AI_APPROVAL_TIMEOUT"); v != "" {
		t.Skipf("ALIANG_AI_APPROVAL_TIMEOUT=%s set at start; skipping default assertion", v)
	}
	if v := os.Getenv("ALIANG_AI_HARD_CEILING"); v != "" {
		t.Skipf("ALIANG_AI_HARD_CEILING=%s set at start; skipping default assertion", v)
	}
	if agentAIApprovalTimeout != 24*time.Hour {
		t.Fatalf("default agentAIApprovalTimeout = %s, want 24h", agentAIApprovalTimeout)
	}
	if agentAIHardCeiling != 48*time.Hour {
		t.Fatalf("default agentAIHardCeiling = %s, want 48h", agentAIHardCeiling)
	}
	// Hard ceiling must exceed the approval timeout or a long approval wait is
	// clipped by the runaway backstop first.
	if agentAIHardCeiling <= agentAIApprovalTimeout {
		t.Fatalf("agentAIHardCeiling (%s) must exceed agentAIApprovalTimeout (%s)",
			agentAIHardCeiling, agentAIApprovalTimeout)
	}
}
