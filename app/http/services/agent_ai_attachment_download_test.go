package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newAttachmentTestServer 起一个 https 测试服务器。白名单按 host 匹配，所以
// 测试里把 server.URL 的 host 显式传给下载器——这条路径同时验证了「白名单
// 生效」与「非白名单被拒」。
func newAttachmentTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return server
}

// withTestTransport 让下载器信任 httptest 的自签证书（仅测试）。
func withTestTransport(t *testing.T, server *httptest.Server) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func hostOf(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Hostname()
}

func TestDownloadAgentAIAttachmentsWritesTempFile(t *testing.T) {
	server := newAttachmentTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Errorf("expected a user agent, got empty")
		}
		_, _ = w.Write([]byte("png-bytes"))
	})
	withTestTransport(t, server)

	attachments, cleanup, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "shot.png", URL: server.URL + "/shot.png"},
		{Type: "text", Name: "note.txt", Text: "inline stays inline"},
	}, []string{hostOf(t, server.URL)})
	if err != nil {
		t.Fatalf("download attachments: %v", err)
	}
	defer cleanup()

	if attachments[0].Path == "" {
		t.Fatal("image attachment should have a local path")
	}
	body, err := os.ReadFile(attachments[0].Path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(body) != "png-bytes" {
		t.Fatalf("downloaded body = %q", body)
	}
	if !strings.HasSuffix(attachments[0].Path, "shot.png") {
		t.Fatalf("temp file should keep the attachment name: %s", attachments[0].Path)
	}
	// 文本素材不联网、不落盘。
	if attachments[1].Path != "" || attachments[1].Text != "inline stays inline" {
		t.Fatalf("text attachment was touched: %#v", attachments[1])
	}

	cleanup()
	if _, err := os.Stat(attachments[0].Path); !os.IsNotExist(err) {
		t.Fatalf("cleanup should remove the temp file, stat err = %v", err)
	}
}

func TestDownloadAgentAIAttachmentsRejectsHostOutsideAllowlist(t *testing.T) {
	server := newAttachmentTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not fetch a non-allowlisted host")
	})
	withTestTransport(t, server)

	if _, _, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "x.png", URL: server.URL + "/x.png"},
	}, []string{"cos.example.com"}); err == nil {
		t.Fatal("non-allowlisted host should be rejected")
	}
}

func TestDownloadAgentAIAttachmentsRequiresAllowlist(t *testing.T) {
	t.Setenv("ALIANG_ATTACHMENT_ALLOWED_HOSTS", "")
	if _, _, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "x.png", URL: "https://cos.example.com/x.png"},
	}, nil); err == nil {
		t.Fatal("empty allowlist must reject, not silently allow")
	}
}

func TestDownloadAgentAIAttachmentsEnvAllowlistFallback(t *testing.T) {
	server := newAttachmentTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	withTestTransport(t, server)
	t.Setenv("ALIANG_ATTACHMENT_ALLOWED_HOSTS", strings.ToUpper(hostOf(t, server.URL)))

	attachments, cleanup, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "x.png", URL: server.URL + "/x.png"},
	}, nil)
	if err != nil {
		t.Fatalf("env allowlist should be honoured: %v", err)
	}
	defer cleanup()
	if attachments[0].Path == "" {
		t.Fatal("expected a downloaded path")
	}
}

func TestDownloadAgentAIAttachmentsCapsSize(t *testing.T) {
	server := newAttachmentTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// 多写 1 字节越过上限；下载器必须中断而不是读完再判。
		chunk := make([]byte, 64*1024)
		for written := 0; written <= attachmentDownloadMaxBytes; written += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	withTestTransport(t, server)

	_, _, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "huge.png", URL: server.URL + "/huge.png"},
	}, []string{hostOf(t, server.URL)})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized attachment should be rejected, err = %v", err)
	}
}

