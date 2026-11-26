package http

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/tcp"
)

// ---- 测试基建 ---------------------------------------------------------------

// newLocalTCPConnPair 建一对真实 TCP 连接(一次性 listener)。用真实 socket 而
// 非 net.Pipe:管道是全同步的,服务端写响应会阻塞到客户端读——路由层的流式
// 写回会与测试主体互相卡死;真实 socket 有内核缓冲,双方异步推进。
func newLocalTCPConnPair(t *testing.T) (server net.Conn, client net.Conn) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	acceptCh := make(chan net.Conn, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			acceptCh <- nil
			return
		}
		acceptCh <- c
	}()
	client, err = net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server = <-acceptCh
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		_ = l.Close()
		_ = client.Close()
		_ = server.Close()
	})
	return server, client
}

// selectResult 是 fakeAISelector 的 Select 队列条目;队列耗尽时重复最后一项。
type selectResult struct {
	id      string
	baseURL string
	apiKey  string
	builtin bool
	err     error
}

type recordedFailure struct {
	id     string
	status int
	errMsg string
}

// fakeAISelector 是 AIEndpointSelector 的测试替身:记录全部调用,Select 结果
// 按队列依次返回(队列耗尽重复最后一项)。
type fakeAISelector struct {
	mu sync.Mutex

	selectQueue []selectResult
	selectCalls []struct {
		protocol, model, explicitID string
		excludeID                   string
	}

	acquired        []string
	released        []string
	successes       []string
	failures        []recordedFailure
	modelsUnionData []map[string]any

	// failOnSelect 非空时,任何一次 Select 调用都判失败(用例 10:非 AI path
	// 不进路由层的反向断言)。
	failOnSelect func(protocol, model, explicitID string)

	releaseOnce func()
}

func (f *fakeAISelector) Select(protocol, model, explicitID, excludeID string) (string, string, string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selectCalls = append(f.selectCalls, struct {
		protocol, model, explicitID string
		excludeID                   string
	}{protocol, model, explicitID, excludeID})
	if f.failOnSelect != nil {
		f.failOnSelect(protocol, model, explicitID)
	}
	if len(f.selectQueue) == 0 {
		return "", "", "", false, fmt.Errorf("fake selector: select queue empty")
	}
	r := f.selectQueue[0]
	if len(f.selectQueue) > 1 {
		f.selectQueue = f.selectQueue[1:]
	}
	return r.id, r.baseURL, r.apiKey, r.builtin, r.err
}

func (f *fakeAISelector) Acquire(id string, maxConcurrency int) (func(), error) {
	f.mu.Lock()
	f.acquired = append(f.acquired, id)
	once := f.releaseOnce
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.released = append(f.released, id)
		f.mu.Unlock()
		if once != nil {
			once()
		}
	}, nil
}

func (f *fakeAISelector) ReportSuccess(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, id)
}

func (f *fakeAISelector) ReportFailure(id string, status int, errMsg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, recordedFailure{id, status, errMsg})
}

func (f *fakeAISelector) ModelsUnion() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.modelsUnionData
}

func (f *fakeAISelector) snapshot() (calls []struct {
	protocol, model, explicitID string
	excludeID                   string
}, acquired, released, successes []string, failures []recordedFailure) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]struct {
			protocol, model, explicitID string
			excludeID                   string
		}{}, f.selectCalls...),
		append([]string{}, f.acquired...), append([]string{}, f.released...),
		append([]string{}, f.successes...), append([]recordedFailure{}, f.failures...)
}

// setAISelectorForTest 替换包级选择器并注册恢复。
func setAISelectorForTest(t *testing.T, s AIEndpointSelector) {
	t.Helper()
	orig := aiSelector
	SetAIEndpointSelector(s)
	t.Cleanup(func() { SetAIEndpointSelector(orig) })
}

// fakeRelayTCPHandler 是内置 endpoint 转发验证用的 tcp handler 替身:从重放
// conn 读出完整请求(头+体),记录 metadata 与请求,再写回一条定长应答。
type fakeRelayTCPHandler struct {
	mu       sync.Mutex
	req      *stdhttp.Request
	body     []byte
	metadata *M.Metadata
	respBody string
}

