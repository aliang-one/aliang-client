package agentcore

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 工具层测试。
//
// 这一组是能力的硬下限：pi 对等的 8 个工具，语义必须与 pi 一致，尤其是 Edit
// 的 exact-oldText（唯一、非重叠、在原文里数次数、CRLF/BOM 保真、NFKC 回退）。
// 围栏与 fail-closed 是安全底线，单独覆盖。

// ---------------------------------------------------------------------------
// 测试脚手架
// ---------------------------------------------------------------------------

// projectEnv 建一个临时项目目录，返回 ToolEnv 与目录路径。
func projectEnv(t *testing.T) (*ToolEnv, string) {
	t.Helper()
	dir := t.TempDir()
	// EvalSymlinks：macOS 上 /var 是 /private/var 的软链，不解析会让围栏判定
	// 与测试里拼出来的路径对不上。
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return &ToolEnv{ProjectPath: resolved}, resolved
}

// writeFile 在 root 下写一个文件（自动建父目录）。
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}

// readFile 读回文件内容。
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// 路径解析
// ---------------------------------------------------------------------------

// 2026-10-28 围栏上移：路径出界与否由审批层（outside-project-path 规则）判定，
// 执行层只负责解析。项目外路径不再在执行层硬拒——否则用户在手机上批准的越界
// 操作会在这里二次失败。
func TestResolveToolPathAllowsOutsidePaths(t *testing.T) {
	env, root := projectEnv(t)

	// 相对路径上跳出项目：解析为项目外的确定路径，不再报错。
	p, err := resolveToolPath(env, "../outside.txt")
	if err != nil {
		t.Fatalf("相对上跳路径不应被拒: %v", err)
	}
	if want := filepath.Clean(filepath.Join(root, "../outside.txt")); p != want {
		t.Fatalf("相对上跳解析结果 = %q, want %q", p, want)
	}

	// 项目外绝对路径：放行并保持原样。
	outside := filepath.Join(t.TempDir(), "outside.go")
	if p, err := resolveToolPath(env, outside); err != nil || p != outside {
		t.Fatalf("项目外绝对路径 = %q, %v, want %q", p, err, outside)
	}

	// ~ 展开：HOME 指向项目时 ~/inside.txt 解析进项目。
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	if p, err := resolveToolPath(env, "~/inside.txt"); err != nil || p != filepath.Join(root, "inside.txt") {
		t.Fatalf("~ 路径 = %q, %v, want %q", p, err, filepath.Join(root, "inside.txt"))
	}

	// 项目内相对路径与空路径行为不变。
	if p, err := resolveToolPath(env, "inside.txt"); err != nil || p != filepath.Join(root, "inside.txt") {
		t.Fatalf("项目内相对路径 = %q, %v", p, err)
	}
	if p, err := resolveToolPath(env, ""); err != nil || p != root {
		t.Fatalf("空路径应解析为项目根，得到 %q, %v", p, err)
	}

	// 未绑定项目时相对路径仍无法解析。
	if _, err := resolveToolPath(nil, "inside.txt"); err == nil {
		t.Fatal("未绑定项目时相对路径应当报错")
	}
}

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

func TestReadLineNumbers(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "one\ntwo\nthree\n")

	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "a.txt"}, env)
	if res.IsError {
		t.Fatalf("Read 失败: %s", res.Content)
	}
	for _, want := range []string{"     1\tone", "     2\ttwo", "     3\tthree"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("输出缺少行 %q\n实际:\n%s", want, res.Content)
		}
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	env, root := projectEnv(t)
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		sb.WriteString("line")
		sb.WriteByte(byte('0' + i%10))
		sb.WriteString("\n")
	}
	writeFile(t, root, "a.txt", sb.String())

	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt",
		"offset":    float64(3),
		"limit":     float64(2),
	}, env)
	if res.IsError {
		t.Fatalf("Read 失败: %s", res.Content)
	}
	if strings.Contains(res.Content, "\tline1") || strings.Contains(res.Content, "\tline2") {
		t.Errorf("offset=3 不应包含前两行:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "     3\tline3") || !strings.Contains(res.Content, "     4\tline4") {
		t.Errorf("应包含第 3、4 行:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "     5\tline5") {
		t.Errorf("limit=2 不应包含第 5 行:\n%s", res.Content)
	}
	// limit 命中时必须提示还有后续内容，否则模型会以为读完了。
	if !strings.Contains(res.Content, "仅显示第 3-4 行") {
		t.Errorf("截断提示缺失:\n%s", res.Content)
	}
}

func TestReadOffsetBeyondEOF(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "one\n")
	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "offset": float64(99),
	}, env)
	if !res.IsError {
		t.Fatal("offset 超出文件范围应当报错")
	}
	if !strings.Contains(res.Content, "超出文件范围") {
		t.Errorf("错误信息应说明超范围: %s", res.Content)
	}
}

