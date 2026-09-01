package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/common/cache"
)

// TestHandleRemoteAIRenameWritesCacheAndAcks closes the broken phone→agent
// direction: PhoneServer publishes ai.session.rename (see PhoneServer
// agentPublish.ts) which this agent previously ignored entirely. Handling must
// persist the rename (origin=phone, agent clock) and ack, so PhoneServer's
// future latestOf guard has an authoritative timestamp to compare against.
func TestHandleRemoteAIRenameWritesCacheAndAcks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
	// ai.session.rename sits behind the enabled-device gate like every other
	// control op; arm the state the way the gate expects.
	service.mu.Lock()
	service.state.Enabled = true
	service.state.Registered = true
	service.mu.Unlock()

	var acks []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok && m["type"] == "ai.session.rename.ack" {
			acks = append(acks, m)
		}
		return nil
	}

	before := time.Now().UTC().Add(-time.Second)
	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        "conv-123",
		"title":             "手机端新标题",
		"source_session_id": "native-sid-abc",
		"provider":          "claude",
	}, writeJSON)
	after := time.Now().UTC().Add(time.Second)

	require.Len(t, acks, 1, "rename must be acknowledged")
	assert.Equal(t, "conv-123", acks[0]["session_id"])
	assert.Equal(t, true, acks[0]["accepted"])

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	entry, ok := cached["native-sid-abc"]
	require.True(t, ok, "phone rename must be persisted under the native session id")
	assert.Equal(t, "手机端新标题", entry.Name)
	assert.Equal(t, "phone", entry.Origin)
	ts, err := time.Parse(time.RFC3339Nano, entry.UpdatedAt)
	require.NoError(t, err)
	assert.False(t, ts.Before(before), "timestamp must come from the agent clock, not the sender")
	assert.False(t, ts.After(after))
}

// TestRemoteAgentMessageRequiresEnabledDeviceIncludesRename keeps the rename
// mutation behind the same enabled-device gate as the other control ops.
func TestRemoteAgentMessageRequiresEnabledDeviceIncludesRename(t *testing.T) {
	assert.True(t, remoteAgentMessageRequiresEnabledDevice("ai.session.rename"))
	assert.False(t, remoteAgentMessageRequiresEnabledDevice("ai.session.rename.ack"))
}
