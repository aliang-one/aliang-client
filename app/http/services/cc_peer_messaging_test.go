package services

import (
	"net"
	"os"
	"path/filepath"
	"strings"
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
	if err := os.WriteFile(filepath.Join(dir, "102.bad.key"), []byte(`{not-json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadClaudePeerToken(home, 102); got != "" {
		t.Fatalf("bad json token = %q, want empty", got)
	}
	if got := loadClaudePeerToken(home, 999); got != "" {
		t.Fatalf("missing pid token = %q, want empty", got)
	}
	if got := loadClaudePeerToken("", 101); got != "" {
		t.Fatalf("empty home token = %q, want empty", got)
	}
}

func TestCcPeerFrames(t *testing.T) {
	auth := ccPeerAuthLine("tok")
	if !strings.Contains(auth, `"type":"auth"`) || !strings.Contains(auth, `"peerToken":"tok"`) {
		t.Fatalf("auth line malformed: %s", auth)
	}
	user := ccPeerUserLine("你好")
	if !strings.Contains(user, `"type":"user"`) || !strings.Contains(user, `"role":"user"`) ||
		!strings.Contains(user, `"msgV":1`) || !strings.Contains(user, `"priority":"next"`) ||
		!strings.Contains(user, "你好") {
		t.Fatalf("user line malformed: %s", user)
	}
	if strings.Contains(ccPeerUserLine("x"), "\n") {
		t.Fatal("frame must be a single line")
	}
	id2 := ccPeerUserLine("y")
	if strings.Contains(id2, `"msg_id":""`) {
		t.Fatal("msg_id must never be empty")
	}
}

// ccPeerShortSocketPath mints a temp dir + inbox.sock path for the listening
// tests. t.TempDir() embeds the full test name, and on macOS a unix socket
// path over 103 bytes fails bind with "invalid argument" (sun_path limit) —
// TestCcPeerInjectWritesFramesFireAndForget's dir alone blows the budget.
func ccPeerShortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccpeer")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "inbox.sock")
}

// spike 实测(计划附录 A):普通送达在注入侧 socket 上零回执 → 注入为
// fire-and-forget,成功 = 帧写出,不需要任何回读。
func TestCcPeerInjectWritesFramesFireAndForget(t *testing.T) {
	sock := ccPeerShortSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
		// 故意不回任何帧:普通送达本就零回执,注入端不得依赖回读。
	}()
	if err := ccPeerInject(sock, "tok", "hello digest"); err != nil {
		t.Fatal(err)
	}
	frames := <-got
	if !strings.Contains(frames, `"peerToken":"tok"`) || !strings.Contains(frames, "hello digest") {
		t.Fatalf("frames not written: %s", frames)
	}
}

func TestCcPeerInjectTokenlessOK(t *testing.T) {
	sock := ccPeerShortSocketPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		got <- string(buf[:n])
	}()
	if err := ccPeerInject(sock, "", "no-auth digest"); err != nil {
		t.Fatal(err)
	}
	frames := <-got
	if strings.Contains(frames, `"type":"auth"`) {
		t.Fatalf("empty token must omit the auth line, got: %s", frames)
	}
	if !strings.Contains(frames, "no-auth digest") {
		t.Fatalf("user frame missing: %s", frames)
	}
}

func TestCcPeerInjectDialFailure(t *testing.T) {
	if err := ccPeerInject(filepath.Join(t.TempDir(), "missing.sock"), "", "x"); err == nil {
		t.Fatal("expected dial error for missing socket")
	}
}
