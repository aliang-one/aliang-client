package mirror

import (
	"net"
	"time"
)

// mirrorConn wraps a net.Conn and captures data read through it for mirroring.
type mirrorConn struct {
	net.Conn
	flow      *Flow
	direction Direction
	srcAddr   string
	dstAddr   string
	offset    uint64
	seq       uint64
}

// Read reads from the underlying connection and captures the data for mirroring.
func (c *mirrorConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		chunk := &StreamChunk{
			FlowID:    c.flow.ID,
			ConnID:    c.flow.ConnID,
			Direction: c.direction,
			Offset:    c.offset,
			Seq:       c.seq,
			Payload:   copyBytes(p[:n]),
			Timestamp: time.Now().UnixMilli(),
			SrcAddr:   c.srcAddr,
			DstAddr:   c.dstAddr,
		}
		c.offset += uint64(n)
		c.seq++
		c.flow.Enqueue(chunk)
	}
	return n, err
}

// copyBytes creates an independent copy of the byte slice.
func copyBytes(b []byte) []byte {
	cp := make([]byte, len(b))
	copy(cp, b)
	return cp
}
