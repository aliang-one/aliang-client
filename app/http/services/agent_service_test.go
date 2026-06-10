package services

import (
	"encoding/json"
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
	"aliang.one/nursorgate/processor/config"
)

func TestAgentServiceBindingEnablesDevice(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	service := NewAgentService()
	started, err := service.StartBinding()
	if err != nil {
		t.Fatalf("StartBinding() error = %v", err)
	}
	if started.SessionID == "" {
		t.Fatal("StartBinding() returned empty session id")
	}
	if started.QRDataURL == "" {
		t.Fatal("StartBinding() returned empty qr data url")
	}

	service.mu.Lock()
	service.sessions[started.SessionID].CreatedAt = time.Now().Add(-agentLocalMVPBindDelay - time.Second)
	service.mu.Unlock()

	status, err := service.BindingStatus(started.SessionID)
	if err != nil {
		t.Fatalf("BindingStatus() error = %v", err)
	}
	if !status.Bound {
		t.Fatalf("BindingStatus() bound = false, want true: %#v", status)
	}

	agentStatus := service.Status()
	if !agentStatus.Enabled || !agentStatus.Bound || agentStatus.Device == nil {
		t.Fatalf("Status() did not reflect enabled bound agent: %#v", agentStatus)
	}
}

func TestAgentServiceRemotePairingRegistersDevice(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	config.ResetGlobalConfigForTest()
	t.Cleanup(config.ResetGlobalConfigForTest)

	var ticketPayload struct {
		DeviceID     string   `json:"device_id"`
		UniqueCode   string   `json:"unique_code"`
		DeviceName   string   `json:"device_name"`
		Platform     string   `json:"platform"`
		AgentVersion string   `json:"agent_version"`
		Capabilities []string `json:"capabilities"`
		Tools        []struct {
			ID      string `json:"id"`
			Command string `json:"command"`
		} `json:"tools"`
		History []struct {
			Tool string `json:"tool"`
			Path string `json:"path"`
		} `json:"history"`
	}
	pairingResultCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/pairing/tickets":
			if err := json.NewDecoder(r.Body).Decode(&ticketPayload); err != nil {
				t.Errorf("decode pairing ticket payload: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"msg":  "success",
				"data": map[string]interface{}{
					"ticket_id":    "pair_test",
					"status":       "pending",
					"pairing_code": "AP-1234-TEST",
					"qr_payload":   "http://localhost:5174/pair?ticket_id=pair_test&secret=AP-1234-TEST",
					"agent_secret": "agent-secret",
					"expires_at":   time.Now().Add(agentBindTTL).UTC().Format(time.RFC3339),
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/pairing/tickets/pair_test/result":
			pairingResultCalled = true
			if got := r.URL.Query().Get("agent_secret"); got != "agent-secret" {
				t.Errorf("agent_secret query = %q, want agent-secret", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 0,
				"msg":  "success",
				"data": map[string]interface{}{
					"ticket_id":    "pair_test",
					"status":       "approved",
					"device_id":    "dev-approved",
					"approved_at":  time.Now().UTC().Format(time.RFC3339),
					"device_token": "dt_test_token",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	config.SetGlobalConfig(&config.Config{Core: &config.CoreConfig{AgentServer: server.URL}})

	service := NewAgentService()
	started, err := service.StartBinding()
	if err != nil {
		t.Fatalf("StartBinding() error = %v", err)
	}
	if started.SessionID != "pair_test" {
		t.Fatalf("SessionID = %q, want pair_test", started.SessionID)
	}
	if started.PairingCode != "AP-1234-TEST" {
		t.Fatalf("PairingCode = %q, want AP-1234-TEST", started.PairingCode)
	}
	if !strings.Contains(started.QRPayload, "ticket_id=pair_test") {
		t.Fatalf("QRPayload = %q, want ticket id", started.QRPayload)
	}
	if ticketPayload.DeviceID == "" {
		t.Fatal("pairing ticket payload missing device_id")
	}
	if ticketPayload.UniqueCode == "" {
		t.Fatal("pairing ticket payload missing unique_code")
	}
	if ticketPayload.DeviceName == "" {
		t.Fatal("pairing ticket payload missing device_name")
	}
	if ticketPayload.Platform == "" {
		t.Fatal("pairing ticket payload missing platform")
	}
	if len(ticketPayload.Capabilities) == 0 {
		t.Fatal("pairing ticket payload missing capabilities")
	}
	if len(ticketPayload.Tools) == 0 {
		t.Fatal("pairing ticket payload missing tools")
	}
	if len(ticketPayload.History) == 0 {
		t.Fatal("pairing ticket payload missing history")
	}

	status, err := service.BindingStatus(started.SessionID)
	if err != nil {
		t.Fatalf("BindingStatus() error = %v", err)
	}
	if !pairingResultCalled {
		t.Fatal("pairing result endpoint was not called")
	}
	if !status.Bound || status.Device == nil {
		t.Fatalf("BindingStatus() did not bind device: %#v", status)
	}
	if status.Device.DeviceID != "dev-approved" {
		t.Fatalf("DeviceID = %q, want dev-approved", status.Device.DeviceID)
	}

	agentStatus := service.Status()
	if !agentStatus.Enabled || !agentStatus.Registered || !agentStatus.Bound {
		t.Fatalf("Status() did not reflect registered device: %#v", agentStatus)
	}
	if agentStatus.Device == nil || agentStatus.Device.Status == "" {
		t.Fatalf("Status() missing device status: %#v", agentStatus.Device)
	}
	service.Disable()
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
		"cwd":        os.TempDir(),
	}, writeJSON)

	waitForAgentEvent(t, &mu, &events, "terminal.created", func(event map[string]interface{}) bool {
		return event["session_id"] == "term_test"
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

func TestAgentAIManagerRunsFakeCodex(t *testing.T) {
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
		"project_path": os.TempDir(),
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
