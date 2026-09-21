package services

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// ---- harness -------------------------------------------------------------

// resetAgentFileUploadSlot clears the process-wide single-flight slot after
// each test so a hung upload from one test cannot poison the next.
func resetAgentFileUploadSlot(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		agentFileUploadMu.Lock()
		if agentFileUploadActive != nil {
			agentFileUploadActive.cancel()
			agentFileUploadActive = nil
		}
		agentFileUploadMu.Unlock()
	})
}

// agentFileUploadCapture adapts the handler's writeJSON into a buffered
// channel so tests can assert message order with select+timeout instead of
// sleeping.
type agentFileUploadCapture struct {
	ch chan map[string]interface{}
}

func newAgentFileUploadCapture() *agentFileUploadCapture {
	return &agentFileUploadCapture{ch: make(chan map[string]interface{}, 1024)}
}

func (c *agentFileUploadCapture) writeJSON(payload interface{}) error {
	msg, ok := payload.(map[string]interface{})
	if !ok {
		return io.ErrUnexpectedEOF
	}
	c.ch <- msg
	return nil
}

func (c *agentFileUploadCapture) next(t *testing.T, what string) map[string]interface{} {
	t.Helper()
	select {
	case msg := <-c.ch:
		return msg
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
		return nil
	}
}

// waitForAgentFileUploadTerminal drains messages until the result or
// file.error for requestID arrives, returning the preceding (progress)
// messages and the terminal payload.
func waitForAgentFileUploadTerminal(t *testing.T, c *agentFileUploadCapture, requestID string) ([]map[string]interface{}, map[string]interface{}) {
	t.Helper()
	var before []map[string]interface{}
	for i := 0; i < 1024; i++ {
		msg := c.next(t, "file.upload terminal message")
		msgType := remoteString(msg, "type")
		if (msgType == models.AgentEventFileUploadResult || msgType == models.AgentEventFileError) &&
			remoteString(msg, "request_id") == requestID {
			return before, msg
		}
		before = append(before, msg)
	}
	t.Fatalf("no terminal message for %s after 1024 messages", requestID)
	return nil, nil
}

type fakeCOSUploadPut struct {
	Method      string
	ContentType string
	Length      int64
	SHA256      string
	BodyLen     int
}

// fakeCOSUploadServer stands in for the presigned COS endpoint: it records
// method/headers/body of each PUT and can read slowly (chunkDelay per
// chunkBytes) to keep an upload in flight for progress/single-flight tests.
type fakeCOSUploadServer struct {
	statusCode int
	chunkBytes int64
	chunkDelay time.Duration

	srv *httptest.Server

	mu        sync.Mutex
	puts      []fakeCOSUploadPut
	firstRead chan struct{}
	firstOnce sync.Once
}

func newFakeCOSUploadServer(t *testing.T, statusCode int, chunkBytes int64, chunkDelay time.Duration) *fakeCOSUploadServer {
	t.Helper()
	f := &fakeCOSUploadServer{
		statusCode: statusCode,
		chunkBytes: chunkBytes,
		chunkDelay: chunkDelay,
		firstRead:  make(chan struct{}),
	}
	if f.statusCode == 0 {
		f.statusCode = http.StatusOK
	}
	if f.chunkBytes <= 0 {
		f.chunkBytes = 1 << 20
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCOSUploadServer) handle(w http.ResponseWriter, r *http.Request) {
	put := fakeCOSUploadPut{
		Method:      r.Method,
		ContentType: r.Header.Get("Content-Type"),
		Length:      r.ContentLength,
	}
	var buf []byte
	chunk := make([]byte, f.chunkBytes)
	for {
		n, err := r.Body.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)
			f.firstOnce.Do(func() { close(f.firstRead) })
			if f.chunkDelay > 0 {
				time.Sleep(f.chunkDelay)
			}
		}
		if err == io.EOF {
			break
		}
		// Client aborted mid-body (cancel test): record what we got and return.
		if err != nil {
			put.BodyLen = len(buf)
			sum := sha256.Sum256(buf)
			put.SHA256 = hex.EncodeToString(sum[:])
			f.mu.Lock()
			f.puts = append(f.puts, put)
			f.mu.Unlock()
			return
		}
	}
	put.BodyLen = len(buf)
	sum := sha256.Sum256(buf)
	put.SHA256 = hex.EncodeToString(sum[:])
	f.mu.Lock()
	f.puts = append(f.puts, put)
	f.mu.Unlock()
	w.WriteHeader(f.statusCode)
}

