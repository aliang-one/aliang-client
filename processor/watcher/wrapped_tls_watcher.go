package tls

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"

	"aliang.one/nursorgate/common/logger"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const maxProtocolSniffBytes = 8192

var http1Methods = []string{
	"GET",
	"POST",
	"PUT",
	"HEAD",
	"PATCH",
	"DELETE",
	"OPTIONS",
	"CONNECT",
	"TRACE",
}

type WatcherWrapConn struct {
	net.Conn

	reqBuf           bytes.Buffer
	respBuf          bytes.Buffer
	prefetched       bool
	http2PrefaceSent bool
	isTokenFound     bool
	isHttp1          bool
	passthrough      bool
	http1ReqContent  string
	http1BodyTracker *http1BodyTracker

	// Connection-scoped HPACK lifecycle for request path:
	// client encoder -> proxy requestDecoderFromClient -> proxy requestEncoderToServer -> server decoder
	requestDecoderFromClient *hpack.Decoder
	requestEncoderBuffer     *bytes.Buffer
	requestEncoderToServer   *hpack.Encoder

	streams                map[uint32]*http2Stream
	recentRequestSummaries map[uint32]string
	recentRequestOrder     []uint32
	streamsMu              sync.Mutex

	serverHTTP2Settings map[uint16]uint32
	settingsMu          sync.Mutex

	pendingBuffer *bytes.Buffer
}

func NewWatcherWrapConn(conn net.Conn) *WatcherWrapConn {
	requestBuffer := bytes.NewBuffer([]byte{})
	requestEncoder := hpack.NewEncoder(requestBuffer)

	return &WatcherWrapConn{
		Conn:                     conn,
		streams:                  map[uint32]*http2Stream{},
		recentRequestSummaries:   map[uint32]string{},
		serverHTTP2Settings:      map[uint16]uint32{},
		requestDecoderFromClient: hpack.NewDecoder(65536, nil),
		requestEncoderBuffer:     requestBuffer,
		requestEncoderToServer:   requestEncoder,
	}
}

func (w *WatcherWrapConn) ConnectionDiagnosticString() string {
	if w == nil {
		return "type=*tls.WatcherWrapConn nil"
	}

	pendingLen := 0
	if w.pendingBuffer != nil {
		pendingLen = w.pendingBuffer.Len()
	}

	streamCount, activeStreams, latestSummary := w.http2DiagnosticSnapshot()
	settingsCount := w.http2SettingsCount()

	return fmt.Sprintf(
		"type=%T proto=%s prefetched=%t http2_preface_sent=%t passthrough=%t sniffed_http1=%t req_buf=%d resp_buf=%d pending=%d streams=%d active=%s latest=%s settings=%d underlying={%s}",
		w,
		w.protocolLabel(),
		w.prefetched,
		w.http2PrefaceSent,
		w.passthrough,
		w.isHttp1,
		w.reqBuf.Len(),
		w.respBuf.Len(),
		pendingLen,
		streamCount,
		activeStreams,
		latestSummary,
		settingsCount,
		connectionDiagnosticString(w.Conn),
	)
}

func (w *WatcherWrapConn) protocolLabel() string {
	switch {
	case w.prefetched && w.passthrough:
		return "http2-passthrough"
	case w.prefetched:
		return "http2"
	case w.isHttp1 && w.passthrough:
		return "http1-passthrough"
	case w.isHttp1:
		return "http1"
	case w.passthrough:
		return "passthrough"
	case w.reqBuf.Len() > 0:
		return "sniffing"
	default:
		return "unknown"
	}
}

