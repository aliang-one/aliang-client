package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aliang.one/nursorgate/common/cache"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// setupAgentIdentityTestEnv isolates on-disk agent state (and the cache-dir
// singleton) into a per-test temp directory so device-identity persistence can
// be exercised across simulated restarts.
func setupAgentIdentityTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	auth.ResetAuthPersistenceForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(func() {
		auth.ResetAuthPersistenceForTest()
		config.ResetGlobalConfigForTest()
	})
}

// agentIdentityFilePathForTest derives the dedicated identity file path from the
// state path so tests do not depend on a not-yet-implemented helper.
func agentIdentityFilePathForTest(t *testing.T) string {
	t.Helper()
	statePath, err := agentStatePath()
	if err != nil {
		t.Fatalf("agentStatePath() error = %v", err)
	}
	return filepath.Join(filepath.Dir(statePath), "device_identity.json")
}

// The device_id is an installation-permanent identity: generated once and never
// regenerated. A fresh process that reads the same on-disk identity file must
// resolve the exact same id without minting any secondary credential.
func TestAgentDeviceIdentityGeneratedOncePersistsAcrossRestart(t *testing.T) {
	setupAgentIdentityTestEnv(t)

	svc1 := NewAgentService()
	svc1.mu.Lock()
	id1 := svc1.resolveDeviceIDLocked()
	_ = svc1.saveStateLocked()
	svc1.mu.Unlock()
	if !strings.HasPrefix(id1, "dev-") {
		t.Fatalf("first device_id = %q, want dev- prefix", id1)
	}

	// Simulated restart: brand-new service reads the persisted identity file.
	svc2 := NewAgentService()
	svc2.mu.Lock()
	id2 := svc2.resolveDeviceIDLocked()
	svc2.mu.Unlock()
	if id2 != id1 {
		t.Fatalf("device_id changed across restart: %q -> %q", id1, id2)
	}
}

// Permanence must hold even if the mutable session-state file is lost/corrupted:
// the device_id is recovered from the dedicated identity file, not regenerated.
func TestAgentDeviceIdentitySurvivesStateFileLoss(t *testing.T) {
	setupAgentIdentityTestEnv(t)

	svc1 := NewAgentService()
	svc1.mu.Lock()
	id1 := svc1.resolveDeviceIDLocked()
	_ = svc1.saveStateLocked()
	svc1.mu.Unlock()

	statePath, err := agentStatePath()
	if err != nil {
		t.Fatalf("agentStatePath() error = %v", err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatalf("remove state file: %v", err)
	}

	// loadState now finds no state file -> empty in-memory state. The identity
	// file must still pin the original device_id.
	svc2 := NewAgentService()
	svc2.mu.Lock()
	id2 := svc2.resolveDeviceIDLocked()
	svc2.mu.Unlock()
	if id2 != id1 {
		t.Fatalf("device_id changed after state file loss: %q -> %q (want %q)", id1, id2, id1)
	}
}

// "Regardless of who logs in": disable (auth_expired/logout) and user-switch
// must never mutate the device_id.
func TestAgentDeviceIdentityUnchangedByDisableAndUserSwitch(t *testing.T) {
	setupAgentIdentityTestEnv(t)

	svc := NewAgentService()
	svc.mu.Lock()
	id := svc.resolveDeviceIDLocked()
	svc.mu.Unlock()

	svc.DisableWithReason("auth_expired")
	svc.mu.Lock()
	afterDisable := svc.resolveDeviceIDLocked()
	// Drive the user-switch reset path directly: it must clear registration but
	// leave the device_id untouched.
	svc.state.Registered = true
	svc.state.RegisteredUser = "user-A"
	svc.resetRegisteredDeviceIfUserChangedLocked("user-B")
	afterSwitch := svc.state.DeviceID
	registeredAfter := svc.state.Registered
	svc.mu.Unlock()

	if afterDisable != id {
		t.Fatalf("device_id changed after disable: %q -> %q", id, afterDisable)
	}
	if afterSwitch != id {
		t.Fatalf("device_id changed after user switch: %q -> %q", id, afterSwitch)
	}
	if registeredAfter {
		t.Fatal("user-switch reset should clear registration")
	}
}

// On device_id_already_bound the agent must KEEP its permanent device_id and
// surface a conflict status — never silently mint a new id. (Replaces the old
// "rotate on already_bound" behavior.)
func TestAgentServiceRegisterKeepsPermanentDeviceIDOnAlreadyBound(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_conflict",
		RefreshToken: "refresh_conflict",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	registerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		registerCalls++
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"error": "device_id_already_bound"})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	svc := NewAgentService()
	svc.mu.Lock()
	svc.state.DeviceID = "dev_permanent"
	err := svc.registerAndSyncLocked()
	idAfter := svc.state.DeviceID
	status := svc.state.LastSyncStatus
	svc.mu.Unlock()

	if err == nil {
		t.Fatal("registerAndSyncLocked() error = nil, want conflict error")
	}
	if idAfter != "dev_permanent" {
		t.Fatalf("device_id changed on already_bound: %q -> %q (must stay dev_permanent)", "dev_permanent", idAfter)
	}
	if registerCalls != 1 {
		t.Fatalf("register call count = %d, want 1 (no rotation retry)", registerCalls)
	}
	if !strings.Contains(status, "conflict") {
		t.Fatalf("LastSyncStatus = %q, want a conflict marker", status)
	}
}

