package handlers

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"
)

// writeTestHostKey 生成 ed25519 密钥并以 OpenSSH PEM 落盘(与 inbound/ssh
// generateHostKey+writeHostKeyAtomic 的文件内容同构;测试不耦合原子写细节,
// 只需文件可被服务端加载路径解析)。返回该钥的指纹与算法名。
func writeTestHostKey(t *testing.T, path string) (fingerprint, algorithm string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	block, err := gossh.MarshalPrivateKey(priv, "test ingress host key")
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	pub := signer.PublicKey()
	return gossh.FingerprintSHA256(pub), pub.Type()
}

func TestSSHIngressHostkeyHandler(t *testing.T) {
	t.Run("GET existing key returns fingerprint envelope", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ingress_host_key")
		fp, alg := writeTestHostKey(t, path)
		before := time.Now().Add(-time.Minute)
		h := NewSSHIngressHandler(path)

		rec := httptest.NewRecorder()
		h.HandleHostkey(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh-ingress/hostkey", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("GET code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Code int `json:"code"`
			Data struct {
				Hostkey struct {
					Fingerprint string `json:"fingerprint"`
					Algorithm   string `json:"algorithm"`
					GeneratedAt string `json:"generated_at"`
				} `json:"hostkey"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode body: %v; body=%s", err, rec.Body.String())
		}
		if resp.Code != 0 {
			t.Fatalf("business code = %d, want 0(success)", resp.Code)
		}
		if resp.Data.Hostkey.Fingerprint != fp {
			t.Fatalf("fingerprint = %q, want %q", resp.Data.Hostkey.Fingerprint, fp)
		}
		if resp.Data.Hostkey.Algorithm != alg || alg != "ssh-ed25519" {
			t.Fatalf("algorithm = %q, want ssh-ed25519", resp.Data.Hostkey.Algorithm)
		}
		generatedAt, err := time.Parse(time.RFC3339, resp.Data.Hostkey.GeneratedAt)
		if err != nil {
			t.Fatalf("generated_at %q not RFC3339: %v", resp.Data.Hostkey.GeneratedAt, err)
		}
		// generated_at = 文件 mtime:写入发生在 before 之后。
		if generatedAt.Before(before) {
			t.Fatalf("generated_at %v predates key file creation %v", generatedAt, before)
		}
	})

	t.Run("GET missing key returns 404 not-started and does NOT generate", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ingress_host_key")
		h := NewSSHIngressHandler(path)

		rec := httptest.NewRecorder()
		h.HandleHostkey(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh-ingress/hostkey", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET on missing key code = %d, want 404; body=%s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "SSH 入口尚未启动") {
			t.Fatalf("404 body should carry not-started copy, got: %s", rec.Body.String())
		}
		// 职责单一裁定:handler 只读不生成——缺失时绝不能落盘新钥
		// (生成归 inbound/ssh Server.Start,防双进程竞态换钥)。
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("handler must not generate host key on missing file, stat err=%v", err)
		}
	})

	t.Run("GET corrupt key returns 500 explicit", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ingress_host_key")
		if err := os.WriteFile(path, []byte("this is not a pem private key"), 0o600); err != nil {
			t.Fatalf("seed corrupt key: %v", err)
		}
		h := NewSSHIngressHandler(path)

		rec := httptest.NewRecorder()
		h.HandleHostkey(rec, httptest.NewRequest(http.MethodGet, "/api/proxy/ssh-ingress/hostkey", nil))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET on corrupt key code = %d, want 500; body=%s", rec.Code, rec.Body.String())
		}
	})

	t.Run("POST returns real 405 not envelope 500", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "ingress_host_key")
		writeTestHostKey(t, path)
		h := NewSSHIngressHandler(path)

		rec := httptest.NewRecorder()
		h.HandleHostkey(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh-ingress/hostkey", nil))

		// common.Error 直传 http.StatusMethodNotAllowed 会被 ErrorCodeToHTTPStatus
		// 映射成 500(仓内坑);必须 http.Error 出真实 405——同 SSHKnownHostsHandler
		// HandleKnownHosts 先例。
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST code = %d, want 405; body=%s", rec.Code, rec.Body.String())
		}
	})
}
