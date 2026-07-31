package services

import (
	"aliang.one/nursorgate/app/http/models"
	auth "aliang.one/nursorgate/processor/auth"
)

const sessionSnapshotType = "session_snapshot"

// SessionSnapshotPayload is the single browser-facing auth contract used by
// both GET /api/auth/session and GET /api/session/events. Credentials never
// cross this boundary.
type SessionSnapshotPayload struct {
	Type       string                   `json:"type"`
	InstanceID string                   `json:"instance_id"`
	Revision   uint64                   `json:"revision"`
	State      string                   `json:"state"`
	Reason     string                   `json:"reason,omitempty"`
	Outcome    string                   `json:"outcome,omitempty"`
	User       *models.UserInfoResponse `json:"user,omitempty"`
}

func BuildSessionSnapshotPayload(snapshot auth.SessionSnapshot) SessionSnapshotPayload {
	payload := SessionSnapshotPayload{
		Type:       sessionSnapshotType,
		InstanceID: snapshot.InstanceID,
		Revision:   snapshot.Revision,
		State:      snapshot.State.String(),
		Reason:     string(snapshot.Reason),
	}

	switch snapshot.State {
	case auth.StateRestoring, auth.StateSoftExpired:
		payload.Outcome = "session_recovering"
	case auth.StateHardInvalid, auth.StateUnauthenticated:
		payload.Outcome = "session_invalid"
	}

	if snapshot.User != nil && (snapshot.State == auth.StateActive || snapshot.State == auth.StateSoftExpired) {
		user := mapUserInfo(snapshot.User)
		payload.User = &user
	}
	return payload
}
