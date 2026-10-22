package services

// 附件下载：把服务端下发的短时效预签名 URL 落成临时文件，供 provider CLI 读。
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
//   - 临时目录 MkdirTemp 一次性创建，cleanup 整目录删除

import (
	"context"
	"encoding/base64"
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

	if parsed.Scheme == "data" {
		return target, writeDataURLAttachment(attachment.URL, target)
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("attachment url must use https: %s", parsed.Scheme)
	}
	if _, ok := allowedHosts[strings.ToLower(parsed.Hostname())]; !ok {
		return "", fmt.Errorf("attachment host is not allowed: %s", parsed.Hostname())
	}

	reqCtx, cancel := context.WithTimeout(ctx, attachmentDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, attachment.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build attachment request: %w", err)
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
		return "", fmt.Errorf("download attachment: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download attachment: http %d", resp.StatusCode)
	}

	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", fmt.Errorf("create attachment file: %w", err)
	}
	// 多读 1 字节用来区分「刚好等于上限」和「超限」，避免信任 Content-Length
	// （服务端可以撒谎，chunked 更是没有）。
	written, copyErr := io.Copy(file, io.LimitReader(resp.Body, attachmentDownloadMaxBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("save attachment: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("save attachment: %w", closeErr)
	}
	if written > attachmentDownloadMaxBytes {
		_ = os.Remove(target)
		return "", fmt.Errorf("attachment exceeds %d MiB: %s", attachmentDownloadMaxBytes/(1024*1024), attachment.Name)
	}
	return target, nil
}

func writeDataURLAttachment(raw, target string) error {
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
	if err := os.WriteFile(target, decoded, 0o600); err != nil {
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
