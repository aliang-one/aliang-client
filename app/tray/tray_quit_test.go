package tray

import (
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
	"aliang.one/nursorgate/processor/statistic"
)

func TestTrayModeDisplayName(t *testing.T) {
	testCases := []struct {
		mode string
		want string
	}{
		{mode: "http", want: "Regular Mode"},
		{mode: "tun", want: "Deep Mode"},
		{mode: "unknown", want: "Unknown Mode"},
		{mode: "", want: "Unknown Mode"},
	}

	for _, tc := range testCases {
		if got := trayModeDisplayName(tc.mode); got != tc.want {
			t.Fatalf("trayModeDisplayName(%q) = %q, want %q", tc.mode, got, tc.want)
		}
	}
}

func TestTrayProxyStatusTitleHidesSelectedNotRunningMode(t *testing.T) {
	testCases := []struct {
		name        string
		mode        string
		running     bool
		description string
		want        string
	}{
		{
			name:        "deep mode selected not running",
			mode:        "tun",
			description: "Deep Mode is selected, service not running",
			want:        "Status: deep-stopped",
		},
		{
			name:        "regular mode selected not running",
			mode:        "http",
			description: "Regular Mode is selected, service not running",
			want:        "Status: regular-stopped",
		},
		{
			name:    "empty running",
			mode:    "tun",
			running: true,
			want:    "Status: deep-running",
		},
		{
			name:        "specific status preserved",
			mode:        "tun",
			running:     true,
			description: "Deep Mode is running",
			want:        "Status: deep-running",
		},
	}

	for _, tc := range testCases {
		if got := trayProxyStatusTitle(tc.mode, tc.running, tc.description); got != tc.want {
			t.Fatalf("%s: trayProxyStatusTitle() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestBuildAIStatusFromTrackerMatchesProviderKeysCaseInsensitively(t *testing.T) {
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

	statuses := BuildAIStatusFromTracker()
	if len(statuses) != 1 {
		t.Fatalf("len(statuses) = %d, want 1", len(statuses))
	}
	if got := statuses[0].Key; got != "anthropic" {
		t.Fatalf("statuses[0].Key = %q, want anthropic", got)
	}
	if !statuses[0].Detected || !statuses[0].Active {
		t.Fatalf("statuses[0] detection mismatch: detected=%v active=%v, want both true", statuses[0].Detected, statuses[0].Active)
	}
}

func TestAIStatusMenuIconUsesProviderIcon(t *testing.T) {
	testCases := []struct {
		name     string
		provider AIProviderStatus
		want     []byte
	}{
		{
			name:     "openai detected",
			provider: AIProviderStatus{Key: "openai", Enabled: true, Detected: true},
			want:     GetProviderIcon("openai", true),
		},
		{
			name:     "anthropic alias",
			provider: AIProviderStatus{Key: "Claude", Enabled: true, Detected: true},
			want:     GetProviderIcon("anthropic", true),
		},
		{
			name:     "vscode inactive",
			provider: AIProviderStatus{Key: "vscode", Enabled: true},
			want:     GetProviderIcon("vscode", false),
		},
		{
			name:     "unknown",
			provider: AIProviderStatus{Key: "unknown", Enabled: true, Detected: true},
			want:     nil,
		},
	}

	for _, tc := range testCases {
		got := AIStatusMenuIcon(tc.provider)
		if string(got) != string(tc.want) {
			t.Fatalf("%s: AIStatusMenuIcon() returned unexpected icon bytes", tc.name)
		}
	}
}

func TestIsAcceptableQuitProxyStopResult(t *testing.T) {
	testCases := []struct {
		name   string
		result map[string]interface{}
		want   bool
	}{
		{
			name: "success",
			result: map[string]interface{}{
				"status": "success",
			},
			want: true,
		},
		{
			name: "already stopped",
			result: map[string]interface{}{
				"status": "failed",
				"error":  "not_running",
			},
			want: true,
		},
		{
			name: "other failure",
			result: map[string]interface{}{
				"status": "failed",
				"error":  "stop_failed",
			},
			want: false,
		},
		{
			name:   "nil result",
			result: nil,
			want:   false,
		},
	}

	for _, tc := range testCases {
		if got := isAcceptableQuitProxyStopResult(tc.result); got != tc.want {
			t.Fatalf("%s: isAcceptableQuitProxyStopResult() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
