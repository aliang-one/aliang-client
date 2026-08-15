package aliang

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

// fakeLinkDialer is a stand-in for AliangServerConnector. It fails the first
// failTimes attempts with a transient error, then returns conn + timings.
type fakeLinkDialer struct {
	mu        sync.Mutex
	attempts  int
	failTimes int
	timings   ProbeTimings
	conn      net.Conn
}

func (f *fakeLinkDialer) DialWithTiming(_ context.Context, _, _, _ string) (net.Conn, ProbeTimings, error) {
	f.mu.Lock()
	f.attempts++
	n := f.attempts
	f.mu.Unlock()
	if n <= f.failTimes {
		return nil, ProbeTimings{}, fmt.Errorf("fake transient EOF (attempt %d)", n)
	}
	return f.conn, f.timings, nil
}

func (f *fakeLinkDialer) attemptCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts
}

func newTestAliang(t *testing.T, dialer aliangDialer, maxAttempts int, baseDelay time.Duration) *Aliang {
	t.Helper()
	return &Aliang{
		Base:      &proxy.Base{Address: "fake:443", Protocol: proto.Aliang},
		config:    &AliangConfig{Addr: "fake:443", LinkDialMaxAttempts: maxAttempts, LinkDialRetryBaseDelay: baseDelay},
		connector: dialer,
		status:    newLinkStatusTracker("fake:443", 0),
	}
}

// A sub-second relay blip surfaces as a handshake EOF on the first dial(s).
// DialContext must retry and succeed instead of surfacing the failure, so the
// proxied connection (and the link status) never see the blip.
func TestAliangDialContextRetriesAndAbsorbsTransientFailures(t *testing.T) {
	clientConn, _ := net.Pipe()
	defer clientConn.Close()
	dialer := &fakeLinkDialer{
		failTimes: 2,
		timings:   ProbeTimings{TCPConnect: 3 * time.Millisecond},
		conn:      clientConn,
	}
	a := newTestAliang(t, dialer, 3, time.Millisecond)

	conn, err := a.DialContext(context.Background(), &metadata.Metadata{ConnID: "c1", AppProto: "http2"})
	if err != nil {
		t.Fatalf("DialContext returned error after retries: %v", err)
	}
	defer conn.Close()
	if got := dialer.attemptCount(); got != 3 {
		t.Fatalf("dialer attempts = %d, want 3 (initial + 2 retries)", got)
	}
}

func TestAliangDialContextFailsAfterExhaustingRetries(t *testing.T) {
	dialer := &fakeLinkDialer{failTimes: 99} // always fails
	a := newTestAliang(t, dialer, 3, time.Millisecond)

	_, err := a.DialContext(context.Background(), &metadata.Metadata{ConnID: "c2"})
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if got := dialer.attemptCount(); got != 3 {
		t.Fatalf("dialer attempts = %d, want 3 (then give up)", got)
	}
}

func TestAliangDialContextStopsRetryingOnContextCancel(t *testing.T) {
	dialer := &fakeLinkDialer{failTimes: 99}
	a := newTestAliang(t, dialer, 10, 50*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := a.DialContext(ctx, &metadata.Metadata{ConnID: "c3"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after context cancel, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("took %v to return after cancel; retry loop did not honor ctx cancellation", elapsed)
	}
}

func waitForLinkState(a *Aliang, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.status.state() == want {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for link state %q (last=%q)", want, a.status.state())
}

// When the link is dropped (sustained failures flip it to disconnected), the
// background health monitor must keep probing and flip it back to connected on
// its own — no manual reconnect click required.
func TestAliangHealthMonitorAutoRecoversDisconnectedLink(t *testing.T) {
	clientConn, _ := net.Pipe()
	defer clientConn.Close()
	// failTimes=4 with maxAttempts=1 (one dial per probe) and threshold=3:
	// probes 1-3 drive state unknown→disconnected, probe 4 still fails,
	// probe 5 succeeds → monitor self-heals to connected.
	dialer := &fakeLinkDialer{
		failTimes: 4,
		conn:      clientConn,
		timings:   ProbeTimings{TCPConnect: 2 * time.Millisecond},
	}
	a := newTestAliang(t, dialer, 1, time.Millisecond)
	a.config.LinkHealthInterval = 50 * time.Millisecond
	a.config.LinkHealthRecoveryInterval = 5 * time.Millisecond
	a.StartHealthMonitor()
	defer a.Close()

	if err := waitForLinkState(a, LinkStateConnected, 3*time.Second); err != nil {
		t.Fatalf("monitor did not auto-recover link: %v", err)
	}
}

// A healthy link should stay quiet (no recovery-probe storm); state stays
// connected without the monitor flipping it.
func TestAliangHealthMonitorLeavesHealthyLinkAlone(t *testing.T) {
	clientConn, _ := net.Pipe()
	defer clientConn.Close()
	dialer := &fakeLinkDialer{conn: clientConn, timings: ProbeTimings{TCPConnect: 2 * time.Millisecond}}
	a := newTestAliang(t, dialer, 1, time.Millisecond)
	a.config.LinkHealthInterval = 50 * time.Millisecond
	a.config.LinkHealthRecoveryInterval = 5 * time.Millisecond
	a.StartHealthMonitor()
	defer a.Close()

	if err := waitForLinkState(a, LinkStateConnected, 2*time.Second); err != nil {
		t.Fatalf("monitor never established connected state: %v", err)
	}
	// Give it a couple of healthy intervals and confirm it stays connected.
	time.Sleep(120 * time.Millisecond)
	if got := a.status.state(); got != LinkStateConnected {
		t.Fatalf("healthy link flapped to %q; monitor should leave it connected", got)
	}
}
