package services

import (
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

func TestConfigureTunnelRejectsAnotherDevice(t *testing.T) {
	svc := &AgentService{
		identity: &agentDeviceIdentity{DeviceID: "dev_local"},
		state:    agentState{DeviceID: "dev_local", Enabled: true, Registered: true},
	}
	var response map[string]interface{}
	svc.configureTunnel(map[string]interface{}{
		"request_id": "req_1",
		"device_id":  "dev_other",
	}, func(payload interface{}) error {
		response = payload.(map[string]interface{})
		return nil
	})
	if response["type"] != models.AgentEventTunnelError {
		t.Fatalf("unexpected response: %#v", response)
	}
	if response["request_id"] != "req_1" {
		t.Fatalf("missing request correlation: %#v", response)
	}
}

func TestTunnelConfigureRequiresEnabledDevice(t *testing.T) {
	if !remoteAgentMessageRequiresEnabledDevice(models.AgentEventTunnelConfigure) {
		t.Fatal("tunnel.configure must require an enabled, registered device")
	}
}

func TestAgentCapabilitiesAdvertiseHTTPAndWebSocketTunnels(t *testing.T) {
	capabilities := agentCapabilities()
	if !stringSliceContains(capabilities, "http_tunnel_v1") {
		t.Fatal("http_tunnel_v1 capability is missing")
	}
	if !stringSliceContains(capabilities, "websocket_tunnel_v1") {
		t.Fatal("websocket_tunnel_v1 capability is missing")
	}
}
