package services

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
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
	// uuid4 形状断言:锁定 ccPeerMsgID 的手工位运算(版本 4 + variant 位)。
	idMatch := regexp.MustCompile(`"msg_id":"([^"]+)"`).FindStringSubmatch(user)
	if idMatch == nil {
		t.Fatalf("msg_id missing: %s", user)
	}
	uuid4 := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !uuid4.MatchString(idMatch[1]) {
		t.Fatalf("msg_id %q is not uuid4-shaped", idMatch[1])
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
		// 读到 EOF(注入端写完即关):比单次 Read 确定性更强。
		b, _ := io.ReadAll(conn)
		got <- string(b)
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
		b, _ := io.ReadAll(conn)
		got <- string(b)
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

func TestCcPeerSyncShouldInject(t *testing.T) {
	now := time.Now()
	base := ccPeerSyncGateInput{
		RecordLive: true, RecordStatus: "idle", SocketPath: "/tmp/cc-socks/1.sock",
		JSONLSize: 100, JSONLModTime: now, TUIStartProxy: now.Add(-time.Hour),
	}
	if !ccPeerSyncShouldInject(base) {
		t.Fatal("happy path should inject")
	}
	dead := base
	dead.RecordLive = false
	if ccPeerSyncShouldInject(dead) {
		t.Fatal("dead TUI must not inject")
	}
	busy := base
	busy.RecordStatus = "busy"
	if ccPeerSyncShouldInject(busy) {
		t.Fatal("busy TUI must not inject (skip, not interrupt)")
	}
	incapable := base
	incapable.SocketPath = ""
	if ccPeerSyncShouldInject(incapable) {
		t.Fatal("old CC (no socket path) must not inject")
	}
	windowsPipe := base
	windowsPipe.SocketPath = `\\.\pipe\cc-inbox`
	if ccPeerSyncShouldInject(windowsPipe) {
		t.Fatal("non-unix socket path (v1 scope) must not inject")
	}
	stale := base
	stale.JSONLModTime = now.Add(-2 * time.Hour) // TUI opened after last write
	if ccPeerSyncShouldInject(stale) {
		t.Fatal("jsonl older than TUI start: TUI already loaded it, must not inject")
	}
	noGrowth := base
	noGrowth.BaselineKnown = true
	noGrowth.BaselineSize = 100
	if ccPeerSyncShouldInject(noGrowth) {
		t.Fatal("no growth since last injection must not re-inject")
	}
}

func TestCcPeerTuiSyncCoalesces(t *testing.T) {
	origWindow := ccPeerSyncCoalesceWindow
	origDial := ccPeerDialInject
	// liveClaudeTUIRecord 要求 pid 活着且 ps 身份含 claude。用本测试进程自己的
	// pid(必然存活)+ stub 身份匹配器(先例:agent_external_interrupt_test.go
	// 对同款 package var 的替换+恢复写法),否则 gate 永远 drop、测试永远红。
	origMatches := externalInterruptTargetMatches
	externalInterruptTargetMatches = func(int) bool { return true }
	defer func() {
		ccPeerSyncCoalesceWindow = origWindow
		ccPeerDialInject = origDial
		externalInterruptTargetMatches = origMatches
	}()
	ccPeerSyncCoalesceWindow = 30 * time.Millisecond
	pid := os.Getpid()

	home := t.TempDir()
	// tuiSync 经 externalTUIHome()=agentHome() 解析 home;HOME/USERPROFILE 必须
	// 指向假 home(先例:agent_external_interrupt_test.go 同款 Setenv),
	// 否则 gate 读到真实 home 无记录、永远 drop。
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := fmt.Sprintf(`{"sessionId":"snat","pid":%d,"status":"idle","messagingSocketPath":"%s"}`, pid, filepath.Join(t.TempDir(), "noop.sock"))
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(pid)+".json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls int
	var lastDigest string
	var mu sync.Mutex
	ccPeerDialInject = func(_ string, _, digest string) error {
		mu.Lock()
		calls++
		lastDigest = digest
		mu.Unlock()
		return nil
	}
	// jsonl 必须真实存在且 mtime 晚于 TUI 启动代理(.key mtime;此处无 .key →
	// 零值,该门放行),size 基线未知(baseline unknown)→ 门放行。
	proj := filepath.Join(home, ".claude", "projects", "p")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "snat.jsonl"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(proj, "snat.jsonl"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	m := &agentAIManager{}
	m.tuiSync(map[string]interface{}{"session_id": "s", "source_session_id": "snat", "digest": "first"}, nil)
	m.tuiSync(map[string]interface{}{"session_id": "s", "source_session_id": "snat", "digest": "second"}, nil)
	time.Sleep(120 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("inject calls = %d, want 1 (coalesced)", calls)
	}
	if lastDigest != "second" {
		t.Fatalf("digest = %q, want newest %q", lastDigest, "second")
	}
}
