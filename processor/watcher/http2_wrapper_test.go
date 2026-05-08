package tls

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func buildHeaderBlockWithDynamicTableSize(t *testing.T, tableSize uint32, field hpack.HeaderField) []byte {
	t.Helper()

	var buf bytes.Buffer
	enc := hpack.NewEncoder(&buf)
	enc.SetMaxDynamicTableSizeLimit(tableSize)
	enc.SetMaxDynamicTableSize(tableSize)
	if err := enc.WriteField(field); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	return buf.Bytes()
}

func buildSettingsFrame(t *testing.T, setting http2.Setting) []byte {
	t.Helper()

	var buf bytes.Buffer
	framer := http2.NewFramer(&buf, nil)
	if err := framer.WriteSettings(setting); err != nil {
		t.Fatalf("WriteSettings() error = %v", err)
	}
	return buf.Bytes()
}

func buildHeadersFrame(t *testing.T, p http2.HeadersFrameParam) []byte {
	t.Helper()

	var buf bytes.Buffer
	framer := http2.NewFramer(&buf, nil)
	if err := framer.WriteHeaders(p); err != nil {
		t.Fatalf("WriteHeaders() error = %v", err)
	}
	return buf.Bytes()
}

func buildHeadersFramePreservingPriorityFlag(t *testing.T, p http2.HeadersFrameParam, forcePriority bool) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := writeHTTP2HeadersFrame(&buf, p, forcePriority); err != nil {
		t.Fatalf("writeHTTP2HeadersFrame() error = %v", err)
	}
	return buf.Bytes()
}

func buildDataFrame(t *testing.T, streamID uint32, endStream bool, payload []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	framer := http2.NewFramer(&buf, nil)
	if err := framer.WriteData(streamID, endStream, payload); err != nil {
		t.Fatalf("WriteData() error = %v", err)
	}
	return buf.Bytes()
}

func extractHeaderBlockFragments(t *testing.T, frames []byte) []byte {
	t.Helper()

	var block bytes.Buffer
	reader := bytes.NewReader(frames)
	framer := http2.NewFramer(nil, reader)

	for {
		frame, err := framer.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}

		switch f := frame.(type) {
		case *http2.HeadersFrame:
			block.Write(f.HeaderBlockFragment())
		case *http2.ContinuationFrame:
			block.Write(f.HeaderBlockFragment())
		default:
			t.Fatalf("unexpected frame type %T while extracting header block", frame)
		}
	}

	return block.Bytes()
}

func TestParseSettingsFrame_ServerUpdatesRequestDecoderAndEncoder(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.ParseSettingsFrame([]byte{
		0x00, 0x01, // HEADER_TABLE_SIZE
		0x00, 0x00, 0x20, 0x00, // 8192
		0x00, 0x05, // MAX_FRAME_SIZE
		0x00, 0x00, 0x10, 0x00, // 4096
	}, http2SettingsSourceServer)

	block := buildHeaderBlockWithDynamicTableSize(t, 8192, hpack.HeaderField{Name: ":method", Value: "GET"})
	headers, err := w.decodeHeaderBlock(block, true)
	if err != nil {
		t.Fatalf("decodeHeaderBlock(request) error = %v", err)
	}
	if got := headers[":method"]; got != "GET" {
		t.Fatalf("decoded request pseudo-header = %q, want %q", got, "GET")
	}

	if got, ok := w.GetServerHTTP2Setting(SETTINGS_MAX_FRAME_SIZE); !ok || got != 4096 {
		t.Fatalf("GetServerHTTP2Setting(MAX_FRAME_SIZE) = (%d, %t), want (4096, true)", got, ok)
	}
	if got := w.requestEncoderToServer.MaxDynamicTableSize(); got != 8192 {
		t.Fatalf("encoder dynamic table size = %d, want %d", got, 8192)
	}
}

