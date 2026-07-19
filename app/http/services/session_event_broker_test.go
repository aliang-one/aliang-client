package services

import (
	"testing"

	auth "aliang.one/nursorgate/processor/auth"
)

func TestSessionEventBrokerBroadcastSubscribeUnsubscribe(t *testing.T) {
	b := NewSessionEventBroker()
	id, ch := b.Subscribe()
	if b.ClientCount() != 1 {
		t.Fatalf("client count=%d want 1", b.ClientCount())
	}

	b.Broadcast(auth.SessionEvent{To: auth.StateActive, Reason: auth.ReasonLogin})
	select {
	case e := <-ch:
		if e.To != auth.StateActive {
			t.Fatalf("received event To=%v want StateActive", e.To)
		}
	default:
		t.Fatal("expected to receive the broadcast")
	}

	b.Unsubscribe(id)
	if b.ClientCount() != 0 {
		t.Fatalf("client count=%d want 0 after unsubscribe", b.ClientCount())
	}
}

func TestSessionEventBrokerBroadcastDoesNotBlockOnFullBuffer(t *testing.T) {
	b := NewSessionEventBroker()
	_, ch := b.Subscribe()
	defer closeDiscard(ch)

	// Overflow the 16-deep buffer; Broadcast must not block (drops instead).
	for i := 0; i < 100; i++ {
		b.Broadcast(auth.SessionEvent{To: auth.StateActive})
	}
}

func TestSessionSnapshotNeverIncludesUserAfterHardInvalid(t *testing.T) {
	snapshot := buildSessionSnapshot(auth.StateHardInvalid, &auth.UserInfo{Username: "stale-user"})
	if _, ok := snapshot["user"]; ok {
		t.Fatalf("hard-invalid snapshot exposed stale user: %#v", snapshot["user"])
	}
	if snapshot["state"] != "hard_invalid" {
		t.Fatalf("snapshot state = %#v, want hard_invalid", snapshot["state"])
	}
}

func TestSessionSnapshotIncludesUserWhileActive(t *testing.T) {
	snapshot := buildSessionSnapshot(auth.StateActive, &auth.UserInfo{Username: "active-user"})
	if _, ok := snapshot["user"]; !ok {
		t.Fatal("active snapshot omitted user")
	}
}

func closeDiscard(_ <-chan auth.SessionEvent) {}
