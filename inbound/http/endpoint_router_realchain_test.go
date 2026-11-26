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
// 返回 client 侧连接(HandleHttpConnection 的读写对象)。
func realChainConn(t *testing.T, body string) net.Conn {
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

	go func() {
		inboundhttp.HandleHttpConnection(serverConn, reader, req)
	}()
	return client
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

	client := realChainConn(t, `{"model":"glm-4.6","messages":[{"role":"user","content":"hi"}]}`)

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
