package tcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/buffer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
)

const (
	// defaultRelayBufferSize is the buffer size for bidirectional relay
	// This matches the size used in tun/buffer
	defaultRelayBufferSize = 32 * 1024
)

// DefaultRelayManager implements the RelayManager interface.
// It handles bidirectional data piping between two connections with:
// - Concurrent unidirectional streams
// - Buffer pooling for efficiency
// - Graceful shutdown and cleanup
// - TCP half-close handling
type DefaultRelayManager struct {
	// No fields needed - buffer pooling is handled globally
}

type relayCompletionTracker struct {
	originConn net.Conn
	remoteConn net.Conn

	clientToServerDone atomic.Bool
	serverToClientDone atomic.Bool
	closeOnce          sync.Once
}

func newRelayCompletionTracker(originConn, remoteConn net.Conn) *relayCompletionTracker {
	return &relayCompletionTracker{
		originConn: originConn,
		remoteConn: remoteConn,
	}
}

func (t *relayCompletionTracker) markDone(direction, connID string) {
	if t == nil {
		return
	}

	switch direction {
	case "client->server":
		t.clientToServerDone.Store(true)
	case "server->client":
		t.serverToClientDone.Store(true)
	default:
		return
	}

	if !t.clientToServerDone.Load() || !t.serverToClientDone.Load() {
		logger.Debug(fmt.Sprintf(
			"[RELAY] conn_id=%s tracked teardown dir=%s completed; waiting for opposite direction before closing both sides",
			connID,
			direction,
		))
		return
	}

	t.closeOnce.Do(func() {
		logger.Debug(fmt.Sprintf(
			"[RELAY] conn_id=%s tracked teardown both directions completed; closing origin and remote connections",
			connID,
		))
		if t.originConn != nil {
			if err := t.originConn.Close(); err != nil && !isConnectionClosedByPeer(err) {
				logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s tracked teardown origin close err=%v", connID, err))
			}
		}
		if t.remoteConn != nil {
			if err := t.remoteConn.Close(); err != nil && !isConnectionClosedByPeer(err) {
				logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s tracked teardown remote close err=%v", connID, err))
			}
		}
	})
}

// NewDefaultRelayManager creates a new relay manager
func NewDefaultRelayManager() *DefaultRelayManager {
	return &DefaultRelayManager{}
}

// Relay implements the RelayManager interface.
// It establishes bidirectional data flow between two connections.
func (r *DefaultRelayManager) Relay(ctx context.Context, originConn, remoteConn net.Conn, metadata *M.Metadata) (*RelayStats, error) {
	stats := &RelayStats{StartedAt: time.Now()}
	connID := "unknown"
	if metadata != nil && metadata.ConnID != "" {
		connID = metadata.ConnID
	}
	var completionTracker *relayCompletionTracker
	if shouldUseTrackedTeardown(metadata) {
		completionTracker = newRelayCompletionTracker(originConn, remoteConn)
		logger.Debug(fmt.Sprintf(
			"[RELAY] conn_id=%s tracked teardown enabled for windows tun connection: origin_type=%T remote_type=%T",
			connID,
			originConn,
			remoteConn,
		))
	}
	logger.Debug(fmt.Sprintf(
		"[RELAY] conn_id=%s start origin_type=%T remote_type=%T origin=%s remote=%s origin_diag=%s remote_diag=%s route=%s host=%s",
		connID,
		originConn,
		remoteConn,
		describeConn(originConn),
		describeConn(remoteConn),
		describeConnDiagnostics(originConn),
		describeConnDiagnostics(remoteConn),
		safeRoute(metadata),
		safeHost(metadata),
	))

	// Use a WaitGroup to wait for both directions to complete
	wg := sync.WaitGroup{}
	wg.Add(2)

	var firstResponseNano int64
	var firstResponseSet int32

	requestCapture := newPayloadCaptureBuffer(128 * 1024)
	responseCapture := newPayloadCaptureBuffer(128 * 1024)

	var clientToServerBytes int64
	var serverToClientBytes int64

	markFirstResponse := func() {
		if atomic.CompareAndSwapInt32(&firstResponseSet, 0, 1) {
			atomic.StoreInt64(&firstResponseNano, time.Now().UnixNano())
		}
	}

	// Start concurrent unidirectional streams
	go r.relayStream(remoteConn, originConn, "client->server", &wg, requestCapture, nil, &clientToServerBytes, ctx, metadata, completionTracker)
	go r.relayStream(originConn, remoteConn, "server->client", &wg, responseCapture, markFirstResponse, &serverToClientBytes, ctx, metadata, completionTracker)

	// Wait for both directions to complete
	wg.Wait()

	stats.CompletedAt = time.Now()
	if firstNs := atomic.LoadInt64(&firstResponseNano); firstNs > 0 {
		stats.FirstResponseAt = time.Unix(0, firstNs)
	}
	stats.ClientToServerByte = atomic.LoadInt64(&clientToServerBytes)
	stats.ServerToClientByte = atomic.LoadInt64(&serverToClientBytes)
	stats.RequestPayload = requestCapture.Bytes()
	stats.ResponsePayload = responseCapture.Bytes()
	logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s finished bytes_up=%d bytes_down=%d duration=%s",
		connID,
		stats.ClientToServerByte,
		stats.ServerToClientByte,
		stats.CompletedAt.Sub(stats.StartedAt).Round(time.Millisecond),
	))

	return stats, nil
}

