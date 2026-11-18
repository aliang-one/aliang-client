package services

// 互传设备侧行为测试：文件名清洗 / 并发槽 / zip 往返与 slip 矩阵 / 发送链
// （分块+ETag+重签+busy/dedupe）/ 接收链（Range 续传+If-Range+sha 校验+
// busy/dedupe+cancel 清理+fence）。假 COS 与假服务端替换点：
// agentTransferHostAllowed / agentTransferFetchParts / agentTransferSendConfirm。
// 注意（跨平台陷阱）：本文件名避开 GOOS 尾段，所有平台都真实执行。

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/mesh/punch"
)

// syscallENOSPCTest 供磁盘满判定测试取本平台的 ENOSPC。
func syscallENOSPCTest() error { return syscall.ENOSPC }

// ---- harness --------------------------------------------------------------

func newAgentTransferEnv(t *testing.T) string {
	t.Helper()
	resetAgentTransferSlotsForTest()
	inbox := t.TempDir()
	t.Setenv("ALIANG_TRANSFER_INBOX_DIR", inbox)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	// 字节面 URL 全部指向 httptest（127.0.0.1:port 带端口），绕开 COS 域钉死。
	origHost := agentTransferHostAllowed
	agentTransferHostAllowed = func(string) error { return nil }
	t.Cleanup(func() { agentTransferHostAllowed = origHost })
	return inbox
}

func newTransferCapture() (*agentFileUploadCapture, func(interface{}) error) {
	c := newAgentFileUploadCapture()
	return c, c.writeJSON
}

func waitForTransferTerminal(t *testing.T, c *agentFileUploadCapture) map[string]interface{} {
	t.Helper()
	for i := 0; i < 1024; i++ {
		msg := c.next(t, "transfer terminal message")
		if remoteString(msg, "type") == models.AgentEventTransferResult {
			return msg
		}
	}
	t.Fatal("no transfer.result after 1024 messages")
	return nil
}

func sha256HexOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func deterministicTransferBytes(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i*1103515245 + 7)
	}
	return buf
}