func (f *fakeCOSUploadServer) putsSnapshot() []fakeCOSUploadPut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fakeCOSUploadPut(nil), f.puts...)
}

func (f *fakeCOSUploadServer) waitUntilReading(t *testing.T) {
	t.Helper()
	select {
	case <-f.firstRead:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for fake COS to start reading the body")
	}
}

func deterministicUploadBytes(n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = byte(i*1103515245 + 12345)
	}
	return buf
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeAgentUploadFile(t *testing.T, project string, name string, size int) []byte {
	t.Helper()
	content := deterministicUploadBytes(size)
	if err := os.WriteFile(filepath.Join(project, name), content, 0o644); err != nil {
		t.Fatalf("write upload fixture: %v", err)
	}
	return content
}

// ---- cases ---------------------------------------------------------------

// 1. Normal flow: a 1MiB file is streamed to the presigned URL with the exact
// Content-Length and a non-empty Content-Type, the received body is
// byte-identical (sha256), and the handler answers with file.upload.result.
func TestAgentFileUploadStreamsFileToPresignedURL(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	const size = 1 << 20
	content := writeAgentUploadFile(t, project, "payload.bin", size)
	cos := newFakeCOSUploadServer(t, 0, 0, 0)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-stream",
		"project_path": project,
		"path":         "payload.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    2 << 20,
	}, sink.writeJSON)

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-stream")
	if remoteString(terminal, "type") != models.AgentEventFileUploadResult {
		t.Fatalf("terminal message = %v, want file.upload.result", terminal)
	}
	if terminal["ok"] != true {
		t.Fatalf("ok = %v, want true", terminal["ok"])
	}
	if got := terminal["size_bytes"]; got != int64(size) {
		t.Fatalf("size_bytes = %v (%T), want %d", got, got, size)
	}
	if got := terminal["http_status"]; got != 200 {
		t.Fatalf("http_status = %v (%T), want 200", got, got)
	}

	puts := cos.putsSnapshot()
	if len(puts) != 1 {
		t.Fatalf("fake COS received %d PUTs, want 1", len(puts))
	}
	put := puts[0]
	if put.Method != http.MethodPut {
		t.Fatalf("method = %q, want PUT", put.Method)
	}
	if put.Length != int64(size) {
		t.Fatalf("Content-Length = %d, want %d", put.Length, size)
	}
	if put.ContentType == "" {
		t.Fatal("Content-Type header is empty, want non-empty")
	}
	if put.BodyLen != size {
		t.Fatalf("received body length = %d, want %d", put.BodyLen, size)
	}
	if want := sha256Hex(content); put.SHA256 != want {
		t.Fatalf("body sha256 = %s, want %s (content corrupted in transit)", put.SHA256, want)
	}
}

// 2. Missing file: file.error is emitted and no PUT ever reaches the COS URL.
func TestAgentFileUploadMissingFileEmitsError(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	cos := newFakeCOSUploadServer(t, 0, 0, 0)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-missing",
		"project_path": project,
		"path":         "missing.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    1 << 20,
	}, sink.writeJSON)

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-missing")
	if remoteString(terminal, "type") != models.AgentEventFileError {
		t.Fatalf("terminal message = %v, want file.error", terminal)
	}
	if got := len(cos.putsSnapshot()); got != 0 {
		t.Fatalf("fake COS received %d PUTs, want 0 (Stat guard must fail before PUT)", got)
	}
}

