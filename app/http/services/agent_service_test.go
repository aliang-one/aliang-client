package services

import (
	"context"
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
	if err := auth.DeleteUserInfo(); err != nil {
		t.Fatalf("DeleteUserInfo() error = %v", err)
	}
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

func TestAgentServiceRegisterRefusedWithoutJwtNoFallback(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	if err := auth.DeleteUserInfo(); err != nil {
		t.Fatalf("DeleteUserInfo() error = %v", err)
	}
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	// No user info saved → no JWT available. Registration must be REFUSED
	// (login_required), NEVER falling back to an admin-console / platform
	// identity. This is the invariant: a device binds to an owner only via JWT.
	registerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/api/devices/register" {
			registerCalled = true
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 0, "data": map[string]interface{}{}})
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
	if registerCalled {
		t.Fatal("register endpoint was called without a JWT — registration must be refused (login_required), not fall back to admin-console")
	}
	if status.SyncStatus != "login_required" {
		t.Fatalf("SyncStatus = %q, want login_required", status.SyncStatus)
	}
	if status.Enabled || status.Registered || status.Bound {
		t.Fatalf("status should reflect a refused registration (not bound): %#v", status)
	}
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
	// device_id is client-owned: the registered id (sent in the payload) must
	// be the one retained, not the server's dev_enable.
	registeredID := remoteString(registerPayload, "device_id")
	if status.Device == nil || status.Device.DeviceID != registeredID || registeredID == "" {
		t.Fatalf("Enable() device = %#v, want client-owned permanent id %q", status.Device, registeredID)
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
	// device_id is client-owned and permanent: it must stay the original id,
	// ignoring the server's dev_backend.
	if status.Device == nil || status.Device.DeviceID != originalDeviceID {
		t.Fatalf("Status() device = %#v, want permanent client id %q", status.Device, originalDeviceID)
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
	// The inventory sync must carry the client's permanent device_id (the same
	// id it registered with), not the server's dev_inventory.
	if remoteString(statusPayload, "device_id") != remoteString(registerPayload, "device_id") {
		t.Fatalf("status payload device_id = %q, want client permanent id %q", remoteString(statusPayload, "device_id"), remoteString(registerPayload, "device_id"))
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
	service.state.UniqueCode = "adc-test-existing"
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
	service.state.UniqueCode = "adc-test-existing"
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
	// device_id is client-owned: the id the client registered with is retained,
	// not the server's dev_phone_server.
	if status.Device.DeviceID != remoteString(registerPayload, "device_id") {
		t.Fatalf("DeviceID = %q, want client permanent id %q", status.Device.DeviceID, remoteString(registerPayload, "device_id"))
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
	service.mu.Lock()
	forwardedDeviceID := service.state.DeviceID
	service.mu.Unlock()
	// device_id is client-owned: must be the client's permanent id, not the
	// server's dev_forwarded.
	if !status.Enabled || status.Device == nil || status.Device.DeviceID != forwardedDeviceID || forwardedDeviceID == "" {
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

func TestAgentServiceDisableWithReasonClearsRemoteState(t *testing.T) {
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
	service.state.RemoteConnected = true
	service.state.DeviceToken = "dt_test"
	service.state.Device = &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "disable-reason-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()

	status := service.DisableWithReason("auth_expired")
	if status.Enabled || status.Registered || status.Bound || status.Device != nil || status.RemoteConnected {
		t.Fatalf("DisableWithReason() status = %#v, want fully disconnected", status)
	}
	if status.SyncStatus != "auth_expired" {
		t.Fatalf("SyncStatus = %q, want auth_expired", status.SyncStatus)
	}
	if !strings.Contains(status.SyncMessage, "session expired") {
		t.Fatalf("SyncMessage = %q, want session expired", status.SyncMessage)
	}
}

func TestAgentServiceRejectsRemoteCommandsAfterDisable(t *testing.T) {
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
		Name:                  "old-ws-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	service.mu.Unlock()
	service.DisableWithReason("auth_expired")

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
		"session_id": "term_after_disable",
		"cwd":        os.TempDir(),
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "agent.error", func(event map[string]interface{}) bool {
		return strings.Contains(remoteString(event, "error"), "disabled")
	})
}

func TestAgentServiceDeviceTokenUnauthorizedDisablesAgent(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/agent/status" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    http.StatusUnauthorized,
				"message": "device token invalid",
				"reason":  "DEVICE_TOKEN_INVALID",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	deviceID := service.state.DeviceID
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_invalid"
	service.state.Device = &models.AgentDevice{
		ID:                    deviceID,
		DeviceID:              deviceID,
		UniqueCode:            service.state.UniqueCode,
		Name:                  "invalid-token-device",
		Platform:              agentPlatform(),
		Status:                "online",
		RemoteTerminalEnabled: true,
		AIControlEnabled:      true,
		BoundAt:               time.Now().UTC().Format(time.RFC3339),
	}
	err := service.syncAgentInventoryLocked("test_device_token_invalid")
	service.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("syncAgentInventoryLocked() error = %v, want 401", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status := service.Status()
		if !status.Enabled && !status.Bound && status.SyncStatus == "device_token_invalid" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status after device token rejection = %#v, want disabled and unbound", service.Status())
}

// TestAgentServiceSyncDoesNotResurrectAfterLogoutWithoutForwardedJwt locks in
// the fix for "user logs out but the agent reconnects on its own". After a
// logout the agent is sticky-disabled (LastSyncStatus=logout, device_token
// cleared). Even if a stale JWT is still observable through a legacy/local
// fallback, a
// background sync (startup_sync / watchdog respawn / in-flight post-logout sync)
// arrives with NO forwarded JWT — the dashboard is logged out. That sync must
// NOT re-register the device. Only an explicitly forwarded JWT (a fresh login)
// may clear the sticky logout state.
func TestAgentServiceSyncDoesNotResurrectAfterLogoutWithoutForwardedJwt(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	registerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/devices/register" {
			registerCalled = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_token": "dt_resurrected",
				"device_id":    "dev_resurrected",
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL, APIServer: server.URL}})

	service := NewAgentService()
	// Simulate a previously-registered, online device.
	service.mu.Lock()
	service.ensureDeviceIdentityLocked()
	service.state.Enabled = true
	service.state.Registered = true
	service.state.DeviceToken = "dt_before_logout"
	service.state.LastSyncStatus = "online"
	_ = service.saveStateLocked()
	service.mu.Unlock()

	// User logs out: agent is disabled, device_token cleared, sticky "logout".
	service.DisableWithReason("logout")

	// Simulate a stale JWT still observable through a legacy/local fallback.
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "stale_access_after_logout",
		RefreshToken: "stale_refresh_after_logout",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	// Background sync with NO forwarded JWT — the post-logout self-resurrection
	// path (authHeader falls back to the agent's own stale cached JWT).
	if err := service.SyncNow(); err != nil {
		t.Fatalf("SyncNow() error = %v", err)
	}

	if registerCalled {
		t.Fatal("register endpoint was called after logout without a forwarded JWT — agent must not self-resurrect via a stale cached JWT")
	}
	status := service.Status()
	if status.Enabled || status.Registered || status.Bound {
		t.Fatalf("agent resurrected after logout: %#v, want sticky-disabled", status)
	}
}

// TestAgentServiceSyncReRegistersAfterLogoutWithForwardedJwt is the companion
// invariant: a sync that carries an explicitly forwarded JWT (the dashboard just
// authenticated the user again) MUST still clear the sticky logout state and
// re-register. Otherwise the logout fix would also break re-login.
func TestAgentServiceSyncReRegistersAfterLogoutWithForwardedJwt(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	registerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/devices/register" {
			registerCalled = true
			if got := r.Header.Get("Authorization"); got != "Bearer fresh_jwt_on_relogin" {
				t.Errorf("Authorization = %q, want Bearer fresh_jwt_on_relogin", got)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_token": "dt_relogin",
				"device_id":    "dev_relogin",
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL, APIServer: server.URL}})

	service := NewAgentService()
	service.DisableWithReason("logout")

	if err := service.SyncNowWithUserContext("Bearer fresh_jwt_on_relogin", ""); err != nil {
		t.Fatalf("SyncNowWithUserContext() error = %v", err)
	}
	if !registerCalled {
		t.Fatal("register endpoint was not called for a re-login sync with a forwarded JWT")
	}
	status := service.Status()
	if !status.Enabled || !status.Registered {
		t.Fatalf("agent not re-registered after re-login sync: %#v", status)
	}
}

