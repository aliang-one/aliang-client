package http_test

// endpoint_router_realchain_test.go — 真链集成测试(final-review 必改 2 的
// 关键覆盖):NewEndpointServiceForTest + NewAIEndpointSelectorAdapter +
// SetAIEndpointSelector + HandleHttpConnection,不用任何 fake selector——
// 「候选1 单次 5xx(未达摘除阈值)→ 重试命中候选2」此前只有 fake-selector
// 形态的测试(fake 的第二次 Select 由队列手动喂出候选2),真链路从未验证。
//
// 包形态是刻意的外部测试包 http_test:inbound/http(被 app/http/services 经
// run.go import)反向 import services 会成环,包内测试无法引用 adapter 构
// 造;外部测试包在编译期是独立单元,经 t.Cleanup 恢复包级选择器,不污染同包
// 其余测试(它们仍用包内 fakeAISelector)。
//
// 前置:候选1 与候选2 都用 httptest 假上游(行为按请求次序 5xx→200),
// store 数据由 NewEndpointServiceForTest 直插:ep-first 优先级 10、ep-second
// 优先级 20,均在阈值 3 之下只有一次失败——单次 5xx 不摘除,旧实现(重试不
// 排除)会重打同一 endpoint,本测试钉死「切下一候选」。

import (
	"bufio"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/services"
	inboundhttp "aliang.one/nursorgate/inbound/http"
	"aliang.one/nursorgate/processor/config"
)