func (h *fakeRelayTCPHandler) Handle(_ context.Context, conn net.Conn, metadata *M.Metadata) error {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	req, err := stdhttp.ReadRequest(reader)
	if err != nil {
		return err
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.req = req
	h.body = body
	h.metadata = metadata
	h.mu.Unlock()

	resp := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " +
		fmt.Sprintf("%d", len(h.respBody)) + "\r\n\r\n" + h.respBody
	_, _ = io.WriteString(conn, resp)
	return nil
}

func (h *fakeRelayTCPHandler) captured() (*stdhttp.Request, []byte, *M.Metadata) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.req, h.body, h.metadata
}

// serveRawRequest 起一条到 HandleHttpConnection 的真实 TCP 会话:clientConn
// 侧由调用方读写。返回服务端处理完成信号。
func serveRawRequest(t *testing.T, rawRequest string) (clientConn net.Conn, handled <-chan struct{}) {
	t.Helper()
	serverConn, clientConn := newLocalTCPConnPair(t)

	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(clientConn, rawRequest)
		writeDone <- err
	}()

	reader := bufio.NewReader(serverConn)
	req, err := stdhttp.ReadRequest(reader)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}

	done := make(chan struct{}, 1)
	go func() {
		HandleHttpConnection(serverConn, reader, req)
		close(done)
	}()

	if err := <-writeDone; err != nil {
		t.Fatalf("client write failed: %v", err)
	}
	return clientConn, done
}

// ---- fake 上游 ---------------------------------------------------------------

// upstreamCapture 记录 fake 上游收到的请求。
type upstreamCapture struct {
	mu          sync.Mutex
	authHeaders []string
	innerHeader bool
	hosts       []string
	bodies      []string
	headers     []stdhttp.Header
}

func (c *upstreamCapture) record(r *stdhttp.Request, body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.authHeaders = append(c.authHeaders, r.Header.Get("Authorization"))
	_, c.innerHeader = r.Header["Authorization-Inner"]
	c.hosts = append(c.hosts, r.Host)
	c.bodies = append(c.bodies, body)
	c.headers = append(c.headers, r.Header.Clone())
}

func (c *upstreamCapture) lastHeaders() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return nil
	}
	h := c.headers[len(c.headers)-1]
	var out []string
	for k := range h {
		out = append(out, k)
	}
	return out
}

func (c *upstreamCapture) lastHeaderSnapshot() stdhttp.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.headers) == 0 {
		return nil
	}
	return c.headers[len(c.headers)-1].Clone()
}

func (c *upstreamCapture) snapshot() (auths []string, inner bool, hosts []string, bodies []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.authHeaders...), c.innerHeader,
		append([]string{}, c.hosts...), append([]string{}, c.bodies...)
}

// newFakeUpstream 起 fake 上游:behaviors 依次决定每个请求的应答,耗尽后重复
// 最后一个。
func newFakeUpstream(t *testing.T, capture *upstreamCapture, behaviors ...func(w stdhttp.ResponseWriter, r *stdhttp.Request)) *httptest.Server {
	t.Helper()
	idx := 0
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		body, _ := io.ReadAll(r.Body)
		capture.record(r, string(body))

		var behavior func(w stdhttp.ResponseWriter, r *stdhttp.Request)
		if len(behaviors) == 0 {
			behavior = func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				_, _ = io.WriteString(w, `{"ok":true}`)
			}
		} else {
			behavior = behaviors[idx]
			if idx < len(behaviors)-1 {
				idx++
			}
		}
		behavior(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func plainBody(w stdhttp.ResponseWriter, status int, payload string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, payload)
}

// ---- 用例 --------------------------------------------------------------------

const anthropicRequestBody = `{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}`

