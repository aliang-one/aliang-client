package tcp

import (
	"sync"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/statistic"
)

var (
	// globalHandler is the singleton TCP connection handler
	globalHandler TCPConnHandler

	// initOnce ensures the handler is initialized only once
	initOnce sync.Once
)

// GetHandler returns the singleton TCP connection handler.
// It initializes with default components on first call.
// Safe for concurrent access.
func GetHandler() TCPConnHandler {
	initOnce.Do(func() {
		// Create handler with default implementations
		protocolDetector := NewDefaultProtocolDetector()
		tlsHandler := NewDefaultTLSHandler()
		relayManager := NewDefaultRelayManager()

		// Get global statistic manager
		statsManager := statistic.DefaultManager

		// Create and store the handler
		globalHandler = NewTCPConnectionHandler(
			protocolDetector,
			tlsHandler,
			relayManager,
			statsManager,
		)

		logger.Debug("TCP connection handler initialized")
	})

	return globalHandler
}

// SetHandler allows manual setting of the handler (for testing)
func SetHandler(handler TCPConnHandler) {
	globalHandler = handler
	initOnce = sync.Once{}
	initOnce.Do(func() {})
}

// ResetHandler resets to uninitialized state (for testing)
func ResetHandler() {
	globalHandler = nil
	initOnce = sync.Once{}
}
