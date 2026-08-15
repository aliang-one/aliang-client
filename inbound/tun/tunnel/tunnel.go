package tunnel

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/adapter"
	"aliang.one/nursorgate/outbound/proxy"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/statistic"

	"go.uber.org/atomic"
)

const (
	// tcpConnectTimeout is the default timeout for TCP handshakes.
	tcpConnectTimeout = 30 * time.Second
	// tcpWaitTimeout implements a TCP half-close timeout.
	tcpWaitTimeout = 60 * time.Second
	// udpSessionTimeout is the default timeout for UDP sessions.
	udpSessionTimeout = 60 * time.Second

	// tcpMaxConcurrentConn limits the number of simultaneously relayed TCP
	// sessions. Each accepted TCP flow gets its own goroutine, so this limiter
	// protects memory/file descriptors while avoiding worker-pool starvation.
	tcpMaxConcurrentConn = 512
	// udpWorkerCount is the number of UDP worker goroutines
	udpWorkerCount = 50
	// tcpStatsLogInterval controls periodic TCP concurrency diagnostics.
	tcpStatsLogInterval = 30 * time.Second
)

var _ adapter.TransportHandler = (*Tunnel)(nil)

type Tunnel struct {
	// UDP queue remains worker-driven because UDP sessions are short-lived and
	// packet-oriented. TCP uses dedicated goroutines plus a limiter instead.
	udpQueue chan adapter.UDPConn
	// tcpLimiter bounds the number of active TCP relay goroutines.
	tcpLimiter chan struct{}
	// activeTCPConns lets engine shutdown interrupt relays that are blocked in
	// network reads instead of waiting for the remote peer to close first.
	activeTCPMu    sync.Mutex
	activeTCPConns map[adapter.TCPConn]struct{}

	// UDP session timeout.
	udpTimeout *atomic.Duration

	// Internal proxy.Dialer for Tunnel.
	dialerMu sync.RWMutex
	dialer   proxy.Dialer

	// Where the Tunnel statistics are sent to.
	manager *statistic.Manager

	procOnce   sync.Once
	procCancel context.CancelFunc

	activeTCPConn   atomic.Int64
	rejectedTCPConn atomic.Int64
	peakTCPConn     atomic.Int64
}

func New(dialer proxy.Dialer, manager *statistic.Manager) *Tunnel {
	return &Tunnel{
		udpQueue:       make(chan adapter.UDPConn, 128),
		tcpLimiter:     make(chan struct{}, tcpMaxConcurrentConn),
		activeTCPConns: make(map[adapter.TCPConn]struct{}),
		udpTimeout:     atomic.NewDuration(udpSessionTimeout),
		dialer:         dialer,
		manager:        manager,
		procCancel:     func() { /* nop */ },
	}
}

// UDPIn return fan-in UDP queue.
func (t *Tunnel) UDPIn() chan<- adapter.UDPConn {
	return t.udpQueue
}

func (t *Tunnel) HandleTCP(conn adapter.TCPConn) {
	lease, err := auth.GetSessionAuthority().AcquireProxyLease(func() { _ = conn.Close() })
	if err != nil {
		_ = conn.Close()
		return
	}
	select {
	case t.tcpLimiter <- struct{}{}:
		t.trackTCPConn(conn)
		active := t.activeTCPConn.Inc()
		t.updatePeakTCP(active)

		go func() {
			defer func() {
				lease.Release()
				if r := recover(); r != nil {
					logger.Error("Recovered from panic in Tunnel.HandleTCP goroutine: ", logger.SafeRecoveredValueString(r))
					debug.PrintStack()
				}
				t.activeTCPConn.Dec()
				t.untrackTCPConn(conn)
				<-t.tcpLimiter
			}()

			t.handleTCPConn(conn)
		}()
	default:
		lease.Release()
		rejected := t.rejectedTCPConn.Inc()
		logger.Warn(fmt.Sprintf(
			"[TUN TCP] concurrency limit reached active=%d limit=%d rejected_total=%d; closing new connection",
			t.activeTCPConn.Load(),
			cap(t.tcpLimiter),
			rejected,
		))
		_ = conn.Close()
	}
}

