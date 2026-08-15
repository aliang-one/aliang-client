package tunnel

import (
	"context"
	"fmt"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/adapter"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	tcphandler "aliang.one/nursorgate/processor/tcp"
)

var tunnelTCPConnCounter uint64

func newTCPHandlerContext() context.Context {
	return context.Background()
}

// getTCPHandler safely gets the TCP handler, with error recovery
func getTCPHandler() tcphandler.TCPConnHandler {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in getTCPHandler: ", logger.SafeRecoveredValueString(r))
		}
	}()
	return tcphandler.GetHandler()
}

func (t *Tunnel) handleTCPConn(originConn adapter.TCPConn) {
	defer originConn.Close()

	id := originConn.ID()
	metadata := &M.Metadata{
		Network: M.TCP,
		ConnID:  fmt.Sprintf("tun-%d", atomic.AddUint64(&tunnelTCPConnCounter, 1)),
		SrcIP:   parseTCPIPAddress(id.RemoteAddress),
		SrcPort: id.RemotePort,
		DstIP:   parseTCPIPAddress(id.LocalAddress),
		DstPort: id.LocalPort,
	}

	ctx := newTCPHandlerContext()

	// Use unified TCP handler from processor/tcp
	handler := getTCPHandler()
	if handler != nil {
		err := handler.Handle(ctx, originConn, metadata)
		if err != nil {
			logger.Debug(fmt.Sprintf("TCP handler error conn_id=%s: %v", metadata.ConnID, err))
		}
		return
	}

	// Handler should always be available - legacy fallback removed
	logger.Error("TCP handler not available")
}