// 3. max_bytes guard: an oversized file is rejected with file.error before
// any PUT is issued (the pre-flight Stat re-check, not just listing metadata).
func TestAgentFileUploadRejectsOversizeBeforePUT(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	writeAgentUploadFile(t, project, "big.bin", 2048)
	cos := newFakeCOSUploadServer(t, 0, 0, 0)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-oversize",
		"project_path": project,
		"path":         "big.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    1024,
	}, sink.writeJSON)

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-oversize")
	if remoteString(terminal, "type") != models.AgentEventFileError {
		t.Fatalf("terminal message = %v, want file.error", terminal)
	}
	if err := remoteString(terminal, "error"); !strings.Contains(err, "exceeds") {
		t.Fatalf("error = %q, want it to mention the size limit", err)
	}
	if got := len(cos.putsSnapshot()); got != 0 {
		t.Fatalf("fake COS received %d PUTs, want 0 (oversize must fail before PUT)", got)
	}
}

// 4. Non-2xx from COS: file.error surfaces the HTTP status.
func TestAgentFileUploadServerErrorSurfacesStatus(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	writeAgentUploadFile(t, project, "payload.bin", 4096)
	cos := newFakeCOSUploadServer(t, http.StatusInternalServerError, 0, 0)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-status500",
		"project_path": project,
		"path":         "payload.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    1 << 20,
	}, sink.writeJSON)

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-status500")
	if remoteString(terminal, "type") != models.AgentEventFileError {
		t.Fatalf("terminal message = %v, want file.error", terminal)
	}
	if err := remoteString(terminal, "error"); !strings.Contains(err, "status 500") {
		t.Fatalf("error = %q, want it to contain \"status 500\"", err)
	}
}

// 5. Progress: against a slow-reading COS (50ms per 64KiB) the handler emits
// file.upload.progress with monotonically increasing uploaded_bytes, all
// before the final file.upload.result.
func TestAgentFileUploadEmitsThrottledProgress(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	const size = 1 << 20
	writeAgentUploadFile(t, project, "slow.bin", size)
	cos := newFakeCOSUploadServer(t, 0, 64*1024, 50*time.Millisecond)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-progress",
		"project_path": project,
		"path":         "slow.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    2 << 20,
	}, sink.writeJSON)

	progress, terminal := waitForAgentFileUploadTerminal(t, sink, "req-progress")
	if len(progress) < 1 {
		t.Fatal("expected at least one file.upload.progress message, got none")
	}
	lastUploaded := int64(-1)
	for _, msg := range progress {
		if remoteString(msg, "type") != models.AgentEventFileUploadProgress {
			t.Fatalf("unexpected pre-terminal message: %v", msg)
		}
		if remoteString(msg, "request_id") != "req-progress" {
			t.Fatalf("progress request_id = %q, want req-progress", remoteString(msg, "request_id"))
		}
		uploaded, ok := msg["uploaded_bytes"].(int64)
		if !ok {
			t.Fatalf("uploaded_bytes = %v (%T), want int64", msg["uploaded_bytes"], msg["uploaded_bytes"])
		}
		total, ok := msg["total_bytes"].(int64)
		if !ok {
			t.Fatalf("total_bytes = %v (%T), want int64", msg["total_bytes"], msg["total_bytes"])
		}
		if total != int64(size) {
			t.Fatalf("total_bytes = %d, want %d", total, size)
		}
		if uploaded <= lastUploaded {
			t.Fatalf("uploaded_bytes not monotonic: %d after %d", uploaded, lastUploaded)
		}
		if uploaded > total {
			t.Fatalf("uploaded_bytes %d exceeds total %d", uploaded, total)
		}
		lastUploaded = uploaded
	}
	if remoteString(terminal, "type") != models.AgentEventFileUploadResult {
		t.Fatalf("terminal message = %v, want file.upload.result (progress must precede result)", terminal)
	}
}

