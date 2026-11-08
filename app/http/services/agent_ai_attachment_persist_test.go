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

// 平台缓存根策略（Windows 103 根因修复）：windows 的服务账户(SYSTEM)与用户
// runtime 进程 %USERPROFILE% 不同，home 侧缓存根在 core/runtime 两进程间不
// 共享（runtimepath 无 windows console-user 解析）——统一落 CoreDataDir。
// goos 注入规避「GOOS 后缀测试在单平台静默跳过」盲区（2026-06-10 事故守则）。
func TestAgentAttachmentCacheRootPlatformPolicy(t *testing.T) {
	dataRoot := t.TempDir()
	homeRoot := t.TempDir()

	// (a) windows：恒 CoreDataDir（env 已重定向），与 home 无关
	t.Setenv("ALIANG_DATA_DIR", dataRoot)
	t.Setenv("HOME", homeRoot)
	t.Setenv("USERPROFILE", homeRoot)
	got, err := agentAttachmentCacheRootFor("windows")
	if err != nil {
		t.Fatalf("windows root: %v", err)
	}
	if want := filepath.Join(dataRoot, "aliang-attachments"); got != want {
		t.Fatalf("windows root = %q want %q", got, want)
	}

	// (b) darwin：home 侧语义不变
	got, err = agentAttachmentCacheRootFor("darwin")
	if err != nil {
		t.Fatalf("darwin root: %v", err)
	}
	if want := filepath.Join(homeRoot, "aliang-attachments"); got != want {
		t.Fatalf("darwin root = %q want %q", got, want)
	}

	// (c) env 覆盖恒最高（部署级逃生口），压过平台决策
	override := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_PERSIST_DIR", override)
	for _, goos := range []string{"windows", "darwin"} {
		got, err = agentAttachmentCacheRootFor(goos)
		if err != nil {
			t.Fatalf("%s root under env override: %v", goos, err)
		}
		if got != override {
			t.Fatalf("%s root under env override = %q want %q", goos, got, override)
		}
	}
}

// home 侧旧根一次性迁移（幂等）：切 ProgramData 根之前落的历史文件搬进新根；
// 新根已有的同名文件保留新根版本不被覆盖；重复执行不炸不重复拷。
func TestMigrateLegacyAttachmentCacheRoot(t *testing.T) {
	legacy := t.TempDir()
	newRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacy, "sess-a"), 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	writeFile := func(path string, content []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	writeFile(filepath.Join(legacy, "sess-a", "m-old.jpg"), []byte("legacy-old"))
	writeFile(filepath.Join(legacy, "sess-b", "m-new.jpg"), []byte("legacy-new"))
	// 新根已有同名文件（版本较新）：迁移不得覆盖
	writeFile(filepath.Join(newRoot, "sess-a", "m-old.jpg"), []byte("newer-version"))

	migrateLegacyAttachmentCacheRoot("windows", newRoot, legacy)
	migrateLegacyAttachmentCacheRoot("windows", newRoot, legacy) // 幂等重跑

	b, err := os.ReadFile(filepath.Join(newRoot, "sess-b", "m-new.jpg"))
	if err != nil || string(b) != "legacy-new" {
		t.Fatalf("legacy-only file not migrated: %v %q", err, string(b))
	}
	b, err = os.ReadFile(filepath.Join(newRoot, "sess-a", "m-old.jpg"))
	if err != nil || string(b) != "newer-version" {
		t.Fatalf("newer file clobbered by migration: %v %q", err, string(b))
	}

	// darwin：迁移为 no-op（平台无撕裂，home 语义不动）
	writeFile(filepath.Join(legacy, "sess-c", "m-darwin.jpg"), []byte("x"))
	migrateLegacyAttachmentCacheRoot("darwin", newRoot, legacy)
	if _, err := os.Stat(filepath.Join(newRoot, "sess-c")); !os.IsNotExist(err) {
		t.Fatalf("darwin migration must be a no-op")
	}
}
