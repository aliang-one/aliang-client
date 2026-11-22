package services

// 网页终端 FS 面(spec docs/superpowers/specs/2026-11-21-web-terminal-design.md
// §6.3,阶段 3):list/read/download/upload 四端点的服务层。守卫统一前置
// agentFSResolve:Clean→InsideDirectory(前检)→EvalSymlinks→InsideDirectory
// (后检,symlink 指向守卫根外在此拦)。守卫根=用户 home(起始浏览点;守卫
// 防的是遍历攻击,授权在闸门链——主开关+令牌头在 handler 层前置)。真执行
// 在 user agent runtime;core 只闸门②+proxy 透传。审计(spec §6.4):fs_upload/
// fs_download 均在 handler 层落(真源 origin 随 WebTerminalAuditFS 一并入摘要,
// P3.6);list/read 不审计(高频噪声)。

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

const (
	// AgentFSReadMaxBytes 文本预览上限(spec §6.3:2MiB,超出 truncated:true)。
	AgentFSReadMaxBytes int64 = 2 << 20
	// AgentFSUploadMaxBytes 上传大小上限(spec §6.3:100MiB);真值测量在
	// handler 层 LimitReader,本层只信入参字节。
	AgentFSUploadMaxBytes int64 = 100 << 20
)

// FS 面哨兵错误(handler 的业务码映射点)。
var (
	// ErrAgentFSEscapeRoot 路径穿越守卫拒绝(handler 映射 CodeForbidden)。
	ErrAgentFSEscapeRoot = errors.New("path escapes web terminal fs root")
	// ErrAgentFSBadFilename 上传文件名清洗拒绝(handler 映射 CodeBadRequest)。
	ErrAgentFSBadFilename = errors.New("invalid upload filename")
)

var (
	agentFSRootOnce sync.Once
	agentFSRootPath string
	agentFSRootErr  error

	// agentFSRootOverride 测试注入守卫根(t.TempDir);空=真实 home。仅本包
	// 测试可触,包内测试不 t.Parallel(同仓测试纪律)故无并发顾虑。
	agentFSRootOverride string
)

// webTerminalFSRoot 守卫根=用户 home,一次缓存。根本身先 EvalSymlinks 规范化:
// macOS 的 /var→/private/var 等挂载点符号链接下,root 不先规范的话 root 下一切
// 真实路径 Eval 后都会「出守卫根」被整体误拒(生产 home 与测试 t.TempDir 同坑)。
func webTerminalFSRoot() (string, error) {
	if agentFSRootOverride != "" {
		return agentFSCanonicalRoot(agentFSRootOverride), nil
	}
	agentFSRootOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			agentFSRootErr = err
			return
		}
		agentFSRootPath = agentFSCanonicalRoot(home)
	})
	return agentFSRootPath, agentFSRootErr
}

func agentFSCanonicalRoot(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// agentFSResolve 路径守卫统一前置:空拒→Clean→相对路径拼守卫根(Join 内含
// Clean,`../` 逃逸在此塌缩)→InsideDirectory 前检→EvalSymlinks→
// InsideDirectory 后检。绝对路径只有在根内才收。尚不存在的目标(如上传新
// 文件)EvalSymlinks 会失败——回退 Eval 父目录+base 拼回再过后检。
func agentFSResolve(raw string) (string, error) {
	root, err := webTerminalFSRoot()
	if err != nil {
		return "", fmt.Errorf("web terminal fs root unavailable: %w", err)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("path is empty")
	}
	cleaned := filepath.Clean(trimmed)
	candidate := cleaned
	if !filepath.IsAbs(cleaned) {
		candidate = filepath.Join(root, cleaned)
	}
	if !agentPathInsideDirectory(candidate, root) {
		return "", fmt.Errorf("%w: %s", ErrAgentFSEscapeRoot, raw)
	}
	resolved, evalErr := filepath.EvalSymlinks(candidate)
	if evalErr != nil {
		resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(candidate))
		if parentErr != nil {
			return "", evalErr
		}
		resolved = filepath.Join(resolvedParent, filepath.Base(candidate))
	}
	if !agentPathInsideDirectory(resolved, root) {
		return "", fmt.Errorf("%w: %s", ErrAgentFSEscapeRoot, raw)
	}
	return resolved, nil
}

// AgentFSList 一级目录列表(spec §6.3):目录优先、同层按名排序;目录 size=0。
// 返回守卫解析后的真身路径(handler 回 {path,entries} 信封)。单条目不可读
// (并发删除等)跳过并告警,不拖垮整表。
func (s *AgentService) AgentFSList(rawDir string) (string, []map[string]interface{}, error) {
	dir, err := agentFSResolve(rawDir)
	if err != nil {
		return "", nil, err
	}
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, err
	}
	sort.Slice(dirEntries, func(i, j int) bool {
		if dirEntries[i].IsDir() != dirEntries[j].IsDir() {
			return dirEntries[i].IsDir()
		}
		return dirEntries[i].Name() < dirEntries[j].Name()
	})
	entries := make([]map[string]interface{}, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, infoErr := de.Info()
		if infoErr != nil {
			logger.Warn(fmt.Sprintf("[WEB-TERMINAL] fs_list_entry_stat_failed dir=%s name=%s error=%v", dir, de.Name(), infoErr))
			continue
		}
		entryType := "file"
		switch {
		case info.IsDir():
			entryType = "dir"
		case info.Mode()&os.ModeSymlink != 0:
			entryType = "symlink"
		}
		size := int64(0)
		if entryType != "dir" {
			size = info.Size()
		}
		entries = append(entries, map[string]interface{}{
			"name":  de.Name(),
			"type":  entryType,
			"size":  size,
			"mtime": info.ModTime().Format(time.RFC3339),
			"mode":  info.Mode().String(),
		})
	}
	return dir, entries, nil
}

