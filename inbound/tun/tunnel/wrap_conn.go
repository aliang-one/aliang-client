package tunnel

import (
	"net"
)

type WrappedConn struct {
	net.Conn
	Buf        []byte
	readOffset int
}

func (w *WrappedConn) Read(p []byte) (int, error) {
	if len(w.Buf) > w.readOffset {
		n := copy(p, w.Buf[w.readOffset:])
		w.readOffset += n
		if w.readOffset >= len(w.Buf) {
			w.Buf = nil
			w.readOffset = 0
		}
		return n, nil
	}
	return w.Conn.Read(p)
}
