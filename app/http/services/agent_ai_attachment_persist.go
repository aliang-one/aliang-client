package services

// 持久附件缓存：手机/网页图片消息在网页端显示的真值源（spec
// 2026-11-05-chat-image-pipeline §3.1）。内核下载/网页上传时顺手落副本到
// <EffectiveAgentHome>/aliang-attachments/<sessionID>/，journal 只落相对
// 引用；网页经 /api/agent/ai/attachment?ref= 取图（Resolve 与端点双层
// 穿越守卫）。
//
// 配额纪律：单文件 ≤10MiB（对齐手机协议；10-20MiB 带内的图能进内核但跳过
// 持久化=有意降级，spec §3.1）、会话 ≤256MiB（写入拒绝）、全局 ≤1GiB
// （agent 启动 sweep 按会话最旧驱逐整目录）。recall/truncateBefore 的孤儿
// 文件不做即时删除，统一由全局 sweep 收敛。
//
// 与 TUI 缓存（agentAIAttachmentCacheDir，.aliang/attachments，临时性）的
// 区别：本目录按会话键持久组织、journal 引用它、生命周期=会话历史本身。

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/internal/runtimepath"
)

const (
	agentAttachmentCacheDirName    = "aliang-attachments"
	agentAttachmentFileMaxBytes    = 10 << 20
	agentAttachmentSessionMaxBytes = 256 << 20
	agentAttachmentGlobalMaxBytes  = 1 << 30
)

// agentAttachmentExtAllowlist：可持久化扩展名（对齐手机 mime 白名单
// jpeg/png/gif/webp）。空/未知扩展拒绝——缓存里只放渲染器认识的形态。
var agentAttachmentExtAllowlist = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true,
}

// agentAttachmentCacheRoot 解析持久缓存根。env 覆盖（ALIANG_ATTACHMENT_
// PERSIST_DIR）供测试与高级部署用；缺省按平台策略（见 For 版）。
func agentAttachmentCacheRoot() (string, error) {
	return agentAttachmentCacheRootFor(runtime.GOOS)
}

// agentAttachmentCacheRootFor 平台策略注入版（goos 可注入供单平台测试，
// 规避「GOOS 后缀测试文件在其它平台静默跳过」盲区）。
//
// 平台撕裂背景（2026-11-08 vm459 Windows 103 根因）：Windows 服务账户
// (SYSTEM) 与用户 runtime 进程的 %USERPROFILE% 不同（SYSTEM=systemprofile），
// 而 runtimepath 没有 windows 的 console-user 解析分支——home 侧缓存根在
// core(56431 服务进程) 与 runtime(56433 用户进程) 之间不共享：下载写入方落
// 用户 home、取图读取方（服务进程）落 systemprofile → 恒 miss → 103。
// journal/转录清单不受影响（数据面代理会把请求转发给 runtime 进程），附件
// 端点没有转发分支，故在本层统一根：windows 恒落 CoreDataDir（ProgramData，
// 两进程读写一致）；darwin/linux 的 EffectiveAgentHome 自带 console-user
// 解析（root 进程也能定位桌面用户家目录），home 侧语义保持不变。
func agentAttachmentCacheRootFor(goos string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ALIANG_ATTACHMENT_PERSIST_DIR")); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs, nil
		}
		return dir, nil
	}
	if goos == "windows" {
		return filepath.Join(runtimepath.CoreDataDir(), agentAttachmentCacheDirName), nil
	}
	home, err := runtimepath.EffectiveAgentHome()
	if err != nil || strings.TrimSpace(home) == "" {
		if userHome, uerr := os.UserHomeDir(); uerr == nil && strings.TrimSpace(userHome) != "" {
			home = userHome
		} else {
			return "", fmt.Errorf("resolve attachment cache root: %w", err)
		}
	}
	return filepath.Join(home, agentAttachmentCacheDirName), nil
}

