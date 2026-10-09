package services

// 注册瞬态错误测试：服务端 5xx/408/429 与传输层故障（超时/连接拒绝）不得
// 推翻内存注册态（2026-10-08 生产事故根因：47 秒链路抖动被 sticky 化成 16
// 小时离线）。致命错误（400 等显式拒绝、类型化 401、409 冲突）保持既有
// Registered=false 语义。

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// setupTransientRegisterTest 隔离状态目录 + 注入 JWT，返回已就绪的 service。
func setupTransientRegisterTest(t *testing.T) *AgentService {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_transient",
		RefreshToken: "refresh_transient",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}
	return NewAgentService()
}

// registerServer 起一个只服务 /api/devices/register 与 inventory 同步的
// httptest server，register 端点按状态码应答。
func registerServer(t *testing.T, registerStatus int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/devices/register":
			if registerStatus != http.StatusOK {
				w.WriteHeader(registerStatus)
				_, _ = w.Write([]byte(`{"code":1,"msg":"upstream down"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{"device_id": "dev_transient"},
			})
		case "/api/agent/status":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
}

// registerOnce 成功注册一次，建立"已注册"前置态（直调内层函数，不经
// Enable/SyncNow，避免拉起 WS 连接 goroutine）。
func registerOnce(t *testing.T, service *AgentService, serverURL string) {
	t.Helper()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: serverURL}})
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.registerAndSyncLockedWithUserContext("", ""); err != nil {
		t.Fatalf("seed register failed: %v", err)
	}
	if !service.state.Registered || !service.state.Enabled {
		t.Fatalf("seed register did not establish registered state: registered=%t enabled=%t",
			service.state.Registered, service.state.Enabled)
	}
}

func TestRegisterTransientServer5xxKeepsRegistration(t *testing.T) {
	service := setupTransientRegisterTest(t)
	okServer := registerServer(t, http.StatusOK)
	defer okServer.Close()
	registerOnce(t, service, okServer.URL)

	badServer := registerServer(t, http.StatusBadGateway)
	defer badServer.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: badServer.URL}})

	if err := service.SyncNow(); err == nil {
		t.Fatal("SyncNow() error = nil, want transient register failure")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.state.Registered {
		t.Fatal("Registered was flipped to false by a transient 502 — must keep registration")
	}
	if !service.state.Enabled {
		t.Fatal("Enabled was flipped to false by a transient 502 — must keep enablement")
	}
	if service.state.LastSyncStatus != "server_unavailable" {
		t.Fatalf("LastSyncStatus = %q, want server_unavailable", service.state.LastSyncStatus)
	}
}

func TestRegisterTransportFailureKeepsRegistration(t *testing.T) {
	service := setupTransientRegisterTest(t)
	okServer := registerServer(t, http.StatusOK)
	defer okServer.Close()
	registerOnce(t, service, okServer.URL)

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // 立即关闭 → connection refused（传输层瞬态）

	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: deadURL}})
	if err := service.SyncNow(); err == nil {
		t.Fatal("SyncNow() error = nil, want transport failure")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if !service.state.Registered {
		t.Fatal("Registered was flipped to false by a transport failure — must keep registration")
	}
	if !service.state.Enabled {
		t.Fatal("Enabled was flipped to false by a transport failure — must keep enablement")
	}
}

func TestRegister408And429AreTransient(t *testing.T) {
	for _, status := range []int{http.StatusRequestTimeout, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			service := setupTransientRegisterTest(t)
			okServer := registerServer(t, http.StatusOK)
			defer okServer.Close()
			registerOnce(t, service, okServer.URL)

			badServer := registerServer(t, status)
			defer badServer.Close()
			config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: badServer.URL}})

			if err := service.SyncNow(); err == nil {
				t.Fatalf("SyncNow() error = nil, want failure for status %d", status)
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if !service.state.Registered {
				t.Fatalf("Registered flipped to false by transient status %d", status)
			}
		})
	}
}

// 负向控制：显式 4xx 拒绝仍按致命语义清注册态（既有行为，不得被本修复改动）。
func TestRegisterPermanent4xxStillClearsRegistration(t *testing.T) {
	service := setupTransientRegisterTest(t)
	okServer := registerServer(t, http.StatusOK)
	defer okServer.Close()
	registerOnce(t, service, okServer.URL)

	badServer := registerServer(t, http.StatusBadRequest)
	defer badServer.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: badServer.URL}})

	if err := service.SyncNow(); err == nil {
		t.Fatal("SyncNow() error = nil, want failure for status 400")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.state.Registered {
		t.Fatal("Registered kept after a permanent 400 — legacy semantics must clear it")
	}
}

// 负向控制：类型化 401 仍按致命语义清注册态。直调内层函数（401 的会话自愈
// 在调用方，见 enableWithUserContext/SyncNowWithUserContext），避免测试触碰
// 真实刷新链路。
func TestRegisterAuth401StillClearsRegistration(t *testing.T) {
	service := setupTransientRegisterTest(t)
	okServer := registerServer(t, http.StatusOK)
	defer okServer.Close()
	registerOnce(t, service, okServer.URL)

	unauth := registerServer(t, http.StatusUnauthorized)
	defer unauth.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: unauth.URL}})

	service.mu.Lock()
	err := service.registerAndSyncLockedWithUserContext("", "")
	registered := service.state.Registered
	service.mu.Unlock()
	if err == nil {
		t.Fatal("register error = nil, want 401 rejection")
	}
	var rejected agentUserAuthRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("error = %v, want agentUserAuthRejectedError", err)
	}
	if registered {
		t.Fatal("Registered kept after typed 401 — legacy semantics must clear it")
	}
}

// statusLocked 必须暴露 raw enabled 标志：派生 Enabled 复合了 Registered/Device，
// 事故态（Registered=false）下恒为 false，watchdog 巡检无法区分"用户开着的
// 设备掉注册"与"显式禁用"。
func TestAgentStatusExposesRawDeviceEnabled(t *testing.T) {
	service := setupTransientRegisterTest(t)

	service.mu.Lock()
	service.state.Enabled = true
	service.state.Registered = false
	service.state.Device = nil
	status := service.statusLocked()
	service.mu.Unlock()
	if !status.DeviceEnabled {
		t.Fatal("DeviceEnabled must expose the raw enabled flag even while registration is lost")
	}
	if status.Enabled {
		t.Fatal("derived Enabled must stay false while unregistered — existing semantics must not change")
	}

	service.mu.Lock()
	service.state.Enabled = false
	status = service.statusLocked()
	service.mu.Unlock()
	if status.DeviceEnabled {
		t.Fatal("DeviceEnabled must be false when the raw enabled flag is false")
	}
}

// 跨平台：Windows 的连接拒绝错误文本是 "actively refused it"（无
// "connection refused" 字样），字符串嗅探漏判会把瞬态当致命。分类器必须
// 走 syscall.Errno 结构化判定（ECONNREFUSED 在各家 OS 的 syscall 包里同名）。
func TestRegisterTransportSyscallRefusedIsTransient(t *testing.T) {
	service := setupTransientRegisterTest(t)
	okServer := registerServer(t, http.StatusOK)
	defer okServer.Close()
	registerOnce(t, service, okServer.URL)

	opErr := &net.OpError{
		Op:  "dial",
		Net: "tcp",
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED),
	}
	if !classifyRegisterFailure(opErr) {
		t.Fatal("syscall ECONNREFUSED must classify as transient on every platform")
	}
}