// relayStream copies data from src to dst in one direction.
// It handles:
// - Buffer management and pooling
// - TCP half-close (CloseRead/CloseWrite)
// - Timeout handling
// - Error logging with appropriate severity levels
func (r *DefaultRelayManager) relayStream(
	dst net.Conn,
	src net.Conn,
	direction string,
	wg *sync.WaitGroup,
	payloadCapture *payloadCaptureBuffer,
	onFirstData func(),
	byteCounter *int64,
	_ context.Context,
	metadata *M.Metadata,
	completionTracker *relayCompletionTracker,
) {
	defer wg.Done()
	connID := "unknown"
	if metadata != nil && metadata.ConnID != "" {
		connID = metadata.ConnID
	}
	logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s stream_start dir=%s dst_type=%T src_type=%T dst=%s src=%s dst_diag=%s src_diag=%s",
		connID, direction, dst, src, describeConn(dst), describeConn(src), describeConnDiagnostics(dst), describeConnDiagnostics(src)))

	// Get buffer from pool
	buf := buffer.Get(defaultRelayBufferSize)
	defer buffer.Put(buf)

	// Copy data with the original stream-based relay path.
	countingDst := &countingWriter{writer: dst, capture: payloadCapture, onFirstData: onFirstData}
	_, err := io.CopyBuffer(countingDst, src, buf)
	if byteCounter != nil {
		atomic.AddInt64(byteCounter, countingDst.written)
	}
	if err != nil && err != io.EOF {
		// Classify relay errors by severity for better debugging
		if isTLSBadRecordMAC(err) {
			logger.Warn(fmt.Sprintf("[RELAY] conn_id=%s TLS record integrity failure [%s]: bytes=%d dst=%s src=%s dst_diag=%s src_diag=%s err=%v",
				connID, direction, countingDst.written, describeConn(dst), describeConn(src), describeConnDiagnostics(dst), describeConnDiagnostics(src), err))
		} else if isUnexpectedConnectionReset(err) {
			logger.Warn(fmt.Sprintf("[RELAY] conn_id=%s Unexpected connection reset [%s]: dst_diag=%s src_diag=%s err=%v",
				connID, direction, describeConnDiagnostics(dst), describeConnDiagnostics(src), err))
		} else if isTimeoutError(err) {
			logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s Connection timeout [%s]: dst_diag=%s src_diag=%s err=%v",
				connID, direction, describeConnDiagnostics(dst), describeConnDiagnostics(src), err))
		} else if isConnectionClosedByPeer(err) {
			logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s Connection closed by peer [%s]: dst_diag=%s src_diag=%s err=%v",
				connID, direction, describeConnDiagnostics(dst), describeConnDiagnostics(src), err))
		} else {
			logger.Warn(fmt.Sprintf("[RELAY] conn_id=%s Data relay error [%s]: dst_diag=%s src_diag=%s err=%v",
				connID, direction, describeConnDiagnostics(dst), describeConnDiagnostics(src), err))
		}
	} else {
		logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s Stream completed [%s]: bytes=%d dst=%s src=%s dst_diag=%s src_diag=%s",
			connID, direction, countingDst.written, describeConn(dst), describeConn(src), describeConnDiagnostics(dst), describeConnDiagnostics(src)))
	}

	if completionTracker != nil {
		completionTracker.markDone(direction, connID)
		return
	}

	if shouldUseConservativeTeardown(metadata) {
		logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s conservative teardown [%s]: skipping half-close/deadline for route=%s",
			connID, direction, safeRoute(metadata)))
		return
	}

	// Perform TCP half-close to signal end of stream
	// Try to close the read side on src (source stops sending)
	if cr, ok := src.(interface{ CloseRead() error }); ok {
		logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s half_close src CloseRead dir=%s src_type=%T", connID, direction, src))
		if err := cr.CloseRead(); err != nil && !isConnectionClosedByPeer(err) {
			logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s half_close src CloseRead failed dir=%s err=%v", connID, direction, err))
		}
	}

	// Try to close the write side on dst (destination stops accepting)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s half_close dst CloseWrite dir=%s dst_type=%T", connID, direction, dst))
		if err := cw.CloseWrite(); err != nil && !isConnectionClosedByPeer(err) {
			logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s half_close dst CloseWrite failed dir=%s err=%v", connID, direction, err))
		}
	}

	// Set a read deadline so we don't wait forever for the other side to close
	logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s set deadline dir=%s dst_type=%T timeout=%ds", connID, direction, dst, DefaultTCPWaitTimeout))
	if err := dst.SetReadDeadline(time.Now().Add(time.Duration(DefaultTCPWaitTimeout) * time.Second)); err != nil && !isConnectionClosedByPeer(err) {
		logger.Debug(fmt.Sprintf("[RELAY] conn_id=%s set deadline failed dir=%s err=%v", connID, direction, err))
	}
}

