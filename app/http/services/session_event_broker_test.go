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

	b.Publish(auth.SessionSnapshot{InstanceID: "instance", Revision: 2, State: auth.StateActive, Reason: auth.ReasonLogin})
	select {
	case snapshot := <-ch:
		if snapshot.State != auth.StateActive {
			t.Fatalf("received state=%v want StateActive", snapshot.State)
		}
	default:
		t.Fatal("expected to receive the broadcast")
	}

	b.Unsubscribe(id)
	if b.ClientCount() != 0 {
		t.Fatalf("client count=%d want 0 after unsubscribe", b.ClientCount())
	}
}

func TestSessionEventBrokerPublishCoalescesToLatestSnapshot(t *testing.T) {
	b := NewSessionEventBroker()
	_, ch := b.Subscribe()
	defer closeDiscard(ch)

	// A slow client keeps one complete latest snapshot rather than an arbitrary
	// stale transition from the front of a larger queue.
	for i := 0; i < 100; i++ {
		b.Publish(auth.SessionSnapshot{InstanceID: "instance", Revision: uint64(i + 1), State: auth.StateActive})
	}
	select {
	case snapshot := <-ch:
		if snapshot.Revision != 100 {
			t.Fatalf("coalesced revision=%d want 100", snapshot.Revision)
		}
	default:
		t.Fatal("expected latest queued snapshot")
	}
}

func TestGlobalSessionEventBrokerSurvivesAuthorityReset(t *testing.T) {
	authority := auth.ResetSessionAuthorityForTest()
	id, ch := sessionEventBroker.Subscribe()
	defer sessionEventBroker.Unsubscribe(id)

	authority.NotifyLoggedIn(&auth.UserInfo{Username: "reset-user"})

	select {
	case snapshot := <-ch:
		if snapshot.State != auth.StateActive || snapshot.User == nil || snapshot.User.Username != "reset-user" {
			t.Fatalf("broker snapshot after authority reset=%+v", snapshot)
		}
	default:
		t.Fatal("global broker listener was not reattached after authority reset")
	}
}

func TestSessionSnapshotNeverIncludesUserAfterHardInvalid(t *testing.T) {
	snapshot := BuildSessionSnapshotPayload(auth.SessionSnapshot{State: auth.StateHardInvalid, User: &auth.UserInfo{Username: "stale-user"}})
	if snapshot.User != nil {
		t.Fatalf("hard-invalid snapshot exposed stale user: %#v", snapshot.User)
	}
	if snapshot.State != "hard_invalid" || snapshot.Outcome != "session_invalid" {
		t.Fatalf("snapshot = %#v, want hard_invalid/session_invalid", snapshot)
	}
}

func TestSessionSnapshotIncludesUserWhileActive(t *testing.T) {
	snapshot := BuildSessionSnapshotPayload(auth.SessionSnapshot{InstanceID: "instance", Revision: 7, State: auth.StateActive, User: &auth.UserInfo{Username: "active-user"}})
	if snapshot.User == nil {
		t.Fatal("active snapshot omitted user")
	}
	if snapshot.InstanceID != "instance" || snapshot.Revision != 7 {
		t.Fatalf("snapshot version lost: %#v", snapshot)
	}
}

func closeDiscard(_ <-chan auth.SessionSnapshot) {}
