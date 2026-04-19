package handlers

import (
	"encoding/json"
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

	handler := NewStatusHandler(nil, nil, tracker)
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
