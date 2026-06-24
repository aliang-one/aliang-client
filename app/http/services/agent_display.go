package services

import "aliang.one/nursorgate/app/http/models"

// agentVibeAssistantSummaryRunes is the one-line cap applied to assistant
// messages when surfacing a transcript for display. User prompts are kept in
// full; system/tool messages are dropped.
const agentVibeAssistantSummaryRunes = 120

// summarizeVibeTranscriptForDisplay prepares a transcript for the read-only
// activity view: user messages stay full-text, assistant messages are capped to
// a one-line summary, and system/tool messages are filtered out. It returns a
// new slice and does not mutate the input.
func summarizeVibeTranscriptForDisplay(messages []models.AgentVibeMessage, maxSummaryRunes int) []models.AgentVibeMessage {
	out := make([]models.AgentVibeMessage, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			out = append(out, msg)
		case "assistant":
			clone := msg
			clone.Content = truncateAgentText(msg.Content, maxSummaryRunes)
			out = append(out, clone)
		default:
			// system / tool / function: drop
		}
	}
	return out
}
