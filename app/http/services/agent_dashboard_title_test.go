package services

import (
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"

	"github.com/stretchr/testify/require"
)

func TestDashboardRenamePersistsJournalTitleAndPublishesToCloudAndBrowser(t *testing.T) {
	setTestAgentHome(t)
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	const id = "local-dashboard-title"
	writeCoreJournalRawFixture(t, id, `{"kind":"session","session_id":"local-dashboard-title","project_path":"/tmp/p","origin":"dashboard"}`)
	s := &AgentService{ai: newAgentAIManager()}
	browser := make(chan map[string]interface{}, 1)
	socketID := s.RegisterLocalAIChatSocket(func(payload interface{}) error {
		browser <- payload.(map[string]interface{})
		return nil
	})
	t.Cleanup(func() { s.UnregisterLocalAIChatSocket(socketID) })
	var cloud, ack map[string]interface{}
	s.setCurrentRemoteWriter(func(payload interface{}) error {
		cloud = payload.(map[string]interface{})
		return nil
	})
	s.DispatchLocalAI(map[string]interface{}{
		"type": "ai.session.rename", "session_id": id, "title": "  New   title  ", "request_id": "req-1",
	}, func(payload interface{}) error {
		ack = payload.(map[string]interface{})
		return nil
	})
	require.Equal(t, "ai.session.rename.ack", ack["type"])
	require.Equal(t, "req-1", ack["request_id"])
	require.Equal(t, true, ack["accepted"])
	require.Equal(t, "New title", loadAgentRenameCache()[id].Name)
	require.Equal(t, "ai.session.title.updated", cloud["type"])
	require.Equal(t, "New title", cloud["title"])
	require.Equal(t, id, cloud["session_id"])
	select {
	case update := <-browser:
		require.Equal(t, cloud, update)
	case <-time.After(time.Second):
		t.Fatal("other browser did not receive title change")
	}
}

func TestDashboardRenameRejectsUnknownJournal(t *testing.T) {
	setTestAgentHome(t)
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	cache.ResetCacheDirForTest()
	t.Cleanup(cache.ResetCacheDirForTest)
	s := &AgentService{ai: newAgentAIManager()}
	var ack map[string]interface{}
	s.DispatchLocalAI(map[string]interface{}{
		"type": "ai.session.rename", "session_id": "local-unknown", "title": "New title", "request_id": "req-1",
	}, func(payload interface{}) error {
		ack = payload.(map[string]interface{})
		return nil
	})
	require.Equal(t, false, ack["accepted"])
	require.Empty(t, loadAgentRenameCache())
}
