package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// 只读工具：Read / LS / Glob / Grep。
//
// 共同约束：路径一律过 resolveToolPath（含符号链接逃逸检查），输出一律过
// truncateToolOutput（超限明确标注，不静默截断）。

// defaultReadLines 是 Read 一次最多返回的行数。
const defaultReadLines = 2000

// readBinaryProbeBytes 是二进制探测的采样窗口。
const readBinaryProbeBytes = 8192

// ---------------------------------------------------------------------------
// Read
// ---------------------------------------------------------------------------

type readTool struct{}

func (t *readTool) Name() string { return "Read" }

func (t *readTool) Description() string {
	return "读取一个文本文件，返回带行号的内容。大文件请用 offset/limit 分段读取；" +
		"二进制文件会被拒绝。"
}

func (t *readTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"file_path": stringProp("文件的绝对路径，或相对项目根的路径"),
		"offset":    intProp("起始行号（1-based）。省略则从第 1 行开始"),
		"limit":     intProp(fmt.Sprintf("最多读取的行数，默认 %d", defaultReadLines)),
	}, "file_path")
}

func (t *readTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	raw := inputString(input, "file_path")
	if strings.TrimSpace(raw) == "" {
		return ErrorResult("缺少 file_path 参数")
	}
	path, err := resolveToolPath(env, raw)
	if err != nil {
		return ErrorResult("%v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult("文件不存在: %s", raw)
		}
		return ErrorResult("无法访问 %s: %v", raw, err)
	}
	if info.IsDir() {
		return ErrorResult("%s 是目录，请改用 LS 工具", raw)
	}

	f, err := os.Open(path)
	if err != nil {
		return ErrorResult("无法打开 %s: %v", raw, err)
	}
	defer f.Close()

	// 二进制探测：前 8KB 出现 NUL 就拒绝。把二进制当文本回填给模型只会浪费
	// 上下文并让它产生幻觉。
	head := make([]byte, readBinaryProbeBytes)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return ErrorResult("%s 看起来是二进制文件，拒绝以文本方式读取", raw)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return ErrorResult("无法重置读取位置: %v", err)
	}

	offset, ok := inputInt(input, "offset")
	if !ok || offset < 1 {
		offset = 1
	}
	limit, ok := inputInt(input, "limit")
	if !ok || limit <= 0 {
		limit = defaultReadLines
	}

	scanner := bufio.NewScanner(f)
	// 单行上限给到 4MB：压缩过的 JSON / 单行 bundle 在真实项目里很常见。
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var sb strings.Builder
	lineNo := 0
	emitted := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return ErrorResult("读取被取消: %v", err)
		}
		lineNo++
		if lineNo < offset {
			continue
		}
		if emitted >= limit {
			break
		}
		fmt.Fprintf(&sb, "%6d\t%s\n", lineNo, scanner.Text())
		emitted++
		if sb.Len() > maxToolOutputBytes {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return ErrorResult("读取 %s 失败: %v", raw, err)
	}
	if emitted == 0 {
		if lineNo == 0 {
			return ToolResult{Content: fmt.Sprintf("%s 是空文件。", raw)}
		}
		return ErrorResult("offset=%d 超出文件范围（共 %d 行）", offset, lineNo)
	}

	out := sb.String()
	if emitted >= limit {
		out += fmt.Sprintf("\n[仅显示第 %d-%d 行，共 %d 行。需要后续内容请提高 offset。]",
			offset, offset+emitted-1, lineNo)
	}
	return ToolResult{Content: truncateToolOutput(out)}
}

// ---------------------------------------------------------------------------
// LS
// ---------------------------------------------------------------------------

type lsTool struct{}

func (t *lsTool) Name() string { return "LS" }

func (t *lsTool) Description() string {
	return "列出目录下的一级条目。目录以 / 结尾，文件附带字节数。"
}

func (t *lsTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"path": stringProp("目录路径，省略则为项目根"),
	})
}