// migrateLegacyAttachmentCacheRoot 一次性迁移（幂等）：windows 切 CoreDataDir
// 根之前落在用户 home 侧的历史缓存搬进新根，新根已有的同名文件保留新根版本
// 不被覆盖；重复执行不炸。非 windows 平台无撕裂，恒 no-op。legacyRoot 由
// 调用方从当前进程 home 推导（runtime boot 进程=用户态，解析得到真用户
// home；SYSTEM 进程 home 侧无历史文件，读空目录即自然跳过）。
func migrateLegacyAttachmentCacheRoot(goos, newRoot, legacyRoot string) {
	if goos != "windows" || strings.TrimSpace(newRoot) == "" || strings.TrimSpace(legacyRoot) == "" {
		return
	}
	sessions, err := os.ReadDir(legacyRoot)
	if err != nil {
		return // 旧根不存在/不可读 = 无历史可迁
	}
	for _, sess := range sessions {
		if !sess.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(legacyRoot, sess.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			dst := filepath.Join(newRoot, sess.Name(), f.Name())
			if _, err := os.Stat(dst); err == nil {
				continue // 新根已有：保留新根版本
			}
			src := filepath.Join(legacyRoot, sess.Name(), f.Name())
			data, err := os.ReadFile(src)
			if err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-ATTACH] migrate read failed path=%s error=%v", src, err))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-ATTACH] migrate mkdir failed dir=%s error=%v", filepath.Dir(dst), err))
				continue
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				logger.Warn(fmt.Sprintf("[AGENT-ATTACH] migrate write failed path=%s error=%v", dst, err))
				continue
			}
			logger.Warn(fmt.Sprintf("[AGENT-ATTACH] migrated legacy cache file dest=%s", dst))
		}
	}
}

// sanitizeAgentAttachmentSessionID 把 sessionID 约束为安全目录名：只留字母
// 数字连字符下划线点，截 80。空/全非法 → 错误（不静默映射，防两 ID 相撞）。
func sanitizeAgentAttachmentSessionID(sessionID string) (string, error) {
	var b strings.Builder
	for _, r := range sessionID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
		if b.Len() >= 80 {
			break
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "", fmt.Errorf("empty session id after sanitize")
	}
	return out, nil
}

// sanitizeAgentAttachmentBase 约束文件名主体（不含扩展名）：同字符集，截 80。
func sanitizeAgentAttachmentBase(base string) string {
	var b strings.Builder
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), ".")
}

// agentAttachmentSessionDir 确保会话子目录存在（0755：root agent 落盘、
// 桌面用户进程可读，TUI 缓存同款取舍）。
func agentAttachmentSessionDir(sessionID string) (root string, dir string, err error) {
	root, err = agentAttachmentCacheRoot()
	if err != nil {
		return "", "", err
	}
	sane, err := sanitizeAgentAttachmentSessionID(sessionID)
	if err != nil {
		return "", "", err
	}
	dir = filepath.Join(root, sane)
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create session attachment dir: %w", err)
	}
	return root, dir, nil
}

// agentAttachmentPersist 把一份图片字节落持久缓存，返回相对引用
// "<sessionID>/<base><ext>"（journal/端点只传这个形状）。同 base 重写为
// 幂等覆盖（重投递/重复落账安全）。
func agentAttachmentPersist(sessionID, base, ext string, src []byte) (string, error) {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if !agentAttachmentExtAllowlist[ext] {
		return "", fmt.Errorf("unsupported attachment ext %q", ext)
	}
	base = sanitizeAgentAttachmentBase(base)
	if base == "" {
		return "", fmt.Errorf("empty attachment base name")
	}
	if len(src) == 0 {
		return "", fmt.Errorf("empty attachment content")
	}
	if len(src) > agentAttachmentFileMaxBytes {
		return "", fmt.Errorf("attachment %d bytes exceeds %d", len(src), agentAttachmentFileMaxBytes)
	}
	_, dir, err := agentAttachmentSessionDir(sessionID)
	if err != nil {
		return "", err
	}
	sane, _ := sanitizeAgentAttachmentSessionID(sessionID)
	if err = agentAttachmentEnforceSessionQuota(dir, agentAttachmentSessionMaxBytes, len(src)); err != nil {
		return "", err
	}
	full := filepath.Join(dir, base+ext)
	if err = os.WriteFile(full, src, 0o644); err != nil {
		return "", fmt.Errorf("write attachment cache: %w", err)
	}
	return filepath.Join(sane, base+ext), nil
}