func TestReadEmptyFile(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "empty.txt", "")
	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "empty.txt"}, env)
	if res.IsError {
		t.Fatalf("空文件不应报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "空文件") {
		t.Errorf("应提示是空文件: %s", res.Content)
	}
}

func TestReadBinaryRejected(t *testing.T) {
	env, root := projectEnv(t)
	// 前 8KB 里有 NUL 就按二进制处理。
	bin := append([]byte("header"), 0x00, 0x01, 0x02)
	bin = append(bin, []byte(strings.Repeat("x", 100))...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), bin, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "blob.bin"}, env)
	if !res.IsError {
		t.Fatal("二进制文件应当被拒绝")
	}
	if !strings.Contains(res.Content, "二进制") {
		t.Errorf("错误信息应说明是二进制: %s", res.Content)
	}
}

func TestReadDirectoryRejected(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "sub/x.txt", "x")
	res := (&readTool{}).Execute(context.Background(), map[string]interface{}{"file_path": "sub"}, env)
	if !res.IsError || !strings.Contains(res.Content, "LS") {
		t.Errorf("读目录应报错并指向 LS，实际: %v %s", res.IsError, res.Content)
	}
}

// ---------------------------------------------------------------------------
// LS
// ---------------------------------------------------------------------------

func TestLSListsEntries(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "12345")
	writeFile(t, root, "dir/b.txt", "x")

	res := (&lsTool{}).Execute(context.Background(), map[string]interface{}{}, env)
	if res.IsError {
		t.Fatalf("LS 失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "dir/") {
		t.Errorf("目录应以 / 结尾:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "a.txt (5 bytes)") {
		t.Errorf("文件应带字节数:\n%s", res.Content)
	}
}

// ---------------------------------------------------------------------------
// Glob
// ---------------------------------------------------------------------------

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "pkg/main.go", true}, // 基名匹配
		{"**/*.go", "a/b/c.go", true}, // ** 剥层
		{"**/*.go", "c.go", true},
		{"a/*.go", "a/b.go", true},
		{"a/*.go", "a/b/c.go", false}, // 单层不跨目录
		{"*.go", "main.ts", false},
	}
	for _, c := range cases {
		if got := globMatch(c.pattern, c.path); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestGlobToolSkipsNoiseDirs(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "main.go", "package main")
	writeFile(t, root, "pkg/util.go", "package pkg")
	writeFile(t, root, "node_modules/dep/index.go", "package dep")
	writeFile(t, root, "vendor/v.go", "package v")

	res := (&globTool{}).Execute(context.Background(), map[string]interface{}{"pattern": "**/*.go"}, env)
	if res.IsError {
		t.Fatalf("Glob 失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "main.go") || !strings.Contains(res.Content, "pkg/util.go") {
		t.Errorf("应命中项目内文件:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "node_modules") || strings.Contains(res.Content, "vendor") {
		t.Errorf("应跳过 node_modules / vendor:\n%s", res.Content)
	}
}

func TestGlobToolNoMatch(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "x")
	res := (&globTool{}).Execute(context.Background(), map[string]interface{}{"pattern": "*.rs"}, env)
	if res.IsError {
		t.Fatalf("无匹配不应报错: %s", res.Content)
	}
	if !strings.Contains(res.Content, "没有匹配") {
		t.Errorf("应说明无匹配: %s", res.Content)
	}
}

// ---------------------------------------------------------------------------
// Grep
// ---------------------------------------------------------------------------

func TestGrepTool(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.go", "package a\nfunc Needle() {}\n")
	writeFile(t, root, "b.go", "package b\nvar x = 1\n")
	writeFile(t, root, "skip/node_modules/dep.go", "func Needle() {}\n")

	res := (&grepTool{}).Execute(context.Background(), map[string]interface{}{"pattern": "Needle"}, env)
	if res.IsError {
		t.Fatalf("Grep 失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go:2:func Needle() {}") {
		t.Errorf("输出应为 路径:行号:内容:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "node_modules") {
		t.Errorf("应跳过 node_modules:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "b.go") {
		t.Errorf("不应命中不含该模式的文件:\n%s", res.Content)
	}
}

func TestGrepIgnoreCaseAndGlobFilter(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.go", "NEEDLE\n")
	writeFile(t, root, "b.ts", "NEEDLE\n")

	res := (&grepTool{}).Execute(context.Background(), map[string]interface{}{
		"pattern": "needle", "ignore_case": true, "glob": "*.go",
	}, env)
	if res.IsError {
		t.Fatalf("Grep 失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "a.go") {
		t.Errorf("ignore_case 应命中大写:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "b.ts") {
		t.Errorf("glob=*.go 不应命中 .ts:\n%s", res.Content)
	}
}

func TestGrepInvalidRegex(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "x")
	res := (&grepTool{}).Execute(context.Background(), map[string]interface{}{"pattern": "["}, env)
	if !res.IsError || !strings.Contains(res.Content, "正则") {
		t.Errorf("非法正则应报错: %v %s", res.IsError, res.Content)
	}
}

// ---------------------------------------------------------------------------
// Write
// ---------------------------------------------------------------------------

func TestWriteCreatesParentDirs(t *testing.T) {
	env, root := projectEnv(t)
	res := (&writeTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "deep/nested/new.txt",
		"content":   "hello",
	}, env)
	if res.IsError {
		t.Fatalf("Write 失败: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "deep/nested/new.txt")); got != "hello" {
		t.Errorf("内容 = %q, want hello", got)
	}
}

func TestWriteOverwrites(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "old content that is long")
	res := (&writeTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "content": "new",
	}, env)
	if res.IsError {
		t.Fatalf("Write 失败: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "new" {
		t.Errorf("覆盖后内容 = %q, want new", got)
	}
}

func TestWriteRejectsDirectory(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "sub/x", "x")
	res := (&writeTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "sub", "content": "x",
	}, env)
	if !res.IsError || !strings.Contains(res.Content, "目录") {
		t.Errorf("写目录应报错: %v %s", res.IsError, res.Content)
	}
}

// 2026-10-28 围栏上移：越界写入的拦截移到审批层（outside-project-path），
// 执行层在审批通过后照常落盘。
func TestWriteResolvesOutsidePath(t *testing.T) {
	env, _ := projectEnv(t)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	res := (&writeTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": outside, "content": "x",
	}, env)
	if res.IsError {
		t.Fatalf("审批通过的项目外写入不应被拒: %s", res.Content)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "x" {
		t.Fatalf("写回内容 = %q, %v, want x", got, err)
	}
}

// ---------------------------------------------------------------------------
// Edit
// ---------------------------------------------------------------------------

func TestEditReplacesUnique(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "before\nTARGET\nafter\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "TARGET", "new_string": "REPLACED",
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "before\nREPLACED\nafter\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditRequiresUnique(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "dup\ndup\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "dup", "new_string": "x",
	}, env)
	if !res.IsError {
		t.Fatal("不唯一的 old_string 必须报错")
	}
	if !strings.Contains(res.Content, "不唯一") {
		t.Errorf("错误信息应说明不唯一: %s", res.Content)
	}
	// 报错时文件必须原封不动。
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "dup\ndup\n" {
		t.Errorf("失败后文件被改动: %q", got)
	}
}