func TestAgentServiceKeepsForwardedAccessTokenInMemory(t *testing.T) {
	t.Setenv(AgentRuntimeEnv, "1")
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.SetSessionOwnerProcess(false)
	t.Cleanup(func() {
		auth.SetSessionOwnerProcess(true)
		cache.ResetCacheDirForTest()
	})

	service := NewAgentService()
	service.mu.Lock()
	_, _, changed := service.resolveForwardedUserContextLocked("Bearer access-1", "id:1")
	service.mu.Unlock()
	if !changed {
		t.Fatal("first forwarded access token was not recorded as changed")
	}
	if got := service.currentAccessToken(); got != "access-1" {
		t.Fatalf("currentAccessToken() = %q, want access-1", got)
	}

	// A stale auth-package snapshot must never override the owner-forwarded token
	// inside the user-agent process.
	auth.SetCurrentUserInfo(&auth.UserInfo{AccessToken: "stale-from-sqlite", TokenType: "Bearer"})
	service.mu.Lock()
	_, _, changed = service.resolveForwardedUserContextLocked("Bearer access-2", "id:1")
	service.mu.Unlock()
	if !changed {
		t.Fatal("rotated forwarded access token was not recorded as changed")
	}
	if got := service.currentAccessToken(); got != "access-2" {
		t.Fatalf("currentAccessToken() = %q, want access-2", got)
	}

	service.DisableWithReason("logout")
	if got := service.currentAccessToken(); got != "" {
		t.Fatalf("currentAccessToken() after logout = %q, want empty", got)
	}
}

// TestAgentServiceRegisterAuth401RecoversSessionBeforeWiping locks in the fix
// for "login expires very quickly". When the agent server returns 401 on the
// register call, the local user session must NOT be wiped immediately — a
// recovery refresh should be attempted first (the 401 may be a stale/expired
// JWT, not a truly dead session). Here the refresh token is still valid, so the
// session must survive.
func TestAgentServiceRegisterAuth401RecoversSessionBeforeWiping(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})

	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_about_to_be_rejected",
		RefreshToken: "refresh_still_valid",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/devices/register":
			// Agent server rejects the (stale) user JWT.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "authentication_required"})
		case "/api/v1/auth/refresh":
			// But the refresh token is still valid → session is recoverable.
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"access_token":  "access_recovered",
					"refresh_token": "refresh_rotated",
					"expires_in":    3600,
					"token_type":    "Bearer",
				},
			})
		case "/api/v1/user/profile":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"id": "1", "email": "x@y.z", "username": "x", "role": "admin", "status": "active",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL, APIServer: server.URL}})

	// Mirror the real flow: login precedes any register-401, so the authority is
	// Active when access is rejected (otherwise NotifyAccessRejected no-ops and
	// the SoftExpired coordinator bails before refreshing). Do NOT reset the
	// authority — its init-registered listeners must stay attached.
	auth.GetSessionAuthority().NotifyLoggedIn(&auth.UserInfo{})

	service := NewAgentService()
	// SyncNow triggers register → 401 → RecoverOrExpire → SoftExpired + async
	// recovery. The recovery refresh succeeds, so the session must survive with
	// a renewed token (recovery runs in a goroutine; poll for it).
	_ = service.SyncNow()

	deadline := time.Now().Add(3 * time.Second)
	var recovered *auth.UserInfo
	for time.Now().Before(deadline) {
		if u := auth.GetCurrentUserInfoOrLoad(); u != nil && u.AccessToken == "access_recovered" {
			recovered = u
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recovered == nil {
		t.Fatal("session was not recovered after a register 401 with a valid refresh token — recovery refresh did not complete (session should survive, not wipe)")
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
	// Remote settings must update name/capabilities/toggles but NOT the
	// device_id — that is client-owned and permanent (ignores dev_remote).
	if status.Device.DeviceID != deviceID || status.Device.Name != "remote-name" {
		t.Fatalf("device settings not applied (device_id must stay %q): %#v", deviceID, status.Device)
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

	got, err := currentAgentWebSocketURL("dt_test", "")
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

func writeFakeCodexAppServerForTest(t *testing.T, binDir string) string {
	t.Helper()
	codexPath := filepath.Join(binDir, "codex")
	if runtime.GOOS == "windows" {
		t.Skip("fake codex app-server shell is POSIX-only")
	}
	script := `#!/bin/sh
if [ "$1" != "app-server" ]; then
  echo "expected codex app-server, got: $*" >&2
  exit 2
fi

mode="${ALIANG_FAKE_CODEX_MODE:-basic}"
thread_method="start"
thread_seen="0"

while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*)
      printf '{"id":0,"result":{"userAgent":"fake-codex","codexHome":"/tmp","platformFamily":"unix","platformOs":"macos"}}\n'
      ;;
    *'"method":"thread/resume"'*)
      thread_method="resume"
      if printf '%s' "$line" | grep -q 'codex-session-raw'; then
        thread_seen="1"
      fi
      printf '{"id":1,"result":{"thread":{"id":"thr_fake"},"model":"fake","modelProvider":"openai","serviceTier":null,"cwd":"%s","instructionSources":[],"approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":{},"reasoningEffort":null}}\n' "$PWD"
      ;;
    *'"method":"thread/start"'*)
      thread_method="start"
      if [ "$mode" = "thread_error" ]; then
        printf '{"id":1,"error":{"code":-32600,"message":"thread/start.runtimeWorkspaceRoots requires experimentalApi capability"}}\n'
        exit 0
      fi
      printf '{"id":1,"result":{"thread":{"id":"thr_fake"},"model":"fake","modelProvider":"openai","serviceTier":null,"cwd":"%s","instructionSources":[],"approvalPolicy":"on-request","approvalsReviewer":"user","sandbox":{},"reasoningEffort":null}}\n' "$PWD"
      ;;
    *'"method":"turn/start"'*)
      printf '{"id":2,"result":{"turn":{"id":"turn_fake"}}}\n'
      case "$mode" in
        approval)
          printf '{"method":"item/commandExecution/requestApproval","id":77,"params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"cmd_fake","startedAtMs":1,"reason":"needs shell","command":"git push","cwd":"%s","availableDecisions":["accept","acceptForSession","decline","cancel"]}}\n' "$PWD"
          ;;
        history)
          if printf '%s' "$line" | grep -q 'ALIANG_FIRST_ASSISTANT'; then
            text="SECOND_CONTEXT_OK"
          else
            text="FIRST ANSWER ALIANG_FIRST_ASSISTANT"
          fi
          printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"%s"}}\n' "$text"
          printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
          exit 0
          ;;
        resume)
          prompt_seen="0"
          if printf '%s' "$line" | grep -q 'continue this imported session'; then
            prompt_seen="1"
          fi
          printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"METHOD:%s THREAD:%s PROMPT:%s PWD:%s"}}\n' "$thread_method" "$thread_seen" "$prompt_seen" "$PWD"
          printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
          exit 0
          ;;
        structured)
          printf '{"method":"item/started","params":{"threadId":"thr_fake","turnId":"turn_fake","item":{"type":"commandExecution","id":"cmd_struct","command":["echo","hello"],"cwd":"%s"}}}\n' "$PWD"
          printf '{"method":"item/completed","params":{"threadId":"thr_fake","turnId":"turn_fake","item":{"type":"commandExecution","id":"cmd_struct","exitCode":0,"stdout":"hello world"}}}\n'
          printf '{"method":"item/started","params":{"threadId":"thr_fake","turnId":"turn_fake","item":{"type":"fileChange","id":"fc_struct","changes":[{"path":"%s/out.txt","kind":"edit","diff":"@@ -1,1 +1,2 @@\\n-old\\n+new\\n+newer\\n"}]}}}\n' "$PWD"
          printf '{"method":"item/completed","params":{"threadId":"thr_fake","turnId":"turn_fake","item":{"type":"fileChange","id":"fc_struct","changes":[{"path":"%s/out.txt","kind":"edit","diff":"@@ -1,1 +1,2 @@\\n-old\\n+new\\n+newer\\n"}]}}}\n' "$PWD"
          printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"STRUCTURED_DONE"}}\n'
          printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
          exit 0
          ;;
        steer)
          ;;
        *)
          printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"ALIANG_FAKE_CODEX_OUTPUT"}}\n'
          printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
          exit 0
          ;;
      esac
      ;;
    *'"id":77'*)
      if printf '%s' "$line" | grep -q '"decision":"accept'; then
        printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"APPROVED_OK"}}\n'
        printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
        exit 0
      fi
      printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"failed"}}}\n'
      exit 1
      ;;
    *'"method":"turn/steer"'*)
      if [ "$mode" = "steer" ]; then
        if printf '%s' "$line" | grep -q '"expectedTurnId":"turn_fake"' && printf '%s' "$line" | grep -q 'steer now'; then
          printf '{"id":"aliang_steer_101","result":{"turnId":"turn_fake"}}\n'
          printf '{"method":"item/agentMessage/delta","params":{"threadId":"thr_fake","turnId":"turn_fake","itemId":"msg_fake","delta":"STEER_APPLIED"}}\n'
          printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"completed"}}}\n'
          exit 0
        fi
        printf '{"id":"aliang_steer_101","error":{"code":"bad_steer","message":"bad steer payload"}}\n'
        printf '{"method":"turn/completed","params":{"threadId":"thr_fake","turn":{"id":"turn_fake","status":"failed","error":{"message":"bad steer"}}}}\n'
        exit 1
      fi
      ;;
  esac
done
`
	if err := os.WriteFile(codexPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	return codexPath
}

func TestAgentAIManagerRunsFakeCodex(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "basic")
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

func TestAgentAIManagerCodexThreadStartErrorSurfaces(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "thread_error")
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
		"session_id":   "ai_thread_error",
		"project_path": projectPath,
		"provider":     "codex",
		"mode":         "agent",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_thread_error"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_thread_error",
		"message_id": "msg_thread_error",
		"content":    "hello broken codex",
	}, writeJSON)

	errEvent := waitForAgentEvent(t, &mu, &events, "ai.error", func(event map[string]interface{}) bool {
		errText := remoteString(event, "error")
		return event["session_id"] == "ai_thread_error" &&
			strings.Contains(errText, "Codex app-server thread/start failed") &&
			strings.Contains(errText, "experimentalApi capability")
	})
	if errEvent["message_id"] != "assistant_msg_thread_error" {
		t.Fatalf("message_id = %v, want assistant_msg_thread_error", errEvent["message_id"])
	}
	mu.Lock()
	defer mu.Unlock()
	for _, event := range events {
		if remoteString(event, "type") == "ai.run.started" && event["session_id"] == "ai_thread_error" {
			t.Fatalf("ai.run.started should not be emitted after thread/start failure: %#v", events)
		}
	}
}

func TestAgentAIManagerCodexSteerSendsTurnSteer(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "steer")
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
		"session_id":   "ai_steer",
		"project_path": projectPath,
		"provider":     "codex",
		"mode":         "agent",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_steer"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_steer",
		"message_id": "msg_steer_root",
		"content":    "start and wait",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.run.started", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_steer"
	})

	manager.steer(map[string]interface{}{
		"type":       "ai.steer",
		"session_id": "ai_steer",
		"message_id": "msg_steer",
		"content":    "steer now",
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "ai.steer.ack", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_steer" &&
			event["message_id"] == "msg_steer" &&
			remoteString(event, "result") == "queued"
	})
	waitForAgentEvent(t, &mu, &events, "ai.steer.ack", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_steer" &&
			event["message_id"] == "msg_steer" &&
			remoteString(event, "result") == "applied"
	})
	waitForAgentEvent(t, &mu, &events, "ai.delta", func(event map[string]interface{}) bool {
		return strings.Contains(remoteString(event, "delta"), "STEER_APPLIED")
	})
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_steer"
	})
}

