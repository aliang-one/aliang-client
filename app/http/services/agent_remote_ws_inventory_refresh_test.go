package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/cache"
)

// TestHandleRemoteInventoryRefreshSendsHello closes the server→agent rescan
// nudge: PhoneServer(手机端「重扫电脑」)publishes agent.inventory.refresh
// and this agent must answer by pushing a full hello immediately instead of
// waiting for the 10s digest tick or the 60s periodic backstop. Read-only,
// so it deliberately sits OUTSIDE the enabled-device gate (same tier as
// hello-class traffic).
func TestHandleRemoteInventoryRefreshSendsHello(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cache.ResetCacheDirForTest()
	service := NewAgentService()

	var hellos []map[string]interface{}
	writeJSON := func(v interface{}) error {
		if m, ok := v.(map[string]interface{}); ok && m["type"] == models.AgentEventHello {
			hellos = append(hellos, m)
		}
		return nil
	}

	service.handleRemoteAgentMessage(map[string]interface{}{
		"type": models.AgentEventInventoryRefresh,
	}, writeJSON)

	require.Len(t, hellos, 1, "inventory refresh must push exactly one hello")
	// 不在 enabled-device 门控名单:未启用状态下也必须响应(只读动作)。
	assert.False(t, remoteAgentMessageRequiresEnabledDevice(models.AgentEventInventoryRefresh))
}