// 6. Single-flight: while a slow upload is in flight, a second file.upload is
// rejected with file.error mentioning "in progress", and only one PUT ever
// reaches COS.
func TestAgentFileUploadSingleFlight(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	writeAgentUploadFile(t, project, "slow.bin", 1<<20)
	cos := newFakeCOSUploadServer(t, 0, 64*1024, 50*time.Millisecond)

	first := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-first",
		"project_path": project,
		"path":         "slow.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    2 << 20,
	}, first.writeJSON)

	// Wait until the first upload is genuinely streaming, then fire the second.
	cos.waitUntilReading(t)
	second := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-second",
		"project_path": project,
		"path":         "slow.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    2 << 20,
	}, second.writeJSON)

	rejected := second.next(t, "single-flight rejection")
	if remoteString(rejected, "type") != models.AgentEventFileError {
		t.Fatalf("second upload response = %v, want file.error", rejected)
	}
	if err := remoteString(rejected, "error"); !strings.Contains(err, "in progress") {
		t.Fatalf("error = %q, want it to contain \"in progress\"", err)
	}

	// The first upload must still complete normally.
	_, terminal := waitForAgentFileUploadTerminal(t, first, "req-first")
	if remoteString(terminal, "type") != models.AgentEventFileUploadResult {
		t.Fatalf("first upload terminal = %v, want file.upload.result", terminal)
	}
	if got := len(cos.putsSnapshot()); got != 1 {
		t.Fatalf("fake COS received %d PUTs, want exactly 1 (second upload must not PUT)", got)
	}
}

// 7. Cancel: file.upload.cancel for the in-flight request aborts the PUT with
// a context-canceled error surfaced through file.error.
func TestAgentFileUploadCancelAbortsInFlightPUT(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	writeAgentUploadFile(t, project, "slow.bin", 1<<20)
	cos := newFakeCOSUploadServer(t, 0, 64*1024, 50*time.Millisecond)

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-cancel",
		"project_path": project,
		"path":         "slow.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    2 << 20,
	}, sink.writeJSON)

	cos.waitUntilReading(t)
	handleAgentFileUploadCancel(map[string]interface{}{
		"type":       models.AgentEventFileUploadCancel,
		"request_id": "req-cancel",
	})

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-cancel")
	if remoteString(terminal, "type") != models.AgentEventFileError {
		t.Fatalf("terminal message = %v, want file.error", terminal)
	}
	if err := remoteString(terminal, "error"); !strings.Contains(err, "cancel") {
		t.Fatalf("error = %q, want it to contain \"cancel\"", err)
	} else if strings.Contains(err, cos.srv.URL) {
		// 脱敏后的 Do 错误不得回嵌预签名 URL（含签名查询串）。
		t.Fatalf("sanitized error = %q leaks the presigned upload URL", err)
	}
}