func TestAgentAIManagerSteerRejectsUnsupportedAndNotRunning(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	manager := newAgentAIManager()
	defer manager.closeAll()
	mu, events, writer := captureAIWriter(t)

	manager.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "ai_idle_steer",
		"project_path": projectPath,
		"provider":     "codex",
	}, writer)
	waitForAgentEvent(t, mu, events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_idle_steer"
	})
	manager.steer(map[string]interface{}{
		"type":       "ai.steer",
		"session_id": "ai_idle_steer",
		"message_id": "msg_idle_steer",
		"content":    "too early",
	}, writer)
	waitForAgentEvent(t, mu, events, "ai.steer.ack", func(event map[string]interface{}) bool {
		return event["message_id"] == "msg_idle_steer" &&
			remoteString(event, "result") == "not_running"
	})

	manager.mu.Lock()
	manager.sessions["ai_claude_steer"] = &agentAISession{
		id:          "ai_claude_steer",
		mode:        "agent",
		projectPath: projectPath,
		provider:    "claudecode",
		cancel:      func() {},
		runSeq:      1,
	}
	manager.mu.Unlock()
	manager.steer(map[string]interface{}{
		"type":       "ai.steer",
		"session_id": "ai_claude_steer",
		"message_id": "msg_claude_steer",
		"content":    "not supported",
	}, writer)
	waitForAgentEvent(t, mu, events, "ai.steer.ack", func(event map[string]interface{}) bool {
		return event["message_id"] == "msg_claude_steer" &&
			remoteString(event, "result") == "unsupported"
	})
}

func TestAgentAIManagerEmitsStructuredCodexEvents(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "structured")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		if event, ok := payload.(map[string]interface{}); ok {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
		return nil
	}

	manager.create(map[string]interface{}{
		"type": "ai.session.create", "session_id": "ai_struct",
		"project_path": projectPath, "mode": "agent",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_struct"
	})

	manager.message(map[string]interface{}{
		"type": "ai.message", "session_id": "ai_struct",
		"message_id": "msg_struct", "content": "run it",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_struct"
	})

	commands := filterEvents(&mu, &events, "ai.command")
	if len(commands) != 2 {
		t.Fatalf("ai.command events = %d, want 2 (started+completed)", len(commands))
	}
	byStatus := map[string]map[string]interface{}{}
	for _, ev := range commands {
		byStatus[remoteString(ev, "status")] = ev
	}
	if started := byStatus["started"]; started == nil || remoteString(started, "command") != "echo hello" {
		t.Fatalf("ai.command started = %#v, want command echo hello", started)
	}
	if completed := byStatus["completed"]; completed == nil {
		t.Fatal("missing ai.command completed")
	} else {
		if remoteString(completed, "command") != "echo hello" {
			t.Fatalf("completed command = %q, want echo hello", remoteString(completed, "command"))
		}
		if code, ok := eventInt(completed, "exit_code"); !ok || code != 0 {
			t.Fatalf("completed exit_code = %v, want 0", completed["exit_code"])
		}
		if remoteString(completed, "output") != "hello world" {
			t.Fatalf("completed output = %q, want hello world", remoteString(completed, "output"))
		}
	}

	fileChanges := filterEvents(&mu, &events, "ai.file_change")
	if len(fileChanges) != 1 {
		t.Fatalf("ai.file_change events = %d, want 1", len(fileChanges))
	}
	fc := fileChanges[0]
	if !strings.HasSuffix(remoteString(fc, "path"), "/out.txt") {
		t.Fatalf("file_change path = %q, want .../out.txt", remoteString(fc, "path"))
	}
	if remoteString(fc, "kind") != "edit" {
		t.Fatalf("file_change kind = %q, want edit", remoteString(fc, "kind"))
	}
	if added, ok := eventInt(fc, "added"); !ok || added != 2 {
		t.Fatalf("file_change added = %v, want 2", fc["added"])
	}
	if removed, ok := eventInt(fc, "removed"); !ok || removed != 1 {
		t.Fatalf("file_change removed = %v, want 1", fc["removed"])
	}
	if remoteString(fc, "diff") == "" {
		t.Fatal("file_change diff should be forwarded to the cloud")
	}
}

