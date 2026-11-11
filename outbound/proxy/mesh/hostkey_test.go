package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// genPubKey 生成一把真 ed25519 公钥:want 指纹/authorized_keys 行与 present
// 公钥都从真钥取,避免手写假指纹被未来实现改动绕过。
func genPubKey(t *testing.T) gossh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	signer, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return signer.PublicKey()
}

// TestPinnedHostKeyMatrix 钉 SPEC §10.4 的两种输入格式 × 三种结果:
// {authorized_keys 行, SHA256: 指纹} × {匹配 → nil, 不匹配 → mismatch,
// 空 → fail-closed}。两种解析分支(指纹比对 vs ParseAuthorizedKey+Marshal
// 字节比对)不许合并,各有自己的钉扎用例。
func TestPinnedHostKeyMatrix(t *testing.T) {
	pub := genPubKey(t)
	pub2 := genPubKey(t)
	akLine := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(pub)))
	fp := gossh.FingerprintSHA256(pub)

	cases := []struct {
		name    string
		want    string
		present gossh.PublicKey
		wantErr string // "" = 必须通过
	}{
		{"authorized_keys match", akLine, pub, ""},
		{"authorized_keys mismatch", akLine, pub2, "host key mismatch"},
		{"authorized_keys empty fail-closed", "", pub, "fail-closed"},
		{"fingerprint match", fp, pub, ""},
		// 指纹分支精确到 "mismatch (fingerprint)":与字节比对分支("host key
		// mismatch" 无后缀)可区分,两分支的错误文案不许互相漂移(SPEC §10.4)。
		{"fingerprint mismatch", fp, pub2, "mismatch (fingerprint)"},
		{"fingerprint empty fail-closed", "", pub, "fail-closed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := pinnedHostKey(c.want)("h", nil, c.present)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want pass, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want err containing %q, got %v", c.wantErr, err)
			}
		})
	}
}

// TestPinnedHostKeyBadConfigRejectsTwice 坏配置必须两次都拒绝:闭包按
// ExitClient 缓存,坏配置不因第二次调用翻案(fail-loud 不降级)。
func TestPinnedHostKeyBadConfigRejectsTwice(t *testing.T) {
	cb := pinnedHostKey("not-a-valid-key-or-fingerprint")
	pub := genPubKey(t)
	if err := cb("h", nil, pub); err == nil || !strings.Contains(err.Error(), "bad host_key config") {
		t.Fatalf("first call: %v", err)
	}
	if err := cb("h", nil, pub); err == nil || !strings.Contains(err.Error(), "bad host_key config") {
		t.Fatalf("second call must still reject (closure cached per ExitClient): %v", err)
	}
}
