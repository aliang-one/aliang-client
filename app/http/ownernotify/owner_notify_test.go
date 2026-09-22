package ownernotify

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/services"
	auth "aliang.one/nursorgate/processor/auth"
)

func resetNotifyServerForTest(t *testing.T) {
	t.Helper()
	origOwner := auth.IsSessionOwnerProcess()
	origOverride := services.SessionOwnerAddrOverride()
	origListen := listenHook
	origServe := serveHook
	notifyMu.Lock()
	origAddr := notifyAddr
	origSrv := notifySrv
	notifyAddr = ""
	notifySrv = nil
	notifyMu.Unlock()
	t.Cleanup(func() {
		notifyMu.Lock()
		notifyAddr = origAddr
		notifySrv = origSrv
		notifyMu.Unlock()
		listenHook = origListen
		serveHook = origServe
		auth.SetSessionOwnerProcess(origOwner)
		services.SetSessionOwnerAddrOverride(origOverride)
	})
}

func TestEnsureServerSkipsNonOwnerProcess(t *testing.T) {
	resetNotifyServerForTest(t)
	auth.SetSessionOwnerProcess(false)

	if addr := EnsureServer(); addr != "" {
		t.Fatalf("EnsureServer() = %q on non-owner process, want empty", addr)
	}
	if Addr() != "" {
		t.Fatalf("Addr() = %q, want empty", Addr())
	}
}

func TestEnsureServerStartsListenerAndSetsOverride(t *testing.T) {
	resetNotifyServerForTest(t)
	auth.SetSessionOwnerProcess(true)

	addr := EnsureServer()
	if addr == "" {
		t.Fatalf("EnsureServer() = empty, want loopback address")
	}
	if got := services.SessionOwnerAddrOverride(); got != addr {
		t.Fatalf("override = %q, want %q", got, addr)
	}
	if again := EnsureServer(); again != addr {
		t.Fatalf("second EnsureServer() = %q, want same addr %q (idempotent)", again, addr)
	}
}

func TestEnsureServerPreservesExistingOverride(t *testing.T) {
	resetNotifyServerForTest(t)
	auth.SetSessionOwnerProcess(true)
	services.SetSessionOwnerAddrOverride("http://127.0.0.1:56431")

	if addr := EnsureServer(); addr == "" {
		t.Fatalf("EnsureServer() = empty, want listener address")
	}
	if got := services.SessionOwnerAddrOverride(); got != "http://127.0.0.1:56431" {
		t.Fatalf("override = %q, want existing override preserved", got)
	}
}

func TestEnsureServerServesAgentAuthRejectedEndpoint(t *testing.T) {
	resetNotifyServerForTest(t)
	auth.SetSessionOwnerProcess(true)
	addr := EnsureServer()
	if addr == "" {
		t.Fatalf("EnsureServer() = empty, want listener address")
	}

	// Generation > 0 且非当前活跃代 → handler 判 stale_generation 丢弃并返回
	// 200（不触发恢复链，测试无副作用）。此断言同时证明端点路由到了真正的
	// HandleAgentAuthRejected。
	payload := map[string]any{
		"reason":      "agent_register_rejected",
		"device_id":   "dev-test",
		"observed_at": time.Now().Unix(),
		"generation":  1 << 40,
	}
	raw, _ := json.Marshal(payload)
	resp, err := http.Post(addr+"/api/auth/agent-auth-rejected", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST agent-auth-rejected: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var decoded struct {
		Code int `json:"code"`
		Data struct {
			Applied bool   `json:"applied"`
			Ignored string `json:"ignored"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response %s: %v", body, err)
	}
	if decoded.Data.Applied || decoded.Data.Ignored != "stale_generation" {
		t.Fatalf("response = %+v, want applied=false ignored=stale_generation", decoded.Data)
	}

	// 错误方法 → 405（方法路由生效）。
	getResp, err := http.Get(addr + "/api/auth/agent-auth-rejected")
	if err != nil {
		t.Fatalf("GET agent-auth-rejected: %v", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", getResp.StatusCode)
	}
}

func TestEnsureServerListenFailureReturnsEmpty(t *testing.T) {
	resetNotifyServerForTest(t)
	auth.SetSessionOwnerProcess(true)
	listenHook = func() (net.Listener, error) {
		return nil, net.InvalidAddrError("stub listen failure")
	}

	if addr := EnsureServer(); addr != "" {
		t.Fatalf("EnsureServer() = %q on listen failure, want empty", addr)
	}
	if got := services.SessionOwnerAddrOverride(); got != "" {
		t.Fatalf("override = %q, want unset after listen failure", got)
	}
}