func TestDownloadAgentAIAttachmentsRejectsNonHTTPS(t *testing.T) {
	if _, _, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "x.png", URL: "http://cos.example.com/x.png"},
	}, []string{"cos.example.com"}); err == nil {
		t.Fatal("plain http attachment should be rejected")
	}
}

func TestDownloadAgentAIAttachmentsDataURL(t *testing.T) {
	// data:image/png;base64,cG5n
	attachments, cleanup, err := downloadAgentAIAttachments(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "inline.png", URL: "data:image/png;base64,cG5n"},
	}, []string{"cos.example.com"})
	if err != nil {
		t.Fatalf("data URL attachment: %v", err)
	}
	defer cleanup()
	body, err := os.ReadFile(attachments[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "png" {
		t.Fatalf("decoded data URL = %q", body)
	}
}

func TestDownloadAgentAIAttachmentsToCachePersistsAndReuses(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_CACHE_DIR", cacheDir)
	server := newAttachmentTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("png-bytes"))
	})
	withTestTransport(t, server)

	attachments, cleanup, err := downloadAgentAIAttachmentsToCache(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "shot.png", URL: server.URL + "/shot.png"},
	}, []string{hostOf(t, server.URL)})
	if err != nil {
		t.Fatalf("download attachments: %v", err)
	}
	cleanup()

	if !strings.HasPrefix(attachments[0].Path, cacheDir+string(os.PathSeparator)) {
		t.Fatalf("expected cache path under %s, got %s", cacheDir, attachments[0].Path)
	}
	body, err := os.ReadFile(attachments[0].Path)
	if err != nil || string(body) != "png-bytes" {
		t.Fatalf("cached body = %q, err = %v", body, err)
	}
	// cleanup 是 no-op：回合结束后文件必须还在。
	if _, err := os.Stat(attachments[0].Path); err != nil {
		t.Fatalf("cache file should survive cleanup: %v", err)
	}

	again, cleanupAgain, err := downloadAgentAIAttachmentsToCache(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "shot.png", URL: server.URL + "/shot.png"},
	}, []string{hostOf(t, server.URL)})
	if err != nil {
		t.Fatalf("download again: %v", err)
	}
	cleanupAgain()
	if again[0].Path != attachments[0].Path {
		t.Fatalf("same content should reuse cache path: %s vs %s", again[0].Path, attachments[0].Path)
	}
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("cache should hold exactly one file, got %d", len(entries))
	}
}

func TestDownloadAgentAIAttachmentsToCacheExpiresOldFiles(t *testing.T) {
	cacheDir := t.TempDir()
	t.Setenv("ALIANG_ATTACHMENT_CACHE_DIR", cacheDir)
	old := filepath.Join(cacheDir, "old.png")
	if err := os.WriteFile(old, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	eightDaysAgo := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(old, eightDaysAgo, eightDaysAgo); err != nil {
		t.Fatal(err)
	}

	attachments, cleanup, err := downloadAgentAIAttachmentsToCache(context.Background(), []agentAIAttachment{
		{Type: "image", Name: "inline.png", URL: "data:image/png;base64,cG5n"},
	}, []string{"cos.example.com"})
	if err != nil {
		t.Fatalf("download data URL: %v", err)
	}
	cleanup()
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("expired cache file should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(attachments[0].Path); err != nil {
		t.Fatalf("new cache file missing: %v", err)
	}
}

func TestSafeAttachmentFileName(t *testing.T) {
	cases := map[string]string{
		"shot.png":          "shot.png",
		"../../etc/passwd":  "passwd",
		".bashrc":           "bashrc",
		"截图 2026-10-22.png": "___2026-10-22.png",
		"":                  "attachment",
		"..":                "attachment",
	}
	for input, want := range cases {
		if got := safeAttachmentFileName(input); got != want {
			t.Errorf("safeAttachmentFileName(%q) = %q, want %q", input, got, want)
		}
	}
}
