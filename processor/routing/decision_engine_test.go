package routing

import (
	"errors"
	"net"
	"testing"
)

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
}
