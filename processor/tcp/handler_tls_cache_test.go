package tcp

import (
	"errors"
	"io"
	"net/netip"
	"syscall"
	"testing"
	"time"

	cachepkg "aliang.one/nursorgate/processor/cache"
)

func TestSelectUniqueCachedDomainEntry_Empty(t *testing.T) {
	entry, uniqueDomains := selectUniqueCachedDomainEntry(nil)
	if entry != nil {
		t.Fatalf("expected nil entry, got %+v", entry)
	}
	if uniqueDomains != 0 {
		t.Fatalf("expected 0 unique domains, got %d", uniqueDomains)
	}
}

func TestSelectUniqueCachedDomainEntry_UsesSingleUniqueDomain(t *testing.T) {
	entry, uniqueDomains := selectUniqueCachedDomainEntry([]*cachepkg.CacheEntry{
		{
			Domain:    "telemetry.individual.githubcopilot.com",
			IP:        netip.MustParseAddr("140.82.113.21"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
	})
	if entry == nil {
		t.Fatal("expected cached entry to be selected")
	}
	if got, want := entry.Domain, "telemetry.individual.githubcopilot.com"; got != want {
		t.Fatalf("unexpected cached domain: got %q want %q", got, want)
	}
	if uniqueDomains != 1 {
		t.Fatalf("expected 1 unique domain, got %d", uniqueDomains)
	}
}

func TestSelectUniqueCachedDomainEntry_RejectsSharedIPDomains(t *testing.T) {
	entry, uniqueDomains := selectUniqueCachedDomainEntry([]*cachepkg.CacheEntry{
		{
			Domain:    "api.individual.githubcopilot.com",
			IP:        netip.MustParseAddr("140.82.113.21"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
		{
			Domain:    "telemetry.individual.githubcopilot.com",
			IP:        netip.MustParseAddr("140.82.113.21"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
	})
	if entry != nil {
		t.Fatalf("expected nil entry for shared-IP cache, got %+v", entry)
	}
	if uniqueDomains != 2 {
		t.Fatalf("expected 2 unique domains, got %d", uniqueDomains)
	}
}

func TestSelectUniqueCachedDomainEntry_DeduplicatesSameDomain(t *testing.T) {
	entry, uniqueDomains := selectUniqueCachedDomainEntry([]*cachepkg.CacheEntry{
		{
			Domain:    "telemetry.individual.githubcopilot.com",
			IP:        netip.MustParseAddr("140.82.113.21"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
		{
			Domain:    "telemetry.individual.githubcopilot.com",
			IP:        netip.MustParseAddr("140.82.113.21"),
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		},
	})
	if entry == nil {
		t.Fatal("expected single deduplicated domain to be selected")
	}
	if uniqueDomains != 1 {
		t.Fatalf("expected 1 unique domain after deduplication, got %d", uniqueDomains)
	}
}

func TestIsExpectedClientDisconnect(t *testing.T) {
	if !isExpectedClientDisconnect(io.EOF) {
		t.Fatal("expected EOF to be treated as client disconnect")
	}
	if !isExpectedClientDisconnect(syscall.ECONNRESET) {
		t.Fatal("expected connection reset to be treated as client disconnect")
	}
	if isExpectedClientDisconnect(errors.New("certificate verify failed")) {
		t.Fatal("did not expect generic certificate failure to be treated as client disconnect")
	}
}