func TestEditReplaceAll(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "dup\ndup\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "dup", "new_string": "x", "replace_all": true,
	}, env)
	if res.IsError {
		t.Fatalf("replace_all 应成功: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "x\nx\n" {
		t.Errorf("内容 = %q, want x\\nx\\n", got)
	}
}

func TestEditCountsInOriginalNotMutatedBuffer(t *testing.T) {
	env, root := projectEnv(t)
	// 替换文本本身包含 old_string：如果在逐步修改的缓冲区里数次数，会无限自我
	// 匹配。pi 的做法是一次性在原文里定位全部位置。
	writeFile(t, root, "a.txt", "a\na\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "a", "new_string": "aa", "replace_all": true,
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	// 原文 2 处，各替换成 "aa"，结果必须是 4 个 a，不是 8 个。
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "aa\naa\n" {
		t.Errorf("内容 = %q, want aa\\naa\\n", got)
	}
}

func TestEditOverlappingPositionsNotDoubleReplaced(t *testing.T) {
	env, root := projectEnv(t)
	// "aaaa" 里 "aa" 有 3 个重叠位置（0/1/2）。replace_all 时必须非重叠替换。
	writeFile(t, root, "a.txt", "aaaa")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "aa", "new_string": "X", "replace_all": true,
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "XX" {
		t.Errorf("内容 = %q, want XX（非重叠替换）", got)
	}
}

func TestEditOverlappingCountsAsAmbiguous(t *testing.T) {
	env, root := projectEnv(t)
	// "aaa" 里 "aa" 有 2 个重叠位置 → 不唯一，必须报错而不是随便挑一个。
	writeFile(t, root, "a.txt", "aaa")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "aa", "new_string": "X",
	}, env)
	if !res.IsError {
		t.Fatal("重叠出现多次时非 replace_all 必须报错")
	}
}