// agentAttachmentEnforceSessionQuota 会话帽：现有占用+新文件超帽即拒绝
// （写前拒绝，不做半写后回滚）。
func agentAttachmentEnforceSessionQuota(dir string, capBytes, incoming int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	if total+int64(incoming) > int64(capBytes) {
		return fmt.Errorf("session attachment quota exceeded: %d+%d > %d", total, incoming, capBytes)
	}
	return nil
}

// agentAttachmentResolve 相对引用 → 缓存内绝对路径。穿越守卫：Clean 后必须
// 仍落在缓存根内（agentPathInsideDirectory 用 filepath.Rel 判定，绝对路径与
// .. 逃逸都在这里被拒）。
func agentAttachmentResolve(rel string) (string, error) {
	root, err := agentAttachmentCacheRoot()
	if err != nil {
		return "", err
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("empty attachment ref")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute attachment ref rejected")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("attachment ref escapes cache root: %q", rel)
	}
	full := filepath.Join(root, clean)
	if !agentPathInsideDirectory(full, root) {
		return "", fmt.Errorf("attachment ref escapes cache root: %q", rel)
	}
	if _, err = os.Stat(full); err != nil {
		return "", fmt.Errorf("attachment cache miss: %w", err)
	}
	return full, nil
}

// persistAgentAIAttachmentFiles 把已下载附件逐个落持久缓存（尽力而为：单件
// 失败仅 Warn 降级为「无网页显示」，绝不影响内核收图）。base=messageID
// （调用方兜底 runSeq），文件名 <base>-<idx><ext>；扩展名从原始文件名/路径
// 取，不在白名单的跳过（持久化跳过=无网页显示，spec §3.1 降级带）。
func persistAgentAIAttachmentFiles(sessionID, base string, list []agentAIAttachment) {
	if strings.TrimSpace(sessionID) == "" {
		logger.Warn("[AGENT-PEER-ATTACH] persist skipped reason=empty_session_id")
		return
	}
	for i := range list {
		attachment := &list[i]
		if attachment.Type != "image" || strings.TrimSpace(attachment.Path) == "" {
			continue
		}
		if strings.TrimSpace(attachment.PersistPath) != "" {
			// 已持久化（网页上传路径在 resolve 阶段就地置位）：跳过重写。
			continue
		}
		ext := strings.ToLower(filepath.Ext(attachment.Name))
		if !agentAttachmentExtAllowlist[ext] {
			ext = strings.ToLower(filepath.Ext(attachment.Path))
		}
		if !agentAttachmentExtAllowlist[ext] {
			logger.Warn(fmt.Sprintf("[AGENT-PEER-ATTACH] persist skipped reason=unsupported_ext name=%s", attachment.Name))
			continue
		}
		src, err := os.ReadFile(attachment.Path)
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-PEER-ATTACH] persist read failed idx=%d error=%v", i, err))
			continue
		}
		rel, err := agentAttachmentPersist(sessionID, fmt.Sprintf("%s-%d", base, i), ext, src)
		if err != nil {
			logger.Warn(fmt.Sprintf("[AGENT-PEER-ATTACH] persist failed idx=%d error=%v", i, err))
			continue
		}
		attachment.PersistPath = rel
	}
}

// agentAIJournalAttachmentsFrom 把带 PersistPath 的附件转成 journal 引用视图
// （Ref/Name/Mime/Bytes/Origin）。无 PersistPath 的（下载失败/降级带）不入列。
// Origin 缺省 phone（resolve 阶段命中缓存根的网页图已带 web）。
func agentAIJournalAttachmentsFrom(list []agentAIAttachment) []agentAIJournalAttachment {
	var out []agentAIJournalAttachment
	for _, a := range list {
		if a.Type != "image" || strings.TrimSpace(a.PersistPath) == "" {
			continue
		}
		entry := agentAIJournalAttachment{
			Name:   a.Name,
			Ref:    a.PersistPath,
			Origin: a.Origin,
		}
		switch strings.ToLower(filepath.Ext(a.PersistPath)) {
		case ".jpg", ".jpeg":
			entry.Mime = "image/jpeg"
		case ".png":
			entry.Mime = "image/png"
		case ".gif":
			entry.Mime = "image/gif"
		case ".webp":
			entry.Mime = "image/webp"
		}
		if abs, err := agentAttachmentResolve(a.PersistPath); err == nil {
			if info, err := os.Stat(abs); err == nil {
				entry.Bytes = info.Size()
			}
		}
		if entry.Origin == "" {
			entry.Origin = "phone"
		}
		out = append(out, entry)
	}
	return out
}

