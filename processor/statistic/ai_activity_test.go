package statistic

import (
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/config"
)

func TestAIActivityTracker_RecordMetadataAndTTL(t *testing.T) {
	tracker := NewAIActivityTracker(15 * time.Second)
	seenAt := time.Date(2026, 4, 19, 12, 0, 0, 0, time.UTC)

	tracker.RecordMetadataAt(&M.Metadata{
		ConnID:   "tcp-1",
		HostName: "api.openai.com",
		Route:    "RouteToALiang",
		DNSInfo: &M.DNSInfo{
			BindingSource: M.BindingSourceSNI,
		},
	}, seenAt)

	summary := tracker.SummaryAt(seenAt.Add(5 * time.Second))
	if !summary.Active {
		t.Fatal("expected summary to be active")
	}
	if summary.ActiveCount != 1 {
		t.Fatalf("summary.ActiveCount = %d, want 1", summary.ActiveCount)
	}
	if got := summary.ActiveDetections[0].ProviderLabel; got != "OpenAI" {
		t.Fatalf("summary.ActiveDetections[0].ProviderLabel = %q, want OpenAI", got)
	}
	if got := summary.ActiveDetections[0].RecentHost; got != "api.openai.com" {
		t.Fatalf("summary.ActiveDetections[0].RecentHost = %q, want api.openai.com", got)
	}
	if !summary.ActiveDetections[0].DetectedBySNI {
		t.Fatal("expected detection source to be SNI")
	}
	if got := summary.ActiveDetections[0].RemainingTTL; got != 15 {
		t.Fatalf("summary.ActiveDetections[0].RemainingTTL = %d, want 15", got)
	}
	if got := summary.ActiveDetections[0].ActiveConnectionCount; got != 1 {
		t.Fatalf("summary.ActiveDetections[0].ActiveConnectionCount = %d, want 1", got)
	}
	if len(summary.RecentProviderTraffic) != 1 {
		t.Fatalf("len(summary.RecentProviderTraffic) = %d, want 1", len(summary.RecentProviderTraffic))
	}
	if !summary.RecentProviderTraffic[0].Active {
		t.Fatal("expected recent provider traffic to remain active inside TTL")
	}

	tracker.CompleteMetadata(&M.Metadata{ConnID: "tcp-1", HostName: "api.openai.com"}, seenAt.Add(30*time.Second))

	summary = tracker.SummaryAt(seenAt.Add(40 * time.Second))
	if !summary.Active {
		t.Fatal("expected summary to stay active within close buffer")
	}
	if got := summary.ActiveDetections[0].ActiveConnectionCount; got != 0 {
		t.Fatalf("summary.ActiveDetections[0].ActiveConnectionCount = %d, want 0", got)
	}
	if got := summary.ActiveDetections[0].LastConnectionDurationSeconds; got != 30 {
		t.Fatalf("summary.ActiveDetections[0].LastConnectionDurationSeconds = %d, want 30", got)
	}

	summary = tracker.SummaryAt(seenAt.Add(46 * time.Second))
	if summary.Active {
		t.Fatal("expected summary to expire after close buffer")
	}
	if summary.ActiveCount != 0 {
		t.Fatalf("summary.ActiveCount = %d, want 0", summary.ActiveCount)
	}
	if len(summary.ActiveDetections) != 0 {
		t.Fatalf("len(summary.ActiveDetections) = %d, want 0", len(summary.ActiveDetections))
	}
	if len(summary.RecentProviderTraffic) != 1 {
		t.Fatalf("len(summary.RecentProviderTraffic) = %d, want 1", len(summary.RecentProviderTraffic))
	}
	if summary.RecentProviderTraffic[0].Active {
		t.Fatal("expected provider to remain visible but inactive after TTL")
	}
	if got := summary.RecentProviderTraffic[0].RemainingTTL; got != 0 {
		t.Fatalf("summary.RecentProviderTraffic[0].RemainingTTL = %d, want 0", got)
	}
	if got := summary.LastHost; got != "api.openai.com" {
		t.Fatalf("summary.LastHost = %q, want api.openai.com", got)
	}

	summary = tracker.SummaryAt(seenAt.Add(10*time.Minute + 31*time.Second))
	if len(summary.RecentProviderTraffic) != 0 {
		t.Fatalf("len(summary.RecentProviderTraffic) = %d, want 0", len(summary.RecentProviderTraffic))
	}
}

func TestAIActivityTracker_RecordMetadataMatchesConfiguredDomains(t *testing.T) {
	config.ResetGlobalConfigForTest()
	defer config.ResetGlobalConfigForTest()

	enabled := true
	config.SetGlobalConfig(&config.Config{
		Customer: &config.CustomerConfig{
			AIRules: map[string]*config.CustomerAIRuleSetting{
				"vscode": {
					Enble:   &enabled,
					Include: []string{"https://api.githubcopilot.com", "marketplace.visualstudio.com"},
				},
			},
		},
	})

	tracker := NewAIActivityTracker(15 * time.Second)
	metadata := &M.Metadata{
		HostName: "api.githubcopilot.com",
		Route:    "RouteToSocks",
		DNSInfo: &M.DNSInfo{
			BindingSource: M.BindingSourceSNI,
		},
	}

	tracker.RecordMetadata(metadata)
	summary := tracker.SummaryAt(time.Now())
	if summary.ActiveCount != 1 {
		t.Fatalf("summary.ActiveCount = %d, want 1", summary.ActiveCount)
	}
	if got := summary.ActiveDetections[0].ProviderKey; got != "vscode" {
		t.Fatalf("summary.ActiveDetections[0].ProviderKey = %q, want vscode", got)
	}
	if got := summary.ActiveDetections[0].ProviderLabel; got != "Copilot" {
		t.Fatalf("summary.ActiveDetections[0].ProviderLabel = %q, want Copilot", got)
	}
	if got := summary.ActiveDetections[0].Domain; got != "api.githubcopilot.com" {
		t.Fatalf("summary.ActiveDetections[0].Domain = %q, want api.githubcopilot.com", got)
	}
	if len(summary.RecentProviderTraffic) != 1 {
		t.Fatalf("len(summary.RecentProviderTraffic) = %d, want 1", len(summary.RecentProviderTraffic))
	}
	if got := matchTrackedAIDomain("sub.marketplace.visualstudio.com"); got != "marketplace.visualstudio.com" {
		t.Fatalf("matchTrackedAIDomain returned %q, want marketplace.visualstudio.com", got)
	}
	if got := matchTrackedAIDomain("api.openai.com"); got != "api.openai.com" {
		t.Fatalf("matchTrackedAIDomain returned %q, want api.openai.com", got)
	}
	if got := matchTrackedAIDomain("example.org"); got != "" {
		t.Fatalf("matchTrackedAIDomain returned %q, want empty string", got)
	}
}
