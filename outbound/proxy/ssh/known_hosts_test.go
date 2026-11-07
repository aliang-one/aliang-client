package ssh

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func testSigner(t *testing.T) gossh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	s, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	return s
}

func newTestKnownHosts(t *testing.T) *KnownHosts {
	t.Helper()
	dir := t.TempDir()
	return NewKnownHosts(filepath.Join(dir, "known_hosts"))
}

func TestKnownHostsTOFU(t *testing.T) {
	kh := newTestKnownHosts(t)
	signer := testSigner(t)
	pub := signer.PublicKey()
	fp := gossh.FingerprintSHA256(pub)

	cb := kh.CallbackFor("h1.example:22")
	// 首连:写入并放行
	if err := cb("h1.example", &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("first connect rejected: %v", err)
	}
	// 复连:同指纹放行
	if err := cb("h1.example", &net.TCPAddr{}, pub); err != nil {
		t.Fatalf("repeat connect rejected: %v", err)
	}
	// 换指纹:拒连
	other := testSigner(t)
	err := cb("h1.example", &net.TCPAddr{}, other.PublicKey())
	if err == nil {
		t.Fatal("mismatched fingerprint must be rejected")
	}
	// List 可见
	entries := kh.List()
	if len(entries) != 1 || entries[0].Fingerprint != fp {
		t.Fatalf("List() = %+v, want single entry fp=%s", entries, fp)
	}
	// 删除后重采
	if err := kh.Remove("h1.example:22"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := cb("h1.example", &net.TCPAddr{}, other.PublicKey()); err != nil {
		t.Fatalf("re-TOFU after remove rejected: %v", err)
	}
}

func TestKnownHostsPersistAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	signer := testSigner(t)

	kh1 := NewKnownHosts(path)
	if err := kh1.CallbackFor("s:22")("s", &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("known_hosts file not created: %v", err)
	}

	kh2 := NewKnownHosts(path)
	if err := kh2.CallbackFor("s:22")("s", &net.TCPAddr{}, signer.PublicKey()); err != nil {
		t.Fatalf("second instance must trust persisted fingerprint: %v", err)
	}
}
