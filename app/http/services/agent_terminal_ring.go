package services

import "sync"

// terminalRingFloorBytes is the lower bound for a usable terminal ring. A
// non-positive (unset/invalid) capacity is lifted to this floor; callers that
// read configuration (e.g. ALIANG_TERMINAL_RING_BYTES) are responsible for
// clamping positive-but-tiny values before constructing the ring.
const terminalRingFloorBytes = 64 * 1024

// terminalRingBuffer keeps the most recent bytes of a terminal output stream so
// a reconnecting client can replay the scrollback. Chunks are stored whole:
// when the ring overflows, whole oldest chunks are dropped (no byte-level
// trimming of interior chunks); only an incoming chunk that alone exceeds the
// capacity is tail-truncated. All methods are safe for concurrent use.
type terminalRingBuffer struct {
	mu     sync.Mutex
	max    int
	chunks [][]byte
	total  int
}

// newTerminalRingBuffer creates a ring holding at most maxBytes bytes. A
// non-positive maxBytes is lifted to terminalRingFloorBytes.
func newTerminalRingBuffer(maxBytes int) *terminalRingBuffer {
	if maxBytes <= 0 {
		maxBytes = terminalRingFloorBytes
	}
	return &terminalRingBuffer{max: maxBytes}
}

// push appends a copy of p to the ring, evicting oldest whole chunks (and
// tail-truncating p itself when it alone exceeds the capacity) so the retained
// tail never exceeds the capacity.
func (r *terminalRingBuffer) push(p []byte) {
	if len(p) == 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	chunk := make([]byte, len(p))
	copy(chunk, p)
	if len(chunk) > r.max {
		chunk = chunk[len(chunk)-r.max:]
	}
	r.chunks = append(r.chunks, chunk)
	r.total += len(chunk)
	for r.total > r.max && len(r.chunks) > 1 {
		r.total -= len(r.chunks[0])
		r.chunks[0] = nil
		r.chunks = r.chunks[1:]
	}
}

// snapshot returns the concatenated retained bytes. The returned slice is a
// fresh copy, so callers may keep or mutate it freely.
func (r *terminalRingBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]byte, 0, r.total)
	for _, chunk := range r.chunks {
		out = append(out, chunk...)
	}
	return out
}