func (w *WatcherWrapConn) http2DiagnosticSnapshot() (int, string, string) {
	w.streamsMu.Lock()
	defer w.streamsMu.Unlock()

	streamCount := len(w.streams)
	if streamCount == 0 {
		latestSummary := "none"
		for i := len(w.recentRequestOrder) - 1; i >= 0; i-- {
			streamID := w.recentRequestOrder[i]
			if summary, ok := w.recentRequestSummaries[streamID]; ok && strings.TrimSpace(summary) != "" {
				latestSummary = fmt.Sprintf("%d:%s", streamID, summary)
				break
			}
		}
		return 0, "[]", latestSummary
	}

	streamIDs := make([]int, 0, len(w.streams))
	for streamID := range w.streams {
		streamIDs = append(streamIDs, int(streamID))
	}
	sort.Ints(streamIDs)

	active := make([]string, 0, minInt(len(streamIDs), 3))
	for i, streamID := range streamIDs {
		if i >= 3 {
			break
		}
		stream := w.streams[uint32(streamID)]
		summary := summarizeHTTP2RequestMap(nil)
		if stream != nil {
			if strings.TrimSpace(stream.ReqSummary) != "" {
				summary = stream.ReqSummary
			} else {
				summary = summarizeHTTP2RequestMap(stream.ReqHeaders)
			}
		}
		active = append(active, fmt.Sprintf("%d:%s", streamID, summary))
	}

	latestSummary := "none"
	for i := len(w.recentRequestOrder) - 1; i >= 0; i-- {
		streamID := w.recentRequestOrder[i]
		if summary, ok := w.recentRequestSummaries[streamID]; ok && strings.TrimSpace(summary) != "" {
			latestSummary = fmt.Sprintf("%d:%s", streamID, summary)
			break
		}
	}

	return streamCount, "[" + strings.Join(active, "; ") + "]", latestSummary
}

func (w *WatcherWrapConn) http2SettingsCount() int {
	w.settingsMu.Lock()
	defer w.settingsMu.Unlock()
	return len(w.serverHTTP2Settings)
}

