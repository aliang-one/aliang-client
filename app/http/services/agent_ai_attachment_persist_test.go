package services

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 持久附件缓存测试：env 重定向缓存根到临时目录（不触真实 home），
// 穿越/配额/清扫逐项钉死（spec 2026-11-05-chat-image-pipeline §3.1）。
func agentAttachmentPersistTestEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_PERSIST_DIR", root)
	return root
}

func TestAgentAttachmentPersistRoundTrip(t *testing.T) {
	root := agentAttachmentPersistTestEnv(t)
	src := []byte("fake-jpeg-bytes-0123456789")

	rel, err := agentAttachmentPersist("sess-abc", "m1-0", ".jpg", src)
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if rel != filepath.Join("sess-abc", "m1-0.jpg") {
		t.Fatalf("unexpected rel %q", rel)
	}
	abs, err := agentAttachmentResolve(rel)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(abs, root) {
		t.Fatalf("resolved %q escapes root %q", abs, root)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(src) {
		t.Fatalf("content mismatch")
	}
	// 会话子目录自动创建且 0755（root agent ↔ 桌面用户读，TUI 缓存同款）
	info, err := os.Stat(filepath.Dir(abs))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("session dir mode = %v", info.Mode().Perm())
	}
	// 同 base 重写为幂等覆盖（重投递/重复落账不炸）
	if _, err = agentAttachmentPersist("sess-abc", "m1-0", ".jpg", []byte("v2")); err != nil {
		t.Fatalf("re-persist: %v", err)
	}
	again, _ := agentAttachmentResolve(rel)
	b, _ := os.ReadFile(again)
	if string(b) != "v2" {
		t.Fatalf("overwrite semantics broken: %q", string(b))
	}
}

func TestAgentAttachmentPersistRejectsBadInput(t *testing.T) {
	agentAttachmentPersistTestEnv(t)
	if _, err := agentAttachmentPersist("sess", "m1", ".exe", []byte("x")); err == nil {
		t.Fatal("unsupported ext accepted")
	}
	if _, err := agentAttachmentPersist("", "m1", ".png", []byte("x")); err == nil {
		t.Fatal("empty session accepted")
	}
	if _, err := agentAttachmentPersist("sess", "", ".png", []byte("x")); err == nil {
		t.Fatal("empty base accepted")
	}
	if _, err := agentAttachmentPersist("sess", "m1", ".png", make([]byte, agentAttachmentFileMaxBytes+1)); err == nil {
		t.Fatal("oversize file accepted")
	}
}

func TestAgentAttachmentResolveRejectsEscape(t *testing.T) {
	agentAttachmentPersistTestEnv(t)
	for _, bad := range []string{
		"../escape.jpg",
		"sess/../../escape.jpg",
		"/etc/passwd",
		"",
		"sess/./sub/../../out.jpg",
	} {
		if _, err := agentAttachmentResolve(bad); err == nil {
			t.Fatalf("escape ref %q accepted", bad)
		}
	}
	// 未存在但合法形状 → 穿透守卫后 Stat 报错（区分穿越拒绝与不存在）
	if _, err := agentAttachmentResolve("sess/missing.jpg"); err == nil {
		t.Fatal("missing file should not resolve")
	}
}

func TestAgentAttachmentSweepEvictsOldestSession(t *testing.T) {
	agentAttachmentPersistTestEnv(t)
	if _, err := agentAttachmentPersist("old-sess", "m1", ".png", []byte("old")); err != nil {
		t.Fatalf("persist old: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := agentAttachmentPersist("new-sess", "m1", ".png", []byte("new")); err != nil {
		t.Fatalf("persist new: %v", err)
	}
	// 把 old 会话目录 mtime 拨早，保证「最旧优先」可断言
	root, _ := agentAttachmentCacheRoot()
	old := filepath.Join(root, "old-sess")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	// 帽子=新会话恰好容纳（"new"=3 字节）：最旧会话必被驱逐、最新必保留
	if err := agentAttachmentSweepCap(3); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("oldest session not evicted")
	}
	if _, err := os.Stat(filepath.Join(root, "new-sess")); err != nil {
		t.Fatalf("newest session evicted too aggressively: %v", err)
	}
}
