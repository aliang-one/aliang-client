package mirror

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"sync"
	"sync/atomic"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// Flow represents one mirrored proxy connection's state.
type Flow struct {
	ID           string
	ConnID       string
	ClientAddr   string
	UpstreamAddr string
	ProtocolHint string
	HostName     string

	// Addresses per direction, set when wrapping
	requestSrc  string
	requestDst  string
	responseSrc string
	responseDst string

	// Lifecycle tracking
	started   int32 // atomic: 0 = not started, 1 = started
	startNano int64
	endOnce   sync.Once
}

// generateFlowID creates a unique flow identifier.
func generateFlowID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewMirrorFlow creates a Flow if mirroring is enabled for the given connection's domain.
// Returns nil if mirroring is not active.
func NewMirrorFlow(metadata *M.Metadata) *Flow {
	if metadata == nil {
		return nil
	}

	fwd := GetGlobalForwarder()
	if fwd == nil {
		return nil
	}

	cfg := loadMirrorConfig()
	if cfg == nil {
		return nil
	}

	if !MatchesAnyDomain(metadata.HostName, cfg.Domains) {
		return nil
	}

	flow := &Flow{
		ID:           generateFlowID(),
		ConnID:       metadata.ConnID,
		ClientAddr:   metadata.SourceAddress(),
		UpstreamAddr: metadata.DestinationAddress(),
		ProtocolHint: metadata.AppProto,
		HostName:     metadata.HostName,
	}

	return flow
}

// Enqueue sends a chunk to the global forwarder. Non-blocking.
func (f *Flow) Enqueue(chunk *StreamChunk) {
	if f == nil || chunk == nil {
		return
	}
	chunk.ClientAddr = f.ClientAddr
	chunk.UpstreamAddr = f.UpstreamAddr
	chunk.ProtocolHint = f.ProtocolHint

	forwarder := GetGlobalForwarder()
	if forwarder != nil {
		forwarder.Enqueue(chunk)
	}
}

// WrapConn wraps a net.Conn to capture data in the given direction.
func (f *Flow) WrapConn(conn net.Conn, direction Direction) net.Conn {
	srcAddr := ""
	dstAddr := ""
	switch direction {
	case DirectionRequest:
		srcAddr = f.ClientAddr
		dstAddr = f.UpstreamAddr
		f.requestSrc = srcAddr
		f.requestDst = dstAddr
	case DirectionResponse:
		srcAddr = f.UpstreamAddr
		dstAddr = f.ClientAddr
		f.responseSrc = srcAddr
		f.responseDst = dstAddr
	}

	return &mirrorConn{
		Conn:      conn,
		flow:      f,
		direction: direction,
		srcAddr:   srcAddr,
		dstAddr:   dstAddr,
	}
}

// EmitStart sends a flow_start event. Idempotent: only the first call emits.
func (f *Flow) EmitStart() {
	if f == nil {
		return
	}
	if !atomic.CompareAndSwapInt32(&f.started, 0, 1) {
		return // already emitted
	}
	f.startNano = time.Now().UnixNano()

	evt := &FlowEvent{
		EventType:    FlowEventStart,
		FlowID:       f.ID,
		ConnID:       f.ConnID,
		Timestamp:    nowUnixMilli(),
		ClientAddr:   f.ClientAddr,
		UpstreamAddr: f.UpstreamAddr,
		ProtocolHint: f.ProtocolHint,
		HostName:     f.HostName,
	}

	forwarder := GetGlobalForwarder()
	if forwarder != nil {
		forwarder.Enqueue(evt)
	}
}

// EmitEnd sends a flow_end event. Safe to call multiple times; only the first call emits.
func (f *Flow) EmitEnd(clientBytes, serverBytes int64, relayErr error) {
	if f == nil {
		return
	}
	f.endOnce.Do(func() {
		durationMs := int64(0)
		if f.startNano > 0 {
			durationMs = (time.Now().UnixNano() - f.startNano) / int64(time.Millisecond)
		}

		evt := &FlowEvent{
			EventType:           FlowEventEnd,
			FlowID:              f.ID,
			ConnID:              f.ConnID,
			Timestamp:           nowUnixMilli(),
			ClientAddr:          f.ClientAddr,
			UpstreamAddr:        f.UpstreamAddr,
			ProtocolHint:        f.ProtocolHint,
			HostName:            f.HostName,
			ClientToServerBytes: clientBytes,
			ServerToClientBytes: serverBytes,
			DurationMs:          durationMs,
			ErrorClass:          classifyError(relayErr),
		}
		if relayErr != nil {
			evt.Error = relayErr.Error()
		}

		forwarder := GetGlobalForwarder()
		if forwarder != nil {
			forwarder.Enqueue(evt)
		}
	})
}

// Close is a safety net meant to be called via defer.
// If EmitEnd has not been called yet, it emits a zero-value flow_end.
func (f *Flow) Close() {
	if f == nil {
		return
	}
	f.EmitEnd(0, 0, nil)
}