func filterEvents(mu *sync.Mutex, events *[]map[string]interface{}, eventType string) []map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	out := make([]map[string]interface{}, 0)
	for _, ev := range *events {
		if remoteString(ev, "type") == eventType {
			out = append(out, ev)
		}
	}
	return out
}

// eventInt reads a numeric event field whether it is stored as a Go int (in-process
// writeJSON capture) or a float64 (after a JSON round-trip over the wire).
func eventInt(event map[string]interface{}, key string) (int, bool) {
	switch v := event[key].(type) {
	case int:
		return v, true
	case float64:
		return int(v), true
	}
	return 0, false
}

func TestAgentAIManagerKeepsSessionHistory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell prompt inspection in this test is POSIX-only")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "history")
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
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "resume")
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
	if !strings.Contains(got, "METHOD:resume") || !strings.Contains(got, "THREAD:1") || !strings.Contains(got, "PROMPT:1") || !strings.Contains(got, "PWD:"+resolvedProjectPath) {
		t.Fatalf("codex resume output = %q, want thread/resume, latest prompt, and project cwd", got)
	}
	if strings.Contains(got, "--color") {
		t.Fatalf("codex resume output = %q, want no unsupported color flag on resume", got)
	}
}

func TestResolveNamedAgentAIToolIgnoresProviderPlaceholderModel(t *testing.T) {
	binDir := t.TempDir()
	claudeName := "claude"
	script := "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		claudeName = "claude.bat"
		script = "@echo off\r\n"
	}
	claudePath := filepath.Join(binDir, claudeName)
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool, err := resolveNamedAgentAITool("claude", "你好", "claude", "", "claude-session-id")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	got := strings.Join(tool.args, " ")
	if strings.Contains(got, "--model claude") {
		t.Fatalf("claude args = %q, want provider placeholder model omitted", got)
	}
	if !strings.Contains(got, "--resume claude-session-id") {
		t.Fatalf("claude args = %q, want resume session id", got)
	}
}

func TestResolveNamedAgentAIToolDoesNotSlimClaudeHeadlessByDefault(t *testing.T) {
	binDir := t.TempDir()
	claudeName := "claude"
	script := "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		claudeName = "claude.bat"
		script = "@echo off\r\n"
	}
	claudePath := filepath.Join(binDir, claudeName)
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ALIANG_CLAUDE_HEADLESS_SLIM", "")
	t.Setenv("ALIANG_CLAUDE_HEADLESS_TOOLS", "")
	t.Setenv("ALIANG_CLAUDE_HEADLESS_ENABLE_MCP", "")

	tool, err := resolveNamedAgentAITool("claude", "hello", "", "", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	got := strings.Join(tool.args, " ")
	if strings.Contains(got, "--tools") || strings.Contains(got, "--strict-mcp-config") {
		t.Fatalf("claude args = %q, want no slim flags by default", got)
	}
}

func TestResolveNamedAgentAIToolClaudeHeadlessSlimStaysDisabledEvenWhenEnvSet(t *testing.T) {
	binDir := t.TempDir()
	claudeName := "claude"
	script := "#!/bin/sh\n"
	if runtime.GOOS == "windows" {
		claudeName = "claude.bat"
		script = "@echo off\r\n"
	}
	claudePath := filepath.Join(binDir, claudeName)
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	// slim 已永久停用(会把 tools 砍到 6 个、清空 MCP, 导致被网关判为 openclaw):
	// 即使 env 显式开启, 也不应再注入 --tools / --strict-mcp-config。
	t.Setenv("ALIANG_CLAUDE_HEADLESS_SLIM", "1")
	t.Setenv("ALIANG_CLAUDE_HEADLESS_TOOLS", "")
	t.Setenv("ALIANG_CLAUDE_HEADLESS_ENABLE_MCP", "")

	tool, err := resolveNamedAgentAITool("claude", "hello", "", "", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	got := strings.Join(tool.args, " ")
	if strings.Contains(got, "--tools") || strings.Contains(got, "--strict-mcp-config") {
		t.Fatalf("claude args = %q, want NO slim flags even with ALIANG_CLAUDE_HEADLESS_SLIM=1 (slim disabled to keep full tool set)", got)
	}
}

func TestAgentAIManagerStreamsClaudeCodeTextDelta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake claude in this test is POSIX-only")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init","cwd":"ignored"}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"SECRET_THINKING"}}}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"你"}}}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"好"}}}\n'
printf '{"type":"assistant","message":{"content":[{"type":"text","text":"你好"}]}}\n'
printf '{"type":"result","result":"你好"}\n'
`
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
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
		"session_id":   "ai_claude_stream",
		"project_path": projectPath,
		"provider":     "claudecode",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_claude_stream"
	})

	manager.message(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "ai_claude_stream",
		"message_id": "msg_hello",
		"content":    "你好",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_claude_stream" && event["message_id"] == "assistant_msg_hello"
	})

	mu.Lock()
	var output strings.Builder
	for _, event := range events {
		if event["session_id"] == "ai_claude_stream" && remoteString(event, "type") == "ai.delta" {
			output.WriteString(remoteString(event, "delta"))
			if remoteString(event, "channel") != "assistant" {
				t.Fatalf("claude delta channel = %q, want assistant", remoteString(event, "channel"))
			}
		}
	}
	mu.Unlock()
	got := output.String()
	if got != "你好" {
		t.Fatalf("claude streamed output = %q, want only visible assistant text", got)
	}
	if strings.Contains(got, "SECRET_THINKING") || strings.Contains(got, "system") {
		t.Fatalf("claude streamed output leaked non-assistant event: %q", got)
	}
}

func TestAgentAIManagerStreamsClaudeStructuredEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake claude in this test is POSIX-only")
	}
	projectPath := setupAgentExecutionProjectForTest(t)
	binDir := t.TempDir()
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init","cwd":"ignored"}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"PLANNING_THOUGHT"}}}\n'
printf '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"call_b1","name":"Bash","input":{"command":"echo hi","cwd":"/repo"}},{"type":"tool_use","id":"call_w1","name":"Write","input":{"file_path":"/repo/x.txt","content":"alpha"}},{"type":"tool_use","id":"call_t1","name":"TodoWrite","input":{"todos":[{"content":"t1","status":"in_progress","activeForm":"doing t1"}]}}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":3}}}\n'
printf '{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"call_b1","content":"hi","is_error":false}]}}\n'
printf '{"type":"result","result":"ok"}\n'
`
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	manager := newAgentAIManager()
	defer manager.closeAll()

	var mu sync.Mutex
	events := make([]map[string]interface{}, 0)
	writeJSON := func(payload interface{}) error {
		if event, ok := payload.(map[string]interface{}); ok {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		}
		return nil
	}

	manager.create(map[string]interface{}{
		"type": "ai.session.create", "session_id": "ai_struct_claude",
		"project_path": projectPath, "provider": "claudecode",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_struct_claude"
	})
	manager.message(map[string]interface{}{
		"type": "ai.message", "session_id": "ai_struct_claude",
		"message_id": "msg_sc", "content": "go",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "ai_struct_claude"
	})

	thinking := filterEvents(&mu, &events, "ai.thinking")
	if len(thinking) != 1 || remoteString(thinking[0], "delta") != "PLANNING_THOUGHT" {
		t.Fatalf("ai.thinking events = %#v, want one PLANNING_THOUGHT", thinking)
	}

	commands := filterEvents(&mu, &events, "ai.command")
	if len(commands) != 2 {
		t.Fatalf("ai.command events = %d, want 2 (started+completed)", len(commands))
	}
	var started, completed map[string]interface{}
	for _, ev := range commands {
		if remoteString(ev, "status") == "started" {
			started = ev
		}
		if remoteString(ev, "status") == "completed" {
			completed = ev
		}
	}
	if started == nil || remoteString(started, "command") != "echo hi" {
		t.Fatalf("ai.command started = %#v, want echo hi", started)
	}
	if completed == nil {
		t.Fatal("missing ai.command completed from tool_result")
	}
	if remoteString(completed, "command") != "echo hi" || remoteString(completed, "output") != "hi" {
		t.Fatalf("ai.command completed = %#v, want echo hi / hi", completed)
	}
	if code, _ := eventInt(completed, "exit_code"); code != 0 {
		t.Fatalf("completed exit_code = %v, want 0", completed["exit_code"])
	}

	fileChanges := filterEvents(&mu, &events, "ai.file_change")
	if len(fileChanges) != 1 {
		t.Fatalf("ai.file_change events = %d, want 1", len(fileChanges))
	}
	if got := remoteString(fileChanges[0], "path"); got != "/repo/x.txt" {
		t.Fatalf("file_change path = %q, want /repo/x.txt", got)
	}
	if added, _ := eventInt(fileChanges[0], "added"); added != 1 {
		t.Fatalf("file_change added = %v, want 1", fileChanges[0]["added"])
	}

	tasks := filterEvents(&mu, &events, "ai.task")
	if len(tasks) != 1 {
		t.Fatalf("ai.task events = %d, want 1", len(tasks))
	}
	taskList, ok := tasks[0]["tasks"].([]map[string]interface{})
	if !ok || len(taskList) != 1 || remoteString(taskList[0], "subject") != "t1" {
		t.Fatalf("ai.task tasks = %#v, want one subject t1", tasks[0]["tasks"])
	}

	usage := filterEvents(&mu, &events, "ai.usage")
	if len(usage) != 1 {
		t.Fatalf("ai.usage events = %d, want 1", len(usage))
	}
	if in, _ := eventInt(usage[0], "input_tokens"); in != 10 {
		t.Fatalf("usage input_tokens = %v, want 10", usage[0]["input_tokens"])
	}
	if out, _ := eventInt(usage[0], "output_tokens"); out != 5 {
		t.Fatalf("usage output_tokens = %v, want 5", usage[0]["output_tokens"])
	}
}

