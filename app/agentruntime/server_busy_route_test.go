package agentruntime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 2026-11-22 生产死锁善后:56433 的 /api/agent/status|sync|disable 三个端点
// 串行在同一把 AgentService.s.mu 上,锁忙(最坏 ~50s)或锁被焊死(当日事故)
// 时客户端无限挂。wrapAgentBusyRoute 用 http.TimeoutHandler 提供服务端收割
// 上限:超时返回 503,而非永久不回。与 services.Status() 的锁忙降级构成双
// 保险——降级覆盖 status 的常规轮询,503 兜底 sync/disable 与极端锁死。

func TestWrapAgentBusyRouteReturns503WhenHandlerStalls(t *testing.T) {
	old := userAgentHandlerBusyTimeout
	userAgentHandlerBusyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { userAgentHandlerBusyTimeout = old })

	srv := httptest.NewServer(wrapAgentBusyRoute(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	})))
	defer srv.Close()

	client := srv.Client()
	client.Timeout = 2 * time.Second
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("busy route request failed: %v(应在超时上限内返回 503,而非挂死或连接错)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stalled handler status = %d, want 503", resp.StatusCode)
	}
	// 2026-11-22 评审:超时体必须是 common.Error 同形 JSON envelope——经 core
	// proxy 透传后,仪表盘/手机按 {code,data} 解析,纯文本体会变成解析错误。
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var envelope struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("503 体不是 JSON envelope(body=%q):消费方按 {code,data} 解析将失败", strings.TrimSpace(string(raw)))
	}
	if envelope.Code != http.StatusServiceUnavailable {
		t.Fatalf("envelope.code = %d, want 503", envelope.Code)
	}
}

func TestWrapAgentBusyRoutePassesThroughWhenHandlerFast(t *testing.T) {
	old := userAgentHandlerBusyTimeout
	userAgentHandlerBusyTimeout = 2 * time.Second
	t.Cleanup(func() { userAgentHandlerBusyTimeout = old })

	srv := httptest.NewServer(wrapAgentBusyRoute(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("fast handler request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("fast handler status = %d, want 418(快速完成必须原样透传)", resp.StatusCode)
	}
}
