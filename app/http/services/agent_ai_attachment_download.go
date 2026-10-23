package services

// 附件下载：把服务端下发的短时效预签名 URL 落成本地文件，供 provider CLI 读。
//
// 为什么 agent 要自己下载：claude/codex/opencode 三个 CLI 都只认本地路径
// （codex 的 --image、opencode 的 --file、claude 的 image block 都要磁盘文件），
// 而 agent 没有 COS 凭据，服务端给的就是一个 10 分钟有效的 GET URL。
//
// 安全边界（对齐计划 Task 10）：
//   - 只允许 https（data:image/ 例外，直接解码，不联网）
//   - host 白名单：服务端下发的 attachment_allowed_hosts 优先，其次环境变量
//     ALIANG_ATTACHMENT_ALLOWED_HOSTS；两者都空 → 直接拒绝，不做「无白名单放行」
//   - 每次重定向都重新校验 host（预签名 URL 的 host 是可信的，但重定向不是）
//   - 单文件 ≤ 20MiB，边下边数，超限立即中断而不是下完再判
//   - spawn/steer 路径用 MkdirTemp 一次性目录，cleanup 整目录删除；
//     TUI 注入路径用持久缓存目录（downloadAgentAIAttachmentsToCache），
//     同一会话后续回合还能再次 Read 同一张图

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	attachmentDownloadMaxBytes = 20 * 1024 * 1024
	attachmentDownloadTimeout  = 2 * time.Minute
	// TUI 附件缓存保留时长。缓存里的图片可能被同一会话后续回合再次 Read，
	// 但不能无限增长；下载前机会式清理超期文件。
	attachmentCacheTTL = 7 * 24 * time.Hour
)

// downloadAgentAIAttachments 把带 URL 的附件下载到临时目录，返回替换过 Path 的
// 副本与 cleanup。调用方必须 defer cleanup()——它删的是整轮 run 的临时目录。
//
// 没有需要下载的附件时返回原切片与一个 no-op cleanup，调用方无需分支。
func downloadAgentAIAttachments(
	ctx context.Context,
	attachments []agentAIAttachment,
	allowedHosts []string,
) ([]agentAIAttachment, func(), error) {
	noop := func() {}
	if len(attachments) == 0 {
		return attachments, noop, nil
	}
	pending := make([]int, 0, len(attachments))
	for i, attachment := range attachments {
		if attachment.Text != "" || attachment.Path != "" {
			continue
		}
		if attachment.URL != "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return attachments, noop, nil
	}

	hosts := normalizeAttachmentAllowedHosts(allowedHosts)
	if len(hosts) == 0 {
		return nil, noop, errors.New("attachment download refused: no allowed hosts configured")
	}

	dir, err := os.MkdirTemp("", "aliang-attach-*")
	if err != nil {
		return nil, noop, fmt.Errorf("create attachment temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	out := append([]agentAIAttachment(nil), attachments...)
	for _, index := range pending {
		attachment := out[index]
		path, err := downloadAgentAIAttachmentToFile(ctx, attachment, dir, hosts)
		if err != nil {
			cleanup()
			return nil, noop, err
		}
		attachment.Path = path
		// 清掉 URL：下载之后它就是本地文件了。留着会让下游把它当成「远程附件」
		// 再走一遍 URL 分支（codex image block 用 url 而不是 localImage、prompt
		// 附件清单重复列一遍），等于同一张图投递两次。
		attachment.URL = ""
		out[index] = attachment
	}
	return out, cleanup, nil
}

// downloadAgentAIAttachmentsToCache 把附件下载到持久缓存目录，供 TUI 注入路径
// 把本地路径交给终端里的模型 Read。与 downloadAgentAIAttachments 的区别：
//   - 目录固定（默认 <TUI 用户 home>/.aliang/attachments，可用
//     ALIANG_ATTACHMENT_CACHE_DIR 覆盖）
//   - cleanup 是 no-op：回合结束不删文件，同一会话后续回合还能再次 Read
//   - 文件名带内容哈希前缀，同一张图重复下发只占一份
//   - 每次下载前机会式清理超过 attachmentCacheTTL 的旧文件
//
// home 必须传 TUI 会话所属用户的 home（plan.Home）。macOS 的 Core 守护进程
// 以 root 运行，而 Claude Code TUI 是桌面用户进程；若用 os.UserHomeDir()
// 会落到 /var/root，注入给 TUI 的路径对桌面用户不可读。
func downloadAgentAIAttachmentsToCache(
	ctx context.Context,
	attachments []agentAIAttachment,
	allowedHosts []string,
	home string,
) ([]agentAIAttachment, func(), error) {
	noop := func() {}
	if len(attachments) == 0 {
		return attachments, noop, nil
	}
	pending := make([]int, 0, len(attachments))
	for i, attachment := range attachments {
		if attachment.Text != "" || attachment.Path != "" {
			continue
		}
		if attachment.URL != "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return attachments, noop, nil
	}

	hosts := normalizeAttachmentAllowedHosts(allowedHosts)
	if len(hosts) == 0 {
		return nil, noop, errors.New("attachment download refused: no allowed hosts configured")
	}

	dir, err := agentAIAttachmentCacheDir(home)
	if err != nil {
		return nil, noop, fmt.Errorf("resolve attachment cache dir: %w", err)
	}
	// 0755 而不是 0700：agent 可能以 root 运行，目录下的文件要能被桌面用户的
	// Claude Code 进程 Read。home 本身通常是 0700/0750，外部用户仍进不来。
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, noop, fmt.Errorf("create attachment cache dir: %w", err)
	}
	// MkdirAll 不会修正已存在目录的权限：旧版本或 root 早期运行可能留下 0700。
	if err := os.Chmod(dir, 0o755); err != nil {
		return nil, noop, fmt.Errorf("make attachment cache dir readable: %w", err)
	}
	cleanupAgentAIAttachmentCache(dir, time.Now())

	out := append([]agentAIAttachment(nil), attachments...)
	for _, index := range pending {
		attachment := out[index]
		path, err := downloadAgentAIAttachmentToCache(ctx, attachment, dir, hosts)
		if err != nil {
			return nil, noop, err
		}
		attachment.Path = path
		attachment.URL = ""
		out[index] = attachment
	}
	return out, noop, nil
}

func downloadAgentAIAttachmentToFile(
	ctx context.Context,
	attachment agentAIAttachment,
	dir string,
	allowedHosts map[string]struct{},
) (string, error) {
	parsed, err := url.Parse(attachment.URL)
	if err != nil {
		return "", fmt.Errorf("parse attachment url: %w", err)
	}
	name := safeAttachmentFileName(firstNonEmpty(attachment.Name, filepath.Base(parsed.Path), "attachment"))
	target := filepath.Join(dir, name)

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create attachment file: %w", err)
	}
	writeErr := writeAttachmentPayload(ctx, attachment, allowedHosts, file)
	closeErr := file.Close()
	if writeErr != nil {
		_ = os.Remove(target)
		return "", writeErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("save attachment: %w", closeErr)
	}
	return target, nil
}

