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
	"strings"
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
	// 会 EvalSymlinks,macOS t.TempDir 在 /var 符号链接之后;信封边界 ToSlash,
	// 故比对也用斜杠形态)+ dir 穿越/非法文件名拒绝
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if p, err = svc.AgentFSUpload("sub", "n.txt", []byte("n")); err != nil {
		t.Fatalf("子目录上传: %v", err)
	} else if p != filepath.ToSlash(filepath.Join(canonicalRoot, "sub", "n.txt")) {
		t.Fatalf("应落在 sub/(信封斜杠形态), got %q", p)
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

// TestAgentFSEnvelopePathsAreSlashSeparated 钉信封边界 ToSlash 契约(导航补全
// 批评审 Fix 1):list/read/upload 三信封回给前端的 path 一律 '/' 分隔——前端
// cd 闸/面包屑/上级全按 '/' 拼接,Windows 原生 '\' 路径会让导航面整体哑火。
// 内部 resolved 不动(os.Open/ReadDir 仍原生);download 的 resolved 只进服务
// 端审计不入信封,不在本契约内。macOS 上 '\' 本就不出现,此测试主要钉住
// Windows 跑测试时的行为。
func TestAgentFSEnvelopePathsAreSlashSeparated(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "hello.txt"), "hi")

	svc := &AgentService{}

	assertNoBackslash := func(where, p string) {
		t.Helper()
		if p == "" {
			t.Fatalf("%s 应返回非空 path", where)
		}
		if strings.Contains(p, `\`) {
			t.Fatalf("%s 信封 path 应 '/' 分隔(ToSlash 边界), got %q", where, p)
		}
	}

	// list:根+子目录两跳(子目录导航=前端 cd 的直接输入)
	listRoot, _, err := svc.AgentFSList(".")
	if err != nil {
		t.Fatalf("list 根: %v", err)
	}
	assertNoBackslash("list(.)", listRoot)
	listSub, _, err := svc.AgentFSList("sub")
	if err != nil {
		t.Fatalf("list sub: %v", err)
	}
	assertNoBackslash("list(sub)", listSub)

	// read:回包 path(前端 preview 侧只认内容,但契约同一信封面)
	readPath, _, _, _, err := svc.AgentFSRead("hello.txt", 1<<20)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assertNoBackslash("read", readPath)

	// upload:回包终名 path(前端选中新文件只认清包 path)
	uploadPath, err := svc.AgentFSUpload("sub", "u.txt", []byte("u"))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	assertNoBackslash("upload", uploadPath)
	if _, err := os.Stat(filepath.Join(root, "sub", "u.txt")); err != nil {
		t.Fatalf("落盘应仍在原生路径: %v", err)
	}

	// mkdir/rename(文件管理批):信封契约同一信封面
	mkdirPath, err := svc.AgentFSMkdir("sub", "made")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	assertNoBackslash("mkdir", mkdirPath)
	writeTestFile(t, filepath.Join(root, "sub", "made", "seed.txt"), "s")
	renamePath, err := svc.AgentFSRename("sub/made", "seed.txt", "seeded.txt")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	assertNoBackslash("rename", renamePath)
}

// ——— 文件管理批:mkdir/rename/delete 服务语义(计划 Task B 攻击矩阵)———

func TestAgentFSMkdirSemantics(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &AgentService{}

	// 快乐路径:单层建目录,回守卫真身(斜杠信封),盘上目录真实存在。
	p, err := svc.AgentFSMkdir(".", "newdir")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if want := filepath.ToSlash(filepath.Join(canonicalRoot, "newdir")); p != want {
		t.Fatalf("mkdir 回包应真身斜杠形态, got %q want %q", p, want)
	}
	if info, err := os.Stat(filepath.Join(root, "newdir")); err != nil || !info.IsDir() {
		t.Fatalf("应真实建目录: err=%v", err)
	}

	// 消毒:Windows 保留字符/控制字符→_;`../` 路径成分被 Base 剥除(越根
	// 语义被清洗杀死,塌缩成无害 base 落守卫根内)。
	p, err = svc.AgentFSMkdir(".", "ba:d\x01name")
	if err != nil {
		t.Fatalf("mkdir 消毒名: %v", err)
	}
	if filepath.Base(p) != "ba_d_name" {
		t.Fatalf("消毒名不符: %q", p)
	}
	p, err = svc.AgentFSMkdir(".", "../escape")
	if err != nil {
		t.Fatalf("mkdir 穿越名: %v", err)
	}
	if filepath.Base(p) != "escape" || !strings.HasPrefix(p, filepath.ToSlash(canonicalRoot)) {
		t.Fatalf("穿越名应塌缩为根内 base, got %q", p)
	}
	for _, bad := range []string{"..", ".", "", "  "} {
		if _, err := svc.AgentFSMkdir(".", bad); !errors.Is(err, ErrAgentFSBadFilename) {
			t.Fatalf("非法名 %q 应 ErrAgentFSBadFilename, got %v", bad, err)
		}
	}

	// 重名 → already_exists 哨兵(handler 映射 409)。
	if _, err := svc.AgentFSMkdir(".", "newdir"); !errors.Is(err, ErrAgentFSAlreadyExists) {
		t.Fatalf("重名应 ErrAgentFSAlreadyExists, got %v", err)
	}

	// 父目录缺失 → 错误(单层 Mkdir 非 MkdirAll,前端在正确目录上下文操作)。
	if _, err := svc.AgentFSMkdir("no-such-parent", "inner"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("父目录缺失应 ErrNotExist, got %v", err)
	}

	// dir 穿越 → 守卫哨兵。
	if _, err := svc.AgentFSMkdir("../..", "x"); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("dir 穿越应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

func TestAgentFSRenameSemantics(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	svc := &AgentService{}
	writeTestFile(t, filepath.Join(root, "old.txt"), "payload")

	// 快乐路径:同目录改名,内容随迁,旧名消失。
	p, err := svc.AgentFSRename(".", "old.txt", "renamed.txt")
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if want := filepath.ToSlash(filepath.Join(canonicalRoot, "renamed.txt")); p != want {
		t.Fatalf("rename 回包应新名真身斜杠形态, got %q want %q", p, want)
	}
	if raw, err := os.ReadFile(filepath.Join(root, "renamed.txt")); err != nil || string(raw) != "payload" {
		t.Fatalf("内容应随迁: err=%v content=%q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("旧名应消失, got %v", err)
	}

	// 新名消毒:保留字符→_(上传同款清洗器复用)。
	writeTestFile(t, filepath.Join(root, "san.txt"), "x")
	p, err = svc.AgentFSRename(".", "san.txt", "b:d.txt")
	if err != nil {
		t.Fatalf("rename 消毒名: %v", err)
	}
	if filepath.Base(p) != "b_d.txt" {
		t.Fatalf("新名消毒不符: %q", p)
	}

	// 旧名缺失 → ErrNotExist(handler 103)。
	if _, err := svc.AgentFSRename(".", "ghost.txt", "x.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("旧名缺失应 ErrNotExist, got %v", err)
	}

	// 新名已存在 → already_exists(os.Rename 会静默覆盖,前置双 Stat 显式拒)。
	writeTestFile(t, filepath.Join(root, "a.txt"), "a")
	writeTestFile(t, filepath.Join(root, "b.txt"), "b")
	if _, err := svc.AgentFSRename(".", "a.txt", "b.txt"); !errors.Is(err, ErrAgentFSAlreadyExists) {
		t.Fatalf("新名已存在应 ErrAgentFSAlreadyExists, got %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(raw) != "b" {
		t.Fatalf("已存在目标不得被覆盖, got %q", raw)
	}

	// 非法旧名 → 清洗哨兵;dir 穿越 → 守卫哨兵(消毒后的 base 理论不可越根,
	// dir 是唯一穿越入口)。
	if _, err := svc.AgentFSRename(".", "..", "x"); !errors.Is(err, ErrAgentFSBadFilename) {
		t.Fatalf("非法旧名应 ErrAgentFSBadFilename, got %v", err)
	}
	if _, err := svc.AgentFSRename("../..", "a.txt", "b.txt"); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("dir 穿越应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

func TestAgentFSDeleteSemantics(t *testing.T) {
	root := t.TempDir()
	setAgentFSRoot(t, root)
	svc := &AgentService{}
	writeTestFile(t, filepath.Join(root, "f.txt"), "x")

	// 文件删除。
	p, err := svc.AgentFSDelete("f.txt")
	if err != nil || p == "" {
		t.Fatalf("delete 文件: p=%q err=%v", p, err)
	}
	if _, err := os.Stat(filepath.Join(root, "f.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("文件应已删, got %v", err)
	}

	// 目录递归删除(嵌套+深层文件一锅端;前端双段强确认前置,服务层 RemoveAll)。
	if err := os.MkdirAll(filepath.Join(root, "tree", "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "tree", "nested", "deep", "leaf.txt"), "l")
	if _, err := svc.AgentFSDelete("tree"); err != nil {
		t.Fatalf("delete 目录递归: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tree")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("目录树应整体删除, got %v", err)
	}

	// 不存在 → ErrNotExist(handler 103)。
	if _, err := svc.AgentFSDelete("ghost"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("不存在应 ErrNotExist, got %v", err)
	}

	// 守卫根本身 → 拒(浏览面锚点,误删=首页没了)。
	if _, err := svc.AgentFSDelete("."); !errors.Is(err, ErrAgentFSRootDelete) {
		t.Fatalf("删守卫根应 ErrAgentFSRootDelete, got %v", err)
	}
	// 根的绝对路径形态同样拒(resolve 后与根相等;守卫根是规范化后的真身,
	// macOS t.TempDir 在 /var 符号链接之后,须喂 canonical 形态)。
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AgentFSDelete(canonicalRoot); !errors.Is(err, ErrAgentFSRootDelete) {
		t.Fatalf("删守卫根(绝对路径)应 ErrAgentFSRootDelete, got %v", err)
	}

	// 穿越 → 守卫哨兵。
	if _, err := svc.AgentFSDelete("../.."); !errors.Is(err, ErrAgentFSEscapeRoot) {
		t.Fatalf("穿越应 ErrAgentFSEscapeRoot, got %v", err)
	}
}

// TestAgentFSMkdirPermissionDenied EACCES 路径(Task C 评审前置项):chmod 000
// 的父目录下 mkdir → os.Stat(前置存在检查)吃 EACCES 上抛 fs.ErrPermission
// 语义错误,handler writeAgentFSMutationError 兜底分支(writeAgentFSQueryError
// default)收敛 102「Path is not accessible」——不向 LAN 泄内部路径细节。root
// 运行时 chmod 000 不拦 root(CI 容器常以 root 跑),以 Geteuid 守卫跳过;
// 结束恢复 0755 双保险(TempDir 清理不依赖此权限,但留干净现场是好纪律)。
func TestAgentFSMkdirPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 运行时 chmod 000 不产生 EACCES,权限路径不可测,跳过")
	}
	root := t.TempDir()
	setAgentFSRoot(t, root)
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}

	svc := &AgentService{}
	_, err := svc.AgentFSMkdir("locked", "inner")
	if err == nil {
		t.Fatal("chmod 000 目录下 mkdir 应失败")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("应上抛权限语义错误(fs.ErrPermission,handler 兜底映射 102), got %v", err)
	}
}
