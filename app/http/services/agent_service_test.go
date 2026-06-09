package services

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		DeviceName   string   `json:"device_name"`
		Platform     string   `json:"platform"`
		AgentVersion string   `json:"agent_version"`
		Capabilities []string `json:"capabilities"`
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
	if ticketPayload.DeviceName == "" {
		t.Fatal("pairing ticket payload missing device_name")
	}
	if ticketPayload.Platform == "" {
		t.Fatal("pairing ticket payload missing platform")
	}
	if len(ticketPayload.Capabilities) == 0 {
		t.Fatal("pairing ticket payload missing capabilities")
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
