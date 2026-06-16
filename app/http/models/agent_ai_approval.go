package models

import "encoding/json"

const (
	AgentAIApprovalKindCommand     = "command"
	AgentAIApprovalKindFileChange  = "file_change"
	AgentAIApprovalKindPermissions = "permissions"
	AgentAIApprovalKindTool        = "tool"

	AgentAIApprovalDecisionAccept           = "accept"
	AgentAIApprovalDecisionAcceptForSession = "accept_for_session"
	AgentAIApprovalDecisionDecline          = "decline"
	AgentAIApprovalDecisionCancel           = "cancel"
)

type AgentAIApprovalRequest struct {
	Type               string          `json:"type"`
	SessionID          string          `json:"session_id"`
	MessageID          string          `json:"message_id,omitempty"`
	ApprovalID         string          `json:"approval_id"`
	Provider           string          `json:"provider"`
	Kind               string          `json:"kind"`
	Status             string          `json:"status"`
	Title              string          `json:"title,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	Command            string          `json:"command,omitempty"`
	CWD                string          `json:"cwd,omitempty"`
	ToolName           string          `json:"tool_name,omitempty"`
	ToolInput          json.RawMessage `json:"tool_input,omitempty"`
	FileChanges        json.RawMessage `json:"file_changes,omitempty"`
	AvailableDecisions []string        `json:"available_decisions,omitempty"`
	Raw                json.RawMessage `json:"raw,omitempty"`
}

type AgentAIApprovalResponse struct {
	Type       string          `json:"type"`
	SessionID  string          `json:"session_id"`
	MessageID  string          `json:"message_id,omitempty"`
	ApprovalID string          `json:"approval_id"`
	Decision   string          `json:"decision"`
	Scope      string          `json:"scope,omitempty"`
	Raw        json.RawMessage `json:"raw,omitempty"`
}
