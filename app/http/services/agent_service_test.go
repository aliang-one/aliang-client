package services

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

func TestAgentServiceEnableRequiresLogin(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	service := NewAgentService()
	status, err := service.Enable()
	if err == nil {
		t.Fatal("Enable() error = nil, want login required")
	}
	if status.Enabled || status.Bound || status.Registered {
		t.Fatalf("Enable() status should remain disabled without login: %#v", status)
	}
}

func TestAgentServiceRegisterAndSyncUsesAdminConsoleFallbackForLoopbackWithoutLogin(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	registerCalled := false
	statusCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/devices/register":
			registerCalled = true
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("Authorization = %q, want empty for admin console fallback", got)
			}
			if got := r.Header.Get("X-Admin-Console"); got != "1" {
				t.Errorf("X-Admin-Console = %q, want 1", got)
			}
			if got := r.Header.Get(AgentUserKeyHeader); !strings.HasPrefix(got, "admin-console:") {
				t.Errorf("%s = %q, want admin-console prefix", AgentUserKeyHeader, got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"device_token": "dt_admin_console",
					"device": map[string]interface{}{
						"id":                      "dev_admin_console",
						"device_id":               "dev_admin_console",
						"name":                    "admin-console-device",
						"platform":                agentPlatform(),
						"status":                  "offline",
						"remote_terminal_enabled": true,
						"ai_control_enabled":      true,
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/status":
			statusCalled = true
			if got := r.Header.Get("Authorization"); got != "Bearer dt_admin_console" {
				t.Errorf("status Authorization = %q, want device token", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"status":             "ok",
					"project_count":      0,
					"vibe_session_count": 0,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	err := service.registerAndSyncLockedWithUserContext("", "")
	status := service.statusLocked()
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLockedWithUserContext() error = %v", err)
	}
	if !registerCalled {
		t.Fatal("register endpoint was not called")
	}
	if !statusCalled {
		t.Fatal("status endpoint was not called")
	}
	if !status.Enabled || !status.Registered || !status.Bound {
		t.Fatalf("status did not reflect admin console fallback registration: %#v", status)
	}
	if status.Device == nil || status.Device.DeviceID != "dev_admin_console" {
		t.Fatalf("device = %#v, want dev_admin_console", status.Device)
	}
	service.Disable()
}

func TestAgentServiceEnableRegistersLoggedInDevice(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_enable",
		RefreshToken: "refresh_enable",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	var registerPayload map[string]interface{}
	registerCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		registerCalled = true
		if got := r.Header.Get("Authorization"); got != "Bearer access_enable" {
			t.Errorf("Authorization = %q, want Bearer access_enable", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&registerPayload); err != nil {
			t.Errorf("decode register payload: %v", err)
		}
		if got := r.Header.Get("X-Agent-Device-Token"); got != "" {
			t.Errorf("X-Agent-Device-Token = %q, want empty during user-auth registration", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_token": "dt_enable",
				"device_id":    "dev_enable",
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	status, err := service.Enable()
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	if !registerCalled {
		t.Fatal("register endpoint was not called")
	}
	if strings.TrimSpace(remoteString(registerPayload, "device_id")) == "" || strings.TrimSpace(remoteString(registerPayload, "unique_code")) == "" {
		t.Fatalf("register payload missing stable identifiers: %#v", registerPayload)
	}
	if len(registerPayload) != 2 {
		t.Fatalf("register payload should only contain device_id and unique_code, got %#v", registerPayload)
	}
	if !status.Enabled || !status.Registered || !status.Bound {
		t.Fatalf("Enable() did not reflect registered device: %#v", status)
	}
	if status.Device == nil || status.Device.DeviceID != "dev_enable" {
		t.Fatalf("Enable() device = %#v, want dev_enable", status.Device)
	}
	service.Disable()
}

func TestAgentServiceRegisterAndSyncUsesLoggedInSession(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_test",
		RefreshToken: "refresh_test",
		TokenType:    "Bearer",
		Username:     "agent-user",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	var registerPayload map[string]interface{}
	registerCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		registerCalled = true
		if got := r.Header.Get("Authorization"); got != "Bearer access_test" {
			t.Errorf("Authorization = %q, want Bearer access_test", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&registerPayload); err != nil {
			t.Errorf("decode register payload: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"msg":  "success",
			"data": map[string]interface{}{
				"device_token": "dt_auto_register",
				"device": map[string]interface{}{
					"id":                      "dev_backend",
					"device_id":               "dev_backend",
					"name":                    "backend-device",
					"platform":                agentPlatform(),
					"status":                  "offline",
					"remote_terminal_enabled": true,
					"ai_control_enabled":      true,
					"bound_at":                "2026-06-10T00:00:00Z",
				},
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	originalDeviceID := service.state.DeviceID
	originalUniqueCode := service.state.UniqueCode
	err := service.registerAndSyncLocked()
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}

	if !registerCalled {
		t.Fatal("register endpoint was not called")
	}
	if remoteString(registerPayload, "device_id") != originalDeviceID {
		t.Fatalf("register payload device_id = %q, want %q", remoteString(registerPayload, "device_id"), originalDeviceID)
	}
	if remoteString(registerPayload, "unique_code") != originalUniqueCode {
		t.Fatalf("register payload unique_code = %q, want %q", remoteString(registerPayload, "unique_code"), originalUniqueCode)
	}
	if len(registerPayload) != 2 {
		t.Fatalf("register payload should only contain device_id and unique_code, got %#v", registerPayload)
	}

	status := service.Status()
	if !status.Enabled || !status.Registered || !status.Bound {
		t.Fatalf("Status() did not reflect registered agent: %#v", status)
	}
	if status.Device == nil || status.Device.DeviceID != "dev_backend" {
		t.Fatalf("Status() device = %#v, want backend device", status.Device)
	}
	if status.SyncStatus != "connecting" {
		t.Fatalf("SyncStatus = %q, want connecting", status.SyncStatus)
	}
	service.Disable()
}

func TestAgentServiceRegisterAndSyncUploadsInventoryWithDeviceToken(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	projectPath := setupAgentExecutionProjectForTest(t)
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_inventory",
		RefreshToken: "refresh_inventory",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	var registerPayload map[string]interface{}
	var statusPayload map[string]interface{}
	statusCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/devices/register":
			if got := r.Header.Get("Authorization"); got != "Bearer access_inventory" {
				t.Errorf("register Authorization = %q, want user auth", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&registerPayload); err != nil {
				t.Errorf("decode register payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"device_token": "dt_inventory",
					"device": map[string]interface{}{
						"id":                      "dev_inventory",
						"device_id":               "dev_inventory",
						"name":                    "inventory-device",
						"platform":                agentPlatform(),
						"status":                  "offline",
						"remote_terminal_enabled": true,
						"ai_control_enabled":      true,
						"bound_at":                "2026-06-10T00:00:00Z",
					},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/status":
			statusCalled = true
			if got := r.Header.Get("Authorization"); got != "Bearer dt_inventory" {
				t.Errorf("status Authorization = %q, want device token", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&statusPayload); err != nil {
				t.Errorf("decode status payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"device": map[string]interface{}{
					"id":                      "dev_inventory",
					"device_id":               "dev_inventory",
					"name":                    "inventory-device",
					"platform":                agentPlatform(),
					"status":                  "online",
					"remote_terminal_enabled": true,
					"ai_control_enabled":      true,
					"bound_at":                "2026-06-10T00:00:00Z",
				},
				"project_count":      1,
				"vibe_session_count": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	err := service.registerAndSyncLocked()
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}
	if len(registerPayload) != 2 {
		t.Fatalf("register payload should only contain device_id and unique_code, got %#v", registerPayload)
	}
	if !statusCalled {
		t.Fatal("status sync endpoint was not called after registration")
	}
	if remoteString(statusPayload, "device_id") != "dev_inventory" {
		t.Fatalf("status payload device_id = %q, want dev_inventory", remoteString(statusPayload, "device_id"))
	}
	projects, ok := statusPayload["projects"].([]interface{})
	if !ok || len(projects) == 0 {
		t.Fatalf("status projects = %#v, want discovered projects", statusPayload["projects"])
	}
	sessions, ok := statusPayload["vibe_sessions"].([]interface{})
	if !ok || len(sessions) == 0 {
		t.Fatalf("status vibe_sessions = %#v, want discovered sessions", statusPayload["vibe_sessions"])
	}
	dirs, ok := statusPayload["authorized_directories"].([]interface{})
	if !ok || len(dirs) == 0 || strings.TrimSpace(remoteString(map[string]interface{}{"path": dirs[0]}, "path")) != projectPath {
		t.Fatalf("status authorized_directories = %#v, want %s", statusPayload["authorized_directories"], projectPath)
	}
	service.Disable()
}

func TestAgentServiceRegisterAndSyncReusesExistingDeviceToken(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_existing",
		RefreshToken: "refresh_existing",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	statusCalled := false
	var statusPayload map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/devices/register":
			t.Fatalf("register endpoint should not be called when device_token already exists")
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/status":
			statusCalled = true
			if got := r.Header.Get("Authorization"); got != "Bearer dt_existing" {
				t.Errorf("status Authorization = %q, want existing device token", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&statusPayload); err != nil {
				t.Errorf("decode status payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": "ok",
				"device": map[string]interface{}{
					"id":                      "dev_existing",
					"device_id":               "dev_existing",
					"name":                    "existing-device",
					"platform":                agentPlatform(),
					"status":                  "online",
					"remote_terminal_enabled": true,
					"ai_control_enabled":      true,
					"bound_at":                "2026-06-10T00:00:00Z",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.state.DeviceID = "dev_existing"
	service.state.UniqueCode = computeAgentUniqueCode("dev_existing")
	service.state.DeviceToken = "dt_existing"
	err := service.registerAndSyncLocked()
	deviceToken := service.state.DeviceToken
	enabled := service.state.Enabled
	registered := service.state.Registered
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}
	if !statusCalled {
		t.Fatal("status sync endpoint was not called for existing device token")
	}
	if remoteString(statusPayload, "device_id") != "dev_existing" {
		t.Fatalf("status payload device_id = %q, want dev_existing", remoteString(statusPayload, "device_id"))
	}
	if deviceToken != "dt_existing" {
		t.Fatalf("device token = %q, want dt_existing", deviceToken)
	}
	if !enabled || !registered {
		t.Fatalf("state enabled=%t registered=%t, want true/true", enabled, registered)
	}
}

func TestAgentServiceRegisterAndSyncReregistersWhenUserChanges(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	registerCalled := false
	statusCalledWithOldToken := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/devices/register":
			registerCalled = true
			if got := r.Header.Get("Authorization"); got != "Bearer user_two" {
				t.Errorf("register Authorization = %q, want Bearer user_two", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"data": map[string]interface{}{
					"device_token": "dt_user_two",
					"device_id":    "dev_existing",
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/agent/status":
			if got := r.Header.Get("Authorization"); got == "Bearer dt_user_one" {
				statusCalledWithOldToken = true
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.state.DeviceID = "dev_existing"
	service.state.UniqueCode = computeAgentUniqueCode("dev_existing")
	service.state.DeviceToken = "dt_user_one"
	service.state.RegisteredUser = "user:user_one"
	err := service.registerAndSyncLockedWithUserContext("Bearer user_two", "user:user_two")
	deviceToken := service.state.DeviceToken
	registeredUser := service.state.RegisteredUser
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLockedWithUserContext() error = %v", err)
	}
	if !registerCalled {
		t.Fatal("register endpoint was not called after user changed")
	}
	if statusCalledWithOldToken {
		t.Fatal("old device token was used after user changed")
	}
	if deviceToken != "dt_user_two" {
		t.Fatalf("device token = %q, want dt_user_two", deviceToken)
	}
	if registeredUser != "user:user_two" {
		t.Fatalf("registered user = %q, want user:user_two", registeredUser)
	}
}

func TestAgentServiceRegisterAndSyncRetriesWithNewDeviceIDOnAlreadyBound(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_conflict",
		RefreshToken: "refresh_conflict",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	registerPayloads := make([]map[string]interface{}, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode register payload: %v", err)
		}
		registerPayloads = append(registerPayloads, payload)
		if len(registerPayloads) == 1 {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "device_id_already_bound"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_token": "dt_conflict_retry",
				"device_id":    remoteString(payload, "device_id"),
				"unique_code":  remoteString(payload, "unique_code"),
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.state.DeviceID = "dev_conflict"
	service.state.UniqueCode = computeAgentUniqueCode("dev_conflict")
	err := service.registerAndSyncLocked()
	deviceID := service.state.DeviceID
	deviceToken := service.state.DeviceToken
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}
	if len(registerPayloads) != 2 {
		t.Fatalf("register call count = %d, want 2", len(registerPayloads))
	}
	if remoteString(registerPayloads[0], "device_id") != "dev_conflict" {
		t.Fatalf("first register device_id = %q, want dev_conflict", remoteString(registerPayloads[0], "device_id"))
	}
	if got := remoteString(registerPayloads[1], "device_id"); got == "" || got == "dev_conflict" {
		t.Fatalf("retry register device_id = %q, want a new device id", got)
	}
	if deviceID == "dev_conflict" {
		t.Fatalf("state device_id was not rotated")
	}
	if deviceToken != "dt_conflict_retry" {
		t.Fatalf("device token = %q, want dt_conflict_retry", deviceToken)
	}
}

func TestAgentServiceRegisterAndSyncAcceptsAliangPhoneServerResponse(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_phone_server",
		RefreshToken: "refresh_phone_server",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	var registerPayload map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access_phone_server" {
			t.Errorf("Authorization = %q, want Bearer access_phone_server", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&registerPayload); err != nil {
			t.Errorf("decode register payload: %v", err)
		}
		now := time.Now().UTC().Format(time.RFC3339)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"device": map[string]interface{}{
				"id":                      "dev_phone_server",
				"device_id":               "dev_phone_server",
				"user_id":                 "user_123",
				"user":                    map[string]interface{}{"id": "user_123", "email": "user@example.local", "name": "User 123", "role": "user"},
				"name":                    "phone-server-device",
				"platform":                agentPlatform(),
				"unique_code":             remoteString(registerPayload, "unique_code"),
				"agent_version":           agentVersion(),
				"status":                  "offline",
				"capabilities":            agentCapabilities(),
				"tools":                   []interface{}{},
				"history":                 []interface{}{},
				"remote_terminal_enabled": true,
				"ai_control_enabled":      true,
				"created_at":              now,
				"paired_at":               now,
				"bound_at":                now,
			},
			"device_id":    "dev_phone_server",
			"user":         map[string]interface{}{"id": "user_123", "email": "user@example.local", "name": "User 123", "role": "user"},
			"device_token": "dt_phone_server",
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	err := service.registerAndSyncLocked()
	deviceToken := service.state.DeviceToken
	service.mu.Unlock()
	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}

	if strings.TrimSpace(remoteString(registerPayload, "device_id")) == "" || strings.TrimSpace(remoteString(registerPayload, "unique_code")) == "" {
		t.Fatalf("register payload missing stable identifiers: %#v", registerPayload)
	}
	if len(registerPayload) != 2 {
		t.Fatalf("register payload should only contain device_id and unique_code, got %#v", registerPayload)
	}
	if deviceToken != "dt_phone_server" {
		t.Fatalf("device token = %q, want dt_phone_server", deviceToken)
	}

	status := service.Status()
	if !status.Enabled || !status.Registered || !status.Bound {
		t.Fatalf("Status() did not reflect registered phone server device: %#v", status)
	}
	if status.Device == nil {
		t.Fatal("Status() missing registered device")
	}
	if status.Device.DeviceID != "dev_phone_server" {
		t.Fatalf("DeviceID = %q, want dev_phone_server", status.Device.DeviceID)
	}
	if status.Device.UserID != "user_123" {
		t.Fatalf("UserID = %q, want user_123", status.Device.UserID)
	}
	if status.Device.User == nil || status.Device.User.Email != "user@example.local" {
		t.Fatalf("User = %#v, want phone server user identity", status.Device.User)
	}
	service.Disable()
}

func TestAgentServiceRegisterUsesForwardedUserAuthorization(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer forwarded_user" {
			t.Errorf("Authorization = %q, want forwarded user auth", got)
		}
		if got := r.Header.Get("X-Agent-Device-Token"); got != "" {
			t.Errorf("X-Agent-Device-Token = %q, want empty", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_token": "dt_forwarded",
				"device_id":    "dev_forwarded",
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	status, err := service.EnableWithAuthorization("Bearer forwarded_user")
	if err != nil {
		t.Fatalf("EnableWithAuthorization() error = %v", err)
	}
	if !status.Enabled || status.Device == nil || status.Device.DeviceID != "dev_forwarded" {
		t.Fatalf("status did not reflect forwarded auth registration: %#v", status)
	}
	service.Disable()
}

func TestAgentServiceRegisterDoesNotTreatUserAccessTokenAsDeviceToken(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_user_only",
		RefreshToken: "refresh_user_only",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"access_token": "user_access_token_is_not_device_token",
				"device_id":    "dev_user_token_only",
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	_, err := service.Enable()
	if err == nil || !strings.Contains(err.Error(), "missing device_token") {
		t.Fatalf("Enable() error = %v, want missing device_token", err)
	}
}

func TestRequestUserAgentSyncAfterAuthRequestsLocalUserAgentWithUserAuthorization(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	originalLocalUserAgentBaseURL := localUserAgentBaseURL
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
		sharedAgentServiceMu.Lock()
		sharedAgentService = nil
		sharedAgentServiceMu.Unlock()
		localUserAgentBaseURL = originalLocalUserAgentBaseURL
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_direct",
		RefreshToken: "refresh_direct",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	syncCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/sync" {
			http.NotFound(w, r)
			return
		}
		syncCalled = true
		if got := r.Header.Get(AgentForwardedAuthorizationHeader); got != "Bearer access_direct" {
			t.Errorf("%s = %q, want Bearer access_direct", AgentForwardedAuthorizationHeader, got)
		}
		if got := r.Header.Get("X-Agent-Device-Token"); got != "" {
			t.Errorf("X-Agent-Device-Token = %q, want empty", got)
		}
		if got := r.URL.Query().Get("reason"); got != "test_local_user_agent" {
			t.Errorf("reason = %q, want test_local_user_agent", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{"status": "ok"},
		})
	}))
	defer server.Close()
	localUserAgentBaseURL = func() string { return server.URL }

	if err := RequestUserAgentSyncAfterAuth("test_local_user_agent"); err != nil {
		t.Fatalf("RequestUserAgentSyncAfterAuth() error = %v", err)
	}
	if !syncCalled {
		t.Fatal("local user-agent sync endpoint was not called")
	}
}

func TestAgentServiceAppliesRemoteDeviceSettings(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	deviceID := service.state.DeviceID
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_test"
	service.state.Device = &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "before",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type": "device.settings.updated",
		"device": map[string]interface{}{
			"id":                      "dev_remote",
			"device_id":               "dev_remote",
			"name":                    "remote-name",
			"status":                  "online",
			"bound_at":                "2026-06-10T00:00:00Z",
			"capabilities":            []interface{}{"terminal", "ai_chat"},
			"remote_terminal_enabled": false,
			"ai_control_enabled":      false,
		},
	}, func(interface{}) error { return nil })

	status := service.Status()
	if status.Device == nil {
		t.Fatal("Status() missing device after remote settings update")
	}
	if status.Device.DeviceID != "dev_remote" || status.Device.Name != "remote-name" {
		t.Fatalf("device settings not applied: %#v", status.Device)
	}
	if status.Device.BoundAt != "2026-06-10T00:00:00Z" {
		t.Fatalf("BoundAt = %q, want cloud bound_at", status.Device.BoundAt)
	}
	if status.Device.RemoteTerminalEnabled || status.Device.AIControlEnabled {
		t.Fatalf("remote feature toggles not applied: %#v", status.Device)
	}
	if got := strings.Join(status.Device.Capabilities, ","); got != "terminal,ai_chat" {
		t.Fatalf("capabilities = %q, want terminal,ai_chat", got)
	}
	if status.SyncStatus != "settings_updated" {
		t.Fatalf("SyncStatus = %q, want settings_updated", status.SyncStatus)
	}
}

func TestAgentServiceRejectsRemoteCommandsWhenDeviceFeaturesDisabled(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	deviceID := service.state.DeviceID
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_test"
	service.state.Device = &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "disabled-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: false,
		AIControlEnabled:      false,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_disabled",
		"cwd":        os.TempDir(),
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "terminal.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_disabled" && strings.Contains(remoteString(event, "error"), "disabled")
	})

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_disabled",
		"project_path": os.TempDir(),
		"mode":         "agent",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_disabled" && strings.Contains(remoteString(event, "error"), "disabled")
	})
}

func TestAgentServiceLaunchRejectsWhenRemoteTerminalDisabled(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	deviceID := service.state.DeviceID
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_test"
	service.state.Device = &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "disabled-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: false,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()

	_, err := service.Launch(models.AgentLaunchRequest{CommandLine: "go version"})
	if err == nil || !strings.Contains(err.Error(), "remote terminal is disabled") {
		t.Fatalf("Launch() error = %v, want remote terminal disabled", err)
	}
}

func TestAgentDeviceFeatureFlagsSerializeFalse(t *testing.T) {
	raw, err := json.Marshal(models.AgentDevice{
		ID:                    "dev_test",
		Name:                  "test",
		Platform:              agentPlatform(),
		RemoteTerminalEnabled: false,
		AIControlEnabled:      false,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("marshal device: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, `"remote_terminal_enabled":false`) {
		t.Fatalf("remote_terminal_enabled=false missing from JSON: %s", text)
	}
	if !strings.Contains(text, `"ai_control_enabled":false`) {
		t.Fatalf("ai_control_enabled=false missing from JSON: %s", text)
	}
}

func TestAgentDeviceFeatureFlagsDefaultEnabledForLegacyState(t *testing.T) {
	raw := []byte(`{"device":{"id":"dev_legacy","name":"legacy","platform":"darwin-arm64","bound_at":"2026-06-10T00:00:00Z"}}`)
	var state agentState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatalf("unmarshal legacy state: %v", err)
	}

	applyAgentDeviceFeatureDefaults(raw, &state)

	if state.Device == nil {
		t.Fatal("device missing after unmarshal")
	}
	if !state.Device.RemoteTerminalEnabled {
		t.Fatal("legacy remote terminal flag should default to enabled")
	}
	if !state.Device.AIControlEnabled {
		t.Fatal("legacy AI control flag should default to enabled")
	}
}

func TestCurrentAgentWebSocketURLUsesDevServerPort(t *testing.T) {
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: "http://localhost:5174"}})

	got, err := currentAgentWebSocketURL("dt_test")
	if err != nil {
		t.Fatalf("currentAgentWebSocketURL() error = %v", err)
	}
	if !strings.HasPrefix(got, "ws://localhost:4000/ws/agent?") {
		t.Fatalf("currentAgentWebSocketURL() = %q, want localhost:4000 ws URL", got)
	}
	if !strings.Contains(got, "token=dt_test") {
		t.Fatalf("currentAgentWebSocketURL() = %q, want token query", got)
	}
}

func TestAgentTerminalManagerShellSession(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentTerminalManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_test",
		"cwd":        projectPath,
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "terminal.created", func(event map[string]interface{}) bool {
		if event["session_id"] != "term_test" {
			return false
		}
		if agentNativePTYSupported() && event["pty"] != true {
			t.Fatalf("terminal.created pty = %#v, want true", event["pty"])
		}
		return true
	})

	command := "echo ALIANG_AGENT_TERMINAL_TEST\nexit\n"
	if runtime.GOOS == "windows" {
		command = "echo ALIANG_AGENT_TERMINAL_TEST\r\nexit\r\n"
	}
	manager.write(map[string]interface{}{
		"type":       "terminal.input",
		"session_id": "term_test",
		"data":       command,
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "terminal.output", func(event map[string]interface{}) bool {
		return strings.Contains(remoteString(event, "data"), "ALIANG_AGENT_TERMINAL_TEST")
	})
	waitForAgentEvent(t, &mu, &events, "terminal.exit", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_test"
	})
}

func TestAgentTerminalManagerResizesPTY(t *testing.T) {
	if !agentNativePTYSupported() {
		t.Skip("native PTY is not supported on this platform")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentTerminalManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_resize",
		"cwd":        projectPath,
		"rows":       31,
		"cols":       101,
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "terminal.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_resize" && event["pty"] == true && remoteInt(event, "rows", 0) == 31 && remoteInt(event, "cols", 0) == 101
	})

	manager.resize(map[string]interface{}{
		"type":       "terminal.resize",
		"session_id": "term_resize",
		"rows":       33,
		"cols":       120,
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "terminal.resized", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_resize" && event["pty"] == true && remoteInt(event, "rows", 0) == 33 && remoteInt(event, "cols", 0) == 120
	})
}

func TestAgentTerminalManagerRejectsUnsafeRemoteExecution(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentTerminalManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	unauthorizedDir := t.TempDir()
	manager.create(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_unauthorized",
		"cwd":        unauthorizedDir,
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "terminal.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_unauthorized" && strings.Contains(remoteString(event, "error"), "authorized project")
	})

	manager.create(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_shell",
		"cwd":        projectPath,
		"shell":      "not-a-shell",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "terminal.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_shell" && strings.Contains(remoteString(event, "error"), "unsupported terminal shell")
	})

	manager.create(map[string]interface{}{
		"type":       "terminal.create",
		"session_id": "term_input",
		"cwd":        projectPath,
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "terminal.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_input"
	})
	manager.write(map[string]interface{}{
		"type":       "terminal.input",
		"session_id": "term_input",
		"data":       strings.Repeat("x", agentTerminalInputLimitBytes+1),
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "terminal.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_input" && strings.Contains(remoteString(event, "error"), "terminal.input exceeds")
	})
}

func TestAgentAIManagerRunsFakeCodex(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	codexName := "codex"
	script := "#!/bin/sh\nprintf 'ALIANG_FAKE_CODEX_OUTPUT\\n'\nprintf '%s\\n' \"$@\"\n"
	if runtime.GOOS == "windows" {
		codexName = "codex.bat"
		script = "@echo off\r\necho ALIANG_FAKE_CODEX_OUTPUT\r\necho %*\r\n"
	}
	codexPath := filepath.Join(binDir, codexName)
	if err := os.WriteFile(codexPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_test",
		"project_path": projectPath,
		"mode":         "agent",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_test"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_test",
		"message_id": "msg_test",
		"content":    "hello fake codex",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.delta", func(event map[string]interface{}) bool {
		return strings.Contains(remoteString(event, "delta"), "ALIANG_FAKE_CODEX_OUTPUT")
	})
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_test" && event["message_id"] == "assistant_msg_test"
	})
}

func TestAgentAIManagerKeepsSessionHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell prompt inspection in this test is POSIX-only")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	script := `#!/bin/sh
last=""
for arg in "$@"; do
  last="$arg"
done
if printf '%s' "$last" | grep -q 'ALIANG_FIRST_ASSISTANT'; then
  printf 'SECOND_CONTEXT_OK\n'
else
  printf 'FIRST ANSWER ALIANG_FIRST_ASSISTANT\n'
fi
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_history",
		"project_path": projectPath,
		"provider":     "codex",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_history"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_history",
		"message_id": "msg_first",
		"content":    "ALIANG_FIRST_USER",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_history" && event["message_id"] == "assistant_msg_first"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_history",
		"message_id": "msg_second",
		"content":    "ALIANG_SECOND_USER",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.delta", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_history" && event["message_id"] == "assistant_msg_second" && strings.Contains(remoteString(event, "delta"), "SECOND_CONTEXT_OK")
	})
}

func TestAgentAIManagerResumesImportedCodexSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell prompt inspection in this test is POSIX-only")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	resolvedProjectPath := projectPath
	if resolved, err := filepath.EvalSymlinks(projectPath); err == nil {
		resolvedProjectPath = resolved
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
printf 'ARGS:%s\n' "$*"
printf 'PWD:%s\n' "$PWD"
`
	codexPath := filepath.Join(binDir, "codex")
	if err := os.WriteFile(codexPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	manager.create(map[string]interface{}{
		"type":              "ai.session.create",
		"session_id":        "ai_imported",
		"project_path":      projectPath,
		"provider":          "codex",
		"resume_session_id": "codex-session-raw",
		"transcript": []interface{}{
			map[string]interface{}{"id": "msg_old", "role": "user", "content": "old local prompt"},
		},
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_imported" && event["resume_session_id"] == "codex-session-raw"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_imported",
		"message_id": "msg_resume",
		"content":    "continue this imported session",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_imported" && event["message_id"] == "assistant_msg_resume"
	})
	mu.Lock()
	var output strings.Builder
	for _, event := range events {
		if event["session_id"] == "ai_imported" && remoteString(event, "type") == "ai.delta" {
			output.WriteString(remoteString(event, "delta"))
		}
	}
	mu.Unlock()
	got := output.String()
	if !strings.Contains(got, "ARGS:exec resume") || !strings.Contains(got, "codex-session-raw") || !strings.Contains(got, "PWD:"+resolvedProjectPath) {
		t.Fatalf("codex resume output = %q, want resume args and project cwd", got)
	}
}

func TestAgentAIManagerRejectsUnsafeRemoteExecution(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	unauthorizedDir := t.TempDir()
	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_unauthorized",
		"project_path": unauthorizedDir,
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_unauthorized" && strings.Contains(remoteString(event, "error"), "authorized project")
	})

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_provider",
		"project_path": projectPath,
		"provider":     "danger",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_provider" && strings.Contains(remoteString(event, "error"), "unsupported AI provider")
	})

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_message_limit",
		"project_path": projectPath,
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_message_limit"
	})
	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_message_limit",
		"message_id": "msg_big",
		"content":    strings.Repeat("x", agentAIMessageLimitBytes+1),
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.error", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_message_limit" && strings.Contains(remoteString(event, "error"), "ai.message exceeds")
	})
}

func TestAgentProtocolContractDefinesRemoteFlow(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()
	if contract.Version == "" {
		t.Fatal("protocol version is empty")
	}
	if len(contract.HTTP) != 2 || contract.HTTP[0].Path != models.AgentHTTPRegisterEndpoint || contract.HTTP[1].Path != models.AgentHTTPStatusSyncEndpoint {
		t.Fatalf("HTTP contract = %#v, want register and status sync endpoints", contract.HTTP)
	}
	if got := strings.Join(contract.HTTP[0].RequestFields, ","); got != "device_id,unique_code" {
		t.Fatalf("register request fields = %q, want device_id,unique_code", got)
	}
	if !strings.Contains(contract.HTTP[0].Auth, "user_access_token") {
		t.Fatalf("register auth = %q, want user auth", contract.HTTP[0].Auth)
	}
	if !strings.Contains(contract.HTTP[1].Auth, "device_token") {
		t.Fatalf("status sync auth = %q, want device token auth", contract.HTTP[1].Auth)
	}
	if !stringSliceContains(contract.HTTP[1].RequestFields, "projects") || !stringSliceContains(contract.HTTP[1].RequestFields, "vibe_sessions") {
		t.Fatalf("status sync fields = %#v, want projects and vibe_sessions", contract.HTTP[1].RequestFields)
	}
	if !strings.Contains(contract.WebSocket.Path, models.AgentWSEndpoint) || !strings.Contains(contract.WebSocket.Auth, "device_token") {
		t.Fatalf("websocket contract = %#v, want device token websocket", contract.WebSocket)
	}
	if !protocolEventsContain(contract.WebSocket.ServerSends, models.AgentEventTerminalResize) {
		t.Fatal("protocol missing terminal.resize server event")
	}
	if !protocolEventsContain(contract.WebSocket.ClientSends, models.AgentEventAIDelta) {
		t.Fatal("protocol missing ai.delta client stream event")
	}
}

func TestAgentHelloPayloadIncludesProjectsAndVibeSessionSummaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	codexProject := filepath.Join(home, "work", "codex-project")
	claudeProject := filepath.Join(home, "work", "claude-project")
	if err := os.MkdirAll(filepath.Join(codexProject, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir codex project: %v", err)
	}
	if err := os.MkdirAll(claudeProject, 0o700); err != nil {
		t.Fatalf("mkdir claude project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexProject, "go.mod"), []byte("module codex-project\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexProject, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "13")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	codexID := "codex-session-1"
	indexLine := `{"id":"` + codexID + `","thread_name":"Codex history title","updated_at":"2026-06-13T01:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, ".codex", "session_index.jsonl"), []byte(indexLine), 0o600); err != nil {
		t.Fatalf("write codex index: %v", err)
	}
	metaLine := `{"timestamp":"2026-06-13T00:59:00Z","type":"session_meta","payload":{"id":"` + codexID + `","cwd":"` + codexProject + `","model":"gpt-5-codex","model_provider":"OpenAI","git":{"branch":"main"}}}` + "\n"
	userLine := `{"timestamp":"2026-06-13T01:00:00Z","type":"message","payload":{"role":"user","content":[{"type":"input_text","text":"Codex user prompt"}]}}` + "\n"
	assistantLine := `{"timestamp":"2026-06-13T01:01:00Z","type":"message","payload":{"role":"assistant","content":[{"type":"output_text","text":"Codex assistant reply"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(codexDir, "session.jsonl"), []byte(metaLine+userLine+assistantLine), 0o600); err != nil {
		t.Fatalf("write codex session: %v", err)
	}

	claudeDir := filepath.Join(home, ".claude", "projects", "-tmp-claude-project")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir claude sessions: %v", err)
	}
	claudeIndex := `{"version":1,"originalPath":"` + claudeProject + `","entries":[{"sessionId":"claude-session-1","firstPrompt":"Claude prompt","summary":"Claude summary","messageCount":7,"created":"2026-06-13T02:00:00Z","modified":"2026-06-13T02:05:00Z","gitBranch":"feature","projectPath":"` + claudeProject + `","isSidechain":false}]}`
	if err := os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(claudeIndex), 0o600); err != nil {
		t.Fatalf("write claude index: %v", err)
	}

	service := NewAgentService()
	payload := service.agentHelloPayload()

	projects, ok := payload["projects"].([]models.AgentProject)
	if !ok || len(projects) != 2 {
		t.Fatalf("hello projects = %#v, want two discovered projects", payload["projects"])
	}
	sessions, ok := payload["vibe_sessions"].([]models.AgentVibeSession)
	if !ok || len(sessions) != 2 {
		t.Fatalf("hello vibe_sessions = %#v, want two discovered sessions", payload["vibe_sessions"])
	}
	dirs, ok := payload["authorized_directories"].([]string)
	if !ok || len(dirs) != 2 {
		t.Fatalf("hello authorized_directories = %#v, want two project paths", payload["authorized_directories"])
	}
	if !agentProjectsContain(projects, codexProject) || !agentProjectsContain(projects, claudeProject) {
		t.Fatalf("hello projects missing expected paths: %#v", projects)
	}
	if !agentVibeSessionsContain(sessions, "codex_"+codexID) || !agentVibeSessionsContain(sessions, "claude_claude-session-1") {
		t.Fatalf("hello vibe sessions missing expected ids: %#v", sessions)
	}
	var codexSession models.AgentVibeSession
	for _, session := range sessions {
		if session.ID == "codex_"+codexID {
			codexSession = session
			break
		}
	}
	if codexSession.MessageCount != 2 {
		t.Fatalf("codex message count = %d, want 2", codexSession.MessageCount)
	}
	if len(codexSession.Transcript) != 0 {
		t.Fatalf("codex transcript = %#v, want summary-only hello payload", codexSession.Transcript)
	}
}

func TestAgentRemoteDetailRequestsReturnProjectAndVibeSessionDetail(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	projectPath := filepath.Join(home, "work", "detail-project")
	if err := os.MkdirAll(filepath.Join(projectPath, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "go.mod"), []byte("module detail-project\n"), 0o600); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, "README.md"), []byte("# Detail Project\n\nREADME body"), 0o600); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectPath, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatalf("write HEAD: %v", err)
	}

	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "14")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	codexID := "detail-session-1"
	lines := []string{
		`{"timestamp":"2026-06-14T00:00:00Z","type":"session_meta","payload":{"id":"` + codexID + `","cwd":"` + projectPath + `","model":"gpt-5-codex","model_provider":"OpenAI","git":{"branch":"main"}}}`,
		`{"timestamp":"2026-06-14T00:01:00Z","type":"message","payload":{"role":"user","content":[{"type":"input_text","text":"Detail user prompt"}]}}`,
		`{"timestamp":"2026-06-14T00:02:00Z","type":"message","payload":{"role":"assistant","content":[{"type":"output_text","text":"Detail assistant reply"}]}}`,
		`{"timestamp":"2026-06-14T00:03:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"Desktop user prompt"}]}}`,
		`{"timestamp":"2026-06-14T00:04:00Z","type":"event_msg","payload":{"type":"agent_message","message":"Desktop assistant reply","phase":"commentary"}}`,
	}
	if err := os.WriteFile(filepath.Join(codexDir, "detail-session.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write codex session: %v", err)
	}

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_test"
	service.state.Device = &models.AgentDevice{
		ID:                    service.state.DeviceID,
		DeviceID:              service.state.DeviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "detail-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		event, ok := payload.(map[string]interface{})
		if !ok {
			t.Fatalf("payload type = %T, want map[string]interface{}", payload)
		}
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		return nil
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":         "project.detail",
		"request_id":   "req_project",
		"project_id":   "proj_detail",
		"project_path": projectPath,
	}, writeJSON)
	projectEvent := waitForAgentEvent(t, &mu, &events, "project.detail.result", func(event map[string]interface{}) bool {
		return event["request_id"] == "req_project"
	})
	expectedProjectPath, err := cleanExistingAgentDirectory(projectPath)
	if err != nil {
		t.Fatalf("clean project path: %v", err)
	}
	project, ok := projectEvent["project"].(*models.AgentProject)
	if !ok || project.Path != expectedProjectPath || !strings.Contains(project.Readme, "README body") || project.FileCount == 0 {
		t.Fatalf("project detail event = %#v project=%+v ok=%t", projectEvent, project, ok)
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":         "file.list",
		"request_id":   "req_files",
		"project_path": projectPath,
		"path":         projectPath,
		"max_entries":  2,
	}, writeJSON)
	fileListEvent := waitForAgentEvent(t, &mu, &events, "file.list.result", func(event map[string]interface{}) bool {
		return event["request_id"] == "req_files"
	})
	entries, ok := fileListEvent["entries"].([]map[string]interface{})
	if !ok || len(entries) != 2 || fileListEvent["truncated"] != true {
		t.Fatalf("file list event = %#v entries=%+v ok=%t", fileListEvent, entries, ok)
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":              "ai.session.detail",
		"request_id":        "req_session",
		"source_session_id": codexID,
		"project_path":      projectPath,
	}, writeJSON)
	sessionEvent := waitForAgentEvent(t, &mu, &events, "ai.session.detail.result", func(event map[string]interface{}) bool {
		return event["request_id"] == "req_session"
	})
	session, ok := sessionEvent["session"].(models.AgentVibeSession)
	if !ok || session.ID != codexID || len(session.Transcript) != 4 {
		t.Fatalf("session detail event = %#v", sessionEvent)
	}
	if session.Transcript[0].Role != "user" || session.Transcript[0].Content != "Detail user prompt" {
		t.Fatalf("first session detail message = %#v", session.Transcript[0])
	}
	if session.Transcript[3].Role != "assistant" || session.Transcript[3].Content != "Desktop assistant reply" {
		t.Fatalf("desktop session detail message = %#v", session.Transcript[3])
	}
}

