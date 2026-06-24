package services

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAgentService_VibeSessions_ReturnsSummariesWithoutTranscript(t *testing.T) {
	s := GetSharedAgentService()
	sessions := s.VibeSessions()
	for _, sess := range sessions {
		assert.Nil(t, sess.Transcript, "列表摘要不应携带 transcript")
		assert.Nil(t, sess.TranscriptPage)
	}
}

func TestAgentService_VibeSessionDetail_NotFound(t *testing.T) {
	s := GetSharedAgentService()
	_, err := s.VibeSessionDetail("definitely-nonexistent-session-id", 0, "")
	assert.Error(t, err)
}