func TestEditNotFound(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "hello\n")
	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "nope", "new_string": "x",
	}, env)
	if !res.IsError || !strings.Contains(res.Content, "找不到") {
		t.Errorf("未命中应报错并提示先 Read: %v %s", res.IsError, res.Content)
	}
}

func TestEditPreservesCRLF(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "one\r\ntwo\r\nthree\r\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "two", "new_string": "TWO",
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	got := readFile(t, filepath.Join(root, "a.txt"))
	if got != "one\r\nTWO\r\nthree\r\n" {
		t.Errorf("CRLF 未保真: %q", got)
	}
	// 不能引入裸 LF。
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("混入了裸 LF: %q", got)
	}
}

func TestEditAcceptsLFOldStringInCRLFFile(t *testing.T) {
	env, root := projectEnv(t)
	// 模型常给出 LF 的 old_string，而文件是 CRLF。必须先归一化再匹配。
	writeFile(t, root, "a.txt", "alpha\r\nbeta\r\ngamma\r\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "beta\ngamma", "new_string": "BETA\nGAMMA",
	}, env)
	if res.IsError {
		t.Fatalf("LF 的 old_string 应能命中 CRLF 文件: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "alpha\r\nBETA\r\nGAMMA\r\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditPreservesBOM(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", utf8BOM+"hello\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "hello", "new_string": "world",
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	got := readFile(t, filepath.Join(root, "a.txt"))
	if !strings.HasPrefix(got, utf8BOM) {
		t.Errorf("BOM 丢失: %q", got)
	}
	if got != utf8BOM+"world\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditFuzzyMatchFullWidth(t *testing.T) {
	env, root := projectEnv(t)
	// 文件里是全角 ＡＢＣ，模型给的是半角 ABC —— NFKC 归一化后应命中。
	writeFile(t, root, "a.txt", "前缀ＡＢＣ后缀\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "ABC", "new_string": "XYZ",
	}, env)
	if res.IsError {
		t.Fatalf("模糊匹配应成功: %s", res.Content)
	}
	got := readFile(t, filepath.Join(root, "a.txt"))
	if got != "前缀XYZ后缀\n" {
		t.Errorf("内容 = %q", got)
	}
	// 用了模糊匹配必须明确告知模型，否则它会以为精确命中。
	if !strings.Contains(res.Content, "NFKC") {
		t.Errorf("应提示发生了模糊匹配: %s", res.Content)
	}
}

func TestEditFuzzyAmbiguousRejected(t *testing.T) {
	env, root := projectEnv(t)
	// 模糊匹配下出现多处时不能批量替换（无法确认每处语义一致）。
	writeFile(t, root, "a.txt", "ＡＢＣ\nＡＢＣ\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "ABC", "new_string": "X", "replace_all": true,
	}, env)
	if !res.IsError {
		t.Fatal("模糊匹配多处命中必须报错")
	}
	if !strings.Contains(res.Content, "模糊匹配") {
		t.Errorf("错误信息应说明是模糊匹配歧义: %s", res.Content)
	}
}

