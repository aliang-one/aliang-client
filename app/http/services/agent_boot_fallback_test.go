package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"aliang.one/nursorgate/common/cache"
)

// TestAgentShouldBootFallbackReconnect pins the boot-reconnect decision: the
// session owner only forwards authority TRANSITIONS, so an agent (re)started
// while the owner's state is quiet — the normal case after an agent crash,
// upgrade, or the owner simply having been running for days — used to wait in
// "awaiting session owner sync" forever and the device showed offline on the
// phone. With enabled+registered persisted state and no session event seen,
// the agent must attempt the connection itself (the server stays the judge of
// token validity).
func TestAgentShouldBootFallbackReconnect(t *testing.T) {
	assert.True(t, agentShouldBootFallbackReconnect(true, true, false),
		"enabled+registered with no session event: fallback must fire")
	assert.False(t, agentShouldBootFallbackReconnect(true, true, true),
		"session event already received: the owner is driving, no fallback")
	assert.False(t, agentShouldBootFallbackReconnect(false, true, false),
		"disabled device must not self-connect")
	assert.False(t, agentShouldBootFallbackReconnect(true, false, false),
		"unregistered device must not self-connect")
}

// TestApplySessionEventMarksBootSeen keeps the two paths from racing: once the
// session owner has spoken, the boot fallback must stand down.
func TestApplySessionEventMarksBootSeen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	// NewAgentService persists state; without this reset the test writes
	// through to the real ~/.aliang/agent/agent_state.json and wipes the
	// device registration (this exact bug happened on 2026-09-01).
	cache.ResetCacheDirForTest()
	service := NewAgentService()

	assert.False(t, service.bootSessionEventSeen.Load())
	service.ApplySessionEvent("active", "login")
	assert.True(t, service.bootSessionEventSeen.Load(),
		"any forwarded transition must mark the boot fallback as superseded")
}

// TestAgentBootGraceAllowsOverride documents the grace window constant used by
// the fallback scheduler (kept under half a minute so an upgrade restart
// recovers before the server's liveness timer flags the device offline).
func TestAgentBootGraceAllowsOverride(t *testing.T) {
	assert.Greater(t, agentBootReconnectGrace, 5*time.Second)
	assert.LessOrEqual(t, agentBootReconnectGrace, 30*time.Second)
}
