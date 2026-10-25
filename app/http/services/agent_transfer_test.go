package services

// 互传设备侧行为测试：文件名清洗 / 并发槽 / zip 往返与 slip 矩阵 / 发送链
// （分块+ETag+重签+busy/dedupe）/ 接收链（Range 续传+If-Range+sha 校验+
// busy/dedupe+cancel 清理+fence）。假 COS 与假服务端替换点：
// agentTransferHostAllowed / agentTransferFetchParts / agentTransferSendConfirm。
// 注意（跨平台陷阱）：本文件名避开 GOOS 尾段，所有平台都真实执行。

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
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
		"报告 最终版.pdf":               "报告 最终版.pdf",
		"截图 2026-10-22.png":           "截图 2026-10-22.png",
		"naïve😀.txt":                  "naïve_.txt", // emoji 替换为 _
		"my.file.v2.tar.gz":            "my.file.v2.tar.gz",
		".hidden":                      ".hidden",
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
		"CON.txt":                             []byte("x"),  // Windows 保留名条目
		"sub/nul.txt":                         []byte("x"),  // 中段段保留名
		"a.txt.":                              []byte("x"),  // 尾点条目（Windows 落盘吞点→重名歧义）
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
	srv           *httptest.Server
	payload       []byte
	etag          string
	doStall       bool
	stall         chan struct{}
	stallClose    sync.Once
	mu            sync.Mutex
	requests      []string
	firstGet      chan struct{}
	firstGetOnce  sync.Once
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