// TestAgentServiceDispatchLocalAIStreamsClaudeDeltas exercises the in-process
// dispatch path used by the in-app chat WebSocket: DispatchLocalAI must route
// ai.session.create / ai.message to the manager so the page receives the live
// ai.run.started -> ai.delta -> ai.done stream, exactly as it would over the
// remote agent link.
func TestAgentServiceDispatchLocalAIStreamsClaudeDeltas(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fake claude in this test is POSIX-only")
	}

	projectPath := setupAgentExecutionProjectForTest(t)
	// resolveAgentAICWD populates the package-level authorized-dirs cache; reset
	// it on exit so this test cannot leak a stale entry that flips a later test's
	// "unauthorized path" rejection.
	defer func() {
		agentAuthorizedDirsMu.Lock()
		agentAuthorizedDirsCache = nil
		agentAuthorizedDirsMu.Unlock()
	}()
	binDir := t.TempDir()
	script := `#!/bin/sh
printf '{"type":"system","subtype":"init","cwd":"ignored"}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"SECRET"}}}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"你"}}}\n'
printf '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"好"}}}\n'
printf '{"type":"result","result":"你好"}\n'
`
	claudePath := filepath.Join(binDir, "claude")
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	svc := &AgentService{ai: newAgentAIManager()}
	defer svc.ai.closeAll()

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

	svc.DispatchLocalAI(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "dispatch_local",
		"project_path": projectPath,
		"provider":     "claudecode",
		"mode":         "vibe",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "dispatch_local"
	})

	svc.DispatchLocalAI(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "dispatch_local",
		"message_id": "msg_dispatch",
		"content":    "你好",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.run.started", func(event map[string]interface{}) bool {
		return event["session_id"] == "dispatch_local"
	})
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "dispatch_local"
	})

	mu.Lock()
	var output strings.Builder
	sawStarted := false
	for _, event := range events {
		if event["session_id"] != "dispatch_local" {
			continue
		}
		switch remoteString(event, "type") {
		case "ai.run.started":
			sawStarted = true
		case "ai.delta":
			output.WriteString(remoteString(event, "delta"))
		}
	}
	mu.Unlock()

	if !sawStarted {
		t.Fatalf("expected ai.run.started before ai.done")
	}
	if got := output.String(); got != "你好" {
		t.Fatalf("dispatched streamed output = %q, want 你好", got)
	}
}

func TestAgentServiceDispatchLocalAIHandlesCodexAppServerApproval(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	defer func() {
		agentAuthorizedDirsMu.Lock()
		agentAuthorizedDirsCache = nil
		agentAuthorizedDirsMu.Unlock()
	}()
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "approval")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	svc := &AgentService{ai: newAgentAIManager()}
	defer svc.ai.closeAll()

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

	svc.DispatchLocalAI(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "codex_approval",
		"project_path": projectPath,
		"provider":     "codex",
		"mode":         "vibe",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "codex_approval"
	})

	svc.DispatchLocalAI(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "codex_approval",
		"message_id": "msg_approval",
		"content":    "run a command",
	}, writeJSON)
	approval := waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "codex_approval" &&
			remoteString(event, "status") == "pending" &&
			remoteString(event, "kind") == models.AgentAIApprovalKindCommand &&
			remoteString(event, "command") == "git push"
	})

	svc.DispatchLocalAI(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "codex_approval",
		"message_id":  remoteString(approval, "message_id"),
		"approval_id": remoteString(approval, "approval_id"),
		"decision":    "accept",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "codex_approval" &&
			remoteString(event, "approval_id") == remoteString(approval, "approval_id") &&
			remoteString(event, "status") == "resolved" &&
			remoteString(event, "decision") == models.AgentAIApprovalDecisionAccept
	})
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "codex_approval" && event["message_id"] == "assistant_msg_approval"
	})

	mu.Lock()
	var output strings.Builder
	for _, event := range events {
		if event["session_id"] == "codex_approval" && remoteString(event, "type") == "ai.delta" {
			output.WriteString(remoteString(event, "delta"))
		}
	}
	mu.Unlock()
	if got := output.String(); got != "APPROVED_OK" {
		t.Fatalf("codex approval output = %q, want APPROVED_OK", got)
	}
}