// AgentFSRead 有界文本读(spec §6.3):读 maxBytes+1 判截断;binary=前 8KiB
// 含 NUL。内容按原字节转 string(Go string 保字节,UTF-8 呈现由前端兜底)。
// 返回守卫解析后的真身路径供 handler 信封。
func (s *AgentService) AgentFSRead(rawPath string, maxBytes int64) (string, string, bool, bool, error) {
	if maxBytes <= 0 {
		maxBytes = AgentFSReadMaxBytes
	}
	resolved, err := agentFSResolve(rawPath)
	if err != nil {
		return "", "", false, false, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", "", false, false, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", "", false, false, err
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	binary := false
	scan := len(data)
	if scan > 8192 {
		scan = 8192
	}
	for i := 0; i < scan; i++ {
		if data[i] == 0 {
			binary = true
			break
		}
	}
	return resolved, string(data), truncated, binary, nil
}

// AgentFSOpen 供 download 流式回放(ServeContent):resolve→open→stat,拒目录
// (ServeContent 回目录无意义且 io 语义怪)。调用方负责 Close;resolved 一并
// 返回供审计。
func (s *AgentService) AgentFSOpen(rawPath string) (*os.File, os.FileInfo, string, error) {
	resolved, err := agentFSResolve(rawPath)
	if err != nil {
		return nil, nil, "", err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, nil, "", err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, "", err
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, nil, "", fmt.Errorf("path is a directory: %s", resolved)
	}
	return f, info, resolved, nil
}

// agentFSSanitizeFilename 上传文件名清洗:Base 剥路径成分→拒空/. /..→控制
// 字符(<0x20、0x7f)与 Windows 保留字符(< > : " / \ | ? *)替换为 `_`
// (0.196 是 Windows 端,这里种下的名字必须在其上合法——strip 而非拒绝,保
// 上传成功率)。
func agentFSSanitizeFilename(name string) (string, error) {
	base := strings.TrimSpace(filepath.Base(strings.TrimSpace(name)))
	if base == "" || base == "." || base == ".." {
		return "", ErrAgentFSBadFilename
	}
	var b strings.Builder
	for _, r := range base {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	return b.String(), nil
}

// AgentFSUpload 上传:dir 守卫解析→文件名清洗→同名追加 _HHMMSS(扩展名前)
// →后置守卫(join 结果再过 agentFSResolve,防未来改动引入回归)→0600 落盘。
// fs_upload 审计自 P3.6 起在 handler 层落(真源 origin 只有 HTTP 层可见,
// 与 fs_download 同走 WebTerminalAuditFS)。
func (s *AgentService) AgentFSUpload(rawDir, filename string, src []byte) (string, error) {
	dir, err := agentFSResolve(rawDir)
	if err != nil {
		return "", err
	}
	name, err := agentFSSanitizeFilename(filename)
	if err != nil {
		return "", err
	}
	// 同名冲突:扩展名前追加 _HHMMSS;同秒多笔会再撞同一后缀——最多 3 轮
	// 改名重查(跨秒自然散开,同秒以 _2/_3 序号兜底),轮尽仍占用则以最后
	// 候选直写(极端同秒高频的覆盖,好过报错丢整笔上传)。
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	full := filepath.Join(dir, name)
	for attempt := 0; attempt < 4; attempt++ {
		if _, statErr := os.Stat(full); statErr != nil {
			break
		}
		if attempt >= 3 {
			break
		}
		suffix := time.Now().Format("150405")
		if attempt > 0 {
			suffix += "_" + strconv.Itoa(attempt+1)
		}
		full = filepath.Join(dir, stem+"_"+suffix+ext)
	}
	full, err = agentFSResolve(full)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, src, 0o600); err != nil {
		return "", err
	}
	return full, nil
}

// WebTerminalAuditFS 落 fs_upload/fs_download 审计行(均由 handler 调:download
// 在 ServeContent 返回后——彼时流已发完;upload 在落盘成功后。path 用守卫解析
// 后的真身)。origin=loopback|cookie 由 handler 按 X-Aliang-WT-Loopback 真源
// 标记头判定(P3.6 Finding B 修正,语义见 markWebTerminalLoopbackSource)。
func (s *AgentService) WebTerminalAuditFS(action, path string, bytes int64, origin string) {
	s.webTerminalJournalAudit(action, fmt.Sprintf("path=%s bytes=%d origin=%s", path, bytes, origin), false)
}