// realChainUpstream 起一个按请求次序应答的假上游:第一发 500,之后全部 200。
func realChainUpstream(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var auths []string
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		auths = append(auths, r.Header.Get("Authorization"))
		if len(auths) == 1 {
			w.WriteHeader(stdhttp.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"err":"first candidate 5xx"}`)
			return
		}
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &auths
}

// realChainConn 建一对真实 TCP 连接并把一条 /v1/messages 请求写给服务端,
// 返回 client 侧连接(HandleHttpConnection 的读写对象)与处理完成信号。done
// 必须等待:测试 body 返回即触发 t.Cleanup 里的 SetAIEndpointSelector(nil),
// 而 handler goroutine 还在读包级 aiSelector——不 join 就是数据竞争
// (race detector 实证,401 锁存真链测试首发即中)。
func realChainConn(t *testing.T, body string) (net.Conn, <-chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })

	type acceptResult struct {
		conn net.Conn
		err  error
	}
	acceptCh := make(chan acceptResult, 1)
	go func() {
		c, err := l.Accept()
		acceptCh <- acceptResult{c, err}
	}()
	client, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	go func() {
		_, _ = io.WriteString(client, "POST /v1/messages HTTP/1.1\r\n"+
			"Host: 127.0.0.1:56432\r\n"+
			"Content-Type: application/json\r\n"+
			"Content-Length: "+fmt.Sprintf("%d", len(body))+"\r\n"+
			"\r\n"+body)
	}()

	res := <-acceptCh
	if res.err != nil {
		t.Fatalf("accept: %v", res.err)
	}
	serverConn := res.conn
	t.Cleanup(func() { _ = serverConn.Close() })

	reader := bufio.NewReader(serverConn)
	req, err := stdhttp.ReadRequest(reader)
	if err != nil {
		t.Fatalf("read request: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		inboundhttp.HandleHttpConnection(serverConn, reader, req)
	}()
	return client, done
}

// waitHandler 等单条请求的 handler goroutine 退出(防与 Cleanup 竞争)。
func waitHandler(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler goroutine did not finish")
	}
}

// TestAIPathRealChainRetrySwitchesEndpoint — 真链:候选1 单次 5xx(未达阈值)
// → 重试必须命中候选2(而非同 endpoint 两枪)。
func TestAIPathRealChainRetrySwitchesEndpoint(t *testing.T) {
	upstream, auths := realChainUpstream(t)

	svc, err := services.NewEndpointServiceForTest()
	if err != nil {
		t.Fatalf("起测试 endpoint service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	// 候选1:ep-first(priority 10,key 一号)。候选2:ep-second(priority 20,
	// key 二号)。两者都挂在同一假上游:请求次序 5xx→200,鉴权头区分谁打的。
	for _, e := range []*config.LLMEndpoint{
		{
			ID: "ep-first", Name: "first", BaseURL: upstream.URL, APIKey: "sk-real-first",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 10, Models: "[]",
		},
		{
			ID: "ep-second", Name: "second", BaseURL: upstream.URL, APIKey: "sk-real-second",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 20, Models: "[]",
		},
	} {
		if err := svc.CreateEndpoint(e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	// 真链接线:adapter(真 Select/Acquire/ReportFailure)注入包级选择器槽。
	// 恢复到 nil 而非读回旧值:包级槽没有导出的 getter(刻意保持窄面),
	// 且同包其余测试均各自 setAISelectorForTest,结束时归 nil 即降级安全态。
	inboundhttp.SetAIEndpointSelector(services.NewAIEndpointSelectorAdapter(svc))
	t.Cleanup(func() { inboundhttp.SetAIEndpointSelector(nil) })

	client, done := realChainConn(t, `{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}`)

	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%q, want 200(重试应命中候选2 成功)", resp.StatusCode, string(respBody))
	}
	waitHandler(t, done)

	// 真切换断言:上游收到两枪,分别是候选1 与候选2 的 key(不是同 key 两枪)。
	if len(*auths) != 2 {
		t.Fatalf("上游收到 %d 发请求, want 2(候选1 失败 + 候选2 重试): %v", len(*auths), *auths)
	}
	if (*auths)[0] != "Bearer sk-real-first" {
		t.Fatalf("第一枪 Authorization = %q, want Bearer sk-real-first", (*auths)[0])
	}
	if (*auths)[1] != "Bearer sk-real-second" {
		t.Fatalf("第二枪 Authorization = %q, want Bearer sk-real-second(重试必须切下一候选,而非同 endpoint 两枪)", (*auths)[1])
	}

	// 健康态契约:候选1 只失败 1 次(< 阈值 3),未摘除;计数被真实落库。
	got, err := svc.GetEndpoint("ep-first")
	if err != nil {
		t.Fatalf("get ep-first: %v", err)
	}
	if got.HealthStatus != "ok" {
		t.Fatalf("单次 5xx 未达阈值不该摘除, status=%s", got.HealthStatus)
	}
	// 候选2 成功:ReportSuccess 清零(本来就零)。
	got2, err := svc.GetEndpoint("ep-second")
	if err != nil {
		t.Fatalf("get ep-second: %v", err)
	}
	if got2.HealthStatus != "ok" || got2.ConsecutiveFailures != 0 {
		t.Fatalf("候选2 应成功且健康, got %+v", got2)
	}
	_ = strings.TrimSpace("") // 保持 strings import(错误信息断言预留)
}

// TestAIPathRealChain401LatchesEndpointDisabled — 真链 401 锁存全链(二期 spec
// §5.3+§6 必修 3):BYO 上游 401 → 路由层 ReportFailure(id, 401, …) → service
// 锁存(disabled + cooldown_until=0 永不自愈)→ 重试 Select 排除该行命中候选2
// → 客户端拿到 200。再发第二枪(全新请求、无 excludeID):被锁存行连 selectAuto
// 的健康过滤都过不了,继续落到候选2——锁存不止单请求内的 exclude,是持久摘除。
func TestAIPathRealChain401LatchesEndpointDisabled(t *testing.T) {
	var mu sync.Mutex
	var auths []string
	srv := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		mu.Lock()
		n := len(auths) + 1
		auths = append(auths, r.Header.Get("Authorization"))
		mu.Unlock()
		if n == 1 {
			w.WriteHeader(stdhttp.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
			return
		}
		w.WriteHeader(stdhttp.StatusOK)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(srv.Close)

	svc, err := services.NewEndpointServiceForTest()
	if err != nil {
		t.Fatalf("起测试 endpoint service: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	for _, e := range []*config.LLMEndpoint{
		{
			ID: "ep-deadkey", Name: "deadkey", BaseURL: srv.URL, APIKey: "sk-dead-key",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 10, Models: "[]",
		},
		{
			ID: "ep-live", Name: "live", BaseURL: srv.URL, APIKey: "sk-live-key",
			ProtocolAnthropic: true, AutoGroup: true, Enabled: true, Priority: 20, Models: "[]",
		},
	} {
		if err := svc.CreateEndpoint(e); err != nil {
			t.Fatalf("create %s: %v", e.ID, err)
		}
	}

	inboundhttp.SetAIEndpointSelector(services.NewAIEndpointSelectorAdapter(svc))
	t.Cleanup(func() { inboundhttp.SetAIEndpointSelector(nil) })

	body := `{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}`

	// 第一枪:ep-deadkey 401 → 锁存 → 重试排除 → ep-live 200。
	client, done1 := realChainConn(t, body)
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := stdhttp.ReadResponse(bufio.NewReader(client), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status = %d body=%q, want 200(401 行应被锁存并 failover 到 ep-live)", resp.StatusCode, string(respBody))
	}
	// 等 handler 退出:既防 Cleanup 竞争,也钉死第二枪的 auths 次序
	// (第一枪的两发必须全部落库后再发第二枪)。
	waitHandler(t, done1)

	mu.Lock()
	gotAuths := append([]string{}, auths...)
	mu.Unlock()
	if len(gotAuths) != 2 || gotAuths[0] != "Bearer sk-dead-key" || gotAuths[1] != "Bearer sk-live-key" {
		t.Fatalf("auths = %v, want [Bearer sk-dead-key Bearer sk-live-key](第一枪 401、第二枪切候选)", gotAuths)
	}

	// service 侧锁存断言(spec §5.3):disabled + 无冷却不自愈。
	got, err := svc.GetEndpoint("ep-deadkey")
	if err != nil {
		t.Fatalf("get ep-deadkey: %v", err)
	}
	if got.HealthStatus != "disabled" {
		t.Fatalf("401 后 health_status = %q, want disabled(锁存)", got.HealthStatus)
	}
	if got.CooldownUntil != 0 {
		t.Fatalf("401 后 cooldown_until = %d, want 0(锁存=无冷却不自愈)", got.CooldownUntil)
	}

	// 第二枪(全新请求,无 excludeID):锁存行被健康过滤天然摘除,仍落 ep-live。
	client2, done2 := realChainConn(t, body)
	_ = client2.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp2, err := stdhttp.ReadResponse(bufio.NewReader(client2), nil)
	if err != nil {
		t.Fatalf("read response 2: %v", err)
	}
	_, _ = io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if resp2.StatusCode != stdhttp.StatusOK {
		t.Fatalf("status 2 = %d, want 200(锁存行在后续请求也应被摘除)", resp2.StatusCode)
	}
	waitHandler(t, done2)

	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 3 || auths[2] != "Bearer sk-live-key" {
		t.Fatalf("auths = %v, want third shot on ep-live only(锁存行后续请求不再入选)", auths)
	}
}