func writeTempFile(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- 文件名清洗（D9）------------------------------------------------------

func TestSanitizeTransferFileName(t *testing.T) {
	ok := map[string]string{
		"报告 最终版.pdf":                      "报告 最终版.pdf",
		"截图 2026-10-22.png":               "截图 2026-10-22.png",
		"naïve😀.txt":                      "naïve_.txt", // emoji 替换为 _
		"my.file.v2.tar.gz":               "my.file.v2.tar.gz",
		".hidden":                         ".hidden",
		strings.Repeat("汉", 150) + ".zip": strings.Repeat("汉", 65) + ".zip", // 超长截断：196 字节预算 ÷ 3 字节/字 = 65 字
	}
	for in, want := range ok {
		got, err := sanitizeTransferFileName(in)
		if err != nil {
			t.Fatalf("sanitize(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("sanitize(%q) = %q, want %q", in, got, want)
		}
	}
	bad := []string{
		"", "  ", ".", "..", "a/b", "a\\b", "a\x00b",
		"report.pdf ", "report.pdf.", "con", "CON.txt", "com1", "lpt9.log",
	}
	for _, in := range bad {
		if got, err := sanitizeTransferFileName(in); err == nil {
			t.Fatalf("sanitize(%q) = %q, want error", in, got)
		}
	}
}

// ---- 并发槽 ---------------------------------------------------------------

func TestAgentTransferSlotSemantics(t *testing.T) {
	s := newAgentTransferSlot(2)
	c1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	_, cancel3 := context.WithCancel(context.Background())
	defer cancel3()

	if ok, dup := s.acquire("tr1", cancel1); !ok || dup {
		t.Fatal("first acquire must succeed")
	}
	if ok, dup := s.acquire("tr1", cancel2); ok || !dup {
		t.Fatalf("same-id re-acquire must report duplicate, got ok=%v dup=%v", ok, dup)
	}
	if ok, dup := s.acquire("tr2", cancel2); !ok || dup {
		t.Fatal("second id must acquire")
	}
	if ok, dup := s.acquire("tr3", cancel3); ok || dup {
		t.Fatalf("full slot must report non-duplicate busy, got ok=%v dup=%v", ok, dup)
	}
	if !s.cancelIfActive("tr1") {
		t.Fatal("cancelIfActive(tr1) must hit")
	}
	if c1.Err() == nil {
		t.Fatal("cancelled context expected")
	}
	s.release("tr1")
	if s.cancelIfActive("tr1") {
		t.Fatal("cancel after release must be no-op")
	}
	if ok, _ := s.acquire("tr3", cancel3); !ok {
		t.Fatal("released slot must be re-acquirable")
	}
}

// ---- zip 打包/解压往返 + slip 矩阵（D10）----------------------------------

func TestAgentTransferZipRoundTrip(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	src := t.TempDir()
	writeTempFile(t, src, "报告.md", []byte("# 中文内容\n测试"))
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, filepath.Join(src, "sub"), "nested.go", []byte("package x\n"))
	if err := os.MkdirAll(filepath.Join(src, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTempFile(t, filepath.Join(src, "node_modules", "pkg"), "index.js", []byte("noise"))

	w, err := newZipStreamWriter(src)
	if err != nil {
		t.Fatal(err)
	}
	packed, err := io.ReadAll(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Wait(); err != nil {
		t.Fatal(err)
	}

	zipPath := writeTempFile(t, t.TempDir(), "src.zip", packed)
	dest := t.TempDir()
	if err := extractAgentTransferZip(context.Background(), zipPath, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "报告.md"))
	if err != nil || string(got) != "# 中文内容\n测试" {
		t.Fatalf("chinese content mismatch: %q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "sub", "nested.go")); err != nil {
		t.Fatalf("nested file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules must be skipped by packer")
	}
}

func buildEvilZip(t *testing.T, dir string, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(dir, "evil.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentTransferZipSlipMatrix(t *testing.T) {
	_ = newAgentTransferEnv(t)
	dir := t.TempDir()
	dest := t.TempDir()
	before, _ := os.ReadDir(dest)

	evil := map[string][]byte{
		"../escape.txt":                       []byte("x"),
		"/abs.txt":                            []byte("x"),
		"C:/win.txt":                          []byte("x"),
		`a\b.txt`:                             []byte("x"),
		strings.Repeat("d/", 40) + "deep.txt": []byte("x"),
		"CON.txt":                             []byte("x"), // Windows 保留名条目
		"sub/nul.txt":                         []byte("x"), // 中段段保留名
		"a.txt.":                              []byte("x"), // 尾点条目（Windows 落盘吞点→重名歧义）
	}
	for name, content := range evil {
		zipPath := buildEvilZip(t, dir, map[string][]byte{name: content})
		if err := extractAgentTransferZip(context.Background(), zipPath, dest); err == nil {
			t.Fatalf("entry %q must be rejected", name)
		} else if !strings.Contains(err.Error(), "zip entry rejected") {
			t.Fatalf("entry %q rejected with unexpected error: %v", name, err)
		}
	}
	after, _ := os.ReadDir(dest)
	if len(after) != len(before) {
		t.Fatalf("destination must stay untouched, before=%d after=%d", len(before), len(after))
	}
}

// ---- 发送链 ----------------------------------------------------------------

type fakeTransferCOS struct {
	srv    *httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	status int
}

func newFakeTransferCOS(t *testing.T, status int) *fakeTransferCOS {
	t.Helper()
	fc := &fakeTransferCOS{status: status}
	if status == 0 {
		fc.status = http.StatusOK
	}
	fc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fc.mu.Lock()
		fc.bodies = append(fc.bodies, body)
		status := fc.status
		fc.mu.Unlock()
		// 非 200 档只作用于「旧 URL」（路径含 /stale）——重签出来的 /fresh
		// 路径必须能成功，否则重签链路永远验不过。
		if status != http.StatusOK && strings.Contains(r.URL.Path, "/stale") {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("ETag", `"etag-put"`)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fc.srv.Close)
	return fc
}

func (fc *fakeTransferCOS) snapshot() [][]byte {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	return append([][]byte(nil), fc.bodies...)
}

func (fc *fakeTransferCOS) url() string { return fc.srv.URL }

func transferUploadMsg(t *testing.T, transferID, sourcePath string, size int64, isDir bool, partSize int64, urls map[int]string) map[string]interface{} {
	t.Helper()
	parts := make([]interface{}, 0, len(urls))
	for num := 1; num <= len(urls); num++ {
		parts = append(parts, map[string]interface{}{"part_number": num, "url": urls[num]})
	}
	return map[string]interface{}{
		"type":                 models.AgentEventTransferUpload,
		"request_id":           "req-" + transferID,
		"transfer_id":          transferID,
		"filename":             "payload.bin",
		"size_bytes":           size,
		"max_bytes":            0,
		"is_directory_archive": isDir,
		"source_path":          sourcePath,
		"upload": map[string]interface{}{
			"key":       "tr/u/" + transferID + "/payload.bin",
			"upload_id": "up-" + transferID,
			"part_size": partSize,
			"parts":     parts,
		},
	}
}

func TestAgentTransferUploadMultiPart(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	capture, write := newTransferCapture()
	data := deterministicTransferBytes(2<<20 + 777) // 3 parts @ 1MiB
	src := writeTempFile(t, t.TempDir(), "payload.bin", data)
	cos := newFakeTransferCOS(t, 0)

	msg := transferUploadMsg(t, "tr_up1", src, int64(len(data)), false, 1<<20, map[int]string{
		1: cos.url() + "/p1", 2: cos.url() + "/p2", 3: cos.url() + "/p3",
	})
	svc := &AgentService{}
	svc.handleAgentTransferUpload(msg, write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("upload must succeed, got %v", terminal)
	}
	if got := agentTransferInt64(terminal, "size_bytes"); got != int64(len(data)) {
		t.Fatalf("size_bytes = %d, want %d", got, len(data))
	}
	if got, want := remoteString(terminal, "sha256"), sha256HexOf(data); got != want {
		t.Fatalf("sha256 = %s, want %s", got, want)
	}
	etags, ok := terminal["etags"].([]map[string]interface{})
	if !ok || len(etags) != 3 {
		t.Fatalf("etags = %v, want 3 entries", terminal["etags"])
	}
	for i, et := range etags {
		if agentTransferInt64(et, "part_number") != int64(i+1) {
			t.Fatalf("etags[%d].part_number = %v", i, et["part_number"])
		}
	}
	parts := cos.snapshot()
	if len(parts) != 3 {
		t.Fatalf("fake COS got %d PUTs, want 3", len(parts))
	}
	var joined bytes.Buffer
	for i := range parts {
		joined.Write(parts[i])
	}
	if !bytes.Equal(joined.Bytes(), data) {
		t.Fatal("concatenated part bytes differ from source")
	}
}

func TestAgentTransferUploadResignOn403(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	data := deterministicTransferBytes(2<<20 + 3)
	src := writeTempFile(t, t.TempDir(), "payload.bin", data)
	cos := newFakeTransferCOS(t, http.StatusForbidden) // 首批 URL 全部过期

	origFetch := agentTransferFetchParts
	var resigned int
	agentTransferFetchParts = func(s *AgentService, transferID string, fromPart int) (map[int]string, error) {
		resigned++
		out := map[int]string{}
		for i := 0; i < 3; i++ {
			out[fromPart+i] = cos.url() + fmt.Sprintf("/fresh-%d", fromPart+i)
		}
		return out, nil
	}
	t.Cleanup(func() { agentTransferFetchParts = origFetch })

	msg := transferUploadMsg(t, "tr_up2", src, int64(len(data)), false, 1<<20, map[int]string{
		1: cos.url() + "/stale1", 2: cos.url() + "/stale2", 3: cos.url() + "/stale3",
	})
	svc := &AgentService{}
	svc.handleAgentTransferUpload(msg, write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("upload must succeed after resign, got %v", terminal)
	}
	if resigned == 0 {
		t.Fatal("resign must have been exercised")
	}
	if got := agentTransferInt64(terminal, "size_bytes"); got != int64(len(data)) {
		t.Fatalf("size_bytes = %d, want %d", got, len(data))
	}
}

func TestAgentTransferUploadDuplicateAndBusy(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	capture, write := newTransferCapture()
	src := writeTempFile(t, t.TempDir(), "payload.bin", deterministicTransferBytes(1024))
	cos := newFakeTransferCOS(t, 0)

	svc := &AgentService{}
	msg := transferUploadMsg(t, "tr_up3", src, 1024, false, 1<<20, map[int]string{1: cos.url() + "/p1"})

	// 同 id 已在飞 → 幂等忽略，零输出
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ok, _ := transferUploadSlot().acquire("tr_up3", cancel); !ok {
		t.Fatal("pre-acquire failed")
	}
	svc.handleAgentTransferUpload(msg, write)
	select {
	case m := <-capture.ch:
		t.Fatalf("duplicate delivery must be silent, got %v", m)
	case <-time.After(300 * time.Millisecond):
	}
	transferUploadSlot().release("tr_up3")

	// 槽满（容量 2，两个不同 id 填满）→ uploader_busy 非终态
	_, otherCancel := context.WithCancel(context.Background())
	defer otherCancel()
	_, otherCancel2 := context.WithCancel(context.Background())
	defer otherCancel2()
	if ok, _ := transferUploadSlot().acquire("tr_other", otherCancel); !ok {
		t.Fatal("fill acquire 1 failed")
	}
	if ok, _ := transferUploadSlot().acquire("tr_other2", otherCancel2); !ok {
		t.Fatal("fill acquire 2 failed")
	}
	msg2 := transferUploadMsg(t, "tr_up4", src, 1024, false, 1<<20, map[int]string{1: cos.url() + "/p1"})
	svc.handleAgentTransferUpload(msg2, write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrUploaderBusy {
		t.Fatalf("want uploader_busy failure, got %v", terminal)
	}
}

// ---- 接收链 ----------------------------------------------------------------

type fakeTransferSource struct {
	srv          *httptest.Server
	payload      []byte
	etag         string
	doStall      bool
	stall        chan struct{}
	stallClose   sync.Once
	mu           sync.Mutex
	requests     []string
	firstGet     chan struct{}
	firstGetOnce sync.Once
}

func newFakeTransferSource(t *testing.T, payload []byte, mutate func(*fakeTransferSource)) *fakeTransferSource {
	t.Helper()
	fs := &fakeTransferSource{payload: payload, etag: `"etag-1"`, stall: make(chan struct{}), firstGet: make(chan struct{})}
	if mutate != nil {
		mutate(fs)
	}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		fs.requests = append(fs.requests, fmt.Sprintf("Range=%s|If-Range=%s", r.Header.Get("Range"), r.Header.Get("If-Range")))
		etag := fs.etag
		doStall := fs.doStall
		fs.mu.Unlock()
		fs.firstGetOnce.Do(func() { close(fs.firstGet) })

		status := http.StatusOK
		body := fs.payload
		if rangeHdr := r.Header.Get("Range"); rangeHdr != "" {
			// If-Range 与对象身份不符 → 忽略 Range 回 200 整文件（对象已换）。
			if ifRange := r.Header.Get("If-Range"); ifRange == "" || ifRange == etag {
				var from int64
				if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-", &from); err == nil && from >= 0 && int(from) <= len(fs.payload) {
					status = http.StatusPartialContent
					body = fs.payload[from:]
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(fs.payload)-1, len(fs.payload)))
				}
			}
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeader(status)
		if doStall {
			_, _ = w.Write(body[:min(8, len(body))])
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-fs.stall
			_, _ = w.Write(body[min(8, len(body)):])
			return
		}
		_, _ = w.Write(body)
	}))
	// cleanup 顺序必须是「先放行 stall、再 Close server」（t.Cleanup LIFO）：
	// 反过来 srv.Close 会等住点的 handler 退出，而 handler 在等 stall，死锁。
	t.Cleanup(fs.srv.Close)
	t.Cleanup(func() { fs.stallClose.Do(func() { close(fs.stall) }) })
	return fs
}

func (fs *fakeTransferSource) url() string { return fs.srv.URL }

func (fs *fakeTransferSource) snapshotRequests() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.requests...)
}