func TestEditRejectsNoopAndEmptyOld(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "x\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "x", "new_string": "x",
	}, env)
	if !res.IsError {
		t.Error("old == new 应报错")
	}

	res = (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "", "new_string": "y",
	}, env)
	if !res.IsError {
		t.Error("空 old_string 应报错")
	}
}

func TestEditDeletion(t *testing.T) {
	env, root := projectEnv(t)
	writeFile(t, root, "a.txt", "keep\nremove me\nkeep2\n")

	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "a.txt", "old_string": "remove me\n", "new_string": "",
	}, env)
	if res.IsError {
		t.Fatalf("Edit 失败: %s", res.Content)
	}
	if got := readFile(t, filepath.Join(root, "a.txt")); got != "keep\nkeep2\n" {
		t.Errorf("内容 = %q", got)
	}
}

func TestEditFileMissing(t *testing.T) {
	env, _ := projectEnv(t)
	res := (&editTool{}).Execute(context.Background(), map[string]interface{}{
		"file_path": "nope.txt", "old_string": "a", "new_string": "b",
	}, env)
	if !res.IsError || !strings.Contains(res.Content, "不存在") {
		t.Errorf("文件不存在应报错: %v %s", res.IsError, res.Content)
	}
}

// ---------------------------------------------------------------------------
// shell 工具
// ---------------------------------------------------------------------------

// fakeShellPolicy 是测试用的 shell 策略。
type fakeShellPolicy struct {
	allowAll   bool
	denySubstr string
	path       string
	baseArgs   []string
	resolveErr error
}

func (p *fakeShellPolicy) Resolve(kind string) (string, []string, error) {
	if p.resolveErr != nil {
		return "", nil, p.resolveErr
	}
	return p.path, p.baseArgs, nil
}

func (p *fakeShellPolicy) Allow(command string) (bool, string) {
	if p.denySubstr != "" && strings.Contains(command, p.denySubstr) {
		return false, "命中禁止名单"
	}
	return p.allowAll, ""
}

// shPolicy 返回一个允许全部命令、用 /bin/sh -c 执行的策略。
func shPolicy() *fakeShellPolicy {
	return &fakeShellPolicy{allowAll: true, path: "/bin/sh", baseArgs: []string{"-c"}}
}

func TestShellFailsClosedWithoutPolicy(t *testing.T) {
	// 没注入 ShellPolicy 时必须拒绝执行，不能默认放行。
	for _, env := range []*ToolEnv{nil, {}} {
		res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "echo hi"}, env)
		if !res.IsError {
			t.Fatalf("未配置策略时必须 fail closed，实际: %+v", res)
		}
		if !strings.Contains(res.Content, "未启用命令执行") {
			t.Errorf("错误信息应说明未启用: %s", res.Content)
		}
	}
}

func TestShellAllowlistRejects(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = &fakeShellPolicy{allowAll: false, denySubstr: "rm", path: "/bin/sh", baseArgs: []string{"-c"}}

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "rm -rf /"}, env)
	if !res.IsError {
		t.Fatal("被策略拒绝的命令不得执行")
	}
	if !strings.Contains(res.Content, "拒绝执行") {
		t.Errorf("错误信息应说明被拒: %s", res.Content)
	}
}

func TestShellEmptyCommand(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = shPolicy()
	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "   "}, env)
	if !res.IsError || !strings.Contains(res.Content, "缺少 command") {
		t.Errorf("空命令应报错: %v %s", res.IsError, res.Content)
	}
}