// 8. Response-header hang: COS swallows the request after the body is fully
// written and never answers (hung cloud LB). The client's
// ResponseHeaderTimeout must fire, surface through file.error (with the
// presigned URL stripped), and release the single-flight slot so a fresh
// upload starts immediately instead of being told "in progress".
func TestAgentFileUploadResponseHeaderHangReleasesSlot(t *testing.T) {
	resetAgentFileUploadSlot(t)
	project := t.TempDir()
	writeAgentUploadFile(t, project, "hang.bin", 4096)

	// Hang server: consumes the whole body, then never responds.
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-release
	}))
	t.Cleanup(hang.Close)
	// LIFO: unblock the hung handler before Close waits on outstanding requests.
	t.Cleanup(func() { close(release) })

	// Shrink the header timeout so the test doesn't wait the production 60s.
	// The client is a package var, so swap it for the duration of this test.
	origClient := agentFileUploadHTTPClient
	agentFileUploadHTTPClient = newAgentFileUploadHTTPClient(50 * time.Millisecond)
	t.Cleanup(func() { agentFileUploadHTTPClient = origClient })

	sink := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-hang",
		"project_path": project,
		"path":         "hang.bin",
		"upload_url":   hang.URL,
		"max_bytes":    1 << 20,
	}, sink.writeJSON)

	_, terminal := waitForAgentFileUploadTerminal(t, sink, "req-hang")
	if remoteString(terminal, "type") != models.AgentEventFileError {
		t.Fatalf("terminal message = %v, want file.error", terminal)
	}
	if err := remoteString(terminal, "error"); !strings.Contains(err, "timeout") {
		t.Fatalf("error = %q, want it to contain \"timeout\"", err)
	} else if strings.Contains(err, hang.URL) {
		t.Fatalf("sanitized error = %q leaks the presigned upload URL", err)
	}

	// The terminal message is emitted just before the deferred slot release,
	// so wait for the release itself before proving a new upload is admitted.
	deadline := time.Now().Add(10 * time.Second)
	for {
		agentFileUploadMu.Lock()
		idle := agentFileUploadActive == nil
		agentFileUploadMu.Unlock()
		if idle {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the single-flight slot to be released after the header timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}

	cos := newFakeCOSUploadServer(t, 0, 0, 0)
	sink2 := newAgentFileUploadCapture()
	handleAgentFileUpload(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-after-hang",
		"project_path": project,
		"path":         "hang.bin",
		"upload_url":   cos.srv.URL,
		"max_bytes":    1 << 20,
	}, sink2.writeJSON)

	_, terminal2 := waitForAgentFileUploadTerminal(t, sink2, "req-after-hang")
	if remoteString(terminal2, "type") != models.AgentEventFileUploadResult {
		t.Fatalf("post-hang upload terminal = %v, want file.upload.result (slot must be free, not \"in progress\")", terminal2)
	}
}

// ---- dispatch wiring + enabled-device gate ---------------------------------

// TestRemoteAgentMessageRequiresEnabledDeviceUpload keeps file.upload behind
// the enabled+registered gate (same tier as file.read), while file.upload.cancel
// stays reachable so a device disabled mid-transfer can still wind it down.
func TestRemoteAgentMessageRequiresEnabledDeviceUpload(t *testing.T) {
	cases := []struct {
		msgType string
		want    bool
	}{
		{models.AgentEventFileUpload, true},
		{models.AgentEventFileUploadCancel, false},
	}
	for _, tc := range cases {
		if got := remoteAgentMessageRequiresEnabledDevice(tc.msgType); got != tc.want {
			t.Fatalf("remoteAgentMessageRequiresEnabledDevice(%q) = %v, want %v", tc.msgType, got, tc.want)
		}
	}
}

// TestHandleRemoteAgentMessageUploadDisabledGate proves the wired dispatch
// path rejects file.upload on a disabled device (guard → error + disconnect)
// while file.upload.cancel passes through the same disabled state untouched.
func TestHandleRemoteAgentMessageUploadDisabledGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
	// Leave Enabled/Registered false: remoteControlAllowed() must trip the
	// guard for file.upload but not for file.upload.cancel.
	service.mu.Lock()
	service.state.Enabled = false
	service.state.Registered = false
	service.mu.Unlock()

	var written []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok {
			written = append(written, m)
		}
		return nil
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":         models.AgentEventFileUpload,
		"request_id":   "req-disabled",
		"project_path": t.TempDir(),
		"path":         "a.bin",
		"upload_url":   "http://127.0.0.1:1/nowhere",
	}, writeJSON)

	if len(written) != 1 || written[0]["type"] != models.AgentEventError {
		t.Fatalf("disabled-device file.upload must be rejected with exactly one error message, got %v", written)
	}
	if msg := fmt.Sprint(written[0]["error"]); !strings.Contains(msg, "disabled") {
		t.Fatalf("error = %q, want it to mention the disabled gate", msg)
	}

	// Cancel must survive the same disabled state without an error reply.
	before := len(written)
	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":       models.AgentEventFileUploadCancel,
		"request_id": "req-disabled",
	}, writeJSON)
	for _, m := range written[before:] {
		if m["type"] == models.AgentEventError {
			t.Fatalf("file.upload.cancel on a disabled device must not be rejected, got error %v", m)
		}
	}
}
