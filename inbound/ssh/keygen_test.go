package ssh

import (
	"encoding/pem"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

// 一键生成人用密钥(spec 2026-11-12 §1.1/§4):格式可解析、公私互证、
// 熵独立、文件名保守清洗。
func TestGenerateIngressKey(t *testing.T) {
	key, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("GenerateIngressKey: %v", err)
	}

	// 公钥行:gossh.ParseAuthorizedKey 可解析,注释前缀可辨
	pub, comment, _, _, err := gossh.ParseAuthorizedKey([]byte(key.PublicKey))
	if err != nil {
		t.Fatalf("public key line unparseable: %v", err)
	}
	if !strings.HasPrefix(comment, "aliang-ingress-") {
		t.Fatalf("comment = %q, want prefix aliang-ingress-", comment)
	}

	// 私钥 PEM:ParsePrivateKey 回读,指纹与公钥互证
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		t.Fatalf("private key PEM undecodable")
	}
	priv, err := gossh.ParsePrivateKey([]byte(key.PrivateKey))
	if err != nil {
		t.Fatalf("private key unparseable: %v", err)
	}
	if gossh.FingerprintSHA256(priv.PublicKey()) != gossh.FingerprintSHA256(pub) {
		t.Fatalf("pubkey/privkey fingerprint mismatch")
	}
	if key.Fingerprint != gossh.FingerprintSHA256(pub) {
		t.Fatalf("Fingerprint field = %q, want %q", key.Fingerprint, gossh.FingerprintSHA256(pub))
	}
	if key.Algorithm != pub.Type() {
		t.Fatalf("Algorithm = %q, want %q", key.Algorithm, pub.Type())
	}

	// 文件名:无路径分隔符、.pem 后缀
	if key.SuggestedFilename == "" ||
		strings.ContainsAny(key.SuggestedFilename, `/\`) ||
		!strings.HasSuffix(key.SuggestedFilename, ".pem") {
		t.Fatalf("SuggestedFilename = %q", key.SuggestedFilename)
	}
}

func TestGenerateIngressKeyEntropyIndependent(t *testing.T) {
	a, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := GenerateIngressKey()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if a.PublicKey == b.PublicKey {
		t.Fatalf("two calls produced identical public keys")
	}
}

// hostname 清洗:特殊字符折叠为 '-',清洗后为空回落 "node"(防空段/前导破折号)。
func TestGenerateIngressKeyFilenameSanitize(t *testing.T) {
	if got := sanitizeIngressHost(`bad host/..\\x`); got != "bad-host-..-x" {
		t.Fatalf("sanitizeIngressHost = %q", got)
	}
	if got := sanitizeIngressHost("///"); got != "node" {
		t.Fatalf("all-unsafe hostname should fall back to node, got %q", got)
	}
	if got := sanitizeIngressHost("My-PC_01"); got != "My-PC_01" {
		t.Fatalf("safe hostname must be kept verbatim, got %q", got)
	}
	if got := sanitizeIngressHost("--foo--"); got != "foo" {
		t.Fatalf("leading/trailing dashes must be trimmed, got %q", got)
	}
}
