package cmd

import (
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/statistic"
)

func TestBuildAIStatusPayloadMatchesProviderKeysCaseInsensitively(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer config.ResetGlobalConfigForTest()
	statistic.GetDefaultAIActivityTracker().Reset()
	defer statistic.GetDefaultAIActivityTracker().Reset()

	enabled := true
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"Anthropic": {
					Label:   "Anthropic",
					Enble:   &enabled,
					Include: []string{"api.anthropic.com"},
				},
			},
		},
	})

	statistic.GetDefaultAIActivityTracker().RecordMetadataAt(&M.Metadata{
		ConnID:   "anthropic-1",
		HostName: "api.anthropic.com",
		Route:    "RouteToALiang",
		DNSInfo: &M.DNSInfo{
			BindingSource: M.BindingSourceSNI,
		},
	}, time.Now())

	payload := buildAIStatusPayload()
	if len(payload) != 1 {
		t.Fatalf("len(payload) = %d, want 1", len(payload))
	}
	if got := payload[0]["key"]; got != "anthropic" {
		t.Fatalf("payload[0][key] = %q, want anthropic", got)
	}
	if got := payload[0]["detected"]; got != true {
		t.Fatalf("payload[0][detected] = %v, want true", got)
	}
	if got := payload[0]["active"]; got != true {
		t.Fatalf("payload[0][active] = %v, want true", got)
	}
}