func TestReadCodexSessionMetaWithOptionsPaginatesTranscript(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "codex-session.jsonl")
	codexID := "codex-paged-session"
	var lines []string
	lines = append(lines, `{"timestamp":"2026-06-13T00:00:00Z","type":"session_meta","payload":{"id":"`+codexID+`","cwd":"`+strings.ReplaceAll(dir, `\`, `\\`)+`","model":"gpt-5-codex","model_provider":"codex","git":{}}}`)
	for i := 0; i < 60; i++ {
		role := "assistant"
		prefix := "recent"
		if i%2 == 0 {
			role = "user"
		}
		if i < 20 {
			prefix = "old"
		}
		lines = append(lines, `{"timestamp":"2026-06-13T01:`+fmt.Sprintf("%02d", i)+`:00Z","type":"message","payload":{"role":"`+role+`","content":[{"type":"text","text":"`+prefix+` message `+fmt.Sprint(i)+`"}]}}`)
	}
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write codex fixture: %v", err)
	}

	latest := readCodexSessionMetaWithOptions(sessionPath, agentVibeSessionReadOptions{Limit: 40, IncludePageMeta: true})
	if latest.MessageCount != 60 {
		t.Fatalf("latest MessageCount = %d, want 60", latest.MessageCount)
	}
	if got := len(latest.Transcript); got != 40 {
		t.Fatalf("latest transcript length = %d, want 40", got)
	}
	if latest.TranscriptPage == nil || !latest.TranscriptPage.HasMore || latest.TranscriptPage.NextBeforeMessageID == "" {
		t.Fatalf("latest page metadata = %#v, want has_more with cursor", latest.TranscriptPage)
	}
	if strings.Contains(latest.Transcript[0].Content, "old message") {
		t.Fatalf("latest page unexpectedly included old message: %#v", latest.Transcript[0])
	}
	if latest.Transcript[0].Index != 20 || latest.Transcript[len(latest.Transcript)-1].Index != 59 {
		t.Fatalf("latest indexes = %d..%d, want 20..59", latest.Transcript[0].Index, latest.Transcript[len(latest.Transcript)-1].Index)
	}

	older := readCodexSessionMetaWithOptions(sessionPath, agentVibeSessionReadOptions{
		Limit:           40,
		BeforeMessageID: latest.TranscriptPage.NextBeforeMessageID,
		IncludePageMeta: true,
	})
	if got := len(older.Transcript); got != 20 {
		t.Fatalf("older transcript length = %d, want 20", got)
	}
	if older.TranscriptPage == nil || older.TranscriptPage.HasMore {
		t.Fatalf("older page metadata = %#v, want no more history", older.TranscriptPage)
	}
	if older.Transcript[0].Index != 0 || older.Transcript[len(older.Transcript)-1].Index != 19 {
		t.Fatalf("older indexes = %d..%d, want 0..19", older.Transcript[0].Index, older.Transcript[len(older.Transcript)-1].Index)
	}
}