func TestAgentServiceDispatchLocalAIListsSlashCommandsByProvider(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	defer func() {
		agentAuthorizedDirsMu.Lock()
		agentAuthorizedDirsCache = nil
		agentAuthorizedDirsMu.Unlock()
	}()

	cmdDir := filepath.Join(projectPath, ".claude", "commands")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cmdDir, "demo.md"), []byte("---\ndescription: Demo command\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := &AgentService{ai: newAgentAIManager()}
	defer svc.ai.closeAll()

	listSlash := func(provider string) map[string]interface{} {
		var mu sync.Mutex
		var got map[string]interface{}
		writeJSON := func(payload interface{}) error {
			if event, ok := payload.(map[string]interface{}); ok {
				if remoteString(event, "type") == models.AgentEventSlashCommandsListResult {
					mu.Lock()
					got = event
					mu.Unlock()
				}
			}
			return nil
		}
		svc.DispatchLocalAI(map[string]interface{}{
			"type":         models.AgentEventSlashCommandsList,
			"request_id":   "req-" + provider,
			"project_path": projectPath,
			"provider":     provider,
		}, writeJSON)
		mu.Lock()
		defer mu.Unlock()
		return got
	}

	namesByProvider := func(result map[string]interface{}) map[string]string {
		out := map[string]string{}
		if result == nil {
			return out
		}
		cmds, _ := result["commands"].([]map[string]interface{})
		for _, c := range cmds {
			out[remoteString(c, "name")] = remoteString(c, "provider")
		}
		return out
	}

	claude := listSlash("claude")
	if claude == nil {
		t.Fatal("claude: expected slash.commands.list.result over the local AI stream")
	}
	claudeNames := namesByProvider(claude)
	if _, ok := claudeNames["demo"]; !ok {
		t.Errorf("claude result missing project command 'demo': %v", claudeNames)
	}
	for name, prov := range claudeNames {
		if prov != "claude" {
			t.Errorf("claude result entry %q has provider=%q, want claude", name, prov)
		}
	}

	codex := listSlash("codex")
	if codex == nil {
		t.Fatal("codex: expected slash.commands.list.result over the local AI stream")
	}
	codexNames := namesByProvider(codex)
	if _, ok := codexNames["model"]; !ok {
		t.Errorf("codex result missing builtin 'model': %v", codexNames)
	}
	if _, leaked := codexNames["demo"]; leaked {
		t.Errorf("codex result must not include the claude project command 'demo': %v", codexNames)
	}
	for name, prov := range codexNames {
		if prov != "codex" {
			t.Errorf("codex result entry %q has provider=%q, want codex", name, prov)
		}
	}
}

func TestAgentServiceRemoteAIApprovalResponseUnblocksCodexAppServer(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	defer func() {
		agentAuthorizedDirsMu.Lock()
		agentAuthorizedDirsCache = nil
		agentAuthorizedDirsMu.Unlock()
	}()
	binDir := t.TempDir()
	writeFakeCodexAppServerForTest(t, binDir)
	t.Setenv("ALIANG_FAKE_CODEX_MODE", "approval")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	svc := &AgentService{
		terminal: newAgentTerminalManager(),
		ai:       newAgentAIManager(),
	}
	// The remote link gates every AI event behind remoteControlAllowed()
	// (Enabled + device token). The local dispatch path bypasses that gate, so
	// only this remote test must opt into the enabled state to reach ai.create.
	svc.mu.Lock()
	svc.state.Enabled = true
	svc.state.DeviceToken = "dt_test"
	svc.mu.Unlock()
	defer svc.ai.closeAll()

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

	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "remote_codex_approval",
		"project_path": projectPath,
		"provider":     "codex",
		"mode":         "vibe",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "remote_codex_approval"
	})

	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":       "ai.message",
		"session_id": "remote_codex_approval",
		"message_id": "msg_remote_approval",
		"content":    "run a command",
	}, writeJSON)
	approval := waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "remote_codex_approval" &&
			remoteString(event, "status") == "pending" &&
			remoteString(event, "kind") == models.AgentAIApprovalKindCommand &&
			remoteString(event, "command") == "git push"
	})

	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "remote_codex_approval",
		"message_id":  remoteString(approval, "message_id"),
		"approval_id": remoteString(approval, "approval_id"),
		"decision":    "accept",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "remote_codex_approval" &&
			remoteString(event, "approval_id") == remoteString(approval, "approval_id") &&
			remoteString(event, "status") == "resolved" &&
			remoteString(event, "decision") == models.AgentAIApprovalDecisionAccept
	})
	waitForAgentEvent(t, &mu, &events, "ai.done", func(event map[string]interface{}) bool {
		return event["session_id"] == "remote_codex_approval" && event["message_id"] == "assistant_msg_remote_approval"
	})

	mu.Lock()
	var output strings.Builder
	for _, event := range events {
		if event["session_id"] == "remote_codex_approval" && remoteString(event, "type") == "ai.delta" {
			output.WriteString(remoteString(event, "delta"))
		}
	}
	mu.Unlock()
	if got := output.String(); got != "APPROVED_OK" {
		t.Fatalf("remote codex approval output = %q, want APPROVED_OK", got)
	}

	mu.Lock()
	beforeDuplicate := len(events)
	mu.Unlock()
	svc.handleRemoteAgentMessage(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "remote_codex_approval",
		"message_id":  remoteString(approval, "message_id"),
		"approval_id": remoteString(approval, "approval_id"),
		"decision":    "decline",
	}, writeJSON)

	mu.Lock()
	defer mu.Unlock()
	for _, event := range events[beforeDuplicate:] {
		if remoteString(event, "type") == models.AgentEventAIStatus && remoteString(event, "status") == "approval_not_found" {
			t.Fatalf("duplicate approval response emitted approval_not_found: %#v", event)
		}
		if remoteString(event, "type") == models.AgentEventAIError {
			t.Fatalf("duplicate approval response emitted error: %#v", event)
		}
	}
}

