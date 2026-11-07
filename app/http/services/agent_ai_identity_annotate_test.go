package services

import (
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-11-07 会话 ID 全链收敛(错位+不可见)回归:
// agentcore 清单条目 ID=会话 id(壳),内核原生句柄只在绑定表——annotate 必须
// 回填 SourceSessionID 并声明 ManagedConversationID,服务端 managed 别名通道
// 才能物化 canonical 记录而非铸造 ai_import_ 壳(壳 id 冒充原生 id 是 S2 根因)。
// 同会话绑定不得翻转 origin(dashboard/phone 头行语义保留,服务端角标不失真)。
func TestAnnotateAgentCoreEntryBackfillsNativeFromOwnBinding(t *testing.T) {
	manager := newAgentAIManager()
	manager.bindings["local-abc"] = agentAIBindingRecord{
		ConversationID:  "local-abc",
		Provider:        "agentcore",
		NativeSessionID: "agentcore-1700000000-1",
		State:           "confirmed",
		BindingVersion:  3,
	}
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "local-abc", Provider: "agentcore", Origin: "dashboard"},
	})
	got := sessions[0]
	if got.SourceSessionID != "agentcore-1700000000-1" {
		t.Fatalf("SourceSessionID = %q, want kernel native id", got.SourceSessionID)
	}
	if got.ManagedConversationID != "local-abc" {
		t.Fatalf("ManagedConversationID = %q, want local-abc", got.ManagedConversationID)
	}
	if got.BindingState != "confirmed" || got.BindingVersion != 3 {
		t.Fatalf("binding meta = %q/%d, want confirmed/3", got.BindingState, got.BindingVersion)
	}
	// 同会话绑定:origin 头行语义(dashboard)保留,不得翻转成 managed。
	if got.Origin != "dashboard" {
		t.Fatalf("Origin = %q, want dashboard preserved", got.Origin)
	}
}

func TestAnnotateAgentCoreEntrySelfDeclaresManagedWithoutBinding(t *testing.T) {
	manager := newAgentAIManager()
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "local-xyz", Provider: "agentcore", Origin: "dashboard"},
	})
	got := sessions[0]
	if got.SourceSessionID != "" {
		t.Fatalf("SourceSessionID = %q, want empty without binding", got.SourceSessionID)
	}
	// 无绑定也声明托管归属:服务端据此物化 canonical 记录(S1)。
	if got.ManagedConversationID != "local-xyz" {
		t.Fatalf("ManagedConversationID = %q, want local-xyz self-declared", got.ManagedConversationID)
	}
	if got.Origin != "dashboard" {
		t.Fatalf("Origin = %q, want dashboard preserved", got.Origin)
	}
}

func TestAnnotateClaudeAliasEntryStillFlipsToManaged(t *testing.T) {
	manager := newAgentAIManager()
	manager.bindings["ai_9x"] = agentAIBindingRecord{
		ConversationID:  "ai_9x",
		Provider:        "claudecode",
		NativeSessionID: "uuid-1",
		State:           "confirmed",
		BindingVersion:  1,
	}
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "claude_uuid-1", Provider: "claude"},
	})
	got := sessions[0]
	if got.Origin != "managed" {
		t.Fatalf("Origin = %q, want managed (异会话别名维持现状)", got.Origin)
	}
	if got.ManagedConversationID != "ai_9x" {
		t.Fatalf("ManagedConversationID = %q, want ai_9x", got.ManagedConversationID)
	}
	if got.SourceSessionID != "uuid-1" {
		t.Fatalf("SourceSessionID = %q, want uuid-1", got.SourceSessionID)
	}
}

func TestAnnotateAgentCoreImportArtifactDoesNotSelfDeclare(t *testing.T) {
	manager := newAgentAIManager()
	sessions := manager.annotateManagedVibeSessions([]models.AgentVibeSession{
		{ID: "ai_import_h1", Provider: "agentcore"},
	})
	got := sessions[0]
	// ai_import_ 前缀是历史拆分产物,归 heal 治愈;自我声明会让服务端复活拆分卡。
	if got.ManagedConversationID != "" {
		t.Fatalf("ManagedConversationID = %q, want empty (no self-declaration)", got.ManagedConversationID)
	}
	if got.SourceSessionID != "" {
		t.Fatalf("SourceSessionID = %q, want empty", got.SourceSessionID)
	}
}
