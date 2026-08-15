package model

import (
	"strings"
	"testing"
)

func TestLegacyReject_RoutingRulesConfig_AliasNoneLaneToDoorRejected(t *testing.T) {
	legacyAliasJSON := []byte(`{
		"none_lane": {
			"set_type": "none_lane",
			"rules": []
		},
		"to_door": {
			"set_type": "to_door",
			"rules": []
		},
		"black_list": {
			"set_type": "black_list",
			"rules": []
		},
		"aliang": {
			"set_type": "aliang",
			"rules": []
		},
		"settings": {
			"aliang_enabled": true,
			"socks_enabled": true,
			"geoip_enabled": false,
			"auto_update": true
		},
		"version": 1,
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z"
	}`)

	_, err := NewRoutingRulesConfigFromJSON(legacyAliasJSON)
	if err == nil {
		t.Fatalf("expected legacy none_lane/to_door payload to fail validation")
	}
	if !strings.Contains(err.Error(), "to_socks validation failed") {
		t.Fatalf("expected to_socks validation failure for legacy alias payload, got: %v", err)
	}
}

func TestLegacyReject_RoutingRuleSet_LegacySetTypesRejected(t *testing.T) {
	tests := []struct {
		name    string
		setType SetType
	}{
		{name: "none_lane rejected", setType: SetType("none_lane")},
		{name: "to_door rejected", setType: SetType("to_door")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := &RoutingRuleSet{SetType: tt.setType}
			err := rs.Validate()
			if err == nil {
				t.Fatalf("expected set_type=%q to be rejected", tt.setType)
			}
			if err.Error() != "invalid set type" {
				t.Fatalf("expected invalid set type error, got: %v", err)
			}
		})
	}
}