func receiveMsg(t *testing.T, transferID string, payload []byte, url, targetDir string) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"type":                 models.AgentEventTransferReceive,
		"request_id":           "req-" + transferID,
		"transfer_id":          transferID,
		"filename":             "下载 文件.bin",
		"size_bytes":           len(payload),
		"sha256":               sha256HexOf(payload),
		"is_directory_archive": false,
		"get_url":              url,
		"object_etag":          `"etag-1"`,
		"target_dir":           targetDir,
	}
}

func TestAgentTransferReceiveHappyPath(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(300 * 1024)
	fs := newFakeTransferSource(t, payload, nil)

	confirmed := make(chan string, 1)
	origConfirm := agentTransferSendConfirm
	agentTransferSendConfirm = func(s *AgentService, transferID string, p map[string]interface{}) error {
		confirmed <- transferID
		return nil
	}
	t.Cleanup(func() { agentTransferSendConfirm = origConfirm })

	svc := &AgentService{}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx1", payload, fs.url(), ""), write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("receive must succeed, got %v", terminal)
	}
	finalPath := remoteString(terminal, "path")
	got, err := os.ReadFile(finalPath)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("final content mismatch err=%v", err)
	}
	if !strings.HasSuffix(finalPath, "下载 文件.bin") {
		t.Fatalf("final path = %s, want original name preserved", finalPath)
	}
	select {
	case id := <-confirmed:
		if id != "tr_rx1" {
			t.Fatalf("confirm id = %s", id)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("confirm not sent")
	}
	marker, err := readAgentTransferMarker("tr_rx1")
	if err != nil || marker == nil || marker.State != transferMarkerStateDone {
		t.Fatalf("marker = %v err=%v", marker, err)
	}
}

func TestAgentTransferReceiveResume(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(400 * 1024)
	prefix := payload[:150*1024]
	fs := newFakeTransferSource(t, payload, nil)

	// 预置 partial（模拟上次断点）
	partial := agentTransferPartialPath(inbox, "tr_rx2", "下载 文件.bin")
	if err := os.WriteFile(partial, prefix, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &AgentService{}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx2", payload, fs.url(), ""), write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("resume receive must succeed, got %v", terminal)
	}
	reqs := fs.snapshotRequests()
	if len(reqs) == 0 {
		t.Fatal("no requests hit fake source")
	}
	if !strings.Contains(reqs[0], fmt.Sprintf("Range=bytes=%d-", len(prefix))) {
		t.Fatalf("first request must resume from %d, got %q", len(prefix), reqs[0])
	}
	if !strings.Contains(reqs[0], `If-Range="etag-1"`) {
		t.Fatalf("resume must bind object identity via If-Range, got %q", reqs[0])
	}
	got, err := os.ReadFile(remoteString(terminal, "path"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("resumed content mismatch err=%v", err)
	}
}

func TestAgentTransferReceiveShaMismatch(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(64 * 1024)
	fs := newFakeTransferSource(t, payload, nil)

	msg := receiveMsg(t, "tr_rx3", payload, fs.url(), "")
	msg["sha256"] = strings.Repeat("0", 64)
	svc := &AgentService{}
	svc.handleAgentTransferReceive(msg, write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrShaMismatch {
		t.Fatalf("want sha_mismatch, got %v", terminal)
	}
	entries, _ := os.ReadDir(inbox)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".partial-") {
			t.Fatalf("partial must be removed on sha mismatch, found %s", e.Name())
		}
	}
}

func TestAgentTransferReceiveBusyAndDuplicate(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(1024)
	fs := newFakeTransferSource(t, payload, nil)
	svc := &AgentService{}

	// 槽被占 → receiver_busy（非终态，D15）
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ok, _ := transferReceiveSlot().acquire("tr_other", cancel); !ok {
		t.Fatal("fill receive slot failed")
	}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx4", payload, fs.url(), ""), write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrReceiverBusy {
		t.Fatalf("want receiver_busy, got %v", terminal)
	}
	transferReceiveSlot().release("tr_other")

	// 同 id 已在飞 → 静默忽略
	if ok, _ := transferReceiveSlot().acquire("tr_rx5", cancel); !ok {
		t.Fatal("acquire failed")
	}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx5", payload, fs.url(), ""), write)
	select {
	case m := <-capture.ch:
		t.Fatalf("duplicate receive delivery must be silent, got %v", m)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestAgentTransferReceiveFenceEscape(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(1024)
	fs := newFakeTransferSource(t, payload, nil)
	svc := &AgentService{}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx6", payload, fs.url(), "../../outside"), write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrFenceEscape {
		t.Fatalf("want fence_escape, got %v", terminal)
	}
}

