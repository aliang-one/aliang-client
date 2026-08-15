package option

import "testing"

func TestDefaultTCPBuffersRemainMemoryBounded(t *testing.T) {
	const maxDefaultBufferSize = 64 << 10
	if tcpDefaultSendBufferSize > maxDefaultBufferSize {
		t.Fatalf("default TCP send buffer = %d, want at most %d", tcpDefaultSendBufferSize, maxDefaultBufferSize)
	}
	if tcpDefaultReceiveBufferSize > maxDefaultBufferSize {
		t.Fatalf("default TCP receive buffer = %d, want at most %d", tcpDefaultReceiveBufferSize, maxDefaultBufferSize)
	}
}
