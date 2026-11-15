package handlers

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/statistic"
)

func TestHandleGetSummary_StripsDomainLevelAIFields(t *testing.T) {
	tracker := statistic.NewAIActivityTracker(statistic.DefaultAIActivityTTL)
	tracker.RecordMetadata(&M.Metadata{
		HostName: "api.openai.com",
		Route:    "RouteToALiang",
		DNSInfo: &M.DNSInfo{
			BindingSource: M.BindingSourceSNI,
		},
	})

	handler := NewStatusHandler(nil, nil, tracker, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status/summary", nil)

	handler.HandleGetSummary(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload.data missing or wrong type: %T", payload["data"])
	}

	ai, ok := data["ai"].(map[string]any)
	if !ok {
		t.Fatalf("payload.data.ai missing or wrong type: %T", data["ai"])
	}

	for _, field := range []string{"lastDomain", "lastHost", "lastSource", "lastRoute", "lastMatchedVia", "trackedPatterns"} {
		if _, exists := ai[field]; exists {
			t.Fatalf("unexpected AI summary field %q leaked into public response", field)
		}
	}

	recent, ok := ai["recentProviderTraffic"].([]any)
	if !ok || len(recent) != 1 {
		t.Fatalf("recentProviderTraffic = %#v, want one item", ai["recentProviderTraffic"])
	}

	detection, ok := recent[0].(map[string]any)
	if !ok {
		t.Fatalf("recentProviderTraffic[0] wrong type: %T", recent[0])
	}

	for _, field := range []string{"domain", "recentHost", "source", "route", "matchedVia"} {
		if _, exists := detection[field]; exists {
			t.Fatalf("unexpected detection field %q leaked into public response", field)
		}
	}

	if got := detection["providerLabel"]; got != "OpenAI" {
		t.Fatalf("providerLabel = %v, want OpenAI", got)
	}
	if got := detection["active"]; got != true {
		t.Fatalf("active = %v, want true", got)
	}
	if got := ai["lastLabel"]; got != "OpenAI" {
		t.Fatalf("lastLabel = %v, want OpenAI", got)
	}
}

func TestHandleGetSummary_IncludesSSHChainStatus(t *testing.T) {
	handler := NewStatusHandler(nil, nil, statistic.NewAIActivityTracker(statistic.DefaultAIActivityTTL), nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status/summary", nil)

	handler.HandleGetSummary(recorder, request)

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload.data missing: %T", payload["data"])
	}
	ssh, ok := data["ssh"].(map[string]any)
	if !ok {
		t.Fatalf("payload.data.ssh missing: %T", data["ssh"])
	}
	// 测试进程注册表未注册 SSH 出口:必须返回 ok=false 的「未配置」态,
	// 而非报错或缺字段。
	if got, exists := ssh["ok"]; !exists || got != false {
		t.Fatalf("ssh.ok = %v (exists=%v), want false", got, exists)
	}
	if chains, exists := ssh["chains"]; !exists || chains == nil {
		t.Fatalf("ssh.chains missing: %v", exists)
	}
}

func TestHandleGetConnections_ListsActiveAndClosed(t *testing.T) {
	manager := statistic.NewTestManager()
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	conn := statistic.NewTCPTracker(server, &M.Metadata{
		Network:  M.TCP,
		HostName: "example.com",
		Route:    "RouteToALiang",
	}, manager)
	_ = conn.Close()

	handler := NewStatusHandler(nil, nil, statistic.NewAIActivityTracker(statistic.DefaultAIActivityTTL), manager)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/status/connections", nil)

	handler.HandleGetConnections(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("payload.data missing: %T", payload["data"])
	}
	closed, ok := data["closed"].([]any)
	if !ok || len(closed) != 1 {
		t.Fatalf("data.closed = %#v, want one record", data["closed"])
	}
	record, ok := closed[0].(map[string]any)
	if !ok {
		t.Fatalf("closed[0] wrong type: %T", closed[0])
	}
	if got := record["route"]; got != "RouteToALiang" {
		t.Fatalf("closed[0].route = %v", got)
	}
	if got := record["host"]; got != "example.com" {
		t.Fatalf("closed[0].host = %v", got)
	}
	active, ok := data["active"].([]any)
	if !ok || len(active) != 0 {
		t.Fatalf("data.active = %#v, want empty", data["active"])
	}
}