func TestAgentTransferCancelReceiveCleansPartial(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(256 * 1024)
	fs := newFakeTransferSource(t, payload, func(fs *fakeTransferSource) { fs.doStall = true })

	done := make(chan struct{})
	svc := &AgentService{}
	go func() {
		defer close(done)
		svc.handleAgentTransferReceive(receiveMsg(t, "tr_rx7", payload, fs.url(), ""), write)
	}()
	select {
	case <-fs.firstGet:
	case <-time.After(10 * time.Second):
		t.Fatal("download never started")
	}
	svc.handleAgentTransferCancel(map[string]interface{}{"type": models.AgentEventTransferCancel, "transfer_id": "tr_rx7"}, write)

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("receive did not wind down after cancel")
	}
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrCancelled {
		t.Fatalf("want cancelled, got %v", terminal)
	}
	if _, err := os.Stat(agentTransferPartialPath(inbox, "tr_rx7", "下载 文件.bin")); !os.IsNotExist(err) {
		t.Fatal("cancel must remove .partial")
	}
}

// ---- 复核修复的回归测试 ----------------------------------------------------

func TestAgentTransferReceiveRejectsTransferIDTraversal(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(1024)
	fs := newFakeTransferSource(t, payload, nil)
	svc := &AgentService{}
	for _, bad := range []string{"../evil", "a/b", "tr/../..x", ""} {
		msg := receiveMsg(t, "tr_ok_id", payload, fs.url(), "")
		msg["transfer_id"] = bad
		svc.handleAgentTransferReceive(msg, write)
		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrBadFilename {
			t.Fatalf("transfer_id %q must be rejected with bad_filename, got %v", bad, terminal)
		}
	}
	// 收件目录零落盘
	entries, _ := os.ReadDir(inbox)
	if len(entries) != 0 {
		t.Fatalf("rejected transfer must not touch inbox, got %d entries", len(entries))
	}
}

