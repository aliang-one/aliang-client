package tunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerConfigureIsIdempotentAndStopCancelsSession(t *testing.T) {
	t.Parallel()
	statuses := make(chan Status, 8)
	manager := NewManager(func(status Status) { statuses <- status })
	var starts atomic.Int32
	stopped := make(chan struct{}, 1)
	manager.run = func(ctx context.Context, _ runConfig, ready func()) error {
		starts.Add(1)
		ready()
		<-ctx.Done()
		stopped <- struct{}{}
		return nil
	}

	config := validTestConfig(t)
	status, changed, err := manager.Configure(config)
	if err != nil || !changed || status.State != "connecting" {
		t.Fatalf("first Configure = (%+v, %t, %v)", status, changed, err)
	}
	waitForState(t, statuses, "connected")

	status, changed, err = manager.Configure(config)
	if err != nil || changed || status.State != "connected" {
		t.Fatalf("idempotent Configure = (%+v, %t, %v)", status, changed, err)
	}
	if starts.Load() != 1 {
		t.Fatalf("runner started %d times", starts.Load())
	}

	manager.Stop()
	waitForState(t, statuses, "stopped")
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("runner was not cancelled")
	}
}

func TestManagerRejectsInsecureRemotePikoURL(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil)
	config := validTestConfig(t)
	config.PikoUpstreamURL = "http://192.168.1.10:8001"
	if _, _, err := manager.Configure(config); err == nil {
		t.Fatal("expected insecure remote URL to be rejected")
	}
}

func TestManagerWaitConnectedTimesOut(t *testing.T) {
	t.Parallel()
	manager := NewManager(nil)
	manager.status = Status{DeviceID: "dev_test", State: "connecting"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.WaitConnected(ctx, "dev_test"); err == nil {
		t.Fatal("expected connection wait to time out")
	}
}

func validTestConfig(t *testing.T) Config {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		DeviceID:        "dev_test",
		PikoUpstreamURL: "http://127.0.0.1:8001",
		TunnelToken:     "test-token",
		RoutePublicKey:  base64.StdEncoding.EncodeToString(publicKey),
		ExpiresAt:       time.Now().Add(time.Minute),
	}
}

func waitForState(t *testing.T, statuses <-chan Status, expected string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case status := <-statuses:
			if status.State == expected {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for state %q", expected)
		}
	}
}