func (t *lsTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	path, err := resolveToolPath(env, inputString(input, "path"))
	if err != nil {
		return ErrorResult("%v", err)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrorResult("目录不存在: %s", path)
		}
		return ErrorResult("无法读取目录 %s: %v", path, err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return ErrorResult("列目录被取消: %v", err)
		}
		name := e.Name()
		if e.IsDir() {
			names = append(names, name+"/")
			continue
		}
		size := int64(0)
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		names = append(names, fmt.Sprintf("%s (%d bytes)", name, size))
	}
	sort.Strings(names)

	if len(names) == 0 {
		return ToolResult{Content: fmt.Sprintf("%s 是空目录。", path)}
	}
	return ToolResult{Content: truncateToolOutput(strings.Join(names, "\n"))}
}

// ---------------------------------------------------------------------------
// Glob
// ---------------------------------------------------------------------------

type globTool struct{}

func (t *globTool) Name() string { return "Glob" }

func (t *globTool) Description() string {
	return "按 glob 模式查找文件（如 **/*.go、internal/*.ts）。返回相对项目根的路径，按修改时间倒序。"
}

func (t *globTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"pattern": stringProp("glob 模式。支持 * 与 **"),
		"path":    stringProp("搜索根目录，省略则为项目根"),
	}, "pattern")
}

// globMaxResults 是 Glob 单次返回上限。
const globMaxResults = 500

func (t *globTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	pattern := strings.TrimSpace(inputString(input, "pattern"))
	if pattern == "" {
		return ErrorResult("缺少 pattern 参数")
	}
	root, err := resolveToolPath(env, inputString(input, "path"))
	if err != nil {
		return ErrorResult("%v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return ErrorResult("无法访问 %s: %v", root, err)
	}
	if !info.IsDir() {
		root = filepath.Dir(root)
	}

	type hit struct {
		rel   string
		mtime int64
	}
	var hits []hit
	walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 权限不足的目录直接跳过
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) && p != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if !globMatch(pattern, rel) {
			return nil
		}
		var mtime int64
		if fi, err := d.Info(); err == nil {
			mtime = fi.ModTime().UnixNano()
		}
		hits = append(hits, hit{rel: filepath.ToSlash(rel), mtime: mtime})
		if len(hits) >= globMaxResults*4 {
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != context.Canceled {
		return ErrorResult("遍历 %s 失败: %v", root, walkErr)
	}

	if len(hits) == 0 {
		return ToolResult{Content: fmt.Sprintf("没有匹配 %q 的文件。", pattern)}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].mtime != hits[j].mtime {
			return hits[i].mtime > hits[j].mtime
		}
		return hits[i].rel < hits[j].rel
	})
	truncated := len(hits) > globMaxResults
	if truncated {
		hits = hits[:globMaxResults]
	}
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, h.rel)
	}
	out := strings.Join(lines, "\n")
	if truncated {
		out += fmt.Sprintf("\n[仅显示前 %d 个结果，请缩小 pattern 或指定 path。]", globMaxResults)
	}
	return ToolResult{Content: truncateToolOutput(out)}
}

// globMatch 把 glob 模式与相对路径做匹配。
//
// filepath.Match 不支持 **，这里对前导 **/ 做剥层重试，并对每级后缀再试一次，
// 覆盖「**/*.go 匹配 a/b/c.go」这类最常见的用法。
func globMatch(pattern, relPath string) bool {
	pattern = filepath.ToSlash(pattern)
	relPath = filepath.ToSlash(relPath)

	match := func(p, s string) bool {
		ok, err := filepath.Match(p, s)
		return err == nil && ok
	}

	rest := pattern
	for {
		if match(rest, relPath) || match(rest, filepath.Base(relPath)) {
			return true
		}
		parts := strings.Split(relPath, "/")
		for i := 1; i < len(parts); i++ {
			if match(rest, strings.Join(parts[i:], "/")) {
				return true
			}
		}
		if !strings.HasPrefix(rest, "**/") {
			return false
		}
		rest = strings.TrimPrefix(rest, "**/")
	}
}

// ---------------------------------------------------------------------------
// Grep
// ---------------------------------------------------------------------------

