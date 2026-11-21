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
	"path/filepath"
	"strings"

	"aliang.one/nursorgate/app/http/common"
	"aliang.one/nursorgate/app/http/services"
)

// guardAttachmentOrigin 附件两端点的同源口径（2026-11-21 审计拍板：对齐聊
// 天 WS 真同源——旧 loopback+Origin 双闸把 LAN 浏览的图片收发整体 403，同页
// 「文字能聊、图片全裂」分叉）：
//   - 有 Origin（浏览器）：真同源判定（aiStreamHostAllowed 同款，Origin.Host
//     == r.Host 精确比较）——LAN 同源页放行，跨源断掉；
//   - 无 Origin：写语义（POST 等）保持 loopback 底线（非浏览器直写）；读语义
//     （<img> 取图天然无 Origin）放行——LAN 页的图不再裂，ref 穿越守卫不变。
func guardAttachmentOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		if !aiStreamHostAllowed(r) {
			common.Error(w, common.CodeForbidden, "Cross-origin requests are not allowed", nil)
			return false
		}
		return true
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err != nil ||
			(host != "127.0.0.1" && host != "::1") {
			common.Error(w, common.CodeForbidden, "Loopback only", nil)
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
	if !guardAttachmentOrigin(w, r) {
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
	// path 契约（2026-11-20 审计⑤）：前端 uploadChatAttachment 强制校验
	// attachment.path，ai.message 载荷的 path 也由它供给（宿主
	// resolveAgentAIAttachments 要求 path 或 url 二选一）。只回 ref 的旧信封
	// 会让网页发图链路 100% 死在「Malformed upload response」。
	attPath, err := services.AgentAttachmentPathByRef(ref)
	if err != nil {
		common.ErrorBadRequest(w, err.Error(), nil)
		return
	}
	common.Success(w, map[string]interface{}{
		"attachment": map[string]interface{}{
			"ref":   ref,
			"path":  attPath,
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
	if !guardAttachmentOrigin(w, r) {
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