// downloadAgentAIAttachmentToCache 先写 .partial-* 临时文件，边写边算内容哈希，
// 成功后改名为 <hash12>-<原名>。同一内容重复下载会命中已有文件，只保留一份。
func downloadAgentAIAttachmentToCache(
	ctx context.Context,
	attachment agentAIAttachment,
	dir string,
	allowedHosts map[string]struct{},
) (string, error) {
	parsed, err := url.Parse(attachment.URL)
	if err != nil {
		return "", fmt.Errorf("parse attachment url: %w", err)
	}
	name := safeAttachmentFileName(firstNonEmpty(attachment.Name, filepath.Base(parsed.Path), "attachment"))

	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return "", fmt.Errorf("create attachment cache file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	hasher := sha256.New()
	if err := writeAttachmentPayload(ctx, attachment, allowedHosts, io.MultiWriter(tmp, hasher)); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close attachment cache file: %w", err)
	}

	sum := hex.EncodeToString(hasher.Sum(nil))
	target := filepath.Join(dir, sum[:12]+"-"+name)
	if _, err := os.Stat(target); err == nil {
		// 旧版本可能留下 0600 的缓存；统一放宽到 0644，保证桌面用户的 TUI 能 Read。
		if err := os.Chmod(target, 0o644); err != nil {
			return "", fmt.Errorf("make cached attachment readable: %w", err)
		}
		return target, nil
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return "", fmt.Errorf("cache attachment: %w", err)
	}
	tmpPath = "" // 已改名，defer 不能再删目标
	if err := os.Chmod(target, 0o644); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("make cached attachment readable: %w", err)
	}
	return target, nil
}

