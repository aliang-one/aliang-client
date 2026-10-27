package services

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSubagentDef(t *testing.T, home, filename, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir 失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(body), 0o644); err != nil {
		t.Fatalf("写定义失败: %v", err)
	}
}

func TestAgentCoreSubagentDefsIn(t *testing.T) {
	home := t.TempDir()
	writeSubagentDef(t, home, "reviewer.md",
		"---\nname: reviewer\ndescription: 代码评审\nmodel: sonnet\ntools: Read, Grep\n---\n你是评审员。")
	writeSubagentDef(t, home, "fallback.md", "没有 frontmatter 的正文。")
	writeSubagentDef(t, home, "broken.md", "---\nname: [ broken\n")
	writeSubagentDef(t, home, "notagent.txt", "忽略非 md")

	defs := agentCoreSubagentDefsIn(home)
	if len(defs) != 3 {
		t.Fatalf("条目数 = %d, want 3（非 md 忽略）: %+v", len(defs), defs)
	}
	byName := map[string]agentCoreSubagentDef{}
	for _, d := range defs {
		byName[d.Name] = d
	}
	r := byName["reviewer"]
	if r.Description != "代码评审" || len(r.ToolsWhitelist) != 2 || r.ToolsWhitelist[0] != "Read" {
		t.Fatalf("frontmatter 解析错误: %+v", r)
	}
	if !strings.Contains(r.Body, "评审员") {
		t.Fatalf("正文缺失: %+v", r)
	}
	if byName["fallback"].Body == "" {
		t.Fatalf("无 frontmatter 文件应保留回落名+正文")
	}
}

func TestAgentCoreSubagentDefsCap(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 30; i++ {
		writeSubagentDef(t, home, fmt.Sprintf("agent-%02d.md", i), "---\nname: a\n---\nx")
	}
	if got := len(agentCoreSubagentDefsIn(home)); got != 24 {
		t.Fatalf("cap = %d, want 24（确定性截断）", got)
	}
}

func TestAgentCoreSubagentBridgeName(t *testing.T) {
	reserved := map[string]bool{"Read": true, "Bash": true}
	seen := map[string]bool{}
	n1 := agentCoreSubagentBridgeName("Read", reserved, seen)
	if n1 == "Read" || seen[n1] != true {
		t.Fatalf("内核保留名必须消解: %q", n1)
	}
	n2 := agentCoreSubagentBridgeName("Read", reserved, seen)
	if n2 == n1 {
		t.Fatalf("重名必须序号消解: %q vs %q", n1, n2)
	}
	if got := agentCoreSubagentBridgeName("a:b/c", reserved, seen); strings.ContainsAny(got, ":/") {
		t.Fatalf("清洗失效: %q", got)
	}
}
