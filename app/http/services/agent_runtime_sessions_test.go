package services

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentServiceRuntimeSessionsReturnsOnlyActiveRuntimeWork(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ai := newAgentAIManager()
	ai.sessions["ai-running"] = &agentAISession{
		id:           "ai-running",
		provider:     "codex",
		model:        "gpt-test",
		projectPath:  "/tmp/project",
		cancel:       func() {},
		lastActiveAt: now,
		history: []agentAIMessage{{
			Role:      "user",
			Content:   "Fix the active session list",
			CreatedAt: now.Add(-time.Minute),
		}},
	}
	ai.sessions["ai-idle"] = &agentAISession{
		id:           "ai-idle",
		provider:     "claude",
		lastActiveAt: now.Add(-time.Hour),
	}

	terminal := newAgentTerminalManager()
	terminal.sessions["terminal-running"] = &agentTerminalSession{
		id:           "terminal-running",
		shell:        "/bin/zsh",
		cwd:          "/tmp/project",
		isPTY:        true,
		startedAt:    now.Add(-5 * time.Minute),
		lastActiveAt: now,
	}

	service := &AgentService{ai: ai, terminal: terminal}
	snapshot := service.RuntimeSessions()

	require.Len(t, snapshot.AIConversations, 1)
	assert.Equal(t, "ai-running", snapshot.AIConversations[0].ID)
	assert.Equal(t, "running", snapshot.AIConversations[0].Status)
	assert.Equal(t, "Fix the active session list", snapshot.AIConversations[0].Title)
	require.Len(t, snapshot.Terminals, 1)
	assert.Equal(t, "terminal-running", snapshot.Terminals[0].ID)
	assert.Equal(t, "/bin/zsh", snapshot.Terminals[0].Shell)
	assert.True(t, snapshot.Terminals[0].PTY)
	assert.NotEmpty(t, snapshot.CollectedAt)
}

func TestAgentServiceRuntimeSessionsUsesEmptyArrays(t *testing.T) {
	snapshot := (&AgentService{}).RuntimeSessions()

	assert.NotNil(t, snapshot.AIConversations)
	assert.NotNil(t, snapshot.Terminals)
	assert.Empty(t, snapshot.AIConversations)
	assert.Empty(t, snapshot.Terminals)
}