// agentAttachmentSweep 全局帽（1GiB）：按会话目录 mtime 从最旧驱逐整目录，
// 直到总量 ≤ 帽。agent 启动时调用一次；孤儿文件（recall/truncateBefore 后）
// 依赖本收敛，不做即时删除。
func agentAttachmentSweep() error {
	return agentAttachmentSweepCap(agentAttachmentGlobalMaxBytes)
}

// AgentAttachmentSweepStartup 供 runtime boot 链调用的导出包装。windows 上
// 先把 home 侧历史缓存迁进统一根（幂等），再走常规配额清扫；runtime boot
// 进程=用户态，此时 legacy 根解析得到的是真用户 home（SYSTEM 进程 home 侧
// 无历史文件，迁移自然空转）。
func AgentAttachmentSweepStartup() error {
	if root, err := agentAttachmentCacheRoot(); err == nil {
		if userHome, uerr := runtimepath.UserHomeDir(); uerr == nil && strings.TrimSpace(userHome) != "" {
			migrateLegacyAttachmentCacheRoot(runtime.GOOS, root, filepath.Join(userHome, agentAttachmentCacheDirName))
		}
	}
	return agentAttachmentSweep()
}

// AgentAttachmentUploadMaxBytes 导出给本地 HTTP 上传端点做读取预检
// （services 内部帽 agentAttachmentFileMaxBytes 的公开别名）。
const AgentAttachmentUploadMaxBytes = agentAttachmentFileMaxBytes

// AgentAttachmentUploadFromBytes 本地 HTTP 上传端点的服务入口（handlers 包
// 调不到包内小写函数）：扩展名从原始文件名推导（白名单外拒绝），base 从
// 文件名主体净化、净化后为空（如纯中文名）用时间戳兜底。
func AgentAttachmentUploadFromBytes(sessionID, originalName string, src []byte) (string, error) {
	ext := strings.ToLower(filepath.Ext(originalName))
	base := sanitizeAgentAttachmentBase(strings.TrimSuffix(originalName, filepath.Ext(originalName)))
	if base == "" {
		base = fmt.Sprintf("up-%d", time.Now().UnixNano())
	}
	return agentAttachmentPersist(sessionID, base, ext, src)
}

// AgentAttachmentOpenByRef 相对引用 → 打开的缓存文件+元信息（取图端点流式
// 回传用）。穿越拒绝与不存在都以 error 返回，端点层区分语义。
func AgentAttachmentOpenByRef(ref string) (*os.File, os.FileInfo, error) {
	path, err := agentAttachmentResolve(ref)
	if err != nil {
		return nil, nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if info.IsDir() {
		_ = f.Close()
		return nil, nil, fmt.Errorf("attachment ref is a directory")
	}
	return f, info, nil
}

// agentAttachmentSweepCap 可注入帽版本（测试用小帽验证驱逐顺序）。
func agentAttachmentSweepCap(capBytes int64) error {
	root, err := agentAttachmentCacheRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	type sessionSize struct {
		dir  string
		size int64
		mod  time.Time
	}
	var sessions []sessionSize
	var total int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		var size int64
		var mod time.Time
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if info, err := f.Info(); err == nil {
				size += info.Size()
				if info.ModTime().After(mod) {
					mod = info.ModTime()
				}
			}
		}
		sessions = append(sessions, sessionSize{dir: dir, size: size, mod: mod})
		total += size
	}
	if total <= capBytes {
		return nil
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].mod.Before(sessions[j].mod) })
	for _, s := range sessions {
		if total <= capBytes {
			break
		}
		if err = os.RemoveAll(s.dir); err != nil {
			return fmt.Errorf("evict session cache %s: %w", s.dir, err)
		}
		total -= s.size
	}
	return nil
}
