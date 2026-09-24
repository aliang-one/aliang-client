// quick_setup_support.go 幸存 helper 的测试（自 v2 render_test.go 迁入/改写）：
// 读盘三态与 URL 派生的行为锁，随 render 链退役必须继续有人把守。
package services

import (
	"os"
	"path/filepath"
	"testing"
)

// TestQuickSetupV1SuffixPerSoftware 锁定 keys 列表 base_url 的 /v1 口径
// （codex/opencode：root + /v1；claude/pi：root 原样）。
func TestQuickSetupV1SuffixPerSoftware(t *testing.T) {
	if got := quickSetupProviderBaseURL("anthropic", "http://127.0.0.1:56432"); got != "http://127.0.0.1:56432/v1" {
		t.Fatalf("codex/opencode: %s", got)
	}
	if got := quickSetupProviderBaseURL("openai", "https://api.example.com/v1"); got != "https://api.example.com/v1" {
		t.Fatalf("already-versioned root must not double-append: %s", got)
	}
	if got := quickSetupProviderBaseURL("other", "https://api.example.com"); got != "https://api.example.com" {
		t.Fatalf("non-versioned provider keeps bare root: %s", got)
	}
}

// TestQuickSetupReadExistingFile_ThreeStates 直测读盘三态（原 Render 链三态对抗
// 检查的单元化改写）：missing（无文件/0 字节/家目录空）→ 无警告形态；unreadable
// （非普通文件占位/超限）→ 点名警告形态；ok → 读出原文。
func TestQuickSetupReadExistingFile_ThreeStates(t *testing.T) {
	const (
		codexPath = "~/.codex/config.toml"
		leafName  = "config.toml"
	)

	t.Run("missing when file absent", func(t *testing.T) {
		home := t.TempDir()
		content, state := quickSetupReadExistingFile("codex", codexPath, home)
		if state != quickSetupReadMissing || content != "" {
			t.Fatalf("state=%v content=%q, want missing/empty", state, content)
		}
	})

	t.Run("missing when home empty", func(t *testing.T) {
		content, state := quickSetupReadExistingFile("codex", codexPath, "  ")
		if state != quickSetupReadMissing || content != "" {
			t.Fatalf("state=%v content=%q, want missing/empty", state, content)
		}
	})

	t.Run("missing when file is zero bytes", func(t *testing.T) {
		home := t.TempDir()
		writeBackupFixture(t, home, ".codex/config.toml", "")
		content, state := quickSetupReadExistingFile("codex", codexPath, home)
		if state != quickSetupReadMissing || content != "" {
			t.Fatalf("zero-byte file must be missing semantics: state=%v content=%q", state, content)
		}
	})

	t.Run("ok reads exact disk bytes", func(t *testing.T) {
		home := t.TempDir()
		writeBackupFixture(t, home, ".codex/config.toml", "user original")
		content, state := quickSetupReadExistingFile("codex", codexPath, home)
		if state != quickSetupReadOK || content != "user original" {
			t.Fatalf("state=%v content=%q, want ok/user original", state, content)
		}
	})

	// unreadable：目录占位命中「存在但非普通文件」（resolve 的普通文件闸门拒绝）。
	// chmod 000 在 root 下仍可读，故统一用目录占位形态。
	t.Run("unreadable when target is a directory placeholder", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, ".codex", leafName), 0o700); err != nil {
			t.Fatal(err)
		}
		content, state := quickSetupReadExistingFile("codex", codexPath, home)
		if state != quickSetupReadUnreadable || content != "" {
			t.Fatalf("directory placeholder must be unreadable: state=%v content=%q", state, content)
		}
	})

	t.Run("unreadable when file exceeds size cap", func(t *testing.T) {
		home := t.TempDir()
		big := make([]byte, quickSetupMaxApplyFileBytes+1)
		writeBackupFixture(t, home, ".codex/config.toml", string(big))
		content, state := quickSetupReadExistingFile("codex", codexPath, home)
		if state != quickSetupReadUnreadable || content != "" {
			t.Fatalf("oversized file must be unreadable: state=%v content len=%d", state, len(content))
		}
	})

	t.Run("outside allowed root falls back to missing", func(t *testing.T) {
		home := t.TempDir()
		// 路径越界（策略拒绝且目标不存在）→ missing，不误报 unreadable。
		content, state := quickSetupReadExistingFile("codex", "~/.ssh/id_rsa", home)
		if state != quickSetupReadMissing || content != "" {
			t.Fatalf("policy-rejected unknown state must stay missing: state=%v content=%q", state, content)
		}
	})
}

// TestQuickSetupLeafFileName 锁定降级警告点名的文件名提取（评审 Minor 3）。
func TestQuickSetupLeafFileName(t *testing.T) {
	if got := quickSetupLeafFileName("~/.codex/auth.json"); got != "auth.json" {
		t.Fatalf("leaf = %q, want auth.json", got)
	}
	if got := quickSetupLeafFileName("config.toml"); got != "config.toml" {
		t.Fatalf("bare name passes through: %q", got)
	}
	if got, want := quickSetupLeafFileName("~"), "~"; got != want {
		t.Fatalf("degenerate path must pass through: %q, want %q", got, want)
	}
}
