package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// 会话级审批模式(2026-11-05):归一/带键解析/三内核覆盖矩阵/会话记忆跳过/
// journal 头行同步。spec docs/superpowers/specs/2026-11-05-chat-approval-mode-and-composer-chips-design.md

func TestNormalizeAgentAIApprovalMode(t *testing.T) {
	cases := map[string]string{
		"": "", "ask_all": "ask_all", "bypass_all": "bypass_all", "read_only": "read_only",
		"ASK_ALL": "", "yolo": "", "  ": "", "bypass": "",
	}
	for in, want := range cases {
		if got := normalizeAgentAIApprovalMode(in); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAgentAIApprovalModeSetMarker(t *testing.T) {
	// 键缺失 = preserve(不动既有模式,老客户端 F2 纪律)
	if v, ok := parseAgentAIApprovalMode(map[string]interface{}{}); ok || v != "" {
		t.Fatalf("缺键应 (\"\",false), got (%q,%v)", v, ok)
	}
	// 显式空串 = 合法(默认推荐)
	if v, ok := parseAgentAIApprovalMode(map[string]interface{}{"approval_mode": ""}); !ok || v != "" {
		t.Fatalf("显式空串应 (\"\",true), got (%q,%v)", v, ok)
	}
	// 非法值归一
	if v, ok := parseAgentAIApprovalMode(map[string]interface{}{"approval_mode": "yolo"}); !ok || v != "" {
		t.Fatalf("非法值应归一 (\"\",true), got (%q,%v)", v, ok)
	}
	if v, ok := parseAgentAIApprovalMode(map[string]interface{}{"approval_mode": "bypass_all"}); !ok || v != "bypass_all" {
		t.Fatalf("合法值应透传, got (%q,%v)", v, ok)
	}
}

func TestApprovalModeDecisionMatrix(t *testing.T) {
	cases := []struct {
		name  string
		mode  string
		in    policyDecision
		tool  string
		want  policyDecision
	}{
		{"默认_require", "", decisionRequireApproval, "", decisionRequireApproval},
		{"默认_autoApprove", "", decisionAutoApprove, "", decisionAutoApprove},
		{"默认_autoDeny", "", decisionAutoDeny, "", decisionAutoDeny},
		{"askAll_require", "ask_all", decisionRequireApproval, "", decisionRequireApproval},
		{"askAll_降级autoApprove", "ask_all", decisionAutoApprove, "", decisionRequireApproval},
		{"askAll_护栏autoDeny", "ask_all", decisionAutoDeny, "", decisionAutoDeny},
		{"bypass_require降放行", "bypass_all", decisionRequireApproval, "", decisionAutoApprove},
		{"bypass_autoApprove", "bypass_all", decisionAutoApprove, "", decisionAutoApprove},
		{"bypass_护栏autoDeny", "bypass_all", decisionAutoDeny, "", decisionAutoDeny},
		// read_only:写/执行族 auto_approve 降 require;require/auto_deny/读工具不动。
		{"readOnly_bashAutoApprove降require", "read_only", decisionAutoApprove, "Bash", decisionRequireApproval},
		{"readOnly_write降require", "read_only", decisionAutoApprove, "Write", decisionRequireApproval},
		{"readOnly_edit降require", "read_only", decisionAutoApprove, "Edit", decisionRequireApproval},
		{"readOnly_read保持autoApprove", "read_only", decisionAutoApprove, "Read", decisionAutoApprove},
		{"readOnly_grep保持autoApprove", "read_only", decisionAutoApprove, "Grep", decisionAutoApprove},
		{"readOnly_mcp不扩权不误压", "read_only", decisionAutoApprove, "mcp__serena__find_symbol", decisionAutoApprove},
		{"readOnly_require原样", "read_only", decisionRequireApproval, "Bash", decisionRequireApproval},
		{"readOnly_护栏autoDeny", "read_only", decisionAutoDeny, "Bash", decisionAutoDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyApprovalModeOverride(tc.mode, tc.in, tc.tool); got != tc.want {
				t.Fatalf("override(%q,%v,%q) = %v, want %v", tc.mode, tc.in, tc.tool, got, tc.want)
			}
		})
	}
}

// writeFamilyAgentAITool:写/执行族词表钉死——read_only 的压制面以此为界。
func TestWriteFamilyAgentAITool(t *testing.T) {
	write := []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "Bash", "PowerShell", "shell", "command", "file-change", " bash "}
	read := []string{"Read", "Grep", "Glob", "LS", "TodoWrite", "WebSearch", "", "mcp__x__y", "Task"}
	for _, name := range write {
		if !writeFamilyAgentAITool(name) {
			t.Fatalf("%q 应判写/执行族", name)
		}
	}
	for _, name := range read {
		if writeFamilyAgentAITool(name) {
			t.Fatalf("%q 不应判写/执行族", name)
		}
	}
}

