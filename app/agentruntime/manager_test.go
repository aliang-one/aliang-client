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
	"aliang.one/nursorgate/app/http/ownernotify"
	"aliang.one/nursorgate/app/http/services"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// withOwnerNotifyDisabled 把测试进程置为"非 session owner"并清空 notify
// 监听与 override：userAgentEnv 现在会在 owner 进程里启动自有 notify 微服务
// 并回灌 override（ownernotify.EnsureServer），而 ownerBaseURL 优先级断言需
// 要无 override 的基线。仅测试使用。
func withOwnerNotifyDisabled(t *testing.T) {
	t.Helper()
	ownernotify.ResetForTest()
	origOverride := services.SessionOwnerAddrOverride()
	services.SetSessionOwnerAddrOverride("")
	origOwner := auth.IsSessionOwnerProcess()
	auth.SetSessionOwnerProcess(false)
	t.Cleanup(func() {
		auth.SetSessionOwnerProcess(origOwner)
		services.SetSessionOwnerAddrOverride(origOverride)
		ownernotify.ResetForTest()
	})
}

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
	withOwnerNotifyDisabled(t)
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
	withOwnerNotifyDisabled(t)
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

func TestUserAgentEnvOwnerProcessInjectsNotifyServerAddr(t *testing.T) {
	// owner 进程下，userAgentEnv 应启动自有 notify 微服务并把 agent 的上报
	// 地址指向它（而非可能被其他进程占用的默认 dashboard 端口）。
	ownernotify.ResetForTest()
	origOwner := auth.IsSessionOwnerProcess()
	origOverride := services.SessionOwnerAddrOverride()
	auth.SetSessionOwnerProcess(true)
	services.SetSessionOwnerAddrOverride("")
	t.Cleanup(func() {
		auth.SetSessionOwnerProcess(origOwner)
		services.SetSessionOwnerAddrOverride(origOverride)
		ownernotify.ResetForTest()
	})
	t.Setenv("ALIANG_MANAGEMENT_ADDR", "")

	env := userAgentEnv([]string{"PATH=/usr/bin"})
	var ownerAddr string
	for _, item := range env {
		if strings.HasPrefix(item, services.SessionOwnerAddrEnv+"=") {
			ownerAddr = strings.TrimPrefix(item, services.SessionOwnerAddrEnv+"=")
		}
	}
	if ownerAddr == "" {
		t.Fatalf("%s missing in agent env", services.SessionOwnerAddrEnv)
	}
	if ownerAddr != ownernotify.Addr() {
		t.Fatalf("%s = %q, want notify server addr %q", services.SessionOwnerAddrEnv, ownerAddr, ownernotify.Addr())
	}
	if ownerAddr == "http://"+config.DefaultManagementAddr {
		t.Fatalf("owner addr = default dashboard %q, want ephemeral notify server", ownerAddr)
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