func TestAgentHelloPayloadSanitizesReportedVibeSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	projectPath := filepath.Join(home, "work", "claude-long-title")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	claudeDir := filepath.Join(home, ".claude", "projects", "-tmp-long-title")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	longTitle := strings.Repeat("题", 260)
	claudeIndex := `{"version":1,"originalPath":"` + projectPath + `","entries":[` +
		`{"sessionId":"claude-long","firstPrompt":"` + longTitle + `","summary":"` + strings.Repeat("摘", 620) + `","messageCount":2,"created":"2026-06-13T02:00:00Z","modified":"2026-06-13T02:05:00Z","projectPath":"` + projectPath + `","isSidechain":false},` +
		`{"sessionId":"claude-root","firstPrompt":"root","messageCount":1,"created":"2026-06-13T03:00:00Z","modified":"2026-06-13T03:05:00Z","projectPath":"/","isSidechain":false}` +
		`]}`
	if err := os.WriteFile(filepath.Join(claudeDir, "sessions-index.json"), []byte(claudeIndex), 0o600); err != nil {
		t.Fatalf("write claude index: %v", err)
	}

	service := NewAgentService()
	payload := service.agentHelloPayload()
	sessions, ok := payload["vibe_sessions"].([]models.AgentVibeSession)
	if !ok || len(sessions) != 1 {
		t.Fatalf("hello vibe_sessions = %#v, want only safe session", payload["vibe_sessions"])
	}
	if len([]rune(sessions[0].Title)) > 200 {
		t.Fatalf("session title length = %d, want <= 200", len([]rune(sessions[0].Title)))
	}
	if len([]rune(sessions[0].Summary)) > 500 {
		t.Fatalf("session summary length = %d, want <= 500", len([]rune(sessions[0].Summary)))
	}
	dirs, ok := payload["authorized_directories"].([]string)
	if !ok || len(dirs) != 1 || dirs[0] != projectPath {
		t.Fatalf("authorized_directories = %#v, want only %s", payload["authorized_directories"], projectPath)
	}
}

