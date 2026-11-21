package handlers

// 本地附件端点测试（spec 2026-11-05-chat-image-pipeline §3.4）：上传往返/
// 守卫三件套（loopback/Origin/穿越）/降级口径。缓存根用 env 重定向到临时
// 目录，不触真实 home。
import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachmentTestEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_PERSIST_DIR", root)
	return root
}

func attachmentUploadRequest(t *testing.T, sessionID, filename string, content []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	if err := writer.WriteField("session_id", sessionID); err != nil {
		t.Fatalf("write field: %v", err)
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/ai/attachment/upload", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.RemoteAddr = "127.0.0.1:55555"
	return req
}

func TestHandleAttachmentUploadRoundTrip(t *testing.T) {
	root := attachmentTestEnv(t)
	h := NewAgentHandler(nil)
	content := []byte("fake-png-bytes")

	upload := attachmentUploadRequest(t, "sess-web", "shot.png", content)
	rr := httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, upload)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload code = %d body=%s", rr.Code, rr.Body.String())
	}
	var envelope struct {
		Code int `json:"code"`
		Data struct {
			Attachment struct {
				Ref   string `json:"ref"`
				Path  string `json:"path"`
				Name  string `json:"name"`
				Bytes int    `json:"bytes"`
			} `json:"attachment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ref := envelope.Data.Attachment.Ref
	if ref == "" || !strings.HasPrefix(ref, "sess-web") {
		t.Fatalf("ref = %q", ref)
	}
	// path 契约（2026-11-20 审计⑤）：ai.message 载荷的硬依赖——宿主
	// resolveAgentAIAttachments 要求 path 或 url 二选一。信封缺 path 时网页
	// 端 uploadChatAttachment 直接「Malformed upload response」（前端契约
	// 测试同钉）。path 必须是缓存里真实存在的文件。
	if envelope.Data.Attachment.Path == "" {
		t.Fatalf("信封缺 path 字段（前端强制校验必拒）: %s", rr.Body.String())
	}
	if _, err := os.Stat(envelope.Data.Attachment.Path); err != nil {
		t.Fatalf("path 不在缓存盘上: %v (path=%q)", err, envelope.Data.Attachment.Path)
	}

	// 取图：内容逐字节一致 + Content-Type 正确
	get := httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref="+url.QueryEscape(ref), nil)
	get.RemoteAddr = "127.0.0.1:55555"
	rr2 := httptest.NewRecorder()
	h.HandleAttachmentGet(rr2, get)
	if rr2.Code != http.StatusOK {
		t.Fatalf("get code = %d", rr2.Code)
	}
	if !bytes.Equal(rr2.Body.Bytes(), content) {
		t.Fatalf("content mismatch: %q", rr2.Body.String())
	}
	if ct := rr2.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("content-type = %q", ct)
	}
	// 落盘位置确实在缓存根下
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ref))); err != nil {
		t.Fatalf("cache file missing: %v", err)
	}
}

func TestHandleAttachmentUploadGuards(t *testing.T) {
	attachmentTestEnv(t)
	h := NewAgentHandler(nil)

	// 非 loopback
	req := attachmentUploadRequest(t, "s", "a.png", []byte("x"))
	req.RemoteAddr = "10.0.0.5:1234"
	rr := httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-loopback = %d", rr.Code)
	}

	// 跨源 Origin
	req = attachmentUploadRequest(t, "s", "a.png", []byte("x"))
	req.RemoteAddr = "127.0.0.1:55555"
	req.Header.Set("Origin", "https://evil.example.com")
	rr = httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin = %d", rr.Code)
	}

	// 缺 session_id
	req = attachmentUploadRequest(t, "", "a.png", []byte("x"))
	req.RemoteAddr = "127.0.0.1:55555"
	rr = httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("missing session_id = %d", rr.Code)
	}

	// 超过 10MiB
	req = attachmentUploadRequest(t, "s", "big.png", make([]byte, 10<<20+1))
	req.RemoteAddr = "127.0.0.1:55555"
	rr = httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversize = %d", rr.Code)
	}

	// 扩展名白名单外
	req = attachmentUploadRequest(t, "s", "evil.txt", []byte("x"))
	req.RemoteAddr = "127.0.0.1:55555"
	rr = httptest.NewRecorder()
	h.HandleAttachmentUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("unsupported ext = %d", rr.Code)
	}
}

func TestHandleAttachmentGetGuards(t *testing.T) {
	attachmentTestEnv(t)
	h := NewAgentHandler(nil)

	// 穿越 ref → 403（端点层转义判定）
	get := httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref="+url.QueryEscape("../escape.png"), nil)
	get.RemoteAddr = "127.0.0.1:55555"
	rr := httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("escape ref = %d", rr.Code)
	}

	// 不存在 → 404
	get = httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref="+url.QueryEscape("s/missing.png"), nil)
	get.RemoteAddr = "127.0.0.1:55555"
	rr = httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("missing ref = %d", rr.Code)
	}

	// 非 loopback 无 Origin GET → 200（2026-11-21 拍板：取图对齐真同源口径，
	// <img> 天然无 Origin，LAN 页图片不再裂；ref 穿越守卫上面已单测）
	get = httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref=s/a.png", nil)
	get.RemoteAddr = "10.0.0.5:1234"
	rr = httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code == http.StatusForbidden {
		t.Fatalf("LAN 无 Origin 取图不应 403（拍板新口径），got %d", rr.Code)
	}

	// 带 Origin 的跨源取图 → 仍 403（真同源闸）
	get = httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref=s/a.png", nil)
	get.Header.Set("Origin", "http://evil.example")
	get.Host = "192.168.1.50:56431"
	get.RemoteAddr = "10.0.0.5:1234"
	rr = httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin get = %d", rr.Code)
	}

	// 跨源 GET → 403
	get = httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref=s/a.png", nil)
	get.RemoteAddr = "127.0.0.1:55555"
	get.Header.Set("Origin", "https://evil.example.com")
	rr = httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-origin get = %d", rr.Code)
	}
}

// ===== LAN 同源口径（2026-11-21 审计拍板：附件对齐聊天 WS 真同源）=====
// 旧口径 loopback+Origin 双闸把 LAN 浏览的图片收发整体 403（文字能聊、图片
// 全裂）。新口径：有 Origin → 真同源（aiStreamHostAllowed 同判定）；无
// Origin 的写语义保持 loopback 底线（非浏览器直写）；无 Origin 的读语义
// （<img> 取图天然无 Origin）放行——ref 穿越守卫不变。

func attachmentRequestWithOrigin(t *testing.T, method, rawURL, origin, remoteAddr string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, rawURL, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.RemoteAddr = remoteAddr
	return req
}

func TestAttachmentUploadAllowsLANSameOrigin(t *testing.T) {
	_ = attachmentTestEnv(t)
	h := NewAgentHandler(nil)
	upload := attachmentUploadRequest(t, "sess-lan", "lan.png", []byte("lan-bytes"))
	// attachmentUploadRequest 默认 Origin 空 + 回环地址——改造成 LAN 同源形态
	upload.Header.Set("Origin", "http://192.168.1.50:56431")
	upload.Host = "192.168.1.50:56431"
	upload.RemoteAddr = "192.168.1.50:54321"
	rec := httptest.NewRecorder()
	h.HandleAttachmentUpload(rec, upload)
	if rec.Code != http.StatusOK {
		t.Fatalf("LAN 同源上传应放行，got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAttachmentGetAllowsLANSameOrigin(t *testing.T) {
	root := attachmentTestEnv(t)
	_ = root
	h := NewAgentHandler(nil)
	content := []byte("lan-img")
	upload := attachmentUploadRequest(t, "sess-lan2", "img.png", content)
	upload.Header.Set("Origin", "http://192.168.1.50:56431")
	upload.Host = "192.168.1.50:56431"
	upload.RemoteAddr = "192.168.1.50:54321"
	rec := httptest.NewRecorder()
	h.HandleAttachmentUpload(rec, upload)
	if rec.Code != http.StatusOK {
		t.Fatalf("上传失败: %d", rec.Code)
	}
	var envelope struct {
		Data struct {
			Attachment struct {
				Ref string `json:"ref"`
			} `json:"attachment"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ref := envelope.Data.Attachment.Ref

	// LAN 取图：<img> 无 Origin 头、RemoteAddr 为 LAN IP——放行（ref 守卫不变）
	get := httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref="+url.QueryEscape(ref), nil)
	get.Host = "192.168.1.50:56431"
	get.RemoteAddr = "192.168.1.50:54321"
	rec2 := httptest.NewRecorder()
	h.HandleAttachmentGet(rec2, get)
	if rec2.Code != http.StatusOK {
		t.Fatalf("LAN 取图应放行，got %d", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), content) {
		t.Fatal("内容不一致")
	}
}

func TestAttachmentUploadStillRejectsCrossOrigin(t *testing.T) {
	_ = attachmentTestEnv(t)
	h := NewAgentHandler(nil)
	upload := attachmentUploadRequest(t, "sess-x", "x.png", []byte("x"))
	upload.Header.Set("Origin", "http://evil.example")
	upload.Host = "192.168.1.50:56431"
	upload.RemoteAddr = "192.168.1.50:54321"
	rec := httptest.NewRecorder()
	h.HandleAttachmentUpload(rec, upload)
	if rec.Code == http.StatusOK {
		t.Fatal("跨源上传仍应拒绝")
	}
}

func TestAttachmentUploadNoOriginNonLoopbackStillRejected(t *testing.T) {
	_ = attachmentTestEnv(t)
	h := NewAgentHandler(nil)
	upload := attachmentUploadRequest(t, "sess-nb", "n.png", []byte("n"))
	// 无 Origin（非浏览器直写）+ LAN 地址：写语义保持 loopback 底线
	upload.RemoteAddr = "192.168.1.50:54321"
	rec := httptest.NewRecorder()
	h.HandleAttachmentUpload(rec, upload)
	if rec.Code == http.StatusOK {
		t.Fatal("无 Origin 非 loopback 上传仍应拒绝")
	}
}
