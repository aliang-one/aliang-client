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

// TestHandleRemoteAIRenameAgentcoreKeysConversationID 收口改名断链的写侧:
// agentcore 清单卡是裸会话 id 卡（journal 文件名键）,inventoryNativeSessionID
// 对无前缀 id 返回空,native id 键的缓存条目对清单不可达。非 claude 家族的
// 改名必须额外以「会话 id」为键落缓存,清单组装处才能按会话 id 读到。
func TestHandleRemoteAIRenameAgentcoreKeysConversationID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
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

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":              "ai.session.rename",
		"session_id":        "conv-agentcore-1",
		"title":             "agentcore改名",
		"source_session_id": "agentcore-1792954925284702588-1",
		"provider":          "agentcore",
	}, writeJSON)

	require.Len(t, acks, 1, "rename must be acknowledged")
	assert.Equal(t, true, acks[0]["accepted"])

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	native, ok := cached["agentcore-1792954925284702588-1"]
	require.True(t, ok, "native id 键必须保留（既有键面不回退）")
	assert.Equal(t, "agentcore改名", native.Name)
	alias, ok := cached["conv-agentcore-1"]
	require.True(t, ok, "会话 id 键必须落缓存（裸 id 清单卡唯一可达键）")
	assert.Equal(t, "agentcore改名", alias.Name)
	assert.Equal(t, "phone", alias.Origin)
	assert.True(t, alias.SkipMirror, "agentcore 不镜像 claude transcript")
}

// TestHandleRemoteAIRenameAgentcoreAcceptsWithoutNativeID 无 native id 且无
// 绑定的 agentcore 会话（journal-only:绑定丢失/未建立）不得整条拒绝——清单
// 卡按会话 id 组织,会话 id 本身就是可用键。claude 家族保持既有拒绝语义。
func TestHandleRemoteAIRenameAgentcoreAcceptsWithoutNativeID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
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

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":       "ai.session.rename",
		"session_id": "conv-agentcore-2",
		"title":      "兜底改名",
		"provider":   "agentcore",
	}, writeJSON)

	require.Len(t, acks, 1)
	assert.Equal(t, true, acks[0]["accepted"], "agentcore 无 native 键时按会话 id 接受,不得拒绝")

	path, err := agentRenameCachePath()
	require.NoError(t, err)
	cached, err := loadAgentRenameCacheFile(path)
	require.NoError(t, err)
	entry, ok := cached["conv-agentcore-2"]
	require.True(t, ok)
	assert.Equal(t, "兜底改名", entry.Name)
	assert.Equal(t, "phone", entry.Origin)
}

// TestHandleRemoteAIRenameClaudeKeepsRejectWithoutNative claude 家族的
// missing-source 拒绝语义不得被 agentcore 兜底放宽波及。
func TestHandleRemoteAIRenameClaudeKeepsRejectWithoutNative(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()
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

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type":       "ai.session.rename",
		"session_id": "conv-claude-x",
		"title":      "claude改名",
		"provider":   "claude",
	}, writeJSON)

	require.Len(t, acks, 1)
	assert.Equal(t, false, acks[0]["accepted"], "claude 无 source_session_id 且无绑定时保持拒绝")
}