func TestWatcherWrapConnWrite_PassthroughsServerFrames(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	w := NewWatcherWrapConn(serverConn)
	w.prefetched = true

	frame := buildSettingsFrame(t, http2.Setting{
		ID:  http2.SettingHeaderTableSize,
		Val: 8192,
	})

	readDone := make(chan error, 1)
	readBuf := make(chan []byte, 1)
	go func() {
		buf := make([]byte, len(frame))
		_, err := io.ReadFull(clientConn, buf)
		readBuf <- buf
		readDone <- err
	}()

	if _, err := w.Write(frame); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("reader error = %v", err)
	}
	if got := <-readBuf; !bytes.Equal(got, frame) {
		t.Fatalf("Write() passthrough mismatch")
	}
	if got, ok := w.GetServerHTTP2Setting(SETTINGS_HEADER_TABLE_SIZE); !ok || got != 8192 {
		t.Fatalf("GetServerHTTP2Setting(HEADER_TABLE_SIZE) = (%d, %t), want (8192, true)", got, ok)
	}
}

func TestWatcherWrapConnWrite_BuffersPartialServerFrames(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	w := NewWatcherWrapConn(serverConn)
	w.prefetched = true

	frame := buildSettingsFrame(t, http2.Setting{
		ID:  http2.SettingHeaderTableSize,
		Val: 4096,
	})

	readDone := make(chan error, 1)
	readBuf := make(chan []byte, 1)
	go func() {
		buf := make([]byte, len(frame))
		_, err := io.ReadFull(clientConn, buf)
		readBuf <- buf
		readDone <- err
	}()

	split := len(frame) / 2
	if _, err := w.Write(frame[:split]); err != nil {
		t.Fatalf("Write(first half) error = %v", err)
	}
	if _, ok := w.GetServerHTTP2Setting(SETTINGS_HEADER_TABLE_SIZE); ok {
		t.Fatalf("GetServerHTTP2Setting(HEADER_TABLE_SIZE) unexpectedly set before complete frame")
	}

	if _, err := w.Write(frame[split:]); err != nil {
		t.Fatalf("Write(second half) error = %v", err)
	}
	if err := <-readDone; err != nil {
		t.Fatalf("reader error = %v", err)
	}
	if got := <-readBuf; !bytes.Equal(got, frame) {
		t.Fatalf("Write() passthrough mismatch after split frame")
	}
	if got, ok := w.GetServerHTTP2Setting(SETTINGS_HEADER_TABLE_SIZE); !ok || got != 4096 {
		t.Fatalf("GetServerHTTP2Setting(HEADER_TABLE_SIZE) = (%d, %t), want (4096, true)", got, ok)
	}
}

func TestRebuildReqHeadersWithInjectedField_SequentialStreamsRemainConnectionDecodable(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.ParseSettingsFrame([]byte{
		0x00, 0x01, // HEADER_TABLE_SIZE
		0x00, 0x00, 0x10, 0x00, // 4096
	}, http2SettingsSourceServer)

	firstFrames, firstFields, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/one"},
			{Name: "user-agent", Value: "agent-a"},
		},
		1,
		http2HeaderRewriteMeta{endStream: false},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("first rebuildReqHeadersWithInjectedField() error = %v", err)
	}
	secondFrames, secondFields, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/two"},
			{Name: "user-agent", Value: "agent-a"},
		},
		3,
		http2HeaderRewriteMeta{endStream: true},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("second rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	serverDecoder := hpack.NewDecoder(4096, nil)

	firstDecoded, err := w.decodeHeaderBlock(extractHeaderBlockFragments(t, firstFrames), true)
	if err != nil {
		t.Fatalf("local decode first block error = %v", err)
	}
	if got := firstDecoded[":path"]; got != "/one" {
		t.Fatalf("decoded first path = %q, want %q", got, "/one")
	}

	serverHeaders1 := map[string]string{}
	serverDecoder.SetEmitFunc(func(f hpack.HeaderField) { serverHeaders1[f.Name] = f.Value })
	if _, err := serverDecoder.Write(extractHeaderBlockFragments(t, firstFrames)); err != nil {
		t.Fatalf("server decoder first block error = %v", err)
	}
	if got := serverHeaders1[":path"]; got != "/one" {
		t.Fatalf("server decoded first path = %q, want %q", got, "/one")
	}

	serverHeaders2 := map[string]string{}
	serverDecoder.SetEmitFunc(func(f hpack.HeaderField) { serverHeaders2[f.Name] = f.Value })
	if _, err := serverDecoder.Write(extractHeaderBlockFragments(t, secondFrames)); err != nil {
		t.Fatalf("server decoder second block error = %v", err)
	}
	if got := serverHeaders2[":path"]; got != "/two" {
		t.Fatalf("server decoded second path = %q, want %q", got, "/two")
	}

	if len(firstFields) == 0 || len(secondFields) == 0 {
		t.Fatal("expected rewritten header field snapshots to be returned")
	}
}

