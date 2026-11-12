package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// keygen 端点(spec 2026-11-12 §1.2/§4):POST-only 405、Success 信封、
// snake_case 线格式六字段、熵独立。
func TestSSHIngressKeygenHandler(t *testing.T) {
	t.Run("POST returns snake_case wire envelope", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		rec := httptest.NewRecorder()
		h.HandleKeygen(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh-ingress/keygen", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("POST code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Code int `json:"code"`
			Data struct {
				Key struct {
					PublicKey         string `json:"public_key"`
					PrivateKey        string `json:"private_key"`
					Fingerprint       string `json:"fingerprint"`
					Algorithm         string `json:"algorithm"`
					Comment           string `json:"comment"`
					SuggestedFilename string `json:"suggested_filename"`
				} `json:"key"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		if resp.Code != 0 {
			t.Fatalf("envelope code = %d, want 0", resp.Code)
		}
		key := resp.Data.Key
		// snake_case 断言即上面 struct tag 本身——空串=tag 直出失败/字段缺失
		if key.PublicKey == "" || key.PrivateKey == "" || key.Fingerprint == "" ||
			key.Algorithm == "" || key.Comment == "" || key.SuggestedFilename == "" {
			t.Fatalf("wire fields empty: %+v", key)
		}
		if _, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey)); err != nil {
			t.Fatalf("public_key line unparseable: %v", err)
		}
	})

	t.Run("two POSTs produce independent keys", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		read := func() string {
			rec := httptest.NewRecorder()
			h.HandleKeygen(rec, httptest.NewRequest(http.MethodPost, "/api/proxy/ssh-ingress/keygen", nil))
			var resp struct {
				Data struct {
					Key struct {
						PublicKey string `json:"public_key"`
					} `json:"key"`
				} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode: %v", err)
			}
			return resp.Data.Key.PublicKey
		}
		if read() == read() {
			t.Fatalf("two POSTs produced identical public keys")
		}
	})

	t.Run("non-POST is 405 via http.Error", func(t *testing.T) {
		h := NewSSHIngressKeygenHandler()
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			rec := httptest.NewRecorder()
			h.HandleKeygen(rec, httptest.NewRequest(method, "/api/proxy/ssh-ingress/keygen", nil))
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s code = %d, want 405", method, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "Method not allowed") {
				t.Fatalf("%s body = %q, want http.Error plain text", method, rec.Body.String())
			}
		}
	})
}
