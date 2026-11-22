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
	"io/fs"
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
	// ErrAgentFSAlreadyExists mkdir/rename 目标已存在(handler 映射 CodeConflict)。
	ErrAgentFSAlreadyExists = errors.New("path already exists")
	// ErrAgentFSRootDelete delete 打到守卫根本身(handler 映射 CodeForbidden)。
	ErrAgentFSRootDelete = errors.New("cannot delete the web terminal fs root")
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
// 返回守卫解析后的真身路径(handler 回 {path,entries} 信封)。信封边界统一
// ToSlash:前端导航(cd 闸/面包屑/上级)全按 '/' 拼接拆分,Windows 原生 '\'
// 路径会让整条导航面哑火;内部 os.ReadDir 仍用原生 resolved(先例
// agent_file_write.go 信封 ToSlash)。
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
	return filepath.ToSlash(dir), entries, nil
}

// AgentFSRead 有界文本读(spec §6.3):读 maxBytes+1 判截断;binary=前 8KiB
// 含 NUL。内容按原字节转 string(Go string 保字节,UTF-8 呈现由前端兜底)。
// 返回守卫解析后的真身路径供 handler 信封(信封边界 ToSlash,同
// AgentFSList 注记;内部 os.Open 用原生 resolved)。
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
	return filepath.ToSlash(resolved), string(data), truncated, binary, nil
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
// 返回终名完整路径供 handler 信封(信封边界 ToSlash,同 AgentFSList 注记;
// 内部 os.Stat/WriteFile 用原生 full)。fs_upload 审计自 P3.6 起在 handler 层
// 落(真源 origin 只有 HTTP 层可见,与 fs_download 同走 WebTerminalAuditFS)。
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
	return filepath.ToSlash(full), nil
}

// WebTerminalAuditFS 落 fs_upload/fs_download 审计行(均由 handler 调:download
// 在 ServeContent 返回后——彼时流已发完;upload 在落盘成功后。path 用守卫解析
// 后的真身)。origin=loopback|cookie 由 handler 按 X-Aliang-WT-Loopback 真源
// 标记头判定(P3.6 Finding B 修正,语义见 markWebTerminalLoopbackSource)。
func (s *AgentService) WebTerminalAuditFS(action, path string, bytes int64, origin string) {
	s.webTerminalJournalAudit(action, fmt.Sprintf("path=%s bytes=%d origin=%s", path, bytes, origin), false)
}

// ——— 阶段 3 续:写操作三端点(文件管理批,spec §6.3 mkdir/rename/delete)———
// 与上面四个只读/上传端点同守卫同纪律:dir/path 一律 agentFSResolve(含
// EvalSymlinks 双检),name 一律 agentFSSanitizeFilename(上传批已抽出的可复用
// 清洗器:Base 剥路径成分→拒空/. /..→控制字符与 Windows 保留字符→_)。审计
// fs_mkdir/fs_rename/fs_delete 与 fs_upload/fs_download 同款在 handler 层落
// (真源 origin 只有 HTTP 层可见),本层三函数保持审计无关。

// AgentFSMkdir 在 rawDir 下新建单层 name 目录。**单层 os.Mkdir 非 MkdirAll**:
// 父目录缺失=错误原样上抛(前端在正确目录上下文里操作,静默建链只会掩盖导航
// 漂移)。重名→ErrAgentFSAlreadyExists(前置 os.Stat;Stat 后 Mkdir 前被占的
// 竞态窗口由 fs.ErrExist 兜底归一)。join 后再过 agentFSResolve 防御性复检
// (消毒后的 base 理论不可越根,复检防未来改动回归,同 AgentFSUpload 注记)。
// 返回 ToSlash(full)(信封边界统一,同 AgentFSList 注记;内部 os.Mkdir 用
// 原生 full)。
func (s *AgentService) AgentFSMkdir(rawDir, name string) (string, error) {
	dir, err := agentFSResolve(rawDir)
	if err != nil {
		return "", err
	}
	clean, err := agentFSSanitizeFilename(name)
	if err != nil {
		return "", err
	}
	full := filepath.Join(dir, clean)
	full, err = agentFSResolve(full)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(full); statErr == nil {
		return "", fmt.Errorf("%w: %s", ErrAgentFSAlreadyExists, clean)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	if err := os.Mkdir(full, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%w: %s", ErrAgentFSAlreadyExists, clean)
		}
		return "", err // 权限/ENOSPC 等系统错误原样(文案带系统消息,前端直显)
	}
	return filepath.ToSlash(full), nil
}

