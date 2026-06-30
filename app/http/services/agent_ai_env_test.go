package services

import (
	"strings"
	"testing"
)

// TestMergeEnvOverriding verifies the override semantics that back
// agentChildProcessEnv: a custom env var must reliably replace a same-named
// inherited variable (no duplicate keys left in the slice), other variables are
// preserved unchanged, and values containing '=' survive intact.
func TestMergeEnvOverriding(t *testing.T) {
	base := []string{
		"PATH=/usr/bin",
		"ANTHROPIC_BASE_URL=https://system.example",
		"HOME=/root",
		"EMPTY=",
	}

	t.Run("overrides same-named key, keeps the rest, adds new", func(t *testing.T) {
		overrides := map[string]string{
			"ANTHROPIC_BASE_URL": "https://custom.example",
			"NEW_VAR":            "added",
		}
		got := mergeEnvOverriding(base, overrides)

		env := envSliceToMap(t, got)
		if env["ANTHROPIC_BASE_URL"] != "https://custom.example" {
			t.Fatalf("ANTHROPIC_BASE_URL = %q, want custom value (system value must be overridden)", env["ANTHROPIC_BASE_URL"])
		}
		if env["PATH"] != "/usr/bin" {
			t.Fatalf("PATH = %q, want inherited value unchanged", env["PATH"])
		}
		if env["HOME"] != "/root" {
			t.Fatalf("HOME = %q, want inherited value unchanged", env["HOME"])
		}
		if env["EMPTY"] != "" {
			t.Fatalf("EMPTY = %q, want empty inherited value preserved", env["EMPTY"])
		}
		if env["NEW_VAR"] != "added" {
			t.Fatalf("NEW_VAR = %q, want added override", env["NEW_VAR"])
		}
		if n := countEnvKey(got, "ANTHROPIC_BASE_URL"); n != 1 {
			t.Fatalf("ANTHROPIC_BASE_URL appears %d times, want exactly 1 (no duplicate)", n)
		}
	})

	t.Run("nil overrides returns base unchanged", func(t *testing.T) {
		got := mergeEnvOverriding(base, nil)
		if len(got) != len(base) {
			t.Fatalf("len = %d, want %d (base unchanged)", len(got), len(base))
		}
	})

	t.Run("preserves value containing equals", func(t *testing.T) {
		got := mergeEnvOverriding(base, map[string]string{"FOO": "a=b=c"})
		if v := envSliceToMap(t, got)["FOO"]; v != "a=b=c" {
			t.Fatalf("FOO = %q, want 'a=b=c' (value with '=' preserved)", v)
		}
	})
}

func envSliceToMap(t *testing.T, env []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(env))
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			t.Fatalf("malformed env entry %q (no '=')", kv)
		}
		out[parts[0]] = parts[1]
	}
	return out
}

func countEnvKey(env []string, key string) int {
	n := 0
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 && kv[:i] == key {
			n++
		}
	}
	return n
}