func TestApprovalModeSkipsSessionMemory(t *testing.T) {
	if approvalModeSkipsSessionMemory("") {
		t.Fatal("默认模式不应跳过会话记忆")
	}
	if !approvalModeSkipsSessionMemory("ask_all") || !approvalModeSkipsSessionMemory("bypass_all") {
		t.Fatal("ask_all/bypass_all 应跳过会话记忆")
	}
}

// read_only 的记忆分流:写/执行族跳过记忆(记忆免问不能复活写权限),读工具
// 记忆照常生效。
func TestApprovalModeSkipsSessionMemoryForTool(t *testing.T) {
	for _, tool := range []string{"Bash", "Write", "Edit", "shell"} {
		if !approvalModeSkipsSessionMemoryForTool("read_only", tool) {
			t.Fatalf("read_only 下 %q 应跳过会话记忆", tool)
		}
	}
	for _, tool := range []string{"Read", "Grep", "Glob"} {
		if approvalModeSkipsSessionMemoryForTool("read_only", tool) {
			t.Fatalf("read_only 下 %q 不应跳过会话记忆(记忆免问照常)", tool)
		}
	}
	if !approvalModeSkipsSessionMemoryForTool("ask_all", "Read") || !approvalModeSkipsSessionMemoryForTool("bypass_all", "Read") {
		t.Fatal("ask_all/bypass_all 全跳过语义不变")
	}
	if approvalModeSkipsSessionMemoryForTool("", "Bash") {
		t.Fatal("默认模式全不跳过语义不变")
	}
}

// journal 头行:ensure 落键 + syncSessionApprovalMode 显式改键(缺 set 不动)。
func TestJournalApprovalModeHeaderSync(t *testing.T) {
	dir := t.TempDir()
	j := &agentCoreJournal{dir: dir}
	j.ensureSessionHeader("sess-mode", "/proj", "glm-4.7", "high", "2026-11-05T00:00:00Z", "dashboard", nil, nil, "bypass_all")
	path := filepath.Join(dir, agentCoreJournalFileName("sess-mode"))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var head map[string]interface{}
	if err := json.Unmarshal(raw[:len(raw)-1], &head); err != nil {
		t.Fatal(err)
	}
	if head["approval_mode"] != "bypass_all" {
		t.Fatalf("头行应带 approval_mode=bypass_all, got %v", head["approval_mode"])
	}

	// 显式改键:sync 落 ask_all
	j2 := &agentCoreJournal{dir: dir}
	j2.syncSessionApprovalMode("sess-mode", "ask_all", true)
	raw, _ = os.ReadFile(path)
	var head2 map[string]interface{}
	_ = json.Unmarshal(raw[:len(raw)-1], &head2)
	if head2["approval_mode"] != "ask_all" {
		t.Fatalf("sync 后应 ask_all, got %v", head2["approval_mode"])
	}

	// 缺 set 不动(老客户端 preserve)
	j2.syncSessionApprovalMode("sess-mode", "", false)
	raw, _ = os.ReadFile(path)
	var head3 map[string]interface{}
	_ = json.Unmarshal(raw[:len(raw)-1], &head3)
	if head3["approval_mode"] != "ask_all" {
		t.Fatalf("缺 set 不应改头行, got %v", head3["approval_mode"])
	}

	// 读侧:journal head 解析回填
	_, headRead, err := readAgentCoreJournalHistoryFrom(dir, "sess-mode")
	if err != nil {
		t.Fatal(err)
	}
	if headRead.approvalMode != "ask_all" {
		t.Fatalf("journal head 应回读 ask_all, got %q", headRead.approvalMode)
	}
}

// 内核注入闸:run.approvalMode=bypass_all 下 require 决策的工具不再走云端往返。
func TestAgentCoreApproveBypassSkipsCloud(t *testing.T) {
	m := newAgentAIManager()
	run := agentAIRun{sessionID: "sess-bypass", approvalMode: "bypass_all"}
	approve := m.agentCoreApprove(run, func(interface{}) error { return nil })
	resp, err := approve(t.Context(), agentcore.ApprovalRequest{ToolName: "shell", Input: map[string]interface{}{"command": "rm -rf /tmp/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Approved {
		t.Fatalf("bypass_all 下无 deny 规则应放行, got %+v", resp)
	}
}

// ask_all 下策略 require 不被放行:带短超时 ctx,断言「未放行」(等云端分支
// 由 ctx 掐断,err 非空/未 Approved 即为达标——矩阵用例已钉降级语义,本用例
// 钉的是接线:run.approvalMode 真的进了 agentCoreApprove)。
func TestAgentCoreApproveAskAllDowngradesAutoApprove(t *testing.T) {
	m := newAgentAIManager()
	run := agentAIRun{sessionID: "sess-ask", approvalMode: "ask_all"}
	approve := m.agentCoreApprove(run, func(interface{}) error { return nil })
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	resp, err := approve(ctx, agentcore.ApprovalRequest{ToolName: "shell", Input: map[string]interface{}{"command": "ls"}})
	if err == nil && resp.Approved {
		t.Fatalf("ask_all 下不应零往返放行, got %+v err=%v", resp, err)
	}
}
