package services

// 会话级 MCP/技能开关的 journal 头行持久化契约（2026-10-31，验收缺陷③的
// 写/读两半）：
//   1. syncSessionSwitches 把名单落进 kind=session 头行；
//   2. readAgentCoreJournalHistoryFrom 读回头行名单（懒建恢复的依据）；
//   3. 无变化零写盘（幂等）；变化时整体重写第一行、消息行原样保留；
//   4. nil 与缺键等价（都=未关任何项），统一「不写键」。

import (
	"os"
	"testing"
	"time"
)

func TestSyncSessionSwitchesPersistsAndRestores(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	j.ensureSessionHeader("s-sw", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
	j.appendUser("s-sw", "m1", "第一问", now)

	// 落盘 + 读回
	j.syncSessionSwitches("s-sw", []string{"github", "shell"}, true, []string{"pdf"}, true)
	_, head, err := readAgentCoreJournalHistoryFrom(j.dir, "s-sw")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(head.disabledMCPServers) != 2 || head.disabledMCPServers[0] != "github" || head.disabledMCPServers[1] != "shell" {
		t.Fatalf("disabledMCPServers = %v, want [github shell]", head.disabledMCPServers)
	}
	if len(head.disabledSkills) != 1 || head.disabledSkills[0] != "pdf" {
		t.Fatalf("disabledSkills = %v, want [pdf]", head.disabledSkills)
	}

	// 幂等：同值再同步，文件字节不变
	path := j.dir + "/" + agentCoreJournalFileName("s-sw")
	before, _ := os.ReadFile(path)
	j.syncSessionSwitches("s-sw", []string{"github", "shell"}, true, []string{"pdf"}, true)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("同值再同步不应改写文件")
	}

	// 变更：名单更新 + nil 删键，消息行必须原样保留
	j.syncSessionSwitches("s-sw", nil, true, []string{"docx"}, true)
	_, head, err = readAgentCoreJournalHistoryFrom(j.dir, "s-sw")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if head.disabledMCPServers != nil {
		t.Fatalf("nil 应删键, got %v", head.disabledMCPServers)
	}
	if len(head.disabledSkills) != 1 || head.disabledSkills[0] != "docx" {
		t.Fatalf("disabledSkills = %v, want [docx]", head.disabledSkills)
	}
	hist, _, err := readAgentCoreJournalHistoryFrom(j.dir, "s-sw")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(hist) != 1 || hist[0].Content != "第一问" {
		t.Fatalf("头行重写不得动消息行: %+v", hist)
	}
}

// 旧文件（头行无开关键的存量会话）读回必须得 nil，不得误报成空数组以外的东西。
func TestJournalHeadSwitchesAbsentInLegacyHeader(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	j.ensureSessionHeader("s-legacy", "/proj", "", "", now, "phone", nil, nil, "")
	_, head, err := readAgentCoreJournalHistoryFrom(j.dir, "s-legacy")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if head.disabledMCPServers != nil || head.disabledSkills != nil {
		t.Fatalf("旧头行应得 nil, got mcp=%v skills=%v", head.disabledMCPServers, head.disabledSkills)
	}
}

// F1 回归锚（2026-10-31 二轮评审）：首建头行必须带当前开关名单——create 先于
// 首个 run 设的键，不能等首个 run 之后的显式同步才落盘，否则重启即丢。
func TestEnsureSessionHeaderBornWithSwitches(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	j.ensureSessionHeader("s-f1", "/proj", "glm-5.3", "", now, "phone", []string{"github"}, nil, "")
	_, head, err := readAgentCoreJournalHistoryFrom(j.dir, "s-f1")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(head.disabledMCPServers) != 1 || head.disabledMCPServers[0] != "github" {
		t.Fatalf("首建头行应带 disabledMCPServers=[github], got %v", head.disabledMCPServers)
	}
	if head.disabledSkills != nil {
		t.Fatalf("nil 侧不写键, got %v", head.disabledSkills)
	}
}

// F2 回归锚：preserve 语义——单键写入绝不许动未带键一侧的持久值
// （二轮评审 confirmed-0 的 F2 反例：单键 create 曾拿未水合的兄弟内存值
// 覆盖兄弟持久值，把持久化机制变成删除机制）。
func TestSyncSessionSwitchesPreservesUnsetSide(t *testing.T) {
	j := newAgentCoreJournal(t.TempDir())
	now := time.Now().UTC().Format(time.RFC3339)
	j.ensureSessionHeader("s-f2", "/proj", "glm-5.3", "", now, "phone", nil, nil, "")
	j.syncSessionSwitches("s-f2", nil, true, []string{"pdf"}, true)

	// 只带 mcp 键：skills 侧持久值必须原样保留（哪怕调用方传的内存值是 nil）
	j.syncSessionSwitches("s-f2", []string{"a"}, true, nil, false)
	_, head, err := readAgentCoreJournalHistoryFrom(j.dir, "s-f2")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if len(head.disabledMCPServers) != 1 || head.disabledMCPServers[0] != "a" {
		t.Fatalf("带键侧应更新, got %v", head.disabledMCPServers)
	}
	if len(head.disabledSkills) != 1 || head.disabledSkills[0] != "pdf" {
		t.Fatalf("未带键侧的持久值必须保留, got %v", head.disabledSkills)
	}

	// 只带 mcp=nil（显式全启用）：skills 侧仍保留
	j.syncSessionSwitches("s-f2", nil, true, nil, false)
	_, head, err = readAgentCoreJournalHistoryFrom(j.dir, "s-f2")
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if head.disabledMCPServers != nil {
		t.Fatalf("显式 nil 应删键, got %v", head.disabledMCPServers)
	}
	if len(head.disabledSkills) != 1 || head.disabledSkills[0] != "pdf" {
		t.Fatalf("未带键侧持久值必须保留(第二轮), got %v", head.disabledSkills)
	}
}