func TestShellRunsAndCapturesStdout(t *testing.T) {
	env, root := projectEnv(t)
	env.ShellPolicy = shPolicy()

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "echo hello-shell"}, env)
	if res.IsError {
		t.Fatalf("命令失败: %s", res.Content)
	}
	if !strings.Contains(res.Content, "hello-shell") {
		t.Errorf("应捕获 stdout: %s", res.Content)
	}

	// 工作目录必须是项目根。用 pwd -P 取物理路径：不带 -P 时 shell 会优先
	// 输出继承来的 $PWD 逻辑路径，而 os/exec 只改 cwd 不改环境变量。
	res = (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "pwd -P"}, env)
	if !strings.Contains(res.Content, root) {
		t.Errorf("cwd 应为项目根 %s，实际输出: %s", root, res.Content)
	}
}

// runShell 必须把 ToolEnv.Env 传给子进程：这是「用户终端环境对齐」的落地点。
// env.Env 为 nil 时保持旧行为（继承宿主进程），保证兼容。
func TestRunShellAppliesToolEnv(t *testing.T) {
	toolEnv := &ToolEnv{
		ShellPolicy: shPolicy(),
		Env:         []string{"PATH=/usr/bin:/bin", "AGENTCORE_ENV_PROBE=hello"},
	}
	out := runShell(context.Background(), "bash",
		map[string]interface{}{"command": `printf 'PROBE=%s' "$AGENTCORE_ENV_PROBE"`}, toolEnv)
	if out.IsError || !strings.Contains(out.Content, "PROBE=hello") {
		t.Fatalf("env 未传入子进程: %+v", out)
	}

	// 完整替换语义：Env 不是与宿主环境 append 合并。往测试进程注入一个
	// Env 里没有的变量，子进程必须看不到——cmd.Env 显式设置后 os/exec 不再
	// 继承宿主进程任何变量（否则宿主私有的 PATH/凭据会漏进子进程）。
	t.Setenv("AGENTCORE_REPLACE_PROBE", "leaked")
	out = runShell(context.Background(), "bash",
		map[string]interface{}{"command": `printf 'REPLACE=%s' "$AGENTCORE_REPLACE_PROBE"`}, toolEnv)
	if out.IsError || !strings.Contains(out.Content, "REPLACE=") || strings.Contains(out.Content, "leaked") {
		t.Fatalf("Env 应是完整替换而非与宿主合并: %+v", out)
	}

	// nil Env：继承宿主（测试进程没有该变量），不应报错。
	out = runShell(context.Background(), "bash",
		map[string]interface{}{"command": `printf 'PROBE=%s' "$AGENTCORE_ENV_PROBE"`},
		&ToolEnv{ShellPolicy: shPolicy()})
	if out.IsError || strings.Contains(out.Content, "PROBE=hello") {
		t.Fatalf("nil Env 应继承宿主进程，不应看到探针变量: %+v", out)
	}
}

func TestShellExitCode(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = shPolicy()

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "exit 3"}, env)
	if !res.IsError {
		t.Fatal("非零退出码必须标记为错误")
	}
	if !strings.Contains(res.Content, "退出码 3") {
		t.Errorf("应回填退出码: %s", res.Content)
	}
}

func TestShellCapturesStderr(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = shPolicy()

	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{"command": "echo oops >&2"}, env)
	if !strings.Contains(res.Content, "[stderr]") || !strings.Contains(res.Content, "oops") {
		t.Errorf("stderr 应单独标注: %s", res.Content)
	}
}

func TestShellTimeout(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = shPolicy()

	start := time.Now()
	res := (&bashTool{}).Execute(context.Background(), map[string]interface{}{
		"command": "sleep 5", "timeout": float64(100),
	}, env)
	elapsed := time.Since(start)

	if !res.IsError {
		t.Fatal("超时必须标记为错误")
	}
	if !strings.Contains(res.Content, "超时") {
		t.Errorf("应说明超时: %s", res.Content)
	}
	if elapsed > 3*time.Second {
		t.Errorf("超时未生效，耗时 %s", elapsed)
	}
}

func TestShellContextCancel(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = shPolicy()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	res := (&bashTool{}).Execute(ctx, map[string]interface{}{"command": "sleep 5"}, env)
	if !res.IsError {
		t.Fatal("取消必须标记为错误")
	}
	if !strings.Contains(res.Content, "取消") {
		t.Errorf("应说明被取消: %s", res.Content)
	}
}

