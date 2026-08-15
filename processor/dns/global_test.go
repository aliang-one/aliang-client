package dns

import (
	"testing"
	"time"
)

func TestSetGlobalResolverClosesPreviousResolver(t *testing.T) {
	ResetGlobalResolver()
	t.Cleanup(ResetGlobalResolver)

	previous := NewBaseResolver(DefaultDNSConfig())
	current := NewBaseResolver(DefaultDNSConfig())
	SetGlobalResolver(previous)
	SetGlobalResolver(current)

	assertResolverClosed(t, previous)
	assertResolverOpen(t, current)

	SetGlobalResolver(current)
	assertResolverOpen(t, current)
}

func TestResetGlobalResolverClosesCurrentResolver(t *testing.T) {
	ResetGlobalResolver()
	t.Cleanup(ResetGlobalResolver)

	resolver := NewBaseResolver(DefaultDNSConfig())
	SetGlobalResolver(resolver)
	ResetGlobalResolver()

	assertResolverClosed(t, resolver)
	if GetGlobalResolver() != nil {
		t.Fatal("global resolver was not cleared")
	}
}

func assertResolverClosed(t *testing.T, resolver *BaseResolver) {
	t.Helper()
	select {
	case <-resolver.cleanupCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("resolver cleanup context was not canceled")
	}
}

func assertResolverOpen(t *testing.T, resolver *BaseResolver) {
	t.Helper()
	select {
	case <-resolver.cleanupCtx.Done():
		t.Fatal("current resolver was unexpectedly closed")
	default:
	}
}
