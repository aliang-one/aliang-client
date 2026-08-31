package tunnel

import (
	"context"
	"testing"
	"time"
)

func TestPikoStateFromLog(t *testing.T) {
	t.Parallel()
	cases := []struct {
		msg   string
		want  string
		match bool
	}{
		{msg: "connected", want: "connected", match: true},
		{msg: "disconnected; reconnecting", want: "reconnecting", match: true},
		{msg: "connect failed; retrying", want: "reconnecting", match: true},
		{msg: "connecting", match: false},
		{msg: "connect failed; non-retryable", match: false},
		{msg: "unrelated", match: false},
	}
	for _, tc := range cases {
		got, ok := pikoStateFromLog(tc.msg)
		if ok != tc.match || (ok && got != tc.want) {
			t.Errorf("pikoStateFromLog(%q) = (%q, %t), want match=%t state=%q", tc.msg, got, ok, tc.match, tc.want)
		}
	}
}

func TestManagerSurfacesReconnectingState(t *testing.T) {
	t.Parallel()
	statuses := make(chan Status, 8)
	manager := NewManager(func(status Status) { statuses <- status })
	proceed := make(chan struct{})
	manager.run = func(ctx context.Context, config runConfig, ready func()) error {
		if config.onState == nil {
			t.Error("expected onState hook to be wired into runConfig")
			return nil
		}
		ready()
		<-proceed
		// onState receives already-mapped states (the pikoStateLogger maps
		// raw piko log lines; TestPikoStateFromLog covers that mapping).
		config.onState("reconnecting")
		// Reconnect succeeds shortly after.
		time.Sleep(10 * time.Millisecond)
		config.onState("connected")
		<-ctx.Done()
		return nil
	}

	if _, _, err := manager.Configure(validTestConfig(t)); err != nil {
		t.Fatal(err)
	}
	waitForState(t, statuses, "connected")

	close(proceed)
	waitForState(t, statuses, "reconnecting")
	waitForState(t, statuses, "connected")

	manager.Stop()
	waitForState(t, statuses, "stopped")
}

func TestWaitConnectedKeepsWaitingThroughReconnecting(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil)
	manager.status = Status{DeviceID: "dev_test", State: "connecting"}
	go func() {
		time.Sleep(20 * time.Millisecond)
		manager.mu.Lock()
		manager.status.State = "reconnecting"
		manager.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		manager.mu.Lock()
		manager.status.State = "connected"
		manager.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := manager.WaitConnected(ctx, "dev_test")
	if err != nil || status.State != "connected" {
		t.Fatalf("WaitConnected = (%+v, %v)", status, err)
	}
}