func TestRebuildReqHeadersWithInjectedField_UsesLatestServerMaxFrameSize(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.ParseSettingsFrame([]byte{
		0x00, 0x05, // MAX_FRAME_SIZE
		0x00, 0x00, 0x00, 0x10, // 16
	}, http2SettingsSourceServer)
	w.ParseSettingsFrame([]byte{
		0x00, 0x05, // MAX_FRAME_SIZE
		0x00, 0x00, 0x00, 0x20, // 32
	}, http2SettingsSourceServer)

	var largeValue bytes.Buffer
	for i := 0; i < 80; i++ {
		largeValue.WriteByte('a')
	}

	frames, _, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/frame-size"},
			{Name: "x-large", Value: largeValue.String()},
		},
		1,
		http2HeaderRewriteMeta{endStream: false},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	reader := bytes.NewReader(frames)
	framer := http2.NewFramer(nil, reader)
	for {
		frame, err := framer.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		switch f := frame.(type) {
		case *http2.HeadersFrame:
			if got := len(f.HeaderBlockFragment()); got > 32 {
				t.Fatalf("headers fragment length = %d, want <= 32", got)
			}
		case *http2.ContinuationFrame:
			if got := len(f.HeaderBlockFragment()); got > 32 {
				t.Fatalf("continuation fragment length = %d, want <= 32", got)
			}
		}
	}
}

func TestRebuildReqHeadersWithInjectedField_PreservesEndStreamOnSplitHeaders(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.ParseSettingsFrame([]byte{
		0x00, 0x05, // MAX_FRAME_SIZE
		0x00, 0x00, 0x00, 0x10, // 16
	}, http2SettingsSourceServer)

	var largeValue bytes.Buffer
	for i := 0; i < 80; i++ {
		largeValue.WriteByte('b')
	}

	frames, _, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/end-stream"},
			{Name: "x-large", Value: largeValue.String()},
		},
		1,
		http2HeaderRewriteMeta{endStream: true},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	reader := bytes.NewReader(frames)
	framer := http2.NewFramer(nil, reader)

	firstFrame, err := framer.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(first) error = %v", err)
	}
	headersFrame, ok := firstFrame.(*http2.HeadersFrame)
	if !ok {
		t.Fatalf("first frame type = %T, want *http2.HeadersFrame", firstFrame)
	}
	if !headersFrame.StreamEnded() {
		t.Fatal("headers frame StreamEnded = false, want true")
	}
	if headersFrame.HeadersEnded() {
		t.Fatal("headers frame HeadersEnded = true, want false for split header block")
	}

	secondFrame, err := framer.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame(second) error = %v", err)
	}
	if _, ok := secondFrame.(*http2.ContinuationFrame); !ok {
		t.Fatalf("second frame type = %T, want *http2.ContinuationFrame", secondFrame)
	}
}

func TestPrepareBufferedOutput_FallbackPreservesPreviouslyConsumedFrames(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.prefetched = true

	settingsFrame := buildSettingsFrame(t, http2.Setting{
		ID:  http2.SettingHeaderTableSize,
		Val: 4096,
	})
	malformedHeaders := buildHeadersFrame(t, http2.HeadersFrameParam{
		StreamID:      1,
		BlockFragment: []byte{0x82},
		EndStream:     false,
		EndHeaders:    false,
	})
	dataFrame := buildDataFrame(t, 1, false, []byte("not-a-continuation"))

	w.reqBuf.Write(settingsFrame)
	w.reqBuf.Write(malformedHeaders)
	w.reqBuf.Write(dataFrame)

	out, progressed, err := w.prepareBufferedOutput()
	if err != nil {
		t.Fatalf("prepareBufferedOutput() error = %v", err)
	}
	if !progressed {
		t.Fatal("prepareBufferedOutput() progressed = false, want true")
	}

	wantFallback := append([]byte(http2.ClientPreface), settingsFrame...)
	wantFallback = append(wantFallback, malformedHeaders...)
	wantFallback = append(wantFallback, dataFrame...)
	if !bytes.Equal(out, wantFallback) {
		t.Fatalf("fallback output mismatch: got %d bytes want %d bytes", len(out), len(wantFallback))
	}
	if !bytes.Contains(out, settingsFrame) {
		t.Fatal("fallback output did not preserve settings frame")
	}
	if !w.passthrough {
		t.Fatal("w.passthrough = false, want true after fallback")
	}
	if !w.http2PrefaceSent {
		t.Fatal("w.http2PrefaceSent = false, want true after fallback")
	}
}

