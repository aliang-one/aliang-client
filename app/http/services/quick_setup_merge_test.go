package services

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	var v map[string]interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMergeQuickSetupJSON(t *testing.T) {
	existing := mustJSON(t, `{"theme":"dark","mcp":{"fs":{"command":"x"}},"provider":{"old":{"npm":"@g/old"}}}`)
	incoming := mustJSON(t, `{"$schema":"https://opencode.ai/config.json","model":"aliang/main","provider":{"aliang":{"npm":"@g/aliang","options":{"baseURL":"https://api.aliang.one/v1"}}}}`)

	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok {
		t.Fatal("merge should succeed")
	}
	out, _ := json.Marshal(merged)
	s := string(out)
	for _, want := range []string{`"theme":"dark"`, `"fs"`, `"old"`, `"aliang/main"`, `"baseURL":"https://api.aliang.one/v1"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestMergeQuickSetupJSONPreservesAuthTokens(t *testing.T) {
	// codex auth.json：只动 OPENAI_API_KEY，保住 ChatGPT 登录态（spec §7）
	existing := mustJSON(t, `{"OPENAI_API_KEY":null,"tokens":{"access_token":"at","account_id":"acc"},"last_refresh":"2026-01-01"}`)
	incoming := mustJSON(t, `{"OPENAI_API_KEY":"sk-aliang"}`)
	merged, ok := mergeQuickSetupJSONObjects(existing, incoming)
	if !ok {
		t.Fatal("merge should succeed")
	}
	out, _ := json.Marshal(merged)
	if !strings.Contains(string(out), `"access_token":"at"`) {
		t.Fatalf("tokens lost: %s", out)
	}
}

func TestMergeQuickSetupJSONBrokenExisting(t *testing.T) {
	if _, ok := mergeQuickSetupJSONObjects(nil, mustJSON(t, `{"a":1}`)); !ok {
		t.Fatal("nil existing should merge cleanly")
	}
}
