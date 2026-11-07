package routing

import (
	"errors"
	"net"
	"strings"
	"testing"

	"aliang.one/nursorgate/common/model"
	"aliang.one/nursorgate/processor/config"
)

func TestDecideRouteFromSnapshot_MeshCapabilityUnavailable(t *testing.T) {
	// mesh 总开关显式关闭:即使 peers 列表含 dev-b,toNode 规则也不得
	// 编译进快照——能力检查(总开关+peer 存在)在编译期把关。
	cfg := &config.Config{
		Customer: &config.CustomerConfig{
			ProxyRules: []string{"domains,internal.corp,mesh:dev-b"},
			Mesh: &config.MeshConfig{
				Enable: boolPtr(false),
				Peers:  []config.MeshPeer{{DeviceID: "dev-b"}},
			},
		},
	}

	snapshot, err := CompileRuntimeSnapshotFromRuntimeInputs(cfg, model.RulesSettings{})
	if err != nil {
		t.Fatalf("CompileRuntimeSnapshotFromRuntimeInputs() error = %v", err)
	}

	for _, rule := range snapshot.Rules() {
		if strings.HasPrefix(string(rule.Target()), "toNode:") {
			t.Fatalf("mesh 总开关关闭时快照不应含 toNode 规则, got rule %+v", rule)
		}
	}

	decision, err := DecideRouteFromSnapshot(snapshot, &MatchContext{Domain: "internal.corp"})
	if err != nil {
		t.Fatalf("DecideRouteFromSnapshot() error = %v", err)
	}
	if decision != RouteDirect {
		t.Fatalf("decision = %s, want %s (mesh 能力不可用 → 落默认 direct)", decision, RouteDirect)
	}
}

func TestMatchGeoIP(t *testing.T) {
	orig := geoipLookup
	t.Cleanup(func() { geoipLookup = orig })

	t.Run("multi-any-hit", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "JP", nil }
		if !matchGeoIP("CN+HK+JP", "1.2.3.4") {
			t.Fatal("expected hit for JP in CN+HK+JP")
		}
	})
	t.Run("case-adaptation-lowercase-condition", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "CN", nil }
		// 快照层小写化产物 + 结构化逗号串归一
		for _, cond := range []string{"cn", "cn+hk", "cn,hk"} {
			if !matchGeoIP(cond, "1.2.3.4") {
				t.Fatalf("expected hit for condition %q", cond)
			}
		}
	})
	t.Run("miss", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "US", nil }
		if matchGeoIP("CN+HK", "1.2.3.4") {
			t.Fatal("expected miss")
		}
	})
	t.Run("unlisted-ip", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "", nil }
		if matchGeoIP("CN", "1.2.3.4") {
			t.Fatal("expected miss for empty code")
		}
	})
	t.Run("lookup-error", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "", errors.New("not loaded") }
		if matchGeoIP("CN", "1.2.3.4") {
			t.Fatal("expected miss on error")
		}
	})
	t.Run("empty-inputs", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "CN", nil }
		if matchGeoIP("", "1.2.3.4") || matchGeoIP("CN", "") || matchGeoIP("CN", "not-an-ip") {
			t.Fatal("expected miss on empty/invalid inputs")
		}
	})
	t.Run("plus-only-condition", func(t *testing.T) {
		geoipLookup = func(ip net.IP) (string, error) { return "CN", nil }
		if matchGeoIP("+", "1.2.3.4") || matchGeoIP(",", "1.2.3.4") {
			t.Fatal("expected miss for separator-only conditions")
		}
	})
}