func TestProcessHTTP2RequestFrame_DoesNotRetainDataPayload(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	streamID := uint32(1)
	stream := w.getOrCreateStream(streamID)
	stream.ReqSummary = `method="POST" authority="example.com" path="/v1/chat"`

	payload := bytes.Repeat([]byte("x"), 128*1024)
	dataFrame := buildDataFrame(t, streamID, false, payload)
	w.reqBuf.Write(dataFrame)

	var out bytes.Buffer
	if err := w.processHttp2RequestFrame(&out); err != nil {
		t.Fatalf("processHttp2RequestFrame() error = %v", err)
	}
	if !bytes.Equal(out.Bytes(), dataFrame) {
		t.Fatalf("forwarded frame mismatch: got %d bytes want %d bytes", out.Len(), len(dataFrame))
	}

	w.streamsMu.Lock()
	defer w.streamsMu.Unlock()
	if _, ok := w.streams[streamID]; !ok {
		t.Fatal("stream was removed before END_STREAM")
	}
}

func TestIsHTTP2InitialRequestHeaders(t *testing.T) {
	if !isHTTP2InitialRequestHeaders([]hpack.HeaderField{
		{Name: ":method", Value: "POST"},
		{Name: ":scheme", Value: "https"},
		{Name: ":authority", Value: "example.com"},
		{Name: ":path", Value: "/chat"},
	}) {
		t.Fatal("initial request headers were not detected")
	}

	if isHTTP2InitialRequestHeaders([]hpack.HeaderField{
		{Name: "grpc-status", Value: "0"},
		{Name: "x-trailer", Value: "done"},
	}) {
		t.Fatal("trailers were incorrectly treated as initial request headers")
	}
}

func TestRebuildReqHeadersWithInjectedField_EmptyInjectKeyDoesNotAddAuthorizationInner(t *testing.T) {
	w := NewWatcherWrapConn(nil)

	_, rewrittenFields, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: "grpc-status", Value: "0"},
			{Name: "x-trailer", Value: "done"},
		},
		1,
		http2HeaderRewriteMeta{endStream: true},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	if _, ok := getHTTP2HeaderFieldValue(rewrittenFields, "authorization-inner"); ok {
		t.Fatal("authorization-inner unexpectedly added to trailer headers")
	}
}

func TestParseHTTP2HeaderRewriteMeta_PreservesPriorityFlagAndPadding(t *testing.T) {
	w := NewWatcherWrapConn(nil)

	rawHeaders := buildHeadersFramePreservingPriorityFlag(t, http2.HeadersFrameParam{
		StreamID: 1,
		BlockFragment: extractHeaderBlockFragments(t, buildHeadersFrame(t, http2.HeadersFrameParam{
			StreamID:      1,
			BlockFragment: buildHeaderBlockWithDynamicTableSize(t, 4096, hpack.HeaderField{Name: ":method", Value: "GET"}),
			EndHeaders:    true,
		})),
		EndHeaders: true,
		PadLength:  3,
		Priority:   http2.PriorityParam{},
	}, true)

	metaFrame, err := w.decodeMetaHeaders(rawHeaders)
	if err != nil {
		t.Fatalf("decodeMetaHeaders() error = %v", err)
	}

	rewriteMeta, err := parseHTTP2HeaderRewriteMeta(rawHeaders, metaFrame)
	if err != nil {
		t.Fatalf("parseHTTP2HeaderRewriteMeta() error = %v", err)
	}
	if !rewriteMeta.hasPriority {
		t.Fatal("rewriteMeta.hasPriority = false, want true")
	}
	if rewriteMeta.padLength != 3 {
		t.Fatalf("rewriteMeta.padLength = %d, want 3", rewriteMeta.padLength)
	}
	if !rewriteMeta.priority.IsZero() {
		t.Fatalf("rewriteMeta.priority = %+v, want zero priority fields", rewriteMeta.priority)
	}
}

