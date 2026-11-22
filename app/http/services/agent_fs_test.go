package services

// 网页终端 FS 面测试(阶段 3 验收:spec §11 遍历攻击用例全拒)。守卫根经
// agentFSRootOverride 注入 t.TempDir,不触碰开发机真实 home;macOS 下
// t.TempDir 位于 /var 符号链接之后,webTerminalFSRoot 的根本身规范化在此
// 被隐式验收(不规范化则一切用例全拒)。

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// setAgentFSRoot 注入测试守卫根;测试间串行(包内无 t.Parallel,同仓纪律)。
func setAgentFSRoot(t *testing.T, dir string) {
	t.Helper()
	agentFSRootOverride = dir
	t.Cleanup(func() { agentFSRootOverride = "" })
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAgentFSResolveTraversalMatrix(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir() // 守卫根外的真实目录(symlink 靶)
	setAgentFSRoot(t, root)

	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "sub", "a.txt"), "x")
	writeTestFile(t, filepath.Join(outside, "secret.txt"), "s")
	// symlink 根内→根内(放行);根内→根外(必须拒)。
	if err := os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "in-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sub", "out-link")); err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	rejects := []string{
		"",                        // 空
		"..",                      // 根上一级
		"../..",                   // 深层上爬
		"../../etc/passwd",        // 子目录内经典穿越
		"sub/../../../etc/passwd", // 嵌套 ../ 塌缩后出根
		"/etc/passwd",             // 绝对路径(根外)
		"/etc",                    // 绝对路径目录(根外)
		"sub/out-link",            // symlink→根外(Eval 后检拦)
		"sub/out-link/secret.txt", // 经根外 symlink 深入
	}
	for _, raw := range rejects {
		if _, err := agentFSResolve(raw); err == nil {
			t.Fatalf("穿越用例 %q 应被守卫拒绝", raw)
		}
	}
	// 穿越拒绝的哨兵可识别(handler 映射 CodeForbidden 依赖它)
	if _, err := agentFSResolve("../../etc/passwd"); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("穿越拒绝应为 ErrAgentFSEscapeRoot, got %v", err)
	}

	accepts := []struct{ raw, want string }{
		{".", canonicalRoot}, // 缺省根
		{"sub/a.txt", filepath.Join(canonicalRoot, "sub", "a.txt")},                                  // 相对
		{"sub/./a.txt", filepath.Join(canonicalRoot, "sub", "a.txt")},                                // Clean 塌缩 ./
		{"sub/../plain.txt", filepath.Join(canonicalRoot, "plain.txt")},                              // a/../b 在根内塌缩(Clean 后不出根)+父目录 Eval 回退
		{"in-link/a.txt", filepath.Join(canonicalRoot, "sub", "a.txt")},                              // symlink 根内→根内
		{filepath.Join(canonicalRoot, "sub", "a.txt"), filepath.Join(canonicalRoot, "sub", "a.txt")}, // 根内绝对路径
	}
	for _, tc := range accepts {
		got, err := agentFSResolve(tc.raw)
		if err != nil {
			t.Fatalf("合法用例 %q 不应拒: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("用例 %q 解析为 %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestAgentFSListSortsDirsFirstAndShapes(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "zdir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "b.txt"), "12345")
	writeTestFile(t, filepath.Join(root, "a.txt"), "1")
	if err := os.Symlink("a.txt", filepath.Join(root, "s-link")); err != nil {
		t.Fatal(err)
	}

	svc := &AgentService{}
	resolved, entries, err := svc.AgentFSList(".")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if resolved == "" {
		t.Fatal("应返回守卫解析后的真身路径")
	}
	if len(entries) != 5 {
		t.Fatalf("应 5 条目, got %d: %v", len(entries), entries)
	}
	wantOrder := []struct{ name, typ string }{
		{"adir", "dir"}, {"zdir", "dir"},
		{"a.txt", "file"}, {"b.txt", "file"}, {"s-link", "symlink"},
	}
	for i, want := range wantOrder {
		if entries[i]["name"] != want.name || entries[i]["type"] != want.typ {
			t.Fatalf("第 %d 条应为 {%s %s}, got %v", i, want.name, want.typ, entries[i])
		}
	}
	byName := map[string]map[string]interface{}{}
	for _, e := range entries {
		byName[e["name"].(string)] = e
	}
	if byName["adir"]["size"] != int64(0) {
		t.Fatalf("目录 size 应 0, got %v", byName["adir"]["size"])
	}
	if byName["b.txt"]["size"] != int64(5) {
		t.Fatalf("b.txt size 应 5, got %v", byName["b.txt"]["size"])
	}
	if byName["a.txt"]["mtime"] == "" || byName["a.txt"]["mode"] == "" {
		t.Fatalf("条目应含 mtime/mode: %v", byName["a.txt"])
	}

	// 不存在目录 → ErrNotExist(handler 映射 103)
	if _, _, err := svc.AgentFSList("no-such-dir"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在目录应 ErrNotExist, got %v", err)
	}
	// 穿越 dir → 守卫哨兵
	if _, _, err := svc.AgentFSList("../.."); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("穿越 dir 应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

func TestAgentFSReadTextTruncateBinaryAndMissing(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	writeTestFile(t, filepath.Join(root, "hello.txt"), "hello world")
	writeTestFile(t, filepath.Join(root, "bin.dat"), "a\x00b")

	svc := &AgentService{}
	resolved, content, truncated, binary, err := svc.AgentFSRead("hello.txt", 1<<20)
	if err != nil || content != "hello world" || truncated || binary || resolved == "" {
		t.Fatalf("全量文本读不符: content=%q truncated=%v binary=%v err=%v", content, truncated, binary, err)
	}
	_, content, truncated, _, err = svc.AgentFSRead("hello.txt", 5)
	if err != nil || content != "hello" || !truncated {
		t.Fatalf("截断读不符: content=%q truncated=%v err=%v", content, truncated, err)
	}
	_, _, _, binary, err = svc.AgentFSRead("bin.dat", 1<<20)
	if err != nil || !binary {
		t.Fatalf("NUL 文件应判 binary, got binary=%v err=%v", binary, err)
	}
	if _, _, _, _, err := svc.AgentFSRead("nope.txt", 1<<20); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在应 ErrNotExist, got %v", err)
	}
	if _, _, _, _, err := svc.AgentFSRead("../x", 1<<20); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("穿越应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

func TestAgentFSOpenForDownload(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	writeTestFile(t, filepath.Join(root, "f.txt"), "0123456789")
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatal(err)
	}

	svc := &AgentService{}
	f, info, resolved, err := svc.AgentFSOpen("f.txt")
	if err != nil || f == nil || info == nil || resolved == "" {
		t.Fatalf("open 快乐路径不符: err=%v", err)
	}
	_ = f.Close()
	if info.Size() != 10 {
		t.Fatalf("size 应 10, got %d", info.Size())
	}
	if _, _, _, err := svc.AgentFSOpen("d"); err == nil {
		t.Fatal("目录应拒(ServeContent 回目录无意义)")
	}
	if _, _, _, err := svc.AgentFSOpen("nope"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在应 ErrNotExist, got %v", err)
	}
	if _, _, _, err := svc.AgentFSOpen("../x"); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("穿越应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

func TestAgentFSUploadSanitizesRenamesAndGuards(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// ai=nil:审计 no-op(零 journal 落盘,测试纪律);upload 不消费 vault。
	svc := &AgentService{}

	// 路径成分剥除:../../x.txt → 只落 base x.txt
	p, err := svc.AgentFSUpload(".", "../../evil.txt", []byte("x"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if filepath.Base(p) != "evil.txt" {
		t.Fatalf("路径成分应剥除, got %q", p)
	}
	if _, err := os.Stat(filepath.Join(root, "evil.txt")); err != nil {
		t.Fatalf("应落在守卫根: %v", err)
	}

	// Windows 保留字符/控制字符 → 替换为 _;空文件 OK;0600 落盘
	for raw, want := range map[string]string{
		"con:":   "con_",
		"a<b":    "a_b",
		"c\x01d": "c_d",
		"e\"f":   "e_f",
	} {
		p, err := svc.AgentFSUpload(".", raw, []byte("z"))
		if err != nil {
			t.Fatalf("sanitize(%q): %v", raw, err)
		}
		if filepath.Base(p) != want {
			t.Fatalf("sanitize(%q) → %q, want %q", raw, filepath.Base(p), want)
		}
	}
	p, err = svc.AgentFSUpload(".", "empty.txt", nil)
	if err != nil {
		t.Fatalf("空文件上传: %v", err)
	}
	info, err := os.Stat(p)
	if err != nil || info.Size() != 0 {
		t.Fatalf("空文件应 0 字节存在: err=%v size=%d", err, info.Size())
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("落盘权限应 0600, got %v", info.Mode().Perm())
	}

	// 同名冲突 → 扩展名前追加 _HHMMSS;同秒第三笔再撞 → _2 序号兜底,
	// 三份内容各自完好(同秒覆盖回归钉,评审 Issue 2)。
	paths := make([]string, 0, 3)
	for i, content := range []string{"1", "2", "3"} {
		p, err := svc.AgentFSUpload(".", "dup.txt", []byte(content))
		if err != nil {
			t.Fatalf("第 %d 次 dup: %v", i+1, err)
		}
		paths = append(paths, p)
	}
	if paths[0] == paths[1] || paths[1] == paths[2] || paths[0] == paths[2] {
		t.Fatalf("同秒三笔同名上传应各得其所: %v", paths)
	}
	if filepath.Base(paths[0]) != "dup.txt" {
		t.Fatalf("首笔应原名, got %q", paths[0])
	}
	suffixed := regexp.MustCompile(`^dup_\d{6}(_\d+)?\.txt$`)
	for _, p := range paths[1:] {
		if !suffixed.MatchString(filepath.Base(p)) {
			t.Fatalf("冲突名应 dup[_HHMMSS[_N]].txt 形态, got %q", filepath.Base(p))
		}
	}
	for i, want := range []string{"1", "2", "3"} {
		if raw, _ := os.ReadFile(paths[i]); string(raw) != want {
			t.Fatalf("第 %d 笔内容不应被覆盖, got %q", i+1, raw)
		}
	}

	// 子目录上传(经守卫;期望路径按规范化守卫根比对——webTerminalFSRoot
	// 会 EvalSymlinks,macOS t.TempDir 在 /var 符号链接之后)+ dir 穿越/
	// 非法文件名拒绝
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = svc.AgentFSUpload("sub", "n.txt", []byte("n")); err != nil {
		t.Fatalf("子目录上传: %v", err)
	} else if p != filepath.Join(canonicalRoot, "sub", "n.txt") {
		t.Fatalf("应落在 sub/, got %q", p)
	}
	if _, err := svc.AgentFSUpload("../..", "x.txt", []byte("x")); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("dir 穿越应拒, got %v", err)
	}
	for _, bad := range []string{"..", ".", ""} {
		if _, err := svc.AgentFSUpload(".", bad, []byte("x")); !errors.Is(err, ErrAgentFSBadFilename) {
			t.Fatalf("非法文件名 %q 应 ErrAgentFSBadFilename, got %v", bad, err)
		}
	}
}