func TestAgentTransferUploadResignURLHostFence(t *testing.T) {
	_ = newAgentTransferEnv(t)
	// 本测试用真实域钉死围栏（不用 env 的放行桩）
	capture, write := newTransferCapture()
	data := deterministicTransferBytes(1024)
	src := writeTempFile(t, t.TempDir(), "payload.bin", data)
	cos := newFakeTransferCOS(t, http.StatusForbidden)

	origHost := agentTransferHostAllowed
	agentTransferHostAllowed = func(raw string) error {
		// 只放行假 COS（127.0.0.1），evil 域拒绝——模拟真实 myqcloud 白名单
		if strings.Contains(raw, "evil.example.com") {
			return errors.New("transfer url host is not an allowed COS domain")
		}
		return nil
	}
	t.Cleanup(func() { agentTransferHostAllowed = origHost })

	origFetch := agentTransferFetchParts
	agentTransferFetchParts = func(s *AgentService, transferID string, fromPart int) (map[int]string, error) {
		// 恶意/被攻破的服务端在重签里给出非 COS 域
		return map[int]string{fromPart: "https://evil.example.com/steal"}, nil
	}
	t.Cleanup(func() { agentTransferFetchParts = origFetch })

	msg := transferUploadMsg(t, "tr_fence", src, int64(len(data)), false, 1<<20, map[int]string{1: cos.url() + "/stale1"})
	svc := &AgentService{}
	svc.handleAgentTransferUpload(msg, write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrUploadFailed {
		t.Fatalf("non-COS resign URL must fail the upload, got %v", terminal)
	}
	if got := strings.Contains(sanitizeTransferErrorMsg(terminal), "evil.example.com"); got {
		t.Fatal("error text must not leak the rejected URL")
	}
}

func sanitizeTransferErrorMsg(m map[string]interface{}) string {
	s, _ := m["error"].(string)
	return s
}

func TestAgentTransferReceiveMaxBytesGate(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	_ = inbox
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(64 * 1024)
	fs := newFakeTransferSource(t, payload, nil)
	msg := receiveMsg(t, "tr_cap", payload, fs.url(), "")
	msg["max_bytes"] = 1024 // 远小于对象
	svc := &AgentService{}
	svc.handleAgentTransferReceive(msg, write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrTooLarge {
		t.Fatalf("want too_large, got %v", terminal)
	}
}

func TestAgentTransferReceiveZipSlipReportsFenceEscape(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	// 打一个含穿越条目的 zip 当「下载载荷」
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("../escape.txt")
	_, _ = w.Write([]byte("x"))
	_ = zw.Close()
	archive := buf.Bytes()
	fs := newFakeTransferSource(t, archive, nil)

	msg := receiveMsg(t, "tr_slip", archive, fs.url(), "")
	msg["is_directory_archive"] = true
	svc := &AgentService{}
	svc.handleAgentTransferReceive(msg, write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrFenceEscape {
		t.Fatalf("zip-slip via full chain must report fence_escape, got %v", terminal)
	}
}

func TestAgentTransferReceiveCompletePartialShortCircuit(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	payload := deterministicTransferBytes(200 * 1024)
	fs := newFakeTransferSource(t, payload, nil)
	// partial 已有完整数据（rename 前中断的形态）
	partial := agentTransferPartialPath(inbox, "tr_eof", "下载 文件.bin")
	if err := os.WriteFile(partial, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	svc := &AgentService{}
	svc.handleAgentTransferReceive(receiveMsg(t, "tr_eof", payload, fs.url(), ""), write)
	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("complete partial must short-circuit to success, got %v", terminal)
	}
	if len(fs.snapshotRequests()) != 0 {
		t.Fatal("no HTTP request expected when partial is already complete")
	}
}

func TestAgentTransferUploadCancelReturnsCancelledNotUploadFailed(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	plan := &agentTransferUploadPlan{key: "k", uploadID: "u", partSize: 1 << 20, parts: map[int]string{1: "https://127.0.0.1:1/x"}}
	ident := agentTransferIdentity{RequestID: "req-x", TransferID: "tr_ctx", Filename: "f.bin"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预先取消
	var uploaded int64
	_, err := putAgentTransferPartWithResign(ctx, &AgentService{}, plan, ident, plan.parts[1], []byte("data"), 1, &uploaded, newAgentTransferThrottle(0), write)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled upload must yield context.Canceled, got %v", err)
	}
	_ = capture
}

func TestAgentTransferDiskFullErrno(t *testing.T) {
	if !isAgentTransferDiskFull(fmt.Errorf("write: %w", syscallENOSPCTest())) {
		t.Fatal("wrapped ENOSPC must be classified as disk full")
	}
	if isAgentTransferDiskFull(errors.New("connection reset")) {
		t.Fatal("non-disk-full error must not match")
	}
}

func TestSanitizeTransferFileNameTruncationCannotRegenerateReserved(t *testing.T) {
	// 截断后复查闸："CONFIG." + 196 个 A 截到预算内仍以保留名收场 → 拒绝
	bad := "CONFIG." + strings.Repeat("A", 196)
	if got, err := sanitizeTransferFileName(bad); err == nil {
		t.Fatalf("truncated-into-reserved name must be rejected, got %q", got)
	}
}

func TestAgentTransferUploadResultDualChannel(t *testing.T) {
	_ = newAgentTransferEnv(t)
	capture, write := newTransferCapture()
	data := deterministicTransferBytes(4096)
	src := writeTempFile(t, t.TempDir(), "payload.bin", data)
	cos := newFakeTransferCOS(t, 0)

	httpPosted := make(chan map[string]interface{}, 4)
	origPost := agentTransferPostUploadResult
	agentTransferPostUploadResult = func(s *AgentService, transferID string, payload map[string]interface{}) error {
		httpPosted <- payload
		return nil
	}
	t.Cleanup(func() { agentTransferPostUploadResult = origPost })

	msg := transferUploadMsg(t, "tr_dual", src, int64(len(data)), false, 1<<20, map[int]string{1: cos.url() + "/p1"})
	svc := &AgentService{}
	svc.handleAgentTransferUpload(msg, write)

	terminal := waitForTransferTerminal(t, capture)
	if terminal["ok"] != true {
		t.Fatalf("upload must succeed, got %v", terminal)
	}
	select {
	case posted := <-httpPosted:
		if posted["ok"] != true || remoteString(posted, "transfer_id") != "tr_dual" {
			t.Fatalf("HTTP receipt mismatch: %v", posted)
		}
		if _, ok := posted["etags"]; !ok {
			t.Fatal("HTTP receipt must carry etags")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP receipt not posted")
	}
}

// ---------------------------------------------------------------------------
// 上传腿 punch 载体(transfer-punch-stun Task 8):拨号缝注入、punch_peer_id
// 解析闸、attemptAgentTransferPunchPush 三臂、上传链分支编排(LAN 失败后/
// COS 前)、静默对端握手窗、生产 serve 体端到端回环。
// ---------------------------------------------------------------------------

// withPunchDialSeam 替换 punch 拨号缝并计数调用(编排用例断言分支是否进入)。
func withPunchDialSeam(t *testing.T, fn func(ctx context.Context, peerID, transferID string) (net.Conn, error)) *int32 {
	t.Helper()
	orig := agentPunchTransferDialFn
	var calls int32
	agentPunchTransferDialFn = func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
		atomic.AddInt32(&calls, 1)
		return fn(ctx, peerID, transferID)
	}
	t.Cleanup(func() { agentPunchTransferDialFn = orig })
	return &calls
}

// punchFakePeerResult 是 net.Pipe 假对端回传的断言面。
type punchFakePeerResult struct {
	helloToken      string
	helloTransferID string
	received        []byte
}

// punchProtocolPeer 在缝返回 conn 的对端扮演收件协议体:读 hello→回 ok→
// 定长收流→回 ACK(实收 sha/size 与落盘路径回填)。hello 里的 token/
// transfer_id/实收字节经 channel 回传供断言(token 必须是 lan_push 候选里
// 的单次凭据)。
func punchProtocolPeer(t *testing.T, peer net.Conn, ackPath string) <-chan punchFakePeerResult {
	t.Helper()
	out := make(chan punchFakePeerResult, 1)
	go func() {
		defer peer.Close()
		reader := bufio.NewReader(peer)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Errorf("punch 假对端读 hello 失败: %v", err)
			out <- punchFakePeerResult{}
			return
		}
		var hello struct {
			Token      string `json:"token"`
			TransferID string `json:"transfer_id"`
			SizeBytes  int64  `json:"size_bytes"`
		}
		if err := json.Unmarshal(line, &hello); err != nil {
			t.Errorf("punch 假对端解 hello 失败: %v(%s)", err, line)
			out <- punchFakePeerResult{}
			return
		}
		if _, err := peer.Write([]byte("{\"ok\":true}\n")); err != nil {
			t.Errorf("punch 假对端回 ok 失败: %v", err)
			out <- punchFakePeerResult{}
			return
		}
		hasher := sha256.New()
		received, copyErr := io.CopyN(hasher, reader, hello.SizeBytes)
		if copyErr != nil {
			t.Errorf("punch 假对端收流失败: %v", copyErr)
			out <- punchFakePeerResult{helloToken: hello.Token, helloTransferID: hello.TransferID}
			return
		}
		ack, _ := json.Marshal(map[string]interface{}{
			"ok": true, "sha256": hex.EncodeToString(hasher.Sum(nil)),
			"size_bytes": received, "path": ackPath,
		})
		if _, err := peer.Write(append(ack, '\n')); err != nil {
			t.Errorf("punch 假对端回 ACK 失败: %v", err)
		}
		out <- punchFakePeerResult{helloToken: hello.Token, helloTransferID: hello.TransferID}
	}()
	return out
}

// deadlineRecordingConn 记录每次 SetDeadline 的原值并把生效 deadline 钳到
// clamp 以内——静默对端用例不真等 5s,但「deadline 已设+设的值就是握手窗
// 量级」两件事都断言到(T7 评审义务的折通形态,报告注明)。
type deadlineRecordingConn struct {
	net.Conn
	mu        sync.Mutex
	requested []time.Time
	clamp     time.Duration
}

func (c *deadlineRecordingConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.requested = append(c.requested, t)
	effective := t
	if d := time.Until(t); d > c.clamp {
		effective = time.Now().Add(c.clamp)
	}
	c.mu.Unlock()
	return c.Conn.SetDeadline(effective)
}

func (c *deadlineRecordingConn) firstRequested() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.requested) == 0 {
		return time.Time{}, false
	}
	return c.requested[0], true
}

func TestParseAgentPunchPeerID(t *testing.T) {
	frame := func(v interface{}) map[string]interface{} { return map[string]interface{}{"punch_peer_id": v} }
	if got := parseAgentPunchPeerID(frame("dev-b")); got != "dev-b" {
		t.Fatalf("普通设备 ID 应原样返回,得到 %q", got)
	}
	if got := parseAgentPunchPeerID(frame("  dev-b\n")); got != "dev-b" {
		t.Fatalf("首尾空白应 TrimSpace,得到 %q", got)
	}
	for name, v := range map[string]interface{}{
		"missing":  nil,
		"empty":    "",
		"blank":    "   ",
		"too_long": strings.Repeat("x", 129),
	} {
		if got := parseAgentPunchPeerID(frame(v)); got != "" {
			t.Fatalf("[%s] 应被长度闸拒为空串,得到 %q", name, got)
		}
	}
	if got := parseAgentPunchPeerID(map[string]interface{}{}); got != "" {
		t.Fatalf("老服务端缺字段必须不进 punch 腿,得到 %q", got)
	}
}