// A server-assigned device_id in the register response must NOT override the
// client-owned permanent identity.
func TestAgentServiceRegisterIgnoresServerAssignedDeviceID(t *testing.T) {
	setupAgentIdentityTestEnv(t)
	if err := auth.SaveUserInfo(&auth.UserInfo{
		AccessToken:  "access_local",
		RefreshToken: "refresh_local",
		TokenType:    "Bearer",
	}); err != nil {
		t.Fatalf("SaveUserInfo() error = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/agent/status" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok"})
			return
		}
		if r.URL.Path != "/api/devices/register" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 0,
			"data": map[string]interface{}{
				"device_id": "dev_server_assigned",
				"device": map[string]interface{}{
					"id":        "dev_server_assigned",
					"device_id": "dev_server_assigned",
					"name":      "server-named",
				},
			},
		})
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	svc := NewAgentService()
	svc.mu.Lock()
	svc.state.DeviceID = "dev_local"
	err := svc.registerAndSyncLocked()
	idAfter := svc.state.DeviceID
	svc.mu.Unlock()

	if err != nil {
		t.Fatalf("registerAndSyncLocked() error = %v", err)
	}
	if idAfter != "dev_local" {
		t.Fatalf("server device_id overrode permanent id: %q (want dev_local)", idAfter)
	}
}

// Upgrade/migration: an installation that already has a device_id in
// agent_state.json (but no dedicated identity file yet) must ADOPT that id as
// the permanent identity — not regenerate a new one.
func TestAgentDeviceIdentityAdoptsExistingStateOnUpgrade(t *testing.T) {
	setupAgentIdentityTestEnv(t)

	statePath, err := agentStatePath()
	if err != nil {
		t.Fatalf("agentStatePath() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(statePath, []byte(`{"device_id":"dev_legacy","unique_code":"adc-legacy","registered":true}`), 0o600); err != nil {
		t.Fatalf("seed state file: %v", err)
	}
	// No device_identity.json exists yet.

	svc := NewAgentService()
	svc.mu.Lock()
	id := svc.resolveDeviceIDLocked()
	_ = svc.saveStateLocked()
	svc.mu.Unlock()
	if id != "dev_legacy" {
		t.Fatalf("device_id = %q, want adopted dev_legacy (not regenerated)", id)
	}

	// The identity file must now persist the adopted id so future restarts keep it.
	identityPath := agentIdentityFilePathForTest(t)
	raw, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}
	var persisted map[string]string
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatalf("unmarshal identity file: %v", err)
	}
	if persisted["device_id"] != "dev_legacy" {
		t.Fatalf("identity file device_id = %q, want dev_legacy", persisted["device_id"])
	}
	if _, ok := persisted["unique_code"]; ok {
		t.Fatal("identity file must not persist unique_code")
	}
}