func TestRebuildReqHeadersWithInjectedField_PreservesPriorityFlagAndPadding(t *testing.T) {
	w := NewWatcherWrapConn(nil)

	frames, _, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/priority"},
		},
		1,
		http2HeaderRewriteMeta{
			endStream:   true,
			hasPriority: true,
			priority:    http2.PriorityParam{},
			padLength:   2,
		},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	reader := bytes.NewReader(frames)
	framer := http2.NewFramer(nil, reader)
	frame, err := framer.ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}

	headersFrame, ok := frame.(*http2.HeadersFrame)
	if !ok {
		t.Fatalf("frame type = %T, want *http2.HeadersFrame", frame)
	}
	if !headersFrame.HasPriority() {
		t.Fatal("headersFrame.HasPriority() = false, want true")
	}
	if !headersFrame.StreamEnded() {
		t.Fatal("headersFrame.StreamEnded() = false, want true")
	}
	if got := headersFrame.FrameHeader.Flags.Has(http2.FlagHeadersPadded); !got {
		t.Fatal("HEADERS padded flag missing after rebuild")
	}
}

func TestRebuildReqHeadersWithInjectedField_FirstFrameRespectsMaxFrameSizeWithPriorityAndPadding(t *testing.T) {
	w := NewWatcherWrapConn(nil)
	w.ParseSettingsFrame([]byte{
		0x00, 0x05,
		0x00, 0x00, 0x00, 0x20,
	}, http2SettingsSourceServer)

	var largeValue bytes.Buffer
	for i := 0; i < 80; i++ {
		largeValue.WriteByte('c')
	}

	frames, _, err := w.rebuildReqHeadersWithInjectedField(
		[]hpack.HeaderField{
			{Name: ":method", Value: "GET"},
			{Name: ":scheme", Value: "https"},
			{Name: ":authority", Value: "example.com"},
			{Name: ":path", Value: "/budget"},
			{Name: "x-large", Value: largeValue.String()},
		},
		1,
		http2HeaderRewriteMeta{
			hasPriority: true,
			priority: http2.PriorityParam{
				StreamDep: 1,
				Weight:    15,
			},
			padLength: 2,
		},
		"",
		"",
	)
	if err != nil {
		t.Fatalf("rebuildReqHeadersWithInjectedField() error = %v", err)
	}

	reader := bytes.NewReader(frames)
	framer := http2.NewFramer(nil, reader)
	for {
		frame, err := framer.ReadFrame()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}
		if frame.Header().Length > 32 {
			t.Fatalf("frame length = %d, want <= 32", frame.Header().Length)
		}
	}
}

type diagnosticTestConn struct {
	net.Conn
	diagnostic string
}

func (c *diagnosticTestConn) ConnectionDiagnosticString() string {
	return c.diagnostic
}

func TestWatcherWrapConnConnectionDiagnosticString_ReportsHTTP2State(t *testing.T) {
	w := NewWatcherWrapConn(&diagnosticTestConn{diagnostic: "underlying=test"})
	w.prefetched = true
	w.http2PrefaceSent = true
	w.serverHTTP2Settings[SETTINGS_MAX_FRAME_SIZE] = 32768
	w.pendingBuffer = bytes.NewBufferString("abc")
	w.reqBuf.WriteString("req")
	w.respBuf.WriteString("resp")

	stream := w.getOrCreateStream(3)
	stream.ReqSummary = `method="GET" authority="example.com" path="/v1/chat"`
	w.rememberRecentHTTP2RequestSummary(3, stream.ReqSummary)

	diagnostic := w.ConnectionDiagnosticString()
	checks := []string{
		"proto=http2",
		"http2_preface_sent=true",
		"streams=1",
		"active=[3:method=\"GET\" authority=\"example.com\" path=\"/v1/chat\"]",
		"latest=3:method=\"GET\" authority=\"example.com\" path=\"/v1/chat\"",
		"settings=1",
		"underlying={underlying=test}",
	}
	for _, check := range checks {
		if !strings.Contains(diagnostic, check) {
			t.Fatalf("diagnostic %q missing %q", diagnostic, check)
		}
	}
}