func TestAttemptAgentTransferPunchPushViaSeam(t *testing.T) {
	_ = newAgentTransferEnv(t)
	data := deterministicTransferBytes(200 * 1024)
	src := writeTempFile(t, t.TempDir(), "punch.bin", data)
	ident := agentTransferIdentity{RequestID: "req-p1", TransferID: "tr_punchseam01", Filename: "punch.bin"}

	const token = "tok-punch-1234567890"
	var gotPeer, gotTransfer string
	calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
		gotPeer, gotTransfer = peerID, transferID
		client, server := net.Pipe()
		punchProtocolPeer(t, server, "/inbox/final/punch.bin")
		return client, nil
	})

	size, sha, path, err := attemptAgentTransferPunchPush(context.Background(), ident, "dev-b", token, src, int64(len(data)), func(interface{}) error { return nil })
	if err != nil {
		t.Fatalf("缝成功路径应推完整个文件: %v", err)
	}
	if size != int64(len(data)) || sha != sha256HexOf(data) {
		t.Fatalf("size/sha 应与源一致: size=%d want=%d sha=%s want=%s", size, len(data), sha, sha256HexOf(data))
	}
	if path != "/inbox/final/punch.bin" {
		t.Fatalf("path 应回带 ACK 落盘路径,得到 %q", path)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("拨号缝应被调恰一次,实调 %d", *calls)
	}
	if gotPeer != "dev-b" || gotTransfer != "tr_punchseam01" {
		t.Fatalf("缝参数应透传 peer/transfer_id: peer=%q transfer=%q", gotPeer, gotTransfer)
	}
}

func TestAttemptAgentTransferPunchPushDialErrorClassified(t *testing.T) {
	_ = newAgentTransferEnv(t)
	src := writeTempFile(t, t.TempDir(), "punch.bin", []byte("payload"))
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_punchdial01"}
	withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
		return nil, errors.New("symmetric nat suspect")
	})
	_, _, _, err := attemptAgentTransferPunchPush(context.Background(), ident, "dev-b", "tok", src, 7, func(interface{}) error { return nil })
	if err == nil {
		t.Fatal("缝返回错误必须恒错误(调用方落 COS)")
	}
	if stage := classifyAgentLanFailStage(err); stage != "probe" {
		t.Fatalf("拨号失败=零字节在流,必须归类 probe(与 lan dial 同款),得到 %q(%v)", stage, err)
	}
}

func TestAttemptAgentTransferPunchPushSkipWhenSizeUnknown(t *testing.T) {
	_ = newAgentTransferEnv(t)
	src := writeTempFile(t, t.TempDir(), "punch.bin", []byte("x"))
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_punchskip01"}
	calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
		t.Error("总量未知必须 skip,不得拨号")
		return nil, errors.New("unreachable")
	})
	_, _, _, err := attemptAgentTransferPunchPush(context.Background(), ident, "dev-b", "tok", src, 0, func(interface{}) error { return nil })
	if !errors.Is(err, errAgentTransferLanSkip) {
		t.Fatalf("总量未知应映射 errAgentTransferLanSkip,得到 %v", err)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatal("skip 路径不得触拨号缝")
	}
}

// TestAttemptAgentTransferPunchPushSilentPeerHandshakeWindow 静默对端围栏
// (T6/T7 评审强制接线的义务用例):已连通但不说话的对端,协议体首行读
// 必须被握手窗 deadline 拦下,不得无限悬挂。折通说明:不真等 5s——用
// deadlineRecordingConn 钳短生效值,断言「首个 SetDeadline 请求值就是
// 握手窗量级(now..now+agentLanHandshakeTimeout]」。
func TestAttemptAgentTransferPunchPushSilentPeerHandshakeWindow(t *testing.T) {
	_ = newAgentTransferEnv(t)
	src := writeTempFile(t, t.TempDir(), "punch.bin", deterministicTransferBytes(4096))
	ident := agentTransferIdentity{RequestID: "r", TransferID: "tr_punchsilent1"}

	client, server := net.Pipe()
	// 静默对端:只吞读不回话(hello 写得进去,ok 行永远不来)。
	go func() {
		_, _ = io.Copy(io.Discard, server)
		_ = server.Close()
	}()
	rec := &deadlineRecordingConn{Conn: client, clamp: 400 * time.Millisecond}
	withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
		return rec, nil
	})

	start := time.Now()
	_, _, _, err := attemptAgentTransferPunchPush(context.Background(), ident, "dev-b", "tok", src, 4096, func(interface{}) error { return nil })
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("静默对端必须在握手窗内失败,不得悬挂")
	}
	if !strings.Contains(err.Error(), "headline") {
		t.Fatalf("失败应发生在 ok 行读(headline),得到 %v", err)
	}
	if stage := classifyAgentLanFailStage(err); stage != "connect" {
		t.Fatalf("静默对端应归类 connect 阶段,得到 %s(%v)", stage, err)
	}
	first, ok := rec.firstRequested()
	if !ok {
		t.Fatal("必须对 punch conn 设过 deadline(评审强制接线)")
	}
	if first.Before(start) || first.After(start.Add(agentLanHandshakeTimeout+50*time.Millisecond)) {
		t.Fatalf("首个 deadline 必须是握手窗量级(≤%s): 请求=%v start=%v", agentLanHandshakeTimeout, first, start)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("钳短后的握手窗失败应远快于 5s: %v", elapsed)
	}
}

// punchUploadMsg 组带 punch 触面(lan_push 候选+punch_peer_id)的上传帧;
// 候选指向死端口,LAN 腿快速失败落 probe,编排随即进入 punch 分支。
func punchUploadMsg(t *testing.T, transferID, sourcePath string, size int64, isDir bool, cosURL string, punchPeer string) map[string]interface{} {
	t.Helper()
	msg := transferUploadMsg(t, transferID, sourcePath, size, isDir, 1<<20, map[int]string{1: cosURL})
	msg["direct_upload_candidates"] = []interface{}{map[string]interface{}{
		"type": "lan_push", "addr": "127.0.0.1:1",
		"token": "tok-punch-1234567890", "fingerprint": strings.Repeat("ab", 32),
	}}
	if punchPeer != "" {
		msg["punch_peer_id"] = punchPeer
	}
	return msg
}