// writeAttachmentPayload 把附件内容写到 w。https 走白名单 + 重定向校验 + 20MiB
// 上限；data URL 直接 base64 解码。临时路径与缓存路径共用这一份安全边界。
func writeAttachmentPayload(
	ctx context.Context,
	attachment agentAIAttachment,
	allowedHosts map[string]struct{},
	w io.Writer,
) error {
	parsed, err := url.Parse(attachment.URL)
	if err != nil {
		return fmt.Errorf("parse attachment url: %w", err)
	}
	if parsed.Scheme == "data" {
		return writeDataURLAttachmentTo(attachment.URL, w)
	}
	if parsed.Scheme != "https" {
		return fmt.Errorf("attachment url must use https: %s", parsed.Scheme)
	}
	if _, ok := allowedHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return fmt.Errorf("attachment host is not allowed: %s", parsed.Hostname())
	}

	reqCtx, cancel := context.WithTimeout(ctx, attachmentDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, attachment.URL, nil)
	if err != nil {
		return fmt.Errorf("build attachment request: %w", err)
	}
	client := &http.Client{
		// 每一跳都过白名单：预签名 URL 本身可信，但 302 目标是对方说了算。
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("attachment download: too many redirects")
			}
			if next.URL.Scheme != "https" {
				return fmt.Errorf("attachment redirect must use https: %s", next.URL.Scheme)
			}
			if _, ok := allowedHosts[strings.ToLower(next.URL.Hostname())]; !ok {
				return fmt.Errorf("attachment redirect host is not allowed: %s", next.URL.Hostname())
			}
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download attachment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download attachment: http %d", resp.StatusCode)
	}

	// 多读 1 字节用来区分「刚好等于上限」和「超限」，避免信任 Content-Length
	// （服务端可以撒谎，chunked 更是没有）。
	written, copyErr := io.Copy(w, io.LimitReader(resp.Body, attachmentDownloadMaxBytes+1))
	if copyErr != nil {
		return fmt.Errorf("save attachment: %w", copyErr)
	}
	if written > attachmentDownloadMaxBytes {
		return fmt.Errorf("attachment exceeds %d MiB: %s", attachmentDownloadMaxBytes/(1024*1024), attachment.Name)
	}
	return nil
}

// agentAIAttachmentCacheDir 返回 TUI 附件缓存目录。优先使用 TUI 用户 home
// （plan.Home），默认 <home>/.aliang/attachments；ALIANG_ATTACHMENT_CACHE_DIR
// 可覆盖（测试/运维）。放在持久目录而不是 MkdirTemp：同一会话的后续回合可能
// 再次 Read 同一张图，回合结束就删会让第二次 Read 失败。
func agentAIAttachmentCacheDir(home string) (string, error) {
	if dir := strings.TrimSpace(os.Getenv("ALIANG_ATTACHMENT_CACHE_DIR")); dir != "" {
		// 相对路径按 agent 进程 cwd 解析不可预测；显式转绝对路径，日志里也能直接看出位置。
		if abs, err := filepath.Abs(dir); err == nil {
			return abs, nil
		}
		return dir, nil
	}
	if home = strings.TrimSpace(home); home != "" {
		return filepath.Join(home, ".aliang", "attachments"), nil
	}
	if userHome, err := os.UserHomeDir(); err == nil && strings.TrimSpace(userHome) != "" {
		return filepath.Join(userHome, ".aliang", "attachments"), nil
	}
	if cache, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cache) != "" {
		return filepath.Join(cache, "aliang", "attachments"), nil
	}
	return filepath.Join(os.TempDir(), "aliang-attachments"), nil
}

// cleanupAgentAIAttachmentCache 机会式清理过期缓存。下载前调用一次，成本只有
// 一次 ReadDir；失败不影响下载。.partial-* 是中断的下载，1 小时后清掉。
func cleanupAgentAIAttachmentCache(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		ttl := attachmentCacheTTL
		if strings.HasPrefix(entry.Name(), ".partial-") {
			ttl = time.Hour
		}
		if now.Sub(info.ModTime()) > ttl {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func writeDataURLAttachmentTo(raw string, w io.Writer) error {
	comma := strings.IndexByte(raw, ',')
	if comma < 0 {
		return errors.New("malformed data URL attachment")
	}
	meta := raw[len("data:"):comma]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return errors.New("data URL attachment must be base64 encoded")
	}
	payload := raw[comma+1:]
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return fmt.Errorf("decode data URL attachment: %w", err)
	}
	if len(decoded) > attachmentDownloadMaxBytes {
		return fmt.Errorf("attachment exceeds %d MiB", attachmentDownloadMaxBytes/(1024*1024))
	}
	if _, err := w.Write(decoded); err != nil {
		return fmt.Errorf("save data URL attachment: %w", err)
	}
	return nil
}

// normalizeAttachmentAllowedHosts 合并服务端下发与环境变量的白名单，统一小写。
// 允许写成 host、host:port 或 https://host/path，一律只取 hostname 比较。
func normalizeAttachmentAllowedHosts(serverHosts []string) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(raw string) {
		host := strings.TrimSpace(raw)
		if host == "" {
			return
		}
		if strings.Contains(host, "://") {
			if parsed, err := url.Parse(host); err == nil {
				host = parsed.Host
			}
		}
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(strings.TrimSpace(host), "[]"))
		if host != "" {
			out[host] = struct{}{}
		}
	}
	for _, host := range serverHosts {
		add(host)
	}
	for _, host := range strings.Split(os.Getenv("ALIANG_ATTACHMENT_ALLOWED_HOSTS"), ",") {
		add(host)
	}
	return out
}

// safeAttachmentFileName 把附件名压成一个安全的单层文件名：去掉任何目录分隔，
// 只保留可打印 ASCII 的安全子集，并剥掉前导点（避免 . / .. / .bashrc）。
func safeAttachmentFileName(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	cleaned := strings.TrimLeft(b.String(), ".")
	if cleaned == "" {
		return "attachment"
	}
	return cleaned
}