func safeRoute(metadata *M.Metadata) string {
	if metadata == nil {
		return ""
	}
	return metadata.Route
}

func safeHost(metadata *M.Metadata) string {
	if metadata == nil {
		return ""
	}
	return metadata.HostName
}

func shouldUseConservativeTeardown(metadata *M.Metadata) bool {
	if metadata == nil {
		return false
	}
	return metadata.Route == "RouteDirect"
}

func shouldUseTrackedTeardown(metadata *M.Metadata) bool {
	return shouldUseTrackedTeardownForGOOS(runtime.GOOS, metadata)
}

func shouldUseTrackedTeardownForGOOS(goos string, metadata *M.Metadata) bool {
	if goos != "windows" || metadata == nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(metadata.ConnID), "tun-")
}

func describeConn(conn net.Conn) string {
	if conn == nil {
		return "nil"
	}
	local := "unknown"
	remote := "unknown"
	if addr := conn.LocalAddr(); addr != nil {
		local = addr.String()
	}
	if addr := conn.RemoteAddr(); addr != nil {
		remote = addr.String()
	}
	return fmt.Sprintf("local=%s remote=%s", local, remote)
}

func describeConnDiagnostics(conn net.Conn) string {
	if conn == nil {
		return "nil"
	}

	if diagnosticConn, ok := conn.(interface{ ConnectionDiagnosticString() string }); ok {
		return diagnosticConn.ConnectionDiagnosticString()
	}

	_, canCloseRead := conn.(interface{ CloseRead() error })
	_, canCloseWrite := conn.(interface{ CloseWrite() error })

	return fmt.Sprintf(
		"type=%T wrapped=false can_close_read=%t can_close_write=%t",
		conn,
		canCloseRead,
		canCloseWrite,
	)
}

// isUnexpectedConnectionReset checks if the error is an unexpected connection reset
func isUnexpectedConnectionReset(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "reset") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "ECONNRESET")
}

// isTimeoutError checks if the error is a timeout
func isTimeoutError(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// isConnectionClosedByPeer checks if the error indicates the peer closed the connection
func isConnectionClosedByPeer(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	return strings.Contains(errMsg, "closed") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "EPIPE")
}

func isTLSBadRecordMAC(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "bad record mac")
}

type payloadCaptureBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
}

func newPayloadCaptureBuffer(limit int) *payloadCaptureBuffer {
	if limit <= 0 {
		limit = 128 * 1024
	}
	return &payloadCaptureBuffer{limit: limit}
}

func (p *payloadCaptureBuffer) Write(data []byte) {
	if p == nil || len(data) == 0 {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	remaining := p.limit - p.buf.Len()
	if remaining <= 0 {
		return
	}

	if len(data) > remaining {
		data = data[:remaining]
	}
	_, _ = p.buf.Write(data)
}

func (p *payloadCaptureBuffer) Bytes() []byte {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	b := p.buf.Bytes()
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

type countingWriter struct {
	writer      io.Writer
	capture     *payloadCaptureBuffer
	onFirstData func()
	written     int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w.onFirstData != nil && len(p) > 0 {
		w.onFirstData()
		w.onFirstData = nil
	}
	if w.capture != nil && len(p) > 0 {
		w.capture.Write(p)
	}
	n, err := w.writer.Write(p)
	if n > 0 {
		w.written += int64(n)
	}
	return n, err
}

// SimpleRelayFunc provides a simple pipe function compatible with existing code.
// It creates a default relay manager and uses it to relay data.
func SimpleRelayFunc(ctx context.Context, originConn, remoteConn net.Conn) {
	// Create a simple relay manager (for backward compatibility)
	manager := NewDefaultRelayManager()

	// Create minimal metadata for relay
	metadata := &M.Metadata{
		Network: M.TCP,
	}

	// Relay the connections (ignore error for backward compatibility)
	_, _ = manager.Relay(ctx, originConn, remoteConn, metadata)
}

// PipeConnections is a convenience function that mimics the old pipe() function
// for backward compatibility during migration.
func PipeConnections(origin, remote net.Conn) {
	// Simple inline version without context
	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer wg.Done()
		buf := buffer.Get(defaultRelayBufferSize)
		defer buffer.Put(buf)

		io.CopyBuffer(remote, origin, buf)

		// Half-close on source side
		if cr, ok := origin.(interface{ CloseRead() error }); ok {
			cr.CloseRead()
		}
		// Half-close on destination side
		if cw, ok := remote.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		remote.SetReadDeadline(time.Now().Add(time.Duration(DefaultTCPWaitTimeout) * time.Second))
	}()

	go func() {
		defer wg.Done()
		buf := buffer.Get(defaultRelayBufferSize)
		defer buffer.Put(buf)

		io.CopyBuffer(origin, remote, buf)

		// Half-close on source side
		if cr, ok := remote.(interface{ CloseRead() error }); ok {
			cr.CloseRead()
		}
		// Half-close on destination side
		if cw, ok := origin.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		origin.SetReadDeadline(time.Now().Add(time.Duration(DefaultTCPWaitTimeout) * time.Second))
	}()

	wg.Wait()
}
