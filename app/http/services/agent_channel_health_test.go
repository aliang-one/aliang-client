package services

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

func TestCallAgentServerRetriesOnceWithRefreshedHeader(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.Header.Get("Authorization") == "Bearer stale" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"ok":true}}`))
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "Bearer fresh", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	raw, err := svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, map[string]interface{}{"a": 1}, "Bearer stale")
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty response body")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (401 then refreshed retry)", got)
	}
}

func TestCallAgentServerSetsEndpointBackoffAfterRepeated401(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	oldRefresher := agentChannelRefreshHeader
	agentChannelRefreshHeader = func() (string, error) { return "", nil }
	t.Cleanup(func() { agentChannelRefreshHeader = oldRefresher })

	svc := &AgentService{client: srv.Client()}
	for i := 0; i < agentChannelAlertThreshold; i++ {
		_, _ = svc.callAgentServerWithAuthorization(http.MethodPost, srv.URL, nil, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(srv.URL, "Bearer stale") {
		t.Fatal("endpoint must be in backoff after repeated 401")
	}
	// 门面的 allow 闸门必须承重：首个 401 落退避后，循环内第 2、3 次已被闸门
	// 挡下（上游只收到 1 次请求），此处再经门面调用必须被 typed 退避错误直接
	// 拒绝，且上游计数不得增加。
	hitsAfterLoop := atomic.LoadInt32(&calls)
	_, err := svc.callAgentServerWithAuthorization(http.MethodGet, srv.URL, nil, "Bearer stale")
	var backoff agentChannelBackoffError
	if !errors.As(err, &backoff) {
		t.Fatalf("err = %v, want agentChannelBackoffError", err)
	}
	if hits := atomic.LoadInt32(&calls); hits != hitsAfterLoop {
		t.Fatalf("upstream calls = %d, want %d（退避期内不得再发请求）", hits, hitsAfterLoop)
	}
}

func TestAgentChannelBackoffBypassedAfterCredentialRotation(t *testing.T) {
	agentChannelHealthInstance.reset()
	oldWriter := agentChannelAlertWriter
	agentChannelAlertWriter = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	const endpoint = "https://api.example.com/rotated"
	for i := 0; i < agentChannelAlertThreshold; i++ {
		agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	}
	if agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatalf("同凭据仍应被退避挡住")
	}
	if agentChannelHealthInstance.allow(endpoint, "") {
		t.Fatal("空凭据不得清掉退避")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer fresh") {
		t.Fatalf("凭据轮转后应放行")
	}
	if !agentChannelHealthInstance.allow(endpoint, "Bearer stale") {
		t.Fatalf("放行后应清掉该端点退避")
	}
}

func TestAgentChannelAlertIsRateLimited(t *testing.T) {
	agentChannelHealthInstance.reset()

	oldWriter := agentChannelAlertWriter
	var alerts int32
	var lastPayload map[string]interface{}
	agentChannelAlertWriter = func(payload map[string]interface{}) error {
		atomic.AddInt32(&alerts, 1)
		lastPayload = payload
		return nil
	}
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	endpoint := "https://api.example.com/x"
	// 阈值 = 3：前两次不得告警。阈值被误改成 1 或 2 时这里就红。
	agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	if got := atomic.LoadInt32(&alerts); got != 0 {
		t.Fatalf("alerts after 2×401 = %d, want 0 (threshold=3)", got)
	}

	// 第 3 次必须告警：阈值被改成 4 或更大时这里红。
	agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	if got := atomic.LoadInt32(&alerts); got != 1 {
		t.Fatalf("alerts after 3×401 = %d, want 1 (threshold=3)", got)
	}

	// 继续打到 20 次仍只有 1 条：30min 限频必须持续生效。
	for i := 0; i < 17; i++ {
		agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	}
	if got := atomic.LoadInt32(&alerts); got != 1 {
		t.Fatalf("alerts after 20×401 = %d, want 1 (30min throttle)", got)
	}

	// payload 必须与协议表登记（models.AgentEventChannelAlert）逐字段一致。
	if lastPayload["type"] != models.AgentEventChannelAlert {
		t.Fatalf("payload type = %v, want %s", lastPayload["type"], models.AgentEventChannelAlert)
	}
	if lastPayload["code"] != "platform_credential_expired" {
		t.Fatalf("payload code = %v", lastPayload["code"])
	}
	if _, ok := lastPayload["consecutive_401"]; !ok {
		t.Fatal("payload missing consecutive_401")
	}
	since, _ := lastPayload["since"].(string)
	if _, err := time.Parse(time.RFC3339, since); err != nil {
		t.Fatalf("payload since = %q, not RFC3339: %v", since, err)
	}
	if msg, _ := lastPayload["message"].(string); msg == "" {
		t.Fatal("payload message 为空")
	}

	// schema 与 payload 逐键对齐：协议表改名而发送侧没跟上时这里红。
	contract := models.DefaultAgentProtocolContract()
	var schema *models.AgentProtocolEvent
	for i := range contract.WebSocket.ClientSends {
		if contract.WebSocket.ClientSends[i].Type == models.AgentEventChannelAlert {
			schema = &contract.WebSocket.ClientSends[i]
			break
		}
	}
	if schema == nil {
		t.Fatalf("%s 未登记进协议表", models.AgentEventChannelAlert)
	}
	want := map[string]bool{}
	for _, k := range schema.Required {
		want[k] = true
	}
	for _, k := range schema.Optional {
		want[k] = true
	}
	if len(lastPayload) != len(want) {
		t.Fatalf("payload 字段数 = %d (%v), 协议表登记 = %d (%v)", len(lastPayload), lastPayload, len(want), want)
	}
	for k := range want {
		if _, ok := lastPayload[k]; !ok {
			t.Fatalf("payload 缺少协议表登记的字段 %q", k)
		}
	}
}

// 限频窗口过后必须能再次告警（re-arm）：否则一次风暴之后，30min 内真的
// 再次过期会被永久静音。
func TestAgentChannelAlertRearmsAfterInterval(t *testing.T) {
	agentChannelHealthInstance.reset()

	oldWriter := agentChannelAlertWriter
	var alerts int32
	agentChannelAlertWriter = func(map[string]interface{}) error {
		atomic.AddInt32(&alerts, 1)
		return nil
	}
	t.Cleanup(func() {
		agentChannelAlertWriter = oldWriter
		agentChannelHealthInstance.reset()
	})

	endpoint := "https://api.example.com/x"
	for i := 0; i < 3; i++ {
		agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	}
	if got := atomic.LoadInt32(&alerts); got != 1 {
		t.Fatalf("first alert = %d, want 1", got)
	}

	// 把限频窗口回拨，模拟 30min 已过。
	agentChannelHealthInstance.mu.Lock()
	agentChannelHealthInstance.lastAlertAt = time.Now().Add(-agentChannelAlertInterval - time.Minute)
	agentChannelHealthInstance.mu.Unlock()

	agentChannelHealthInstance.record401(endpoint, "Bearer stale")
	if got := atomic.LoadInt32(&alerts); got != 2 {
		t.Fatalf("alerts after window = %d, want 2 (re-arm)", got)
	}
}

// TestClassifyRegisterFailureTreatsChannelBackoffAsTransient 钉死本轮的应修点：
// 退避窗口内我们**没发请求**，这是本地限流，不是服务端明确拒绝。被判成非瞬时
// 就会走 Registered=false 分支，把一次未发生的请求记成注销理由（2026-10-08
// 静默离线事故的同型病灶）。
func TestClassifyRegisterFailureTreatsChannelBackoffAsTransient(t *testing.T) {
	err := agentChannelBackoffError{endpoint: "api.example.com/api/devices/register"}
	if !classifyRegisterFailure(err) {
		t.Fatalf("classifyRegisterFailure(%v) = false，退避必须算瞬时故障", err)
	}
	// 包装一层也必须认得出来：调用链上任何地方 wrap 都不该改变语义。
	if !classifyRegisterFailure(fmt.Errorf("register failed: %w", err)) {
		t.Fatal("被 wrap 的退避错误仍必须算瞬时故障")
	}
}

// TestClassifyRegisterFailureKeepsTyped401Fatal 是上一条的反向守卫：
// 退避的早返回不能插到 typed 401 判定前面，否则真凭据拒绝会被当成瞬时，
// 注销与自愈链双双失效。
func TestClassifyRegisterFailureKeepsTyped401Fatal(t *testing.T) {
	if classifyRegisterFailure(agentUserAuthRejectedError{status: http.StatusUnauthorized}) {
		t.Fatal("typed 401 必须仍是非瞬时（退避判定不得抢在它前面）")
	}
}
