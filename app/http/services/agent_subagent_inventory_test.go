package services

// subagent 采集测试(设备级用户域 ~/.claude/agents):frontmatter 解析/文件名
// 回落/截断上限/确定性排序/fail-soft。密闭用 t.TempDir,不依赖真实 HOME。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectSubagentsFromDir(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "code-reviewer.md", "---\nname: code-reviewer\ndescription: Reviews code for quality and correctness\n---\n\nBody ignored.\n")
	writeAgent(t, dir, "no-frontmatter.md", "Just prose, no YAML block.\n")
	writeAgent(t, dir, "quoted.md", "---\nname: \"quoted-name\"\ndescription: 'single quoted desc'\n---\n")
	writeAgent(t, dir, "fallback-name.md", "no yaml at all\n")
	writeAgent(t, dir, "notes.txt", "not markdown\n")
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}

	out := collectSubagentsFromDir(dir)
	if len(out) != 4 {
		t.Fatalf("want 4 subagents, got %d: %+v", len(out), out)
	}
	// 确定性排序(文件名字母序):code-reviewer < fallback-name < no-frontmatter < quoted
	if out[0].Name != "code-reviewer" || out[1].Name != "fallback-name" || out[2].Name != "no-frontmatter" || out[3].Name != "quoted-name" {
		t.Fatalf("unexpected order/names: %+v", out)
	}
	if out[0].Description != "Reviews code for quality and correctness" {
		t.Fatalf("description = %q", out[0].Description)
	}
	// 无 frontmatter 的 .md:文件名回落为 name,description 为空
	if out[2].Description != "" {
		t.Fatalf("no-frontmatter description should be empty, got %q", out[2].Description)
	}
	for _, sub := range out {
		if sub.Source != "user" {
			t.Fatalf("source = %q, want user", sub.Source)
		}
	}
}

func TestCollectSubagentsFromDir_EmptySemantics(t *testing.T) {
	// 目录缺失 = 采集面不可得(home 解析失败/目录从未存在):nil → hello 载荷
	// subagents:null → 服务端保持旧值(非权威状态,不得清空)。
	if out := collectSubagentsFromDir(filepath.Join(t.TempDir(), "does-not-exist")); out != nil {
		t.Fatalf("missing dir must be nil, got %+v", out)
	}
	// 目录存在但无有效定义 = 权威的空清单:非 nil 空切片 → hello 发
	// subagents:[] → 服务端清空。此前此处返回 nil(载荷 null),「删光最后
	// 一个定义」永远无法同步到云端(cross-repo 契约,服务端 normalizeAgent-
	// Subagents 同步改为显式空清单→[]、非空全被拒→undefined)。
	if out := collectSubagentsFromDir(t.TempDir()); out == nil || len(out) != 0 {
		t.Fatalf("existing empty dir must be non-nil empty slice, got %+v", out)
	}
	// 目录存在但只有非 .md 文件/子目录:同样是权威空清单(解析器对 .md 有
	// 文件名兜底,「有 .md 却全部被拒」实际不可达,空清单唯一来源是无 .md)。
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := collectSubagentsFromDir(dir); out == nil || len(out) != 0 {
		t.Fatalf("dir with no .md files must be non-nil empty slice, got %+v", out)
	}
}

// home 解析失败(agentHome()=="")必须返回 nil:否则 filepath.Join("",
// ".claude","agents") 退化为 cwd 相对路径,进程 cwd 里恰有空 .claude/agents
// 目录时,非权威的空清单会被当「显式清空」上报、抹掉云端既有数据。
func TestCollectUserSubagentsHomeUnavailable(t *testing.T) {
	prev := subagentInventoryHome
	subagentInventoryHome = func() string { return "" }
	defer func() { subagentInventoryHome = prev }()
	if out := collectUserSubagents(); out != nil {
		t.Fatalf("home unavailable must yield nil, got %+v", out)
	}
}

func TestCollectSubagentsFromDir_TruncatesAndCaps(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 30; i++ {
		name := filepath.Base("s") // deterministic prefix
		_ = name
		file := "a" + strings.Repeat("0", 2) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".md"
		content := "---\nname: n" + strings.Repeat("m", 100) + "\ndescription: " + strings.Repeat("d", 400) + "\n---\n"
		writeAgent(t, dir, file, content)
	}
	out := collectSubagentsFromDir(dir)
	if len(out) != subagentMaxCount {
		t.Fatalf("want capped %d, got %d", subagentMaxCount, len(out))
	}
	if len(out[0].Name) > subagentNameMaxLen || len(out[0].Description) > subagentDescMaxLen {
		t.Fatalf("limits breached: name=%d desc=%d", len(out[0].Name), len(out[0].Description))
	}
}