// 用例 1:POST /v1/messages → 选中 BYO → 转发,断言 Authorization: Bearer、
// 无 Authorization-Inner、Host=上游 host、body 原样。
func TestAIPathAnthropicForwardedByo(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := newFakeUpstream(t, capture)

	sel := &fakeAISelector{selectQueue: []selectResult{{
		id: "byo-1", baseURL: upstream.URL, apiKey: "test-key-123", builtin: false,
	}}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Authorization: sk-aliang-local\r\n"+
			"X-Endpoint-Id: byo-1\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%q", resp.StatusCode, string(respBody))
	}

	auths, inner, hosts, bodies := capture.snapshot()
	if len(auths) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(auths))
	}
	if auths[0] != "Bearer test-key-123" {
		t.Fatalf("upstream Authorization = %q, want Bearer test-key-123", auths[0])
	}
	if inner {
		t.Fatal("upstream must not receive Authorization-Inner from the router")
	}
	if hosts[0] != strings.TrimPrefix(upstream.URL, "http://") {
		t.Fatalf("upstream Host = %q, want %q", hosts[0], strings.TrimPrefix(upstream.URL, "http://"))
	}
	if bodies[0] != anthropicRequestBody {
		t.Fatalf("upstream body = %q, want %q", bodies[0], anthropicRequestBody)
	}
	// X-Endpoint-Id 是 56432 私有路由指令,命中后绝不泄漏到上游
	// (quality-fix Minor 8)。
	for _, h := range capture.lastHeaders() {
		if strings.EqualFold(h, "X-Endpoint-Id") {
			t.Fatalf("X-Endpoint-Id leaked to upstream: %v", capture.lastHeaderSnapshot())
		}
	}

	calls, acquired, released, successes, _ := sel.snapshot()
	if len(calls) != 1 || calls[0].protocol != "anthropic" || calls[0].model != "glm-4.6" {
		t.Fatalf("select calls = %+v", calls)
	}
	if len(acquired) != 1 || acquired[0] != "byo-1" {
		t.Fatalf("acquired = %v", acquired)
	}
	if len(released) != 1 {
		t.Fatalf("released = %v (release must be called exactly once)", released)
	}
	if len(successes) != 1 || successes[0] != "byo-1" {
		t.Fatalf("successes = %v", successes)
	}
}

// 用例 2:POST /v1/chat/completions → openai 族同款转发。
func TestAIPathOpenAIForwardedByo(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := newFakeUpstream(t, capture)

	sel := &fakeAISelector{selectQueue: []selectResult{{
		id: "byo-oai", baseURL: upstream.URL, apiKey: "oai-key", builtin: false,
	}}}
	setAISelectorForTest(t, sel)

	openaiBody := `{"model":"gpt-x","messages":[{"role":"user","content":"yo"}]}`
	client, handled := serveRawRequest(t,
		"POST /v1/chat/completions HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Authorization: sk-anything\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(openaiBody))+"\r\n"+
			"\r\n"+openaiBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	calls, _, _, _, _ := sel.snapshot()
	if len(calls) != 1 || calls[0].protocol != "openai" || calls[0].model != "gpt-x" {
		t.Fatalf("select calls = %+v", calls)
	}

	auths, _, hosts, bodies := capture.snapshot()
	if len(auths) != 1 || auths[0] != "Bearer oai-key" {
		t.Fatalf("auths = %v", auths)
	}
	if hosts[0] != strings.TrimPrefix(upstream.URL, "http://") {
		t.Fatalf("host = %q", hosts[0])
	}
	if bodies[0] != openaiBody {
		t.Fatalf("body = %q", bodies[0])
	}
}

// 用例 3:候选1 5xx → ReportFailure → 切候选2 成功(响应头未发出前)。
func TestAIPathRetriesOn5xxBeforeResponseWritten(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := newFakeUpstream(t, capture,
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) { plainBody(w, 500, `{"err":"boom"}`) },
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) { plainBody(w, 200, `{"ok":true}`) },
	)

	sel := &fakeAISelector{selectQueue: []selectResult{
		{id: "cand-1", baseURL: upstream.URL, apiKey: "k1"},
		{id: "cand-2", baseURL: upstream.URL, apiKey: "k2"},
	}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%q (client must get candidate 2's 200, not candidate 1's 500)", resp.StatusCode, string(respBody))
	}

	_, _, _, successes, failures := sel.snapshot()
	if len(failures) != 1 || failures[0].id != "cand-1" || failures[0].status != 500 {
		t.Fatalf("failures = %+v, want single cand-1/500", failures)
	}
	if len(successes) != 1 || successes[0] != "cand-2" {
		t.Fatalf("successes = %v, want cand-2 only", successes)
	}

	auths, _, _, _ := capture.snapshot()
	if len(auths) != 2 {
		t.Fatalf("upstream got %d requests, want 2 (one per candidate)", len(auths))
	}
	if auths[0] != "Bearer k1" || auths[1] != "Bearer k2" {
		t.Fatalf("auths = %v, want [Bearer k1 Bearer k2]", auths)
	}
}