func TestAgentAIManagerRoutesDuplicateApprovalIDsBySession(t *testing.T) {
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

	type approvalResult struct {
		response agentAIApprovalResponse
		err      error
	}
	ctxA, cancelA := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelA()
	ctxB, cancelB := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelB()
	resultA := make(chan approvalResult, 1)
	resultB := make(chan approvalResult, 1)

	go func() {
		response, err := manager.requestApproval(ctxA, agentAIRun{
			sessionID: "approval_session_a",
			messageID: "msg_a",
			runSeq:    1,
			provider:  "codex",
		}, writeJSON, agentAIApprovalRequest{
			ID:   "same_approval_id",
			Kind: models.AgentAIApprovalKindCommand,
		})
		resultA <- approvalResult{response: response, err: err}
	}()
	go func() {
		response, err := manager.requestApproval(ctxB, agentAIRun{
			sessionID: "approval_session_b",
			messageID: "msg_b",
			runSeq:    1,
			provider:  "codex",
		}, writeJSON, agentAIApprovalRequest{
			ID:   "same_approval_id",
			Kind: models.AgentAIApprovalKindCommand,
		})
		resultB <- approvalResult{response: response, err: err}
	}()

	waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "approval_session_a" &&
			remoteString(event, "approval_id") == "same_approval_id" &&
			remoteString(event, "status") == "pending"
	})
	waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "approval_session_b" &&
			remoteString(event, "approval_id") == "same_approval_id" &&
			remoteString(event, "status") == "pending"
	})

	manager.approval(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "approval_session_b",
		"approval_id": "same_approval_id",
		"decision":    "decline",
	}, writeJSON)
	select {
	case result := <-resultB:
		if result.err != nil {
			t.Fatalf("session B approval error = %v", result.err)
		}
		if result.response.Decision != models.AgentAIApprovalDecisionDecline {
			t.Fatalf("session B decision = %q, want decline", result.response.Decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session B approval result")
	}
	select {
	case result := <-resultA:
		t.Fatalf("session A was resolved by session B response: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}

	manager.approval(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "approval_session_a",
		"approval_id": "same_approval_id",
		"decision":    "accept",
	}, writeJSON)
	select {
	case result := <-resultA:
		if result.err != nil {
			t.Fatalf("session A approval error = %v", result.err)
		}
		if result.response.Decision != models.AgentAIApprovalDecisionAccept {
			t.Fatalf("session A decision = %q, want accept", result.response.Decision)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for session A approval result")
	}
}

func TestAgentServiceHandleAIApprovalHookRoundTrip(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	svc := &AgentService{ai: newAgentAIManager()}
	defer svc.ai.closeAll()

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

	svc.ai.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "hook_session",
		"project_path": projectPath,
		"provider":     "claudecode",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "hook_session"
	})

	_, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	svc.ai.mu.Lock()
	session := svc.ai.sessions["hook_session"]
	if session == nil {
		svc.ai.mu.Unlock()
		t.Fatal("hook_session was not created")
	}
	session.cancel = runCancel
	session.activeWriter = writeJSON
	session.approvalToken = "token-hook"
	session.runSeq = 1
	svc.ai.mu.Unlock()

	type hookResult struct {
		response map[string]interface{}
		err      error
	}
	hookCtx, cancelHook := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHook()
	resultCh := make(chan hookResult, 1)
	go func() {
		response, err := svc.HandleAIApprovalHook(hookCtx, "hook_session", "msg_hook", "token-hook", map[string]interface{}{
			"tool_name":         "Bash",
			"permission_prompt": "Needs shell access",
			"tool_input": map[string]interface{}{
				"command": "git push",
			},
		})
		resultCh <- hookResult{response: response, err: err}
	}()

	approval := waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "hook_session" &&
			remoteString(event, "message_id") == "assistant_msg_hook" &&
			remoteString(event, "kind") == models.AgentAIApprovalKindCommand &&
			remoteString(event, "command") == "git push"
	})
	svc.DispatchLocalAI(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "hook_session",
		"approval_id": remoteString(approval, "approval_id"),
		"decision":    "accept",
	}, writeJSON)

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("HandleAIApprovalHook() error = %v", result.err)
		}
		hookOutput, _ := result.response["hookSpecificOutput"].(map[string]interface{})
		if remoteString(hookOutput, "hookEventName") != "PermissionRequest" {
			t.Fatalf("hookEventName = %#v, want PermissionRequest", hookOutput["hookEventName"])
		}
		decision, _ := hookOutput["decision"].(map[string]interface{})
		if remoteString(decision, "behavior") != "allow" {
			t.Fatalf("decision.behavior = %#v, want allow", decision["behavior"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval hook response")
	}
}

func TestAgentServiceHandleAIPreToolUseApprovalHookRoundTrip(t *testing.T) {
	projectPath := setupAgentExecutionProjectForTest(t)
	svc := &AgentService{ai: newAgentAIManager()}
	defer svc.ai.closeAll()

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

	svc.ai.create(map[string]interface{}{
		"type":         "ai.session.create",
		"session_id":   "pretool_session",
		"project_path": projectPath,
		"provider":     "claudecode",
	}, writeJSON)
	waitForAgentEvent(t, &mu, &events, "ai.session.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "pretool_session"
	})

	_, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	svc.ai.mu.Lock()
	session := svc.ai.sessions["pretool_session"]
	if session == nil {
		svc.ai.mu.Unlock()
		t.Fatal("pretool_session was not created")
	}
	session.cancel = runCancel
	session.activeWriter = writeJSON
	session.approvalToken = "token-pretool"
	session.runSeq = 1
	svc.ai.mu.Unlock()

	type hookResult struct {
		response map[string]interface{}
		err      error
	}
	hookCtx, cancelHook := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelHook()
	resultCh := make(chan hookResult, 1)
	go func() {
		response, err := svc.HandleAIApprovalHook(hookCtx, "pretool_session", "msg_pretool", "token-pretool", map[string]interface{}{
			"hook_event_name": "PreToolUse",
			"tool_name":       "Bash",
			"tool_input": map[string]interface{}{
				"command": "git push",
			},
		})
		resultCh <- hookResult{response: response, err: err}
	}()

	approval := waitForAgentEvent(t, &mu, &events, "ai.approval.request", func(event map[string]interface{}) bool {
		return event["session_id"] == "pretool_session" &&
			remoteString(event, "message_id") == "assistant_msg_pretool" &&
			remoteString(event, "kind") == models.AgentAIApprovalKindCommand &&
			remoteString(event, "command") == "git push"
	})
	svc.DispatchLocalAI(map[string]interface{}{
		"type":        "ai.approval.response",
		"session_id":  "pretool_session",
		"approval_id": remoteString(approval, "approval_id"),
		"decision":    "accept",
	}, writeJSON)

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("HandleAIApprovalHook() error = %v", result.err)
		}
		hookOutput, _ := result.response["hookSpecificOutput"].(map[string]interface{})
		if remoteString(hookOutput, "hookEventName") != "PreToolUse" {
			t.Fatalf("hookEventName = %#v, want PreToolUse", hookOutput["hookEventName"])
		}
		if remoteString(hookOutput, "permissionDecision") != "allow" {
			t.Fatalf("permissionDecision = %#v, want allow", hookOutput["permissionDecision"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for approval hook response")
	}
}

func TestClaudeCodeToolFallsBackToClaudeBinary(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf 'fallback-ok\\n'\n"
	claudePath := filepath.Join(binDir, "claude")
	if runtime.GOOS == "windows" {
		claudePath = filepath.Join(binDir, "claude.bat")
		script = "@echo off\r\necho fallback-ok\r\n"
	}
	if err := os.WriteFile(claudePath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	tool, err := resolveNamedAgentAITool("claudecode", "你好", "", "", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	if tool.id != "claudecode" {
		t.Fatalf("tool.id = %q, want claudecode", tool.id)
	}
	if !strings.Contains(strings.Join(tool.args, " "), "--output-format stream-json") {
		t.Fatalf("claudecode args = %v, want stream-json output", tool.args)
	}
}

// TestCodexEffortSuffixApplied locks in the codex reasoning-effort mechanism:
// effort is conveyed to the codex CLI as a `<base>-<effort>` model-name suffix
// (the downstream gateway derives reasoning_effort from it). The suffix is
// applied AFTER normalization and never doubled.
func TestCodexEffortSuffixApplied(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf 'codex-ok\\n'\n"
	codexPath := filepath.Join(binDir, "codex")
	if runtime.GOOS == "windows" {
		codexPath = filepath.Join(binDir, "codex.bat")
		script = "@echo off\r\necho codex-ok\r\n"
	}
	if err := os.WriteFile(codexPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// 1) effort appended to base model.
	tool, err := resolveNamedAgentAITool("codex", "你好", "gpt-5.4", "xhigh", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	joined := strings.Join(tool.args, " ")
	if !strings.Contains(joined, "--model gpt-5.4-xhigh") {
		t.Fatalf("codex args = %v, want --model gpt-5.4-xhigh", tool.args)
	}

	// 2) double-suffix guard: a model already carrying a suffix is left alone.
	tool2, err := resolveNamedAgentAITool("codex", "你好", "gpt-5.4-xhigh", "high", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	joined2 := strings.Join(tool2.args, " ")
	if strings.Contains(joined2, "gpt-5.4-xhigh-high") {
		t.Fatalf("codex args = %v, suffix double-applied", tool2.args)
	}
	if !strings.Contains(joined2, "--model gpt-5.4-xhigh") {
		t.Fatalf("codex args = %v, want --model gpt-5.4-xhigh (preserved)", tool2.args)
	}

	// 3) empty effort → no suffix (base model forwarded as-is).
	tool3, err := resolveNamedAgentAITool("codex", "你好", "gpt-5.4", "", "")
	if err != nil {
		t.Fatalf("resolveNamedAgentAITool() error = %v", err)
	}
	joined3 := strings.Join(tool3.args, " ")
	if !strings.Contains(joined3, "--model gpt-5.4") {
		t.Fatalf("codex args = %v, want --model gpt-5.4", tool3.args)
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
	if len(contract.HTTP) != 3 || contract.HTTP[0].Path != models.AgentHTTPRegisterEndpoint || contract.HTTP[1].Path != models.AgentHTTPStatusSyncEndpoint || contract.HTTP[2].Path != "/api/agent/disable" {
		t.Fatalf("HTTP contract = %#v, want register, status sync, and local disable endpoints", contract.HTTP)
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
	if !stringSliceContains(contract.HTTP[2].RequestFields, "reason?") {
		t.Fatalf("disable request fields = %#v, want optional reason", contract.HTTP[2].RequestFields)
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

func TestAgentHelloPayloadOverlaysActiveAIRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	projectPath := filepath.Join(home, "work", "active-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	service := NewAgentService()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.ai.mu.Lock()
	service.ai.sessions["live-session-1"] = &agentAISession{
		id:          "live-session-1",
		mode:        "vibe",
		projectPath: projectPath,
		provider:    "claude",
		model:       "claude-sonnet-4-5",
		cancel:      cancel,
		runSeq:      1,
		history: []agentAIMessage{{
			Role:      "user",
			MessageID: "msg-1",
			Content:   "Keep working on the active task",
			CreatedAt: time.Date(2026, 6, 29, 1, 2, 3, 0, time.UTC),
		}},
	}
	service.ai.mu.Unlock()

	payload := service.agentHelloPayload()
	sessions, ok := payload["vibe_sessions"].([]models.AgentVibeSession)
	if !ok {
		t.Fatalf("hello vibe_sessions = %T, want []AgentVibeSession", payload["vibe_sessions"])
	}
	var live models.AgentVibeSession
	for _, session := range sessions {
		if session.ID == "live-session-1" {
			live = session
			break
		}
	}
	if live.ID == "" || live.Status != "running" || live.ProjectPath != projectPath {
		t.Fatalf("live session = %#v, want running session for %s", live, projectPath)
	}

	projects, ok := payload["projects"].([]models.AgentProject)
	if !ok {
		t.Fatalf("hello projects = %T, want []AgentProject", payload["projects"])
	}
	var activeProject models.AgentProject
	for _, project := range projects {
		if project.Path == projectPath {
			activeProject = project
			break
		}
	}
	if activeProject.Path == "" || activeProject.Status != "running" {
		t.Fatalf("active project = %#v, want running project", activeProject)
	}
}

func TestAgentStatusSyncPayloadOverlaysActiveAIRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	projectPath := filepath.Join(home, "work", "status-active-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	service := NewAgentService()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.ai.mu.Lock()
	service.ai.sessions["status-live-session"] = &agentAISession{
		id:          "status-live-session",
		mode:        "vibe",
		projectPath: projectPath,
		provider:    "codex",
		model:       "gpt-5-codex",
		cancel:      cancel,
		runSeq:      1,
		history: []agentAIMessage{{
			Role:      "user",
			MessageID: "msg-2",
			Content:   "Still running",
			CreatedAt: time.Date(2026, 6, 29, 2, 3, 4, 0, time.UTC),
		}},
	}
	service.ai.mu.Unlock()

	service.mu.Lock()
	payload := service.buildAgentStatusSyncPayloadLocked("online")
	service.mu.Unlock()

	var live models.AgentVibeSession
	for _, session := range payload.VibeSessions {
		if session.ID == "status-live-session" {
			live = session
			break
		}
	}
	if live.ID == "" || live.Status != "running" || live.Provider != "codex" {
		t.Fatalf("status live session = %#v, want running codex session", live)
	}
	var activeProject models.AgentProject
	for _, project := range payload.Projects {
		if project.Path == projectPath {
			activeProject = project
			break
		}
	}
	if activeProject.Path == "" || activeProject.Status != "running" {
		t.Fatalf("status active project = %#v, want running project", activeProject)
	}
}

func TestAgentVibeSessionsOverlaysActiveAIRun(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	projectPath := filepath.Join(home, "work", "local-active-project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	service := NewAgentService()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.ai.mu.Lock()
	service.ai.sessions["local-live-session"] = &agentAISession{
		id:          "local-live-session",
		mode:        "vibe",
		projectPath: projectPath,
		provider:    "claude",
		cancel:      cancel,
		runSeq:      1,
		history: []agentAIMessage{{
			Role:      "user",
			MessageID: "msg-local",
			Content:   "Local view should still show running",
			CreatedAt: time.Date(2026, 6, 29, 3, 4, 5, 0, time.UTC),
		}},
	}
	service.ai.mu.Unlock()

	sessions := service.VibeSessions()
	for _, session := range sessions {
		if session.ID == "local-live-session" {
			if session.Status != "running" || session.ProjectPath != projectPath {
				t.Fatalf("local live session = %#v, want running session for %s", session, projectPath)
			}
			return
		}
	}
	t.Fatalf("local live session missing from VibeSessions(): %#v", sessions)
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
		`{"timestamp":"2026-06-14T00:04:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"output_text","text":"Desktop assistant reply"}]}}`,
		`{"timestamp":"2026-06-14T00:05:00Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"Developer context"}]}}`,
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
	if !ok || session.ID != codexID || len(session.Transcript) != 5 {
		t.Fatalf("session detail event = %#v", sessionEvent)
	}
	if session.Transcript[0].Role != "user" || session.Transcript[0].Content != "Detail user prompt" {
		t.Fatalf("first session detail message = %#v", session.Transcript[0])
	}
	if session.Transcript[3].Role != "assistant" || session.Transcript[3].Content != "Desktop assistant reply" {
		t.Fatalf("desktop session detail message = %#v", session.Transcript[3])
	}
	if session.Transcript[4].Role != "system" || session.Transcript[4].Content != "Developer context" {
		t.Fatalf("developer session detail message = %#v", session.Transcript[4])
	}
}

func TestReadClaudeSessionMetaClassifiesToolResultsAsSystem(t *testing.T) {
	dir := t.TempDir()
	projectPath := filepath.Join(dir, "project")
	if err := os.MkdirAll(projectPath, 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	sessionPath := filepath.Join(dir, "claude-session.jsonl")
	lines := []string{
		`{"timestamp":"2026-06-14T01:00:00Z","type":"user","cwd":"` + projectPath + `","sessionId":"claude-role-session","message":{"role":"user","content":[{"type":"text","text":"Claude user prompt"}]}}`,
		`{"timestamp":"2026-06-14T01:01:00Z","type":"assistant","cwd":"` + projectPath + `","sessionId":"claude-role-session","message":{"role":"assistant","content":[{"type":"text","text":"Claude assistant reply"}]}}`,
		`{"timestamp":"2026-06-14T01:02:00Z","type":"user","cwd":"` + projectPath + `","sessionId":"claude-role-session","message":{"role":"user","content":[{"type":"tool_result","content":"Tool output should not be user"}]}}`,
	}
	if err := os.WriteFile(sessionPath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write claude fixture: %v", err)
	}

	session := readClaudeSessionMetaWithOptions(sessionPath, agentVibeSessionReadOptions{Limit: 10, IncludePageMeta: true})
	if got := len(session.Transcript); got != 3 {
		t.Fatalf("claude transcript length = %d, want 3", got)
	}
	if session.Transcript[0].Role != "user" || session.Transcript[1].Role != "assistant" || session.Transcript[2].Role != "system" {
		t.Fatalf("claude transcript roles = %#v", session.Transcript)
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
