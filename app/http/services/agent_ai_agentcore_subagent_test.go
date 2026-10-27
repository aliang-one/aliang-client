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
	if r.Model != "sonnet" {
		t.Fatalf("model 解析错误: %+v", r)
	}
	if !strings.Contains(r.Body, "评审员") {
		t.Fatalf("正文缺失: %+v", r)
	}
	if byName["fallback"].Body == "" {
		t.Fatalf("无 frontmatter 文件应保留回落名+正文")
	}
	// broken.md：frontmatter 未闭合 → 名回落文件名，全文回落正文。
	b := byName["broken"]
	if b.Name != "broken" || b.Body != "---\nname: [ broken" {
		t.Fatalf("未闭合 frontmatter 回落语义错误: %+v", b)
	}
}

func TestAgentCoreSubagentFrontmatterOnlyStub(t *testing.T) {
	// 有 frontmatter 但正文为空：Body 必须是空串，
	// 绝不能把整块 frontmatter 泄漏进指令文本。
	def, ok := agentCoreSubagentFrontmatter("stub.md", "---\nname: stub\ndescription: 只有头\n---\n")
	if !ok {
		t.Fatalf("frontmatter-only stub 应是有效定义")
	}
	if def.Name != "stub" || def.Description != "只有头" {
		t.Fatalf("头字段解析错误: %+v", def)
	}
	if def.Body != "" || strings.Contains(def.Body, "---") {
		t.Fatalf("空正文泄漏 frontmatter: %q", def.Body)
	}
}

func TestAgentCoreSubagentBodyTruncation(t *testing.T) {
	content := "---\nname: big\n---\n" + strings.Repeat("x", 9*1024)
	def, ok := agentCoreSubagentFrontmatter("big.md", content)
	if !ok {
		t.Fatalf("应有效")
	}
	if got := len(def.Body); got != 8*1024 {
		t.Fatalf("正文截断 = %d, want 8192", got)
	}
}

func TestAgentCoreSubagentToolsBlockListFailsOpen(t *testing.T) {
	// YAML 块列表不被行式解析支持：白名单为空=全量放行（Warn 留痕）。
	def, ok := agentCoreSubagentFrontmatter("blk.md", "---\nname: blk\ntools:\n  - Read\n  - Grep\n---\nx")
	if !ok {
		t.Fatalf("应有效")
	}
	if len(def.ToolsWhitelist) != 0 {
		t.Fatalf("块列表不应解析出白名单: %+v", def.ToolsWhitelist)
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