// 用例 4(不变量 4):候选1 返回 200 + 首个 SSE 事件后断连 → 不切候选2,
// 客户端收到已发出的部分数据后连接结束。
func TestAIPathNeverSwitchesAfterStreamStarted(t *testing.T) {
	sentEvent := "event: message_start\ndata: {\"t\":1}\n\n"
	capture := &upstreamCapture{}
	// 候选1:声明 Content-Length 比实际大,发一个 SSE 事件后粗暴断连
	// (hijack 直写后 Close)。
	upstream := newFakeUpstream(t, capture,
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			hj, ok := w.(stdhttp.Hijacker)
			if !ok {
				t.Error("upstream: hijack unsupported")
				return
			}
			conn, bw, err := hj.Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(bw, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: 100\r\n\r\n%s", sentEvent)
			_ = bw.Flush()
		},
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) { plainBody(w, 200, `{"ok":true}`) },
	)

	sel := &fakeAISelector{selectQueue: []selectResult{
		{id: "cand-1", baseURL: upstream.URL, apiKey: "k1"},
		{id: "cand-2", baseURL: upstream.URL, apiKey: "k2"},
	}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	// 客户端读到 200 + 部分 SSE 事件,然后连接结束。
	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response headers: %v", err)
	}
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200 (headers already sent to client)", resp.StatusCode)
	}
	partial, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr == nil {
		t.Fatal("expected a read error (truncated stream), got clean EOF")
	}
	if !strings.Contains(string(partial), "message_start") {
		t.Fatalf("partial body = %q, want the first SSE event", string(partial))
	}

	_, _, _, successes, failures := sel.snapshot()
	if len(successes) != 0 {
		t.Fatalf("successes = %v, want none (stream broke)", successes)
	}
	if len(failures) != 1 || failures[0].id != "cand-1" {
		t.Fatalf("failures = %+v, want single cand-1", failures)
	}

	// 候选2 收到的请求数 = 0(第二个 behavior 未被触达)。
	auths, _, _, _ := capture.snapshot()
	if len(auths) != 1 {
		t.Fatalf("upstream got %d requests, want exactly 1 (no retry after stream started)", len(auths))
	}
}