func connectionDiagnosticString(conn net.Conn) string {
	if conn == nil {
		return "nil"
	}
	if diagnosticConn, ok := conn.(interface{ ConnectionDiagnosticString() string }); ok {
		return diagnosticConn.ConnectionDiagnosticString()
	}

	_, canCloseRead := conn.(interface{ CloseRead() error })
	_, canCloseWrite := conn.(interface{ CloseWrite() error })
	return fmt.Sprintf(
		"type=%T can_close_read=%t can_close_write=%t",
		conn,
		canCloseRead,
		canCloseWrite,
	)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (w *WatcherWrapConn) getOrCreateStream(id uint32) *http2Stream {
	w.streamsMu.Lock()
	defer w.streamsMu.Unlock()
	if s, ok := w.streams[id]; ok {
		return s
	}
	s := &http2Stream{}
	w.streams[id] = s
	return s
}

func (w *WatcherWrapConn) Read(p []byte) (int, error) {
	for {
		if w.pendingBuffer != nil && w.pendingBuffer.Len() > 0 {
			return w.readFromPending(p)
		}

		if w.passthrough {
			if w.reqBuf.Len() > 0 {
				buffered := append([]byte(nil), w.reqBuf.Bytes()...)
				w.reqBuf.Reset()
				w.pendingBuffer = bytes.NewBuffer(buffered)
				continue
			}
			return w.Conn.Read(p)
		}

		out, progressed, err := w.prepareBufferedOutput()
		if err != nil {
			logger.Warn(fmt.Sprintf("WatcherWrapConn.Read: prepareBufferedOutput failed: %v", err))
			return 0, err
		}
		if len(out) > 0 {
			w.pendingBuffer = bytes.NewBuffer(out)
			continue
		}
		if progressed {
			continue
		}

		readSize := len(p)
		if readSize < 1 {
			readSize = 1
		}
		tmp := make([]byte, readSize)
		n, err := w.Conn.Read(tmp)
		if n > 0 {
			w.reqBuf.Write(tmp[:n])
		}
		if err != nil {
			if n > 0 {
				continue
			}
			logger.Warn(fmt.Sprintf("WatcherWrapConn.Read: underlying read failed: %v state=%s", err, w.ConnectionDiagnosticString()))
			return 0, err
		}
	}
}

func (w *WatcherWrapConn) readFromPending(p []byte) (int, error) {
	copied := copy(p, w.pendingBuffer.Bytes())
	w.pendingBuffer.Next(copied)
	if w.pendingBuffer.Len() == 0 {
		w.pendingBuffer = nil
	}
	return copied, nil
}

func (w *WatcherWrapConn) prepareBufferedOutput() ([]byte, bool, error) {
	if w.prefetched {
		out, err := w.parseHttp2Req()
		if err != nil {
			logger.Warn(fmt.Sprintf("HTTP/2 request parsing failed, falling back to passthrough: %v", err))
			return w.fallbackHTTP2ToPassthrough(out), true, nil
		}
		if !w.http2PrefaceSent {
			w.http2PrefaceSent = true
			return append([]byte(http2.ClientPreface), out...), true, nil
		}
		if len(out) > 0 {
			return out, true, nil
		}
		return nil, false, nil
	}

	if w.isHttp1 {
		if w.http1BodyTracker != nil {
			out, progressed, err := w.consumeHTTP1Body()
			if err != nil {
				logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/1 body consumption failed: %v", err))
				return nil, false, err
			}
			if len(out) > 0 {
				return out, true, nil
			}
			if progressed {
				return nil, true, nil
			}
			return nil, false, nil
		}

		out, ready, err := w.parseHttp1Req()
		if err != nil {
			logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/1 request parsing failed: %v", err))
			return nil, false, err
		}
		if ready {
			return out, true, nil
		}
		return nil, false, nil
	}

	decision, decided := classifyBufferedProtocol(w.reqBuf.Bytes())
	if !decided {
		if w.reqBuf.Len() >= maxProtocolSniffBytes {
			w.passthrough = true
			return nil, true, nil
		}
		return nil, false, nil
	}

	switch decision {
	case "http2":
		w.prefetched = true
		w.reqBuf.Next(len(http2.ClientPreface))
		logger.Debug("HTTP/2 connection preface detected")
		return nil, true, nil
	case "http1":
		w.isHttp1 = true
		logger.Debug("HTTP/1 connection preface detected")
		return nil, true, nil
	default:
		w.passthrough = true
		return nil, true, nil
	}
}

func classifyBufferedProtocol(buf []byte) (string, bool) {
	if len(buf) == 0 {
		return "", false
	}

	preface := []byte(http2.ClientPreface)
	if len(buf) >= len(preface) && bytes.Equal(preface, buf[:len(preface)]) {
		return "http2", true
	}
	if len(buf) < len(preface) && bytes.Equal(preface[:len(buf)], buf) {
		return "", false
	}

	upperPrefix := strings.ToUpper(string(buf))
	for _, method := range http1Methods {
		switch {
		case strings.HasPrefix(upperPrefix, method+" "):
			return "http1", true
		case strings.HasPrefix(method, upperPrefix):
			return "", false
		}
	}

	if len(buf) < len(preface) {
		return "", false
	}
	return "passthrough", true
}

func (w *WatcherWrapConn) parseHttp1Req() ([]byte, bool, error) {
	return w.processH1ReqHeaders()
}

func (w *WatcherWrapConn) parseHttp2Req() ([]byte, error) {
	preBuff := bytes.NewBuffer(nil)
	if err := w.processHttp2RequestFrame(preBuff); err != nil {
		logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/2 request frame processing failed: %v", err))
		return preBuff.Bytes(), err
	}
	return preBuff.Bytes(), nil
}

func (w *WatcherWrapConn) fallbackHTTP2ToPassthrough(consumed []byte) []byte {
	w.passthrough = true

	var out bytes.Buffer
	if !w.http2PrefaceSent {
		w.http2PrefaceSent = true
		out.WriteString(http2.ClientPreface)
	}
	if len(consumed) > 0 {
		out.Write(consumed)
	}
	if w.reqBuf.Len() > 0 {
		out.Write(w.reqBuf.Bytes())
		w.reqBuf.Reset()
	}
	return out.Bytes()
}

func (w *WatcherWrapConn) Write(p []byte) (n int, err error) {
	if len(p) > 0 && w.prefetched && !w.passthrough {
		w.observeHTTP2ResponseFrames(p)
	}
	n, err = w.Conn.Write(p)
	if err != nil {
		logger.Warn(fmt.Sprintf("WatcherWrapConn.Write: underlying write failed: %v state=%s", err, w.ConnectionDiagnosticString()))
	}
	return n, err
}

func (w *WatcherWrapConn) tryExtractFrameFromBuf(buf *bytes.Buffer, shouldMove bool) ([]byte, bool) {
	data := buf.Bytes()
	if len(data) < frameHeaderLen {
		return nil, false
	}
	length := binary.BigEndian.Uint32(append([]byte{0}, data[0:3]...))
	totalLen := frameHeaderLen + int(length)
	if len(data) < totalLen {
		return nil, false
	}
	frame := make([]byte, totalLen)
	copy(frame, data[:totalLen])
	if shouldMove {
		buf.Next(totalLen)
	}
	return frame, true
}