func TestShellResolveError(t *testing.T) {
	env, _ := projectEnv(t)
	env.ShellPolicy = &fakeShellPolicy{allowAll: true, resolveErr: os.ErrNotExist}
	res := (&powershellTool{}).Execute(context.Background(), map[string]interface{}{"command": "Get-Location"}, env)
	if !res.IsError {
		t.Fatal("平台不支持时 Resolve 报错应回填")
	}
}

// ---------------------------------------------------------------------------
// 输出截断
// ---------------------------------------------------------------------------

func TestTruncateToolOutput(t *testing.T) {
	small := "short"
	if got := truncateToolOutput(small); got != small {
		t.Errorf("小输出不应被改动: %q", got)
	}

	big := strings.Repeat("0123456789\n", maxToolOutputBytes/11+100)
	got := truncateToolOutput(big)
	if len(got) >= len(big) {
		t.Fatal("超限输出必须被截断")
	}
	if !strings.Contains(got, "输出已截断") {
		t.Error("截断必须明确标注，否则模型以为读完了全文")
	}
	// 截断后仍是合法 UTF-8（不能切半个汉字）。
	if !utf8Valid(got) {
		t.Error("截断破坏了 UTF-8 编码")
	}
}

func TestCappedWriter(t *testing.T) {
	var sb strings.Builder
	w := &cappedWriter{buf: &sb, limit: 10}

	n, err := w.Write([]byte("12345"))
	if n != 5 || err != nil {
		t.Fatalf("Write 返回 %d, %v", n, err)
	}
	// 超限：写入被截断但返回长度必须是入参长度（io.Writer 契约）。
	n, err = w.Write([]byte("67890abcdef"))
	if n != 11 || err != nil {
		t.Fatalf("超限 Write 返回 %d, %v", n, err)
	}
	if sb.String() != "1234567890" {
		t.Errorf("缓冲区 = %q, want 1234567890", sb.String())
	}
	if !w.truncated {
		t.Error("超限后应置 truncated 标记")
	}
	// 已截断后继续写入不应报错，也不应再增长。
	if _, err := w.Write([]byte("more")); err != nil {
		t.Errorf("截断后写入不应报错: %v", err)
	}
	if sb.String() != "1234567890" {
		t.Errorf("截断后缓冲区不应再增长: %q", sb.String())
	}
}

// utf8Valid 是 strings.ToValidUTF8 的等价校验，避免测试里再引一个包。
func utf8Valid(s string) bool {
	return strings.ToValidUTF8(s, "�") == s
}

// ---------------------------------------------------------------------------
// 注册表
// ---------------------------------------------------------------------------

func TestDefaultRegistryHasEightTools(t *testing.T) {
	r := DefaultRegistry()
	// PowerShell 只在 Windows 注册（非 Windows 上它是必然失败的 schema 槽位）。
	want := []string{"Bash", "Edit", "Glob", "Grep", "LS", "Read", "Write"}
	if runtime.GOOS == "windows" {
		want = []string{"Bash", "Edit", "Glob", "Grep", "LS", "PowerShell", "Read", "Write"}
	}
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("缺少工具 %s", name)
		}
	}
	defs := r.Defs()
	if len(defs) != len(want) {
		t.Fatalf("工具定义数量 = %d, want %d", len(defs), len(want))
	}
	// 顺序必须稳定（宿主会拿它做能力比较）。
	for i, d := range defs {
		if d.Name != want[i] {
			t.Errorf("Defs[%d].Name = %q, want %q（顺序不稳定）", i, d.Name, want[i])
		}
		if d.InputSchema["type"] != "object" {
			t.Errorf("%s 的 schema 必须是 object", d.Name)
		}
	}
}

func TestRegistryUnknownTool(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("Nope"); ok {
		t.Error("未注册的工具不应能取到")
	}
	var nilReg *Registry
	if _, ok := nilReg.Get("X"); ok {
		t.Error("nil 注册表应返回 false 而不是 panic")
	}
}
