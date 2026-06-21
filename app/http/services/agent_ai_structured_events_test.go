package services

import (
	"strings"
	"testing"

	"aliang.one/nursorgate/app/http/models"
)

// The structured AI events let the cloud render Claude-Code-style activity
// (commands, file edits, reasoning, token usage, task lists) instead of a flat
// prose stream. They ride the same agent→cloud channel as ai.delta. This pins
// the event-type constants and their Required/Optional contract so the protocol
// test guards them.
func TestAgentProtocolStructuredAIEventsContract(t *testing.T) {
	contract := models.DefaultAgentProtocolContract()

	wantConstants := map[string]string{
		"AgentEventAICommand":    models.AgentEventAICommand,
		"AgentEventAIFileChange": models.AgentEventAIFileChange,
		"AgentEventAIThinking":   models.AgentEventAIThinking,
		"AgentEventAIUsage":      models.AgentEventAIUsage,
		"AgentEventAITask":       models.AgentEventAITask,
	}
	for name, value := range wantConstants {
		if value == "" {
			t.Fatalf("%s is empty", name)
		}
		if !strings.HasPrefix(value, "ai.") {
			t.Fatalf("%s = %q, want ai.* prefix", name, value)
		}
	}

	required := map[string][]string{
		models.AgentEventAICommand:    {"type", "session_id", "message_id", "item_id", "status"},
		models.AgentEventAIFileChange: {"type", "session_id", "message_id", "item_id"},
		models.AgentEventAIThinking:   {"type", "session_id", "message_id", "delta"},
		models.AgentEventAIUsage:      {"type", "session_id"},
		models.AgentEventAITask:       {"type", "session_id", "message_id", "tasks"},
	}
	for eventType, wantRequired := range required {
		ev := protocolEventByType(contract.WebSocket.ClientSends, eventType)
		if ev == nil {
			t.Fatalf("ClientSends missing structured event %q", eventType)
		}
		for _, field := range wantRequired {
			if !stringSliceContains(ev.Required, field) {
				t.Fatalf("event %q Required = %#v, want field %q", eventType, ev.Required, field)
			}
		}
	}

	// Structured events are emitted during a message run; the contract should
	// advertise them on ai.message's Emits so consumers know to expect them.
	for _, eventType := range []string{
		models.AgentEventAICommand,
		models.AgentEventAIFileChange,
		models.AgentEventAIThinking,
		models.AgentEventAIUsage,
		models.AgentEventAITask,
	} {
		if !structuredEventEmittedByMessage(contract, eventType) {
			t.Fatalf("ai.message Emits missing %q", eventType)
		}
	}
}

func protocolEventByType(events []models.AgentProtocolEvent, eventType string) *models.AgentProtocolEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}

func structuredEventEmittedByMessage(contract models.AgentProtocolContract, eventType string) bool {
	for _, ev := range contract.WebSocket.ServerSends {
		if ev.Type != models.AgentEventAIMessage {
			continue
		}
		for _, emits := range ev.Emits {
			if emits == eventType {
				return true
			}
		}
	}
	return false
}
