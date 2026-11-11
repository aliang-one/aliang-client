package mesh

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"
)

func TestPeerSpecCacheKeyOrderInsensitive(t *testing.T) {
	a := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"1.1.1.1:56432", "2.2.2.2:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	b := PeerSpec{DeviceID: "dev-b", LANAddrs: []string{"2.2.2.2:56432", "1.1.1.1:56432"}, HostKey: "h", SSHUser: "mesh", Relay: true}
	if a.CacheKey("pk", "") != b.CacheKey("pk", "") {
		t.Fatal("same set, different order → same key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk2", "") {
		t.Fatal("private key change must change key")
	}
	if a.CacheKey("pk", "") == a.CacheKey("pk", "pw") {
		t.Fatal("passphrase change must change key")
	}
	c := a
	c.HostKey = "h2"
	if a.CacheKey("pk", "") == c.CacheKey("pk", "") {
		t.Fatal("host key change must change key")
	}
	d := a
	d.DeviceID = "dev-c"
	if a.CacheKey("pk", "") == d.CacheKey("pk", "") {
		t.Fatal("device id change must change key")
	}
	e := a
	e.SSHUser = "mesh2"
	if a.CacheKey("pk", "") == e.CacheKey("pk", "") {
		t.Fatal("ssh user change must change key")
	}
	f := a
	f.Relay = false
	if a.CacheKey("pk", "") == f.CacheKey("pk", "") {
		t.Fatal("relay change must change key")
	}
	// nil 与空切片语义同为「无 LAN 入口」,分键只会造重复缓存条目。
	nilSpec := PeerSpec{DeviceID: "dev-n", LANAddrs: nil, HostKey: "h", SSHUser: "mesh", Relay: true}
	emptySpec := PeerSpec{DeviceID: "dev-n", LANAddrs: []string{}, HostKey: "h", SSHUser: "mesh", Relay: true}
	if nilSpec.CacheKey("pk", "") != emptySpec.CacheKey("pk", "") {
		t.Fatal("nil and empty LANAddrs must share a key")
	}
}

func TestSignerFromPEM(t *testing.T) {
	t.Run("empty or whitespace key fails with configured-missing error", func(t *testing.T) {
		for _, bad := range []string{"", "  \n\t ", "\n\n"} {
			_, err := SignerFromPEM(bad, "")
			if err == nil {
				t.Fatalf("SignerFromPEM(%q) must fail, got nil error", bad)
			}
			if !strings.Contains(err.Error(), "private key not configured") {
				t.Fatalf("error must name missing key, got: %v", err)
			}
		}
	})

	t.Run("garbage bytes error not panic", func(t *testing.T) {
		_, err := SignerFromPEM("definitely not a pem block", "")
		if err == nil {
			t.Fatal("garbage bytes must return an error")
		}
	})

	t.Run("passphrase key round-trips through ParsePrivateKeyWithPassphrase", func(t *testing.T) {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate ed25519 key: %v", err)
		}
		block, err := gossh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pw"))
		if err != nil {
			t.Fatalf("MarshalPrivateKeyWithPassphrase: %v", err)
		}
		pemText := string(pem.EncodeToMemory(block))

		signer, err := SignerFromPEM(pemText, "pw")
		if err != nil {
			t.Fatalf("correct passphrase must parse: %v", err)
		}
		if signer == nil {
			t.Fatal("correct passphrase must yield a non-nil signer")
		}

		if _, err := SignerFromPEM(pemText, "wrong"); err == nil {
			t.Fatal("wrong passphrase must return an error")
		}
	})
}