func protocolEventsContain(events []models.AgentProtocolEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func agentProjectsContain(projects []models.AgentProject, path string) bool {
	for _, project := range projects {
		if project.Path == path {
			return true
		}
	}
	return false
}

func agentVibeSessionsContain(sessions []models.AgentVibeSession, id string) bool {
	for _, session := range sessions {
		if session.ID == id {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func setupAgentExecutionProjectForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	projectPath := filepath.Join(home, "work", "execution-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir execution project: %v", err)
	}

	codexDir := filepath.Join(home, ".codex", "sessions", "2026", "06", "13")
	if err := os.MkdirAll(codexDir, 0o700); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	sessionID := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	indexRow, err := json.Marshal(map[string]interface{}{
		"id":          sessionID,
		"thread_name": "Execution guard test",
		"updated_at":  "2026-06-13T01:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal codex index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "session_index.jsonl"), append(indexRow, '\n'), 0o600); err != nil {
		t.Fatalf("write codex index: %v", err)
	}

	metaRow, err := json.Marshal(map[string]interface{}{
		"timestamp": "2026-06-13T00:59:00Z",
		"type":      "session_meta",
		"payload": map[string]interface{}{
			"id":             sessionID,
			"cwd":            projectPath,
			"model":          "gpt-5-codex",
			"model_provider": "OpenAI",
		},
	})
	if err != nil {
		t.Fatalf("marshal codex session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "session.jsonl"), append(metaRow, '\n'), 0o600); err != nil {
		t.Fatalf("write codex session: %v", err)
	}
	return projectPath
}

func TestResolveAgentLaunchSpecCommandLine(t *testing.T) {
	spec, err := resolveAgentLaunchSpec(modelsAgentLaunchRequestForTest("go version"))
	if err != nil {
		t.Fatalf("resolveAgentLaunchSpec() error = %v", err)
	}
	if spec.ToolID != "command" {
		t.Fatalf("ToolID = %q, want command", spec.ToolID)
	}
	if spec.Path == "" {
		t.Fatal("Path is empty")
	}
	if len(spec.Args) != 1 || spec.Args[0] != "version" {
		t.Fatalf("Args = %#v, want [version]", spec.Args)
	}
}

func modelsAgentLaunchRequestForTest(commandLine string) models.AgentLaunchRequest {
	return models.AgentLaunchRequest{CommandLine: commandLine}
}

func waitForAgentEvent(t *testing.T, mu *sync.Mutex, events *[]map[string]interface{}, eventType string, match func(map[string]interface{}) bool) map[string]interface{} {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		for _, event := range *events {
			if remoteString(event, "type") == eventType && (match == nil || match(event)) {
				mu.Unlock()
				return event
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("timed out waiting for %s in events: %#v", eventType, *events)
	return nil
}