type grepTool struct{}

func (t *grepTool) Name() string { return "Grep" }

func (t *grepTool) Description() string {
	return "在文件内容里做正则搜索，输出 路径:行号:内容。默认跳过 .git / node_modules / vendor 等目录与二进制文件。"
}

func (t *grepTool) InputSchema() map[string]interface{} {
	return objectSchema(map[string]interface{}{
		"pattern":     stringProp("Go 正则表达式"),
		"path":        stringProp("搜索根目录或单个文件，省略则为项目根"),
		"glob":        stringProp("只搜索匹配该 glob 的文件，如 *.go"),
		"ignore_case": boolProp("忽略大小写"),
		"max_results": intProp(fmt.Sprintf("最多返回的匹配行数，默认 %d", defaultGrepMaxResults)),
	}, "pattern")
}

const defaultGrepMaxResults = 200

func (t *grepTool) Execute(ctx context.Context, input map[string]interface{}, env *ToolEnv) ToolResult {
	pattern := inputString(input, "pattern")
	if pattern == "" {
		return ErrorResult("缺少 pattern 参数")
	}
	if inputBool(input, "ignore_case") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return ErrorResult("正则表达式无效: %v", err)
	}

	root, err := resolveToolPath(env, inputString(input, "path"))
	if err != nil {
		return ErrorResult("%v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return ErrorResult("无法访问 %s: %v", root, err)
	}

	globFilter := strings.TrimSpace(inputString(input, "glob"))
	maxResults, ok := inputInt(input, "max_results")
	if !ok || maxResults <= 0 {
		maxResults = defaultGrepMaxResults
	}

	var out []string
	truncated := false

	collect := func(path, rel string) error {
		fileHits, err := grepFile(ctx, path, rel, re, maxResults-len(out))
		if err != nil {
			return nil // 单文件读失败不影响整体
		}
		out = append(out, fileHits...)
		if len(out) >= maxResults {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	}

	if !info.IsDir() {
		if err := collect(root, filepath.Base(root)); err != nil && err != filepath.SkipAll {
			return ErrorResult("搜索失败: %v", err)
		}
	} else {
		walkErr := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}
			if d.IsDir() {
				if shouldSkipDir(d.Name()) && p != root {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return nil
			}
			relSlash := filepath.ToSlash(rel)
			if globFilter != "" && !globMatch(globFilter, relSlash) {
				return nil
			}
			return collect(p, relSlash)
		})
		if walkErr != nil && walkErr != context.Canceled && walkErr != filepath.SkipAll {
			return ErrorResult("遍历 %s 失败: %v", root, walkErr)
		}
	}

	if len(out) == 0 {
		return ToolResult{Content: fmt.Sprintf("没有匹配 %q 的内容。", pattern)}
	}
	text := strings.Join(out, "\n")
	if truncated {
		text += fmt.Sprintf("\n[已达 %d 条上限，请缩小范围或加 glob 过滤。]", maxResults)
	}
	return ToolResult{Content: truncateToolOutput(text)}
}

// grepFile 在单个文件里找匹配行。
func grepFile(ctx context.Context, path, rel string, re *regexp.Regexp, budget int) ([]string, error) {
	if budget <= 0 {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	head := make([]byte, readBinaryProbeBytes)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, nil // 二进制跳过
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var hits []string
	lineNo := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return hits, err
		}
		lineNo++
		line := scanner.Text()
		if !re.MatchString(line) {
			continue
		}
		if len(line) > 400 {
			line = line[:400] + "…"
		}
		hits = append(hits, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
		if len(hits) >= budget {
			break
		}
	}
	return hits, nil
}

// shouldSkipDir 判定遍历时应当整目录跳过的名字。
//
// 这些都是体量大、几乎不会是用户想让 agent 搜索的目标目录；不跳过会让一次
// Grep 变成几十万次文件打开。
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "dist", "build",
		".venv", "venv", "__pycache__", ".next", ".nuxt", ".turbo",
		".idea", ".gradle", ".terraform", "Pods", "DerivedData":
		return true
	}
	return false
}