func TestAgentTransferUploadPunchOrchestration(t *testing.T) {
	t.Run("punch_success_via_punch", func(t *testing.T) {
		_ = newAgentTransferEnv(t)
		capture, write := newTransferCapture()
		data := deterministicTransferBytes(300 * 1024)
		src := writeTempFile(t, t.TempDir(), "payload.bin", data)
		cos := newFakeTransferCOS(t, 0)

		calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
			client, server := net.Pipe()
			punchProtocolPeer(t, server, "/inbox/landed/payload.bin")
			return client, nil
		})

		msg := punchUploadMsg(t, "tr_punchok01", src, int64(len(data)), false, cos.url(), "dev-b")
		svc := &AgentService{}
		svc.handleAgentTransferUpload(msg, write)

		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != true {
			t.Fatalf("punch 成功路径必须终态成功: %v", terminal)
		}
		if via := remoteString(terminal, "via"); via != "punch" {
			t.Fatalf("via 必须是 punch,得到 %q", via)
		}
		if got := agentTransferInt64(terminal, "size_bytes"); got != int64(len(data)) {
			t.Fatalf("size_bytes = %d, want %d", got, len(data))
		}
		if got, want := remoteString(terminal, "sha256"), sha256HexOf(data); got != want {
			t.Fatalf("sha256 = %s, want %s", got, want)
		}
		if got := remoteString(terminal, "path"); got != "/inbox/landed/payload.bin" {
			t.Fatalf("path 应回带 ACK 落盘路径,得到 %q", got)
		}
		if n := len(cos.snapshot()); n != 0 {
			t.Fatalf("punch 成功必须整段跳过 COS,实收 %d 次 PUT", n)
		}
		if atomic.LoadInt32(calls) != 1 {
			t.Fatalf("punch 拨号缝应被调恰一次,实调 %d", *calls)
		}
	})

	t.Run("punch_fail_falls_back_to_cos_with_stage", func(t *testing.T) {
		_ = newAgentTransferEnv(t)
		capture, write := newTransferCapture()
		data := deterministicTransferBytes(2048)
		src := writeTempFile(t, t.TempDir(), "payload.bin", data)
		cos := newFakeTransferCOS(t, 0)

		// 流中断失败注入(非拨号失败):对端读完 hello 回 ok 后立即断流——
		// B 端 io.Copy 落 io.ErrClosedPipe,失败发生在传输中段(classify
		// default 臂=mid_transfer),终态回带=落 COS 近因。
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			reader := bufio.NewReader(server)
			if _, err := reader.ReadBytes('\n'); err != nil { // hello
				return
			}
			_, _ = server.Write([]byte("{\"ok\":true}\n"))
			// 不收流,直接关:net.Pipe 无缓冲,ok 写透即被 B 读走,断流后
			// B 的首个 body 写必失败。
		}()
		calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
			return client, nil
		})

		msg := punchUploadMsg(t, "tr_punchfb01", src, int64(len(data)), false, cos.url(), "dev-b")
		svc := &AgentService{}
		svc.handleAgentTransferUpload(msg, write)

		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != true {
			t.Fatalf("punch 失败必须落 COS 兜底成功: %v", terminal)
		}
		if via := remoteString(terminal, "via"); via != "cos" {
			t.Fatalf("兜底终态 via 必须是 cos,得到 %q", via)
		}
		if stage := remoteString(terminal, "lan_fail_stage"); stage != "mid_transfer" {
			t.Fatalf("punch 流中断失败阶段必须钉死 mid_transfer(LAN probe 被 punch 近因覆盖),得到 %q: %v", stage, terminal)
		}
		if n := len(cos.snapshot()); n != 1 {
			t.Fatalf("COS 兜底应收 1 次 PUT,实收 %d", n)
		}
		if atomic.LoadInt32(calls) != 1 {
			t.Fatalf("punch 拨号缝应被调恰一次,实调 %d", *calls)
		}
	})

	// 取消臂编排级锁(Task 8 评审 Important):取消落在 punch 腿必须 cancelled
	// 终态+绝不落 COS 重传。选型=实现最短的拨号阶段取消——缝内阻塞到
	// ctx.Done 再回 ctx.Err(),与流中取消走的是编排层同一个 ctx.Err() 判定
	// (agent_transfer_upload.go 取消臂),断言形态对齐 LAN 腿同款用例
	// TestAttemptAgentTransferLanPush_CtxCancelMidStream。
	t.Run("punch_cancel_emits_cancelled_never_cos", func(t *testing.T) {
		_ = newAgentTransferEnv(t)
		capture, write := newTransferCapture()
		data := deterministicTransferBytes(2048)
		src := writeTempFile(t, t.TempDir(), "payload.bin", data)
		cos := newFakeTransferCOS(t, 0)

		dialed := make(chan struct{})
		calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
			close(dialed)
			<-ctx.Done()
			return nil, ctx.Err()
		})

		msg := punchUploadMsg(t, "tr_punchcx01", src, int64(len(data)), false, cos.url(), "dev-b")
		svc := &AgentService{}
		done := make(chan struct{})
		go func() {
			defer close(done)
			svc.handleAgentTransferUpload(msg, write)
		}()
		select {
		case <-dialed:
		case <-time.After(10 * time.Second):
			t.Fatal("punch 拨号缝未被进入,取消无从谈起")
		}
		svc.handleAgentTransferCancel(map[string]interface{}{
			"type": models.AgentEventTransferCancel, "transfer_id": "tr_punchcx01",
		}, write)
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("取消后上传链未收口")
		}

		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != false || remoteString(terminal, "error_code") != transferErrCancelled {
			t.Fatalf("取消落在 punch 腿必须 cancelled 终态,不得静默完成: %v", terminal)
		}
		// 帧源判别:COS 腿循环顶的 ctx 兜底回带裸 context canceled,只有
		// punch 取消臂回带「punch dial …: context canceled」——锁死这帧
		// 确由编排层取消臂发出(删臂即红)。
		if errText := remoteString(terminal, "error"); !strings.Contains(errText, "punch dial") {
			t.Fatalf("cancelled 帧必须源自 punch 取消臂(error 含 punch dial 近因),得到 %q: %v", errText, terminal)
		}
		if n := len(cos.snapshot()); n != 0 {
			t.Fatalf("取消必须绝不落 COS 重传,实收 %d 次 PUT", n)
		}
		if atomic.LoadInt32(calls) != 1 {
			t.Fatalf("punch 拨号缝应被调恰一次,实调 %d", *calls)
		}
	})

	t.Run("no_punch_peer_id_keeps_cos_path", func(t *testing.T) {
		_ = newAgentTransferEnv(t)
		capture, write := newTransferCapture()
		data := deterministicTransferBytes(2048)
		src := writeTempFile(t, t.TempDir(), "payload.bin", data)
		cos := newFakeTransferCOS(t, 0)

		calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
			t.Error("无 punch_peer_id 不得进 punch 分支")
			return nil, errors.New("unreachable")
		})

		msg := punchUploadMsg(t, "tr_punchnoid1", src, int64(len(data)), false, cos.url(), "")
		svc := &AgentService{}
		svc.handleAgentTransferUpload(msg, write)

		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != true {
			t.Fatalf("无 punch_peer_id 的 COS 兜底路径必须不变: %v", terminal)
		}
		if via := remoteString(terminal, "via"); via != "cos" {
			t.Fatalf("LAN 失败后仍应 via=cos,得到 %q", via)
		}
		if stage := remoteString(terminal, "lan_fail_stage"); stage != "probe" {
			t.Fatalf("LAN 死端口失败阶段应为 probe,得到 %q", stage)
		}
		if n := len(cos.snapshot()); n != 1 {
			t.Fatalf("COS 应收 1 次 PUT,实收 %d", n)
		}
		if atomic.LoadInt32(calls) != 0 {
			t.Fatal("不得触 punch 拨号缝")
		}
	})

	t.Run("archive_never_enters_punch", func(t *testing.T) {
		_ = newAgentTransferEnv(t)
		capture, write := newTransferCapture()
		dir := t.TempDir()
		writeTempFile(t, dir, "note.txt", []byte("归档内容"))
		cos := newFakeTransferCOS(t, 0)

		calls := withPunchDialSeam(t, func(ctx context.Context, peerID, transferID string) (net.Conn, error) {
			t.Error("归档传输不得进 punch 分支")
			return nil, errors.New("unreachable")
		})

		msg := punchUploadMsg(t, "tr_puncharc01", dir, 0, true, cos.url(), "dev-b")
		svc := &AgentService{}
		svc.handleAgentTransferUpload(msg, write)

		terminal := waitForTransferTerminal(t, capture)
		if terminal["ok"] != true {
			t.Fatalf("归档走 COS 常规上传必须成功: %v", terminal)
		}
		if _, hasVia := terminal["via"]; hasVia {
			t.Fatalf("两腿都未失败,不得携带 via: %v", terminal)
		}
		if n := len(cos.snapshot()); n == 0 {
			t.Fatal("归档 COS 上传必须发生")
		}
		if atomic.LoadInt32(calls) != 0 {
			t.Fatal("归档不得触 punch 拨号缝")
		}
	})
}

