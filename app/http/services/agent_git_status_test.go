package services

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifyGitStatusCode(t *testing.T) {
	cases := []struct{ code, want string }{
		{"??", "added"},    // untracked
		{" M", "modified"}, // unstaged modify
		{"M ", "modified"}, // staged modify
		{"MM", "modified"}, // staged + unstaged
		{"A ", "added"},    // staged new
		{"AM", "added"},    // staged new + further modify → added
		{" D", "deleted"},  // unstaged delete
		{"D ", "deleted"},  // staged delete
		{"RM", "modified"}, // rename
		{"CM", "modified"}, // copy
		{"", "modified"},   // too short → safe default
	}
	for _, c := range cases {
		if got := classifyGitStatusCode(c.code); got != c.want {
			t.Errorf("classifyGitStatusCode(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

func TestGitDirectoryStatus(t *testing.T) {
	dir := "/repo/src"
	// direct child changed
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/src/a.ts": "modified"}); got != "modified" {
		t.Errorf("child file: got %q, want modified", got)
	}
	// deeply nested changed
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/src/nested/b.ts": "modified"}); got != "modified" {
		t.Errorf("nested file: got %q, want modified", got)
	}
	// sibling path with overlapping name prefix must NOT match
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/srcX/c.ts": "modified"}); got != "clean" {
		t.Errorf("sibling name prefix: got %q, want clean", got)
	}
	// outside the dir
	if got := gitDirectoryStatus(dir, map[string]string{"/repo/readme.md": "modified"}); got != "clean" {
		t.Errorf("outside: got %q, want clean", got)
	}
	// empty
	if got := gitDirectoryStatus(dir, map[string]string{}); got != "clean" {
		t.Errorf("empty: got %q, want clean", got)
	}
	// trailing slash tolerated
	if got := gitDirectoryStatus(dir+"/", map[string]string{"/repo/src/a.ts": "modified"}); got != "modified" {
		t.Errorf("trailing slash: got %q, want modified", got)
	}
}

func TestLoadGitStatusMap(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := realPath(t, t.TempDir())
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "clean.go")
	run("commit", "-m", "init")
	// now modify the committed file + add an untracked one
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte("package z\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := loadGitStatusMap(dir)
	if m[filepath.Join(dir, "clean.go")] != "modified" {
		t.Errorf("clean.go: got %q, want modified", m[filepath.Join(dir, "clean.go")])
	}
	if m[filepath.Join(dir, "new.go")] != "added" {
		t.Errorf("new.go: got %q, want added", m[filepath.Join(dir, "new.go")])
	}
	// non-git dir → empty map (graceful)
	if got := loadGitStatusMap(t.TempDir()); len(got) != 0 {
		t.Errorf("non-git dir: got %v, want empty", got)
	}
}

// 2026-10-28 审计 #3:中文文件名的 git 状态必须能对上 ReadDir 的 UTF-8 名。
// core.quotePath 默认会把非 ASCII 路径八进制转义,agentRunGit 现以
// -c core.quotepath=false 关掉;此测试钉住回归(哪天有人拿掉那个 -c 先红)。
func TestLoadGitStatusMapChineseFilenames(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := realPath(t, t.TempDir())
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	run("config", "commit.gpgsign", "false")

	const (
		modifiedName = "文档.md"
		addedName    = "需求说明.txt"
		nestedDir    = "源代码"
	)
	if err := os.WriteFile(filepath.Join(dir, modifiedName), []byte("# old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")

	if err := os.WriteFile(filepath.Join(dir, modifiedName), []byte("# new\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, addedName), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, nestedDir)
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := loadGitStatusMap(dir)
	if got := m[filepath.Join(dir, modifiedName)]; got != "modified" {
		t.Errorf("中文修改文件: got %q, want modified (map=%v)", got, m)
	}
	if got := m[filepath.Join(dir, addedName)]; got != "added" {
		t.Errorf("中文新增文件: got %q, want added (map=%v)", got, m)
	}
	// 未跟踪目录 `?? dir/`(quotePath 关闭后为 UTF-8)→ 目录行直接命中 added;
	// 普通聚合(目录下有改动文件)路径由 gitDirectoryStatus 既有测试覆盖。
	if got := agentDirectoryGitStatus(nested, m); got != "added" {
		t.Errorf("未跟踪中文目录: got %q, want added (map=%v)", got, m)
	}
	// 中文修改文件仍然聚合出目录 glow:建一个已跟踪目录下的改动。
	// (文档.md 在仓库根,这里补验聚合路径没被直接命中分支挡住。)
	if got := gitDirectoryStatus(dir, m); got != "modified" {
		t.Errorf("仓库根聚合: got %q, want modified", got)
	}
}

// --- repo 级缓存 + single-flight(2026-10-30 文件浏览器慢治理)---
// loadGitStatusMap 现按 repo root 缓存 porcelain 结果(5s TTL,在飞 single-flight):
// 子目录 listing 复用同一次全仓扫描,不再每次展开/刷新都重扫。以下钉子用
// 计数桩包住真实 git 验证:status 调用次数、子目录共享、TTL 过期重扫、
// 并发 single-flight、非 git 目录不入缓存。

// 计数桩:包住原 agentRunGit,返回 status 调用次数指针;测试结束自动还原。
func withCountedGitRun(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := agentRunGitFn
	agentRunGitFn = func(dir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "status" {
			calls++
		}
		return orig(dir, args...)
	}
	t.Cleanup(func() { agentRunGitFn = orig })
	return &calls
}

// 清空缓存 + 记录 TTL/桩还原。
func resetGitStatusCacheForTest(t *testing.T) {
	t.Helper()
	drain := func() {
		gitStatusCacheMu.Lock()
		gitStatusCache = map[string]*gitStatusCacheEntry{}
		gitStatusCacheMu.Unlock()
	}
	drain()
	origTTL := gitStatusCacheTTL
	t.Cleanup(func() {
		drain()
		gitStatusCacheTTL = origTTL
	})
}

// realPath 解析符号链接:t.TempDir() 在 macOS 走 /var→/private/var,而
// git rev-parse --show-toplevel 返回真路径——缓存的键空间是真路径,断言
// 查找必须用同一拼写(生产用户目录无此差异)。
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newGitRepoWithChanges(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := realPath(t, t.TempDir())
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "clean.go"), []byte("package x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("package y\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadGitStatusMapCacheHitWithinTTL(t *testing.T) {
	dir := newGitRepoWithChanges(t)
	resetGitStatusCacheForTest(t)
	statusCalls := withCountedGitRun(t)

	first := loadGitStatusMap(dir)
	second := loadGitStatusMap(dir)

	if *statusCalls != 1 {
		t.Fatalf("status 调用次数 = %d, want 1(TTL 内第二次必须命中缓存)", *statusCalls)
	}
	if len(first) != len(second) || first[filepath.Join(dir, "dirty.go")] != "added" {
		t.Fatalf("缓存命中结果不一致: first=%v second=%v", first, second)
	}
}

func TestLoadGitStatusMapSubdirSharesRootEntry(t *testing.T) {
	dir := newGitRepoWithChanges(t)
	resetGitStatusCacheForTest(t)
	statusCalls := withCountedGitRun(t)

	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	sub = realPath(t, sub)
	mSub := loadGitStatusMap(sub)
	mRoot := loadGitStatusMap(dir)

	if *statusCalls != 1 {
		t.Fatalf("子目录+根共 %d 次 status, want 1(共享 root 条目)", *statusCalls)
	}
	// repo 全量 map:子目录调用也能看到根下的改动键(绝对路径同一空间)。
	if mSub[filepath.Join(dir, "dirty.go")] != "added" || mRoot[filepath.Join(dir, "dirty.go")] != "added" {
		t.Fatalf("map 缺 dirty.go 键: sub=%v root=%v", mSub, mRoot)
	}
}

func TestLoadGitStatusMapTTLExpiryRescans(t *testing.T) {
	dir := newGitRepoWithChanges(t)
	resetGitStatusCacheForTest(t)
	gitStatusCacheTTL = 1 * time.Millisecond
	statusCalls := withCountedGitRun(t)

	if m := loadGitStatusMap(dir); m[filepath.Join(dir, "dirty.go")] != "added" {
		t.Fatalf("首次扫描缺 dirty.go: %v", m)
	}
	// TTL 内改文件 → 缓存仍旧;过期后重扫必须看到新文件。
	time.Sleep(5 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "fresh.go"), []byte("package f\n"), 0644); err != nil {
		t.Fatal(err)
	}
	m := loadGitStatusMap(dir)
	if *statusCalls != 2 {
		t.Fatalf("status 调用次数 = %d, want 2(TTL 过期必须重扫)", *statusCalls)
	}
	if m[filepath.Join(dir, "fresh.go")] != "added" {
		t.Fatalf("过期重扫未看到新文件: %v", m)
	}
}

func TestLoadGitStatusMapSingleFlight(t *testing.T) {
	dir := newGitRepoWithChanges(t)
	resetGitStatusCacheForTest(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	calls := 0
	orig := agentRunGitFn
	agentRunGitFn = func(rdir string, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "status" {
			calls++
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return " M dirty.go\n", nil
		}
		return orig(rdir, args...)
	}
	t.Cleanup(func() { agentRunGitFn = orig })

	type result struct {
		m   map[string]string
		err bool
	}
	done := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			m := loadGitStatusMap(dir)
			done <- result{m: m}
		}()
	}
	<-entered          // runner 已进入(一次)
	close(release)     // 放行
	r1, r2 := <-done, <-done
	if calls != 1 {
		t.Fatalf("并发下 status 调用 = %d, want 1(single-flight)", calls)
	}
	if r1.m[filepath.Join(dir, "dirty.go")] != "modified" || r2.m[filepath.Join(dir, "dirty.go")] != "modified" {
		t.Fatalf("两个并发调用结果不一致: %v / %v", r1.m, r2.m)
	}
}

func TestLoadGitStatusMapNonGitNotCached(t *testing.T) {
	resetGitStatusCacheForTest(t)
	statusCalls := withCountedGitRun(t)

	plain := t.TempDir() // 非 git 目录
	if got := loadGitStatusMap(plain); len(got) != 0 {
		t.Fatalf("非 git 目录: got %v, want empty", got)
	}
	if got := loadGitStatusMap(plain); len(got) != 0 {
		t.Fatalf("非 git 目录第二次: got %v, want empty", got)
	}
	if *statusCalls != 0 {
		t.Fatalf("非 git 目录不应触发 status, got %d 次", *statusCalls)
	}
}
