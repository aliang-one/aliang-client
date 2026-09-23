package services

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadClaudeRenameRecordsCarriesMessagingSocketPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("101.json", `{"sessionId":"s1","pid":101,"status":"idle","messagingSocketPath":"/tmp/cc-socks/101.sock"}`)
	write("102.json", `{"sessionId":"s2","pid":102,"status":"idle"}`)

	records := loadClaudeRenameRecords(home)
	if got := records["s1"].MessagingSocketPath; got != "/tmp/cc-socks/101.sock" {
		t.Fatalf("s1 socket path = %q, want /tmp/cc-socks/101.sock", got)
	}
	if got := records["s2"].MessagingSocketPath; got != "" {
		t.Fatalf("s2 socket path = %q, want empty (capability gate)", got)
	}
}

func TestLoadClaudePeerToken(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "101.abc123.key"), []byte(`{"peerToken":"tok123","procStart":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadClaudePeerToken(home, 101); got != "tok123" {
		t.Fatalf("token = %q, want tok123", got)
	}
	if got := loadClaudePeerToken(home, 999); got != "" {
		t.Fatalf("missing pid token = %q, want empty", got)
	}
	if got := loadClaudePeerToken("", 101); got != "" {
		t.Fatalf("empty home token = %q, want empty", got)
	}
}