// AgentFSRename 同目录内重命名(oldName→newName)。新旧名都过消毒,dir 过
// 守卫;旧名必须存在(fs.ErrNotExist 上抛,handler 映射 103),新名必须不存
// 在(ErrAgentFSAlreadyExists)——os.Rename 在多数平台会静默覆盖既有目标,
// 前置双 Stat 把「覆盖」语义显式变成错误(改名意图从不包含覆盖)。返回
// ToSlash(新 full);新 join 结果同样防御性复检(同 AgentFSMkdir 注记)。
func (s *AgentService) AgentFSRename(rawDir, oldName, newName string) (string, error) {
	dir, err := agentFSResolve(rawDir)
	if err != nil {
		return "", err
	}
	oldClean, err := agentFSSanitizeFilename(oldName)
	if err != nil {
		return "", err
	}
	newClean, err := agentFSSanitizeFilename(newName)
	if err != nil {
		return "", err
	}
	oldFull := filepath.Join(dir, oldClean)
	newFull := filepath.Join(dir, newClean)
	if oldFull, err = agentFSResolve(oldFull); err != nil {
		return "", err
	}
	if newFull, err = agentFSResolve(newFull); err != nil {
		return "", err
	}
	if _, statErr := os.Stat(oldFull); statErr != nil {
		return "", statErr
	}
	if _, statErr := os.Stat(newFull); statErr == nil {
		return "", fmt.Errorf("%w: %s", ErrAgentFSAlreadyExists, newClean)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", statErr
	}
	if err := os.Rename(oldFull, newFull); err != nil {
		return "", err
	}
	return filepath.ToSlash(newFull), nil
}

// AgentFSDelete 删除文件或目录,统一 os.RemoveAll(目录=递归;前端双段强确认
// 前置,服务层不设防递归开关——闸门链+审计已是纵深)。守卫根自身保护:
// resolved==守卫根 → ErrAgentFSRootDelete(根是整个浏览面的锚点,误删=首页
// 没了)。不存在→fs.ErrNotExist(handler 映射 103)。返回 ToSlash(resolved)
// 供 handler 信封与 fs_delete 审计(其余两函数同款回真身路径)。
func (s *AgentService) AgentFSDelete(rawPath string) (string, error) {
	resolved, err := agentFSResolve(rawPath)
	if err != nil {
		return "", err
	}
	root, err := webTerminalFSRoot()
	if err != nil {
		return "", fmt.Errorf("web terminal fs root unavailable: %w", err)
	}
	if resolved == root {
		return "", ErrAgentFSRootDelete
	}
	if _, statErr := os.Stat(resolved); statErr != nil {
		return "", statErr
	}
	if err := os.RemoveAll(resolved); err != nil {
		return "", err
	}
	return filepath.ToSlash(resolved), nil
}

// WebTerminalAuditFSMutation 落 fs_mkdir/fs_rename/fs_delete 审计行(均由
// handler 调,与 WebTerminalAuditFS 同款落点与理由:真源 origin 只有 HTTP 层
// 可见)。path 用守卫解析后的真身(斜杠形态)。delete 带 recursive 标志——
// 服务层 os.RemoveAll 语义上目录即递归,标志值由前端双段确认 UX 透传(文件
// 删 false/目录删 true),审计行据此可辨「用户被告知并确认了递归」。
func (s *AgentService) WebTerminalAuditFSMutation(action, path, origin string, recursive bool) {
	if recursive {
		s.webTerminalJournalAudit(action, fmt.Sprintf("path=%s recursive=true origin=%s", path, origin), false)
		return
	}
	s.webTerminalJournalAudit(action, fmt.Sprintf("path=%s origin=%s", path, origin), false)
}
