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

	// 非 loopback → 403
	get = httptest.NewRequest(http.MethodGet, "/api/agent/ai/attachment?ref=s/a.png", nil)
	get.RemoteAddr = "10.0.0.5:1234"
	rr = httptest.NewRecorder()
	h.HandleAttachmentGet(rr, get)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-loopback get = %d", rr.Code)
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