// 用例 5:无候选 + builtin 健康 → 走 builtin(原 metadata 交给 tcp handler,
// 由强制路由链处理);客户端 Authorization 原样透传,路由层不注入 Authorization-Inner。
func TestAIPathBuiltinFallsThroughTCPHandler(t *testing.T) {
	relay := &fakeRelayTCPHandler{respBody: `{"ok":true}`}
	origHandler := tcp.GetHandler()
	tcp.SetHandler(relay)
	t.Cleanup(func() { tcp.SetHandler(origHandler) })

	sel := &fakeAISelector{selectQueue: []selectResult{{
		id: "builtin-aliang", baseURL: "", apiKey: "", builtin: true,
	}}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Authorization: sk-aliang-local\r\n"+
			// 客户端伪造的 relay 注入头:路由层是最后一跳,builtin 重放必须剥
			// (quality-fix Important 1;watcher 会无条件覆写,今天无害但口径统一)。
			"Authorization-Inner: forged-inner-credential\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	req, body, metadata := relay.captured()
	if req == nil {
		t.Fatal("tcp handler did not receive a request")
	}
	if string(body) != anthropicRequestBody {
		t.Fatalf("tcp handler body = %q, want %q (router consumed body then replayed it)", string(body), anthropicRequestBody)
	}
	if got := req.Header.Get("Authorization"); got != "sk-aliang-local" {
		t.Fatalf("Authorization = %q, want passthrough sk-aliang-local (relay injects Authorization-Inner, router must not)", got)
	}
	if got, present := req.Header["Authorization-Inner"]; present {
		t.Fatalf("builtin replay must strip client-supplied Authorization-Inner, got %v", got)
	}
	if metadata == nil || metadata.DstPort != 56432 || metadata.HostName != "127.0.0.1" {
		t.Fatalf("metadata = %+v, want original 127.0.0.1:56432 shape (force-aliang route trigger)", metadata)
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK || string(respBody) != `{"ok":true}` {
		t.Fatalf("status=%d body=%q", resp.StatusCode, string(respBody))
	}

	_, _, _, successes, _ := sel.snapshot()
	if len(successes) != 1 || successes[0] != "builtin-aliang" {
		t.Fatalf("successes = %v", successes)
	}
}

// 用例 6:无候选 + builtin 不健康 → 503 no_endpoint_available。
func TestAIPathNoEndpointReturns503(t *testing.T) {
	sel := &fakeAISelector{selectQueue: []selectResult{{
		err: fmt.Errorf("endpoint: 无可用 endpoint(候选为空且内置行不健康): %w", ErrAINoEndpoint),
	}}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%q", resp.StatusCode, string(respBody))
	}
	if !strings.Contains(string(respBody), "no_endpoint_available") {
		t.Fatalf("body = %q, want no_endpoint_available", string(respBody))
	}

	_, acquired, _, successes, failures := sel.snapshot()
	if len(acquired) != 0 {
		t.Fatalf("acquired = %v, want none (select failed)", acquired)
	}
	if len(successes) != 0 || len(failures) != 0 {
		t.Fatalf("successes=%v failures=%v, want none (no endpoint was acquired)", successes, failures)
	}
}

// 用例 7:GET /v1/models 聚合 —— 响应形状 {object:"list",data:[{id,object:"model",owned_by}]}。
func TestAIPathModelsAggregation(t *testing.T) {
	sel := &fakeAISelector{modelsUnionData: []map[string]any{
		{"id": "glm-4.6", "owner": "ep-a"},
		{"id": "gpt-x", "owner": "ep-b"},
	}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"GET /v1/models HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"\r\n")

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%q", resp.StatusCode, string(respBody))
	}

	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &payload); err != nil {
		t.Fatalf("unmarshal %q: %v", string(respBody), err)
	}
	if payload.Object != "list" {
		t.Fatalf("object = %q, want list", payload.Object)
	}
	if len(payload.Data) != 2 {
		t.Fatalf("data = %+v, want 2 entries", payload.Data)
	}
	if payload.Data[0].ID != "glm-4.6" || payload.Data[0].Object != "model" || payload.Data[0].OwnedBy != "ep-a" {
		t.Fatalf("data[0] = %+v", payload.Data[0])
	}
	if payload.Data[1].ID != "gpt-x" || payload.Data[1].Object != "model" || payload.Data[1].OwnedBy != "ep-b" {
		t.Fatalf("data[1] = %+v", payload.Data[1])
	}

	calls, _, _, _, _ := sel.snapshot()
	if len(calls) != 0 {
		t.Fatalf("select calls = %v, want none (/v1/models aggregates, does not forward)", calls)
	}
}

// 用例 8:X-Endpoint-Id 显式指定 → 透传给 Select;不可用(协议不匹配等)→ 404。
func TestAIPathExplicitEndpointID(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := newFakeUpstream(t, capture)

	sel := &fakeAISelector{selectQueue: []selectResult{{
		id: "auto-zero-row", baseURL: upstream.URL, apiKey: "k-explicit", builtin: false,
	}}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"X-Endpoint-Id: auto-zero-row\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d, want 200 body=%q", resp.StatusCode, string(respBody))
	}

	calls, _, _, _, _ := sel.snapshot()
	if len(calls) != 1 || calls[0].explicitID != "auto-zero-row" || calls[0].protocol != "anthropic" {
		t.Fatalf("select calls = %+v", calls)
	}

	// X-Endpoint-Id 是 56432 私有路由指令:命中转发后绝不泄漏到上游
	// (quality-fix Minor 8)。upstreamCapture 记录全部头,这里断言没有
	// X-Endpoint-Id 出现在上游请求里。
	auths, _, _, _ := capture.snapshot()
	if len(auths) != 1 {
		t.Fatalf("upstream got %d requests, want 1", len(auths))
	}
	for _, h := range capture.lastHeaders() {
		if strings.EqualFold(h, "X-Endpoint-Id") {
			t.Fatalf("X-Endpoint-Id leaked to upstream: %v", capture.lastHeaderSnapshot())
		}
	}
}

// 用例 8b:显式点名不可用(协议不匹配等)→ 404 endpoint_not_available。
func TestAIPathExplicitEndpointIDNotAvailable(t *testing.T) {
	sel := &fakeAISelector{selectQueue: []selectResult{{
		err: fmt.Errorf("endpoint: 指定 endpoint 不可用(不存在/未启用/协议不匹配): %w", ErrAIEndpointNotAvailable),
	}}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"X-Endpoint-Id: auto-zero-row\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusNotFound {
		t.Fatalf("status = %d, want 404 body=%q", resp.StatusCode, string(respBody))
	}
	if !strings.Contains(string(respBody), "endpoint_not_available") {
		t.Fatalf("body = %q, want endpoint_not_available", string(respBody))
	}
}