func (t *Tunnel) HandleUDP(conn adapter.UDPConn) {
	t.UDPIn() <- conn
}

func (t *Tunnel) process(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Recovered from panic in Tunnel.process: ", logger.SafeRecoveredValueString(r))
			debug.PrintStack()
		}
	}()

	go t.logTCPStats(ctx)

	// Start UDP worker pool
	for i := 0; i < udpWorkerCount; i++ {
		go func(workerID int) {
			defer func() {
				if r := recover(); r != nil {
					logger.Error("UDP worker ", workerID, " panic: ", logger.SafeRecoveredValueString(r))
					debug.PrintStack()
				}
			}()
			for {
				select {
				case conn := <-t.udpQueue:
					flow := newUDPFlowCloser(conn)
					lease, err := auth.GetSessionAuthority().AcquireProxyLease(flow.Close)
					if err != nil {
						flow.Close()
						continue
					}
					func() {
						defer lease.Release()
						t.handleUDPConn(conn, flow)
					}()
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}

	// Wait for context cancellation
	<-ctx.Done()
}

// ProcessAsync can be safely called multiple times, but will only be effective once.
func (t *Tunnel) ProcessAsync() {
	t.procOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		t.procCancel = cancel
		go t.process(ctx)
	})
}

// Close closes the Tunnel and releases its resources.
func (t *Tunnel) Close() {
	t.CloseActiveTCPConnections()
	t.procCancel()
}

func (t *Tunnel) trackTCPConn(conn adapter.TCPConn) {
	if conn == nil {
		return
	}
	t.activeTCPMu.Lock()
	t.activeTCPConns[conn] = struct{}{}
	t.activeTCPMu.Unlock()
}

func (t *Tunnel) untrackTCPConn(conn adapter.TCPConn) {
	if conn == nil {
		return
	}
	t.activeTCPMu.Lock()
	delete(t.activeTCPConns, conn)
	t.activeTCPMu.Unlock()
}

// CloseActiveTCPConnections interrupts active TUN relays and releases the
// references held by the tunnel. It returns the number of close attempts.
func (t *Tunnel) CloseActiveTCPConnections() int {
	if t == nil {
		return 0
	}

	t.activeTCPMu.Lock()
	connections := make([]adapter.TCPConn, 0, len(t.activeTCPConns))
	for conn := range t.activeTCPConns {
		connections = append(connections, conn)
	}
	t.activeTCPConns = make(map[adapter.TCPConn]struct{})
	t.activeTCPMu.Unlock()

	for _, conn := range connections {
		_ = conn.Close()
	}
	return len(connections)
}

func (t *Tunnel) Dialer() proxy.Dialer {
	t.dialerMu.RLock()
	d := t.dialer
	t.dialerMu.RUnlock()
	return d
}

func (t *Tunnel) SetDialer(dialer proxy.Dialer) {
	t.dialerMu.Lock()
	t.dialer = dialer
	t.dialerMu.Unlock()
}

func (t *Tunnel) SetUDPTimeout(timeout time.Duration) {
	t.udpTimeout.Store(timeout)
}

func (t *Tunnel) updatePeakTCP(active int64) {
	for {
		peak := t.peakTCPConn.Load()
		if active <= peak {
			return
		}
		if t.peakTCPConn.CompareAndSwap(peak, active) {
			return
		}
	}
}

func (t *Tunnel) logTCPStats(ctx context.Context) {
	ticker := time.NewTicker(tcpStatsLogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			logger.Debug(fmt.Sprintf(
				"[TUN TCP] active=%d peak=%d limit=%d rejected_total=%d",
				t.activeTCPConn.Load(),
				t.peakTCPConn.Load(),
				cap(t.tcpLimiter),
				t.rejectedTCPConn.Load(),
			))
		}
	}
}
