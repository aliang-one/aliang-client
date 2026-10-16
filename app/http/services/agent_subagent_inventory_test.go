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

func TestCollectSubagentsFromDir_MissingDirIsNil(t *testing.T) {
	if out := collectSubagentsFromDir(filepath.Join(t.TempDir(), "does-not-exist")); out != nil {
		t.Fatalf("missing dir must be nil, got %+v", out)
	}
	if out := collectSubagentsFromDir(t.TempDir()); out != nil {
		t.Fatalf("empty dir must be nil, got %+v", out)
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
