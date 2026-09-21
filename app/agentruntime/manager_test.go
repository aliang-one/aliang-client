package agentruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/processor/config"
)

func TestWaitForCurrentAgentAPIRetriesUntilProtocolIsReady(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocolPath {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&attempts, 1) < 3 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": models.DefaultAgentProtocolContract(),
		})
	}))
	defer server.Close()

	originalAddr := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(func() {
		config.DefaultUserAgentAddr = originalAddr
	})

	gotAttempts, err := waitForCurrentAgentAPI(time.Second, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForCurrentAgentAPI() error = %v", err)
	}
	if gotAttempts != 3 {
		t.Fatalf("waitForCurrentAgentAPI() attempts = %d, want 3", gotAttempts)
	}
}

func TestNeedsAuthenticatedSyncOnlyForRecoverableDisabledStates(t *testing.T) {
	status := models.AgentStatusResponse{SyncStatus: "logout"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != capabilityPath {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": status,
		})
	}))
	defer server.Close()

	originalAddr := config.DefaultUserAgentAddr
	config.DefaultUserAgentAddr = strings.TrimPrefix(server.URL, "http://")
	t.Cleanup(func() { config.DefaultUserAgentAddr = originalAddr })

	if !NeedsAuthenticatedSync(time.Second) {
		t.Fatal("logout state should request authenticated reconciliation")
	}
	status.SyncStatus = "auth_expired"
	if !NeedsAuthenticatedSync(time.Second) {
		t.Fatal("auth_expired state should request authenticated reconciliation")
	}
	status.SyncStatus = "disabled"
	if NeedsAuthenticatedSync(time.Second) {
		t.Fatal("manual disable must not request authenticated reconciliation")
	}
	status.SyncStatus = "logout"
	status.Enabled = true
	if NeedsAuthenticatedSync(time.Second) {
		t.Fatal("enabled Agent must not request authenticated reconciliation")
	}
}

func TestUserAgentEnvInjectsRuntimeAndOwnerAddr(t *testing.T) {
	// 钉死部署级覆盖 env：本测试断言默认注入值（对齐 notify 测试手法）。
	t.Setenv("ALIANG_MANAGEMENT_ADDR", "")

	env := userAgentEnv([]string{
		"ALIANG_DATA_DIR=/should/be/blocked",
		"PATH=/usr/bin:/bin",
	})

	has := func(key string) (string, bool) {
		for _, item := range env {
			if strings.HasPrefix(item, key+"=") {
				return strings.TrimPrefix(item, key+"="), true
			}
		}
		return "", false
	}
	if _, leaked := has("ALIANG_DATA_DIR"); leaked {
		t.Fatal("ALIANG_DATA_DIR must stay blocked in the agent environment")
	}
	if v, ok := has(services.AgentRuntimeEnv); !ok || v != "1" {
		t.Fatalf("%s = %q (present=%t), want \"1\"", services.AgentRuntimeEnv, v, ok)
	}
	// 默认注入 owner dashboard 基地址，agent 凭据被拒时沿此地址上报 owner。
	if v, ok := has(services.SessionOwnerAddrEnv); !ok || v != "http://"+config.DefaultManagementAddr {
		t.Fatalf("%s = %q (present=%t), want %q", services.SessionOwnerAddrEnv, v, ok, "http://"+config.DefaultManagementAddr)
	}
}

func TestOwnerBaseURLHonorsManagementAddrOverride(t *testing.T) {
	// 钉死部署级覆盖 env：先断言默认值再显式覆盖（对齐 notify 测试手法）。
	t.Setenv("ALIANG_MANAGEMENT_ADDR", "")
	if got := ownerBaseURL(); got != "http://"+config.DefaultManagementAddr {
		t.Fatalf("ownerBaseURL() = %q, want default %q", got, "http://"+config.DefaultManagementAddr)
	}
	t.Setenv("ALIANG_MANAGEMENT_ADDR", "127.0.0.1:60000")
	if got := ownerBaseURL(); got != "http://127.0.0.1:60000" {
		t.Fatalf("ownerBaseURL() with override = %q, want http://127.0.0.1:60000", got)
	}
}

func TestOwnerBaseURLPrefersSessionOwnerAddrOverride(t *testing.T) {
	// 显式 override（server 端口回退时回灌）优先于部署级 env 与默认监听地址。
	services.SetSessionOwnerAddrOverride("http://127.0.0.1:49152")
	t.Cleanup(func() { services.SetSessionOwnerAddrOverride("") })

	if got := ownerBaseURL(); got != "http://127.0.0.1:49152" {
		t.Fatalf("ownerBaseURL() = %q, want override http://127.0.0.1:49152", got)
	}
	// env 存在时 override 仍胜出。
	t.Setenv("ALIANG_MANAGEMENT_ADDR", "127.0.0.1:60000")
	if got := ownerBaseURL(); got != "http://127.0.0.1:49152" {
		t.Fatalf("ownerBaseURL() with env = %q, want override http://127.0.0.1:49152", got)
	}
	// 清除 override 后回退到 env。
	services.SetSessionOwnerAddrOverride("")
	if got := ownerBaseURL(); got != "http://127.0.0.1:60000" {
		t.Fatalf("ownerBaseURL() after clear = %q, want env http://127.0.0.1:60000", got)
	}
}