// TestAgentPunchTransferServeEndToEndLoopback T7 评审转来的义务用例:经生产
// agentPunchTransferServe 的端到端回环——注册接收上下文→真 punch 拨号(缝
// 注入真实拨号链)→推文件→断言接收侧 inbox 落盘+ACK(sha/size 双端核对)
// +done marker+单次消费。
func TestAgentPunchTransferServeEndToEndLoopback(t *testing.T) {
	inbox := newAgentTransferEnv(t)
	const transferID = "tr_punche2e01"
	const token = "punch-e2e-token-1"

	// B 侧:真 ServeListener+生产 transferServe 默认;classify 桩宣告 loopback
	// 映射(取 socket 本端口——A 侧 DialPeer 的候选可拨,不碰外网)。
	b, _, _ := newPunchBForTest(t, func(bb *agentPeerPunchB) {
		bb.classify = func(sock *net.UDPConn, _ []string) (punch.PublicMapping, bool, error) {
			return punch.PublicMapping{IP: net.IPv4(127, 0, 0, 1), Port: sock.LocalAddr().(*net.UDPAddr).Port}, false, nil
		}
	})

	// A 侧:真实拨号链;信令桥把 A 自发帧喂 B、B 回帧喂回 A(双角色同进程
	// loopback,方向按帧出口区分——session=A 发,result/candidates=B 发)。
	// 桥内先过 JSON round-trip 复刻线协议形态(数字转 float64、切片成
	// []interface{}——remoteStringSlice 只认后者,进程内 []string 会被候选
	// 过滤面误吞)。
	wireClone := func(v interface{}) map[string]interface{} {
		t.Helper()
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("信令桥序列化失败: %v", err)
		}
		m := map[string]interface{}{}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("信令桥反序列化失败: %v", err)
		}
		return m
	}
	a, _ := newPunchAForTest(t)
	write := func(v interface{}) error {
		m := wireClone(v)
		switch m["type"] {
		case models.AgentEventPeerPunchSessionResult:
			a.handleResultFrame(m)
		case models.AgentEventPeerPunchCandidates:
			a.handleCandidatesFrame(m)
		}
		return nil
	}
	a.send = func(payload map[string]interface{}) error {
		m := wireClone(payload)
		switch m["type"] {
		case models.AgentEventPeerPunchSession:
			b.handleSession(m, write, "device-b-self")
		case models.AgentEventPeerPunchCandidates:
			b.handleCandidates(m)
		}
		return nil
	}

	// 接收侧上下文经生产注册入口(listen 帧时刻的传输参数)。
	registerAgentPunchTransferContext(transferID, agentPunchTransferContext{
		token:     token,
		filename:  "e2e-report.bin",
		targetDir: "",
		maxBytes:  int64(4 << 20),
		writeJSON: func(interface{}) error { return nil },
	}, 30*time.Second)

	// 缝注入真实件:生产缝值是 AgentPeerPunchTransferDial(进程级单例),
	// 测试内把同一真实链路接到本用例自持的 A 实例(参数逐字同款)。
	calls := withPunchDialSeam(t, func(ctx context.Context, peerID, tid string) (net.Conn, error) {
		if tid != transferID {
			return nil, fmt.Errorf("transfer_id 透传失配: %q", tid)
		}
		return a.dialSourceOpts(ctx, peerID, punchDialOpts{
			budget:     agentTransferPunchBudget,
			purpose:    "transfer",
			transferID: tid,
		})
	})

	data := deterministicTransferBytes(256 * 1024)
	src := writeTempFile(t, t.TempDir(), "e2e-report.bin", data)
	ident := agentTransferIdentity{RequestID: "req-e2e", TransferID: transferID, Filename: "e2e-report.bin", SizeBytes: int64(len(data))}
	size, sha, finalPath, err := attemptAgentTransferPunchPush(context.Background(), ident, "device-b", token, src, int64(len(data)), func(interface{}) error { return nil })
	if err != nil {
		t.Fatalf("端到端 punch 传输失败: %v", err)
	}
	if size != int64(len(data)) || sha != sha256HexOf(data) {
		t.Fatalf("ACK 双端核对失配: size=%d want=%d sha=%s want=%s", size, len(data), sha, sha256HexOf(data))
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Fatalf("拨号缝应被调恰一次,实调 %d", *calls)
	}
	// 接收侧落盘:finalPath 在 inbox 根下且内容逐字一致。
	if rel, relErr := filepath.Rel(inbox, finalPath); relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("finalPath 应落在 inbox 根: %s(inbox=%s)", finalPath, inbox)
	}
	got, readErr := os.ReadFile(finalPath)
	if readErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("接收侧落盘内容失配: err=%v len=%d want=%d", readErr, len(got), len(data))
	}
	// done marker 佐证生产 serve 体完整走完(收满→rename→marker→ACK)。
	marker, mErr := readAgentTransferMarker(transferID)
	if mErr != nil || marker == nil || marker.State != transferMarkerStateDone || marker.SHA256 != sha256HexOf(data) {
		t.Fatalf("done marker 缺失/不符: %+v err=%v", marker, mErr)
	}
	// 单次消费:上下文已被 session 帧取走,二次 take 必空。
	if _, ok := takeAgentPunchTransferContext(transferID); ok {
		t.Fatal("传输上下文应已被单次消费")
	}
}