// 用例 9:aiSelector=nil → AI path 落回原透明代理行为(降级安全)。
func TestAIPathWithNilSelectorUsesTransparentProxy(t *testing.T) {
	capture := &captureTCPHandler{}
	origHandler := tcp.GetHandler()
	tcp.SetHandler(capture)
	t.Cleanup(func() { tcp.SetHandler(origHandler) })

	// 显式置 nil(即使本测试前已有别的测试设置过)。
	setAISelectorForTest(t, nil)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
	_ = client

	if capture.req == nil {
		t.Fatal("transparent proxy path did not run (tcp handler not reached)")
	}
	if capture.metadata == nil || capture.metadata.DstPort != 56432 {
		t.Fatalf("metadata = %+v", capture.metadata)
	}
}

// 用例 10:非 AI path(如 /foo)不进路由层 —— 走透明代理,Select 不被调用。
func TestNonAIPathBypassesRouter(t *testing.T) {
	capture := &captureTCPHandler{}
	origHandler := tcp.GetHandler()
	tcp.SetHandler(capture)
	t.Cleanup(func() { tcp.SetHandler(origHandler) })

	sel := &fakeAISelector{
		failOnSelect: func(protocol, model, explicitID string) {
			t.Errorf("Select must not be called for non-AI path (got protocol=%q model=%q id=%q)", protocol, model, explicitID)
		},
	}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /foo HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: 2\r\n"+
			"\r\nhi")

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	if capture.req == nil {
		t.Fatal("transparent proxy path did not run for /foo")
	}
	if got := string(capture.body); got != "hi" {
		t.Fatalf("body = %q, want hi", got)
	}
	calls, _, _, _, _ := sel.snapshot()
	if len(calls) != 0 {
		t.Fatalf("select calls = %v, want none", calls)
	}
	_ = client
}

// quality-fix Minor 8:请求体 8MB+1 → 413,Select 不被调用。
func TestAIPathBodyTooLargeReturns413(t *testing.T) {
	sel := &fakeAISelector{
		failOnSelect: func(protocol, model, explicitID string) {
			t.Errorf("Select must not be called for oversized body (got protocol=%q)", protocol)
		},
	}
	setAISelectorForTest(t, sel)

	// 8MB+1 字节体:Content-Length 声明实际大小(读侧按 LimitReader 判超限)。
	oversized := strings.Repeat("x", maxAIRequestBodyBytes+1)
	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(oversized))+"\r\n"+
			"\r\n"+oversized)

	select {
	case <-handled:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 body=%q", resp.StatusCode, string(respBody))
	}
	if !strings.Contains(string(respBody), "request_body_too_large") {
		t.Fatalf("body = %q, want request_body_too_large", string(respBody))
	}
}

// quality-fix Minor 8:重试耗尽(候选1 5xx → 候选2 5xx)→ 透传最后一次
// 上游 5xx 响应(而非合成错误),两次 ReportFailure、一次 ReportSuccess 无。
func TestAIPathRetryExhaustedPassesLast5xx(t *testing.T) {
	capture := &upstreamCapture{}
	upstream := newFakeUpstream(t, capture,
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) { plainBody(w, 502, `{"err":"first"}`) },
		func(w stdhttp.ResponseWriter, r *stdhttp.Request) { plainBody(w, 503, `{"err":"second"}`) },
	)

	sel := &fakeAISelector{selectQueue: []selectResult{
		{id: "cand-1", baseURL: upstream.URL, apiKey: "k1"},
		{id: "cand-2", baseURL: upstream.URL, apiKey: "k2"},
	}}
	setAISelectorForTest(t, sel)

	client, handled := serveRawRequest(t,
		"POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(anthropicRequestBody))+"\r\n"+
			"\r\n"+anthropicRequestBody)

	select {
	case <-handled:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (candidate 2's response passed through) body=%q", resp.StatusCode, string(respBody))
	}
	if !strings.Contains(string(respBody), "second") {
		t.Fatalf("body = %q, want the LAST upstream error body ({\"err\":\"second\"})", string(respBody))
	}

	_, _, _, successes, failures := sel.snapshot()
	if len(successes) != 0 {
		t.Fatalf("successes = %v, want none", successes)
	}
	if len(failures) != 2 ||
		failures[0].id != "cand-1" || failures[0].status != 502 ||
		failures[1].id != "cand-2" || failures[1].status != 503 {
		t.Fatalf("failures = %+v, want cand-1/502 + cand-2/503", failures)
	}

	auths, _, _, _ := capture.snapshot()
	if len(auths) != 2 || auths[0] != "Bearer k1" || auths[1] != "Bearer k2" {
		t.Fatalf("auths = %v, want [Bearer k1 Bearer k2]", auths)
	}
}
