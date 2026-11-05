package handlers

// 本地附件端点（spec 2026-11-05-chat-image-pipeline §3.4）：网页发图的上传
// 入口与网页显示的取图出口。安全三件套与 transfers D13 同源——①loopback
// 强制；②跨源 Origin 拒绝；③取图 ref 双层穿越守卫（端点层 Clean+inside
// 判定，服务层 agentAttachmentResolve 再守一遍）。PhoneServer 零改动。

import (
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

// guardAttachmentLoopbackOrigin 复刻 transfers D13 的本地入口加固：强制
// loopback + 拒绝跨源 Origin（浏览器跨源 POST 恒带 Origin，text/plain 免
// 预检的 CSRF 盲触发在此断掉；同源 GET 不带 Origin 不受影响）。
func guardAttachmentLoopbackOrigin(w http.ResponseWriter, r *http.Request) bool {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil ||
		(host != "127.0.0.1" && host != "::1") {
		common.Error(w, common.CodeForbidden, "Loopback only", nil)
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			common.Error(w, common.CodeForbidden, "Invalid origin", nil)
			return false
		}
		switch u.Hostname() {
		case "127.0.0.1", "localhost", "::1":
			// 本机仪表盘页面，放行
		default:
			common.Error(w, common.CodeForbidden, "Cross-origin requests are not allowed", nil)
			return false
		}
	}
	return true
}

// HandleAttachmentUpload POST /api/agent/ai/attachment/upload——multipart：
// session_id（非空，落对应会话缓存目录）+ file。白名单 jpeg/png/gif/webp、
// 单文件 ≤10MiB（服务层钳）。
func (h *AgentHandler) HandleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !guardAttachmentLoopbackOrigin(w, r) {
		return
	}
	if err := r.ParseMultipartForm(12 << 20); err != nil {
		common.ErrorBadRequest(w, "Invalid multipart form", nil)
		return
	}

	sessionID := strings.TrimSpace(r.FormValue("session_id"))
	if sessionID == "" {
		common.ErrorBadRequest(w, "Missing required field: session_id", nil)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		common.ErrorBadRequest(w, "Missing multipart file: file", nil)
		return
	}
	defer func() { _ = file.Close() }()
	src, err := io.ReadAll(io.LimitReader(file, services.AgentAttachmentUploadMaxBytes+1))
	if err != nil {
		common.ErrorBadRequest(w, "Read upload failed", nil)
		return
	}
	if len(src) > services.AgentAttachmentUploadMaxBytes {
		common.ErrorBadRequest(w, "Attachment exceeds 10 MiB", nil)
		return
	}
	ref, err := services.AgentAttachmentUploadFromBytes(sessionID, header.Filename, src)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{
		"attachment": map[string]interface{}{
			"ref":   ref,
			"name":  header.Filename,
			"mime":  mime.TypeByExtension(filepath.Ext(header.Filename)),
			"bytes": len(src),
		},
	})
}

// HandleAttachmentGet GET /api/agent/ai/attachment?ref=——流式回传缓存文件。
// ref 是 journal 里的相对引用；穿越/不存在在此层转 403/404（服务层已守）。
func (h *AgentHandler) HandleAttachmentGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		common.Error(w, http.StatusMethodNotAllowed, "Method not allowed", nil)
		return
	}
	if !guardAttachmentLoopbackOrigin(w, r) {
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		common.ErrorBadRequest(w, "Missing required query: ref", nil)
		return
	}
	f, info, err := services.AgentAttachmentOpenByRef(ref)
	if err != nil {
		// 业务码按仓内惯例：穿越=102 禁止、缺失=103 不存在（HTTP 由
		// ErrorCodeToHTTPStatus 映射；传裸 http.Status 常量会落到 500）。
		if strings.Contains(err.Error(), "escapes cache root") {
			common.Error(w, common.CodeForbidden, "Invalid attachment ref", nil)
			return
		}
		common.Error(w, common.CodeNotFound, "Attachment not found", nil)
		return
	}
	defer func() { _ = f.Close() }()
	// nosniff + 显式 Content-Type：缓存扩展名白名单已限图片，双保险防嗅探漂移。
	contentType := mime.TypeByExtension(filepath.Ext(info.Name()))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(info.Name())+`"`)
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
