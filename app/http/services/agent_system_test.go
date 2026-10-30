package services

// 设备画像冷/热层(spec §4.2/§4.3)单测:profile_hash 稳定性与敏感度、
// net 快照过滤(非回环/非链路本地)、摘要格式。

import (
	"strings"
	"testing"
)

func TestAgentProfileHash_StableAndOrderInsensitive(t *testing.T) {
	a := agentProfileHash("darwin-arm64", []string{"peer_v1", "terminal"}, []string{"claude-code@2.1.224", "codex@0.9.1"}, []string{"~/work", "~/src"})
	b := agentProfileHash("darwin-arm64", []string{"terminal", "peer_v1"}, []string{"codex@0.9.1", "claude-code@2.1.224"}, []string{"~/src", "~/work"})
	if a == "" || a != b {
		t.Fatalf("hash must be stable under reordering: %q vs %q", a, b)
	}
	if len(a) != 32 {
		t.Fatalf("hash length = %d, want 32 hex chars (16 bytes)", len(a))
	}
}

func TestAgentProfileHash_ChangesWithColdLayer(t *testing.T) {
	base := agentProfileHash("darwin-arm64", []string{"peer_v1"}, []string{"codex@0.9.1"}, []string{"~/work"})
	cases := map[string]string{
		"platform":   agentProfileHash("windows-amd64", []string{"peer_v1"}, []string{"codex@0.9.1"}, []string{"~/work"}),
		"capability": agentProfileHash("darwin-arm64", []string{"peer_v1", "peer_path_v1"}, []string{"codex@0.9.1"}, []string{"~/work"}),
		"tool ver":   agentProfileHash("darwin-arm64", []string{"peer_v1"}, []string{"codex@0.9.2"}, []string{"~/work"}),
		"auth dirs":  agentProfileHash("darwin-arm64", []string{"peer_v1"}, []string{"codex@0.9.1"}, []string{"~/work", "~/src"}),
	}
	for name, got := range cases {
		if got == base {
			t.Fatalf("%s change must alter the profile hash", name)
		}
	}
}

func TestCollectAgentNetSnapshot_FiltersLoopbackAndLinkLocal(t *testing.T) {
	snap := collectAgentNetSnapshot()
	for _, iface := range snap.Interfaces {
		if iface.IPv4 == "" {
			t.Fatalf("empty ipv4 in %+v", iface)
		}
		if strings.HasPrefix(iface.IPv4, "127.") {
			t.Fatalf("loopback leaked: %+v", iface)
		}
		if strings.HasPrefix(iface.IPv4, "169.254.") {
			t.Fatalf("link-local leaked: %+v", iface)
		}
		if iface.PrefixLen <= 0 || iface.PrefixLen > 32 {
			t.Fatalf("bad prefix: %+v", iface)
		}
	}
	if snap.CollectedAt == "" {
		t.Fatal("collected_at must be set")
	}
}

func TestCollectAgentNetSnapshot_CachedWithinTTL(t *testing.T) {
	first := collectAgentNetSnapshot()
	second := collectAgentNetSnapshot()
	// 2s TTL 内两次调用共用同一快照(同一拍 hello+heartbeat 不重复枚举)。
	if first.CollectedAt != second.CollectedAt {
		t.Fatalf("expected cached snapshot, got %q then %q", first.CollectedAt, second.CollectedAt)
	}
}

func TestAgentSystemSummary_OmitsMissingFields(t *testing.T) {
	if got := agentSystemSummary(agentSystemSnapshot{}); got != "" {
		t.Fatalf("empty snapshot must yield empty summary, got %q", got)
	}
	got := agentSystemSummary(agentSystemSnapshot{
		OSName:        "windows",
		OSVersion:     "11 Pro",
		Arch:          "amd64",
		KernelVersion: "10.0.26100",
		CPUModel:      "AMD Ryzen 9 5900X",
		MemTotalBytes: 64 << 30,
	})
	for _, want := range []string{"windows 11 Pro", "amd64", "kernel 10.0.26100", "AMD Ryzen 9 5900X", "65536 MiB RAM"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q missing %q", got, want)
		}
	}
}

func TestAgentNetSummary_Format(t *testing.T) {
	if got := agentNetSummary(agentNetSnapshot{}); got != "" {
		t.Fatalf("empty net must yield empty summary, got %q", got)
	}
	got := agentNetSummary(agentNetSnapshot{Interfaces: []agentNetInterface{
		{Name: "en0", IPv4: "192.168.1.23", PrefixLen: 24},
		{Name: "en1", IPv4: "10.8.0.4", PrefixLen: 16},
	}})
	if want := "en0=192.168.1.23/24, en1=10.8.0.4/16"; got != want {
		t.Fatalf("net summary = %q, want %q", got, want)
	}
}
