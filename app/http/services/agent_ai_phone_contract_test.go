package services

import (
	"strings"
	"testing"
)

func TestNormalizeAgentAISurface(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"phone", agentAISurfacePhone},
		{"", ""},      // 老 server 不发该字段
		{"tui", ""},   // 未知值一律归 TUI 通道
		{"PHONE", ""}, // 大小写敏感,白名单外
		{" phone", ""},
	}
	for _, tc := range cases {
		if got := normalizeAgentAISurface(tc.in); got != tc.want {
			t.Fatalf("normalizeAgentAISurface(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWrapAgentAIPhoneContract(t *testing.T) {
	wrapped := wrapAgentAIPhoneContract("帮我看看这个仓库")
	if !strings.HasPrefix(wrapped, "<system-reminder>") {
		t.Fatalf("wrap should prefix system-reminder, got: %.80s", wrapped)
	}
	if !strings.Contains(wrapped, agentAIPhoneContractMarker) {
		t.Fatalf("wrap should contain marker")
	}
	if !strings.HasSuffix(wrapped, "帮我看看这个仓库") {
		t.Fatalf("wrap should keep original content at the end, got tail: %.60s", wrapped[max(0, len(wrapped)-60):])
	}
	// 幂等:history 重放/续接场景二次包裹不重复。
	again := wrapAgentAIPhoneContract(wrapped)
	if again != wrapped {
		t.Fatalf("wrap should be idempotent")
	}
	if got := wrapAgentAIPhoneContract(""); got != "" {
		t.Fatalf("empty content should stay empty, got %q", got)
	}
	// 契约要求:提到 ask_user 优先、aliang-options 兜底、AskUserQuestion 禁用。
	for _, want := range []string{"mcp__aliang__ask_user", "aliang-options", "AskUserQuestion"} {
		if !strings.Contains(wrapped, want) {
			t.Fatalf("contract text missing %q", want)
		}
	}
}

func TestClaudeApprovalHookSettingsDeniesInteractiveToolsForPhoneSurface(t *testing.T) {
	run := agentAIRun{
		sessionID:     "sess-1",
		messageID:     "msg-1",
		approvalToken: "tok-1",
		surface:       agentAISurfacePhone,
	}
	// 非 full 档:ask overlay 存在,deny 追加进同一 permissions。
	run.claudePolicy.enabled = true
	run.claudePolicy.trustTier = "sanitized"
	run.claudePolicy.permissionAsk = []string{"Bash", "Edit"}
	settings, err := claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatalf("settings build failed: %v", err)
	}
	perms, _ := settings["permissions"].(map[string]interface{})
	if perms == nil {
		t.Fatalf("sanitized+phone should carry permissions")
	}
	ask, _ := perms["ask"].([]string)
	if len(ask) != 2 {
		t.Fatalf("ask overlay should survive, got %v", ask)
	}
	deny, _ := perms["deny"].([]string)
	if len(deny) != 1 || deny[0] != "AskUserQuestion" {
		t.Fatalf("deny should contain exactly AskUserQuestion, got %v", deny)
	}

	// full 档:本不带 permissions,phone 面单独注入 deny。
	run.claudePolicy.trustTier = "full"
	settings, err = claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatalf("settings build failed: %v", err)
	}
	perms, _ = settings["permissions"].(map[string]interface{})
	if perms == nil {
		t.Fatalf("full+phone should still carry deny")
	}
	if _, hasAsk := perms["ask"]; hasAsk {
		t.Fatalf("full tier must not gain ask overlay")
	}
	deny, _ = perms["deny"].([]string)
	if len(deny) != 1 || deny[0] != "AskUserQuestion" {
		t.Fatalf("full+phone deny = %v", deny)
	}

	// TUI 通道(无 surface):零变化,不出 deny。
	run.surface = ""
	run.claudePolicy.trustTier = "full"
	settings, err = claudeApprovalHookSettings(claudeApprovalHookPermissionRequestHTTP, run)
	if err != nil {
		t.Fatalf("settings build failed: %v", err)
	}
	if _, hasPerms := settings["permissions"]; hasPerms {
		t.Fatalf("TUI full-tier run must stay untouched, got permissions %v", settings["permissions"])
	}
}
