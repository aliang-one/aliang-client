package tls

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	user "aliang.one/nursorgate/processor/auth"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

const (
	frameHeaderLen        = 9
	frameTypeData         = 0x0
	frameTypeHeaders      = 0x1
	frameTypePriority     = 0x2
	frameTypeRstStream    = 0x3
	frameTypeSettings     = 0x4
	frameTypePushPromise  = 0x5
	frameTypePing         = 0x6
	frameTypeGoaway       = 0x7
	frameTypeWindowUpdate = 0x8
	frameTypeContinuation = 0x9

	flagEndStream  = 0x1
	flagAck        = 0x1
	flagEndHeaders = 0x4
	flagPadded     = 0x8
	flagPriority   = 0x20

	SETTINGS_HEADER_TABLE_SIZE      = 0x1
	SETTINGS_ENABLE_PUSH            = 0x2
	SETTINGS_MAX_CONCURRENT_STREAMS = 0x3
	SETTINGS_INITIAL_WINDOW_SIZE    = 0x4
	SETTINGS_MAX_FRAME_SIZE         = 0x5
	SETTINGS_MAX_HEADER_LIST_SIZE   = 0x6
)

type http2SettingsSource string

const (
	http2SettingsSourceServer http2SettingsSource = "server"
)

func (w *WatcherWrapConn) applyHTTP2Setting(setting http2.Setting, source http2SettingsSource) {
	w.settingsMu.Lock()
	defer w.settingsMu.Unlock()

	identifier := uint16(setting.ID)
	value := setting.Val
	w.serverHTTP2Settings[identifier] = value

	var settingName string
	switch identifier {
	case SETTINGS_HEADER_TABLE_SIZE:
		settingName = "HEADER_TABLE_SIZE"
		w.requestDecoderFromClient.SetAllowedMaxDynamicTableSize(value)
		w.requestEncoderToServer.SetMaxDynamicTableSizeLimit(value)
		w.requestEncoderToServer.SetMaxDynamicTableSize(value)
	case SETTINGS_ENABLE_PUSH:
		settingName = "ENABLE_PUSH"
	case SETTINGS_MAX_CONCURRENT_STREAMS:
		settingName = "MAX_CONCURRENT_STREAMS"
	case SETTINGS_INITIAL_WINDOW_SIZE:
		settingName = "INITIAL_WINDOW_SIZE"
	case SETTINGS_MAX_FRAME_SIZE:
		settingName = "MAX_FRAME_SIZE"
	case SETTINGS_MAX_HEADER_LIST_SIZE:
		settingName = "MAX_HEADER_LIST_SIZE"
	default:
		settingName = fmt.Sprintf("UNKNOWN_%d", identifier)
	}

	logger.Debug(fmt.Sprintf("HTTP/2 SETTINGS(%s): %s = %d", source, settingName, value))
}

func (w *WatcherWrapConn) ParseSettingsFrame(payload []byte, source http2SettingsSource) {
	if len(payload)%6 != 0 {
		logger.Warn(fmt.Sprintf("WatcherWrapConn: invalid HTTP/2 SETTINGS payload length=%d source=%s", len(payload), source))
		return
	}

	for i := 0; i < len(payload); i += 6 {
		w.applyHTTP2Setting(http2.Setting{
			ID:  http2.SettingID(binary.BigEndian.Uint16(payload[i : i+2])),
			Val: binary.BigEndian.Uint32(payload[i+2 : i+6]),
		}, source)
	}
}

func (w *WatcherWrapConn) getHTTP2Setting(identifier uint16, source http2SettingsSource) (uint32, bool) {
	w.settingsMu.Lock()
	defer w.settingsMu.Unlock()
	value, exists := w.serverHTTP2Settings[identifier]
	return value, exists
}

func (w *WatcherWrapConn) GetServerHTTP2Setting(identifier uint16) (uint32, bool) {
	return w.getHTTP2Setting(identifier, http2SettingsSourceServer)
}

func (w *WatcherWrapConn) getAllHTTP2Settings(source http2SettingsSource) map[uint16]uint32 {
	w.settingsMu.Lock()
	defer w.settingsMu.Unlock()

	settings := make(map[uint16]uint32, len(w.serverHTTP2Settings))
	for k, v := range w.serverHTTP2Settings {
		settings[k] = v
	}
	return settings
}

func (w *WatcherWrapConn) GetAllServerHTTP2Settings() map[uint16]uint32 {
	return w.getAllHTTP2Settings(http2SettingsSourceServer)
}

type http2Stream struct {
	ReqHeaders   map[string]string
	ReqBody      bytes.Buffer
	ReqEndStream bool
	ReqSummary   string
	AuthInjected bool

	RespHeaders   map[string]string
	RespBody      bytes.Buffer
	RespEndStream bool
}

type http2HeaderRewriteMeta struct {
	endStream   bool
	hasPriority bool
	priority    http2.PriorityParam
	padLength   uint8
}

const maxRecentHTTP2RequestSummaries = 128

func getHTTP2HeaderFieldValue(fields []hpack.HeaderField, target string) (string, bool) {
	for _, field := range fields {
		if strings.EqualFold(strings.TrimSpace(field.Name), target) {
			return field.Value, true
		}
	}
	return "", false
}

func summarizeHTTP2Request(fields []hpack.HeaderField) string {
	method, _ := getHTTP2HeaderFieldValue(fields, ":method")
	path, _ := getHTTP2HeaderFieldValue(fields, ":path")
	authority, ok := getHTTP2HeaderFieldValue(fields, ":authority")
	if !ok || strings.TrimSpace(authority) == "" {
		authority, _ = getHTTP2HeaderFieldValue(fields, "host")
	}
	return fmt.Sprintf("method=%q authority=%q path=%q", method, authority, path)
}

func summarizeHTTP2RequestMap(headers map[string]string) string {
	if len(headers) == 0 {
		return "method=\"\" authority=\"\" path=\"\""
	}
	method := headers[":method"]
	authority := headers[":authority"]
	if strings.TrimSpace(authority) == "" {
		authority = headers["host"]
	}
	path := headers[":path"]
	return fmt.Sprintf("method=%q authority=%q path=%q", method, authority, path)
}

func isHTTP2InitialRequestHeaders(fields []hpack.HeaderField) bool {
	if len(fields) == 0 {
		return false
	}
	_, hasMethod := getHTTP2HeaderFieldValue(fields, ":method")
	_, hasPath := getHTTP2HeaderFieldValue(fields, ":path")
	_, hasScheme := getHTTP2HeaderFieldValue(fields, ":scheme")
	return hasMethod || hasPath || hasScheme
}

func http2ErrorCodeName(code uint32) string {
	switch http2.ErrCode(code) {
	case http2.ErrCodeNo:
		return "NO_ERROR"
	case http2.ErrCodeProtocol:
		return "PROTOCOL_ERROR"
	case http2.ErrCodeInternal:
		return "INTERNAL_ERROR"
	case http2.ErrCodeFlowControl:
		return "FLOW_CONTROL_ERROR"
	case http2.ErrCodeSettingsTimeout:
		return "SETTINGS_TIMEOUT"
	case http2.ErrCodeStreamClosed:
		return "STREAM_CLOSED"
	case http2.ErrCodeFrameSize:
		return "FRAME_SIZE_ERROR"
	case http2.ErrCodeRefusedStream:
		return "REFUSED_STREAM"
	case http2.ErrCodeCancel:
		return "CANCEL"
	case http2.ErrCodeCompression:
		return "COMPRESSION_ERROR"
	case http2.ErrCodeConnect:
		return "CONNECT_ERROR"
	case http2.ErrCodeEnhanceYourCalm:
		return "ENHANCE_YOUR_CALM"
	case http2.ErrCodeInadequateSecurity:
		return "INADEQUATE_SECURITY"
	case http2.ErrCodeHTTP11Required:
		return "HTTP_1_1_REQUIRED"
	default:
		return "UNKNOWN"
	}
}

func headerFieldsToMap(fields []hpack.HeaderField) map[string]string {
	headers := make(map[string]string, len(fields))
	for _, field := range fields {
		headers[field.Name] = field.Value
	}
	return headers
}

func (w *WatcherWrapConn) rememberRecentHTTP2RequestSummary(streamID uint32, summary string) {
	if strings.TrimSpace(summary) == "" {
		return
	}

	if _, exists := w.recentRequestSummaries[streamID]; !exists {
		w.recentRequestOrder = append(w.recentRequestOrder, streamID)
	}
	w.recentRequestSummaries[streamID] = summary

	if len(w.recentRequestOrder) <= maxRecentHTTP2RequestSummaries {
		return
	}

	oldestID := w.recentRequestOrder[0]
	w.recentRequestOrder = w.recentRequestOrder[1:]
	delete(w.recentRequestSummaries, oldestID)
}

func (w *WatcherWrapConn) summarizeHTTP2RequestByStreamID(streamID uint32) string {
	w.streamsMu.Lock()
	defer w.streamsMu.Unlock()

	if stream, ok := w.streams[streamID]; ok {
		if strings.TrimSpace(stream.ReqSummary) != "" {
			return stream.ReqSummary
		}
		return summarizeHTTP2RequestMap(stream.ReqHeaders)
	}

	if summary, ok := w.recentRequestSummaries[streamID]; ok && strings.TrimSpace(summary) != "" {
		return summary
	}

	return summarizeHTTP2RequestMap(nil)
}

func (w *WatcherWrapConn) activeHTTP2RequestSummaries() []string {
	w.streamsMu.Lock()
	defer w.streamsMu.Unlock()

	summaries := make([]string, 0, len(w.streams))
	for streamID, stream := range w.streams {
		summary := summarizeHTTP2RequestMap(nil)
		if stream != nil {
			if strings.TrimSpace(stream.ReqSummary) != "" {
				summary = stream.ReqSummary
			} else {
				summary = summarizeHTTP2RequestMap(stream.ReqHeaders)
			}
		}
		summaries = append(summaries, fmt.Sprintf("stream=%d %s", streamID, summary))
	}
	return summaries
}

func (w *WatcherWrapConn) processHttp2RequestFrame(preBuff *bytes.Buffer) error {
	for {
		frame, ok := w.tryExtractFrameFromBuf(&w.reqBuf, false)
		if !ok {
			return nil
		}

		ftype := frame[3]
		flags := frame[4]
		streamID := binary.BigEndian.Uint32(frame[5:9]) & 0x7FFFFFFF
		payload := frame[frameHeaderLen:]

		logger.Debug(fmt.Sprintf("[HTTP/2 REQ] Frame type=%d flags=0x%02x stream=%d len=%d", ftype, flags, streamID, len(payload)))

		if streamID != 0 {
			w.getOrCreateStream(streamID)
		}

		switch ftype {
		case frameTypeHeaders:
			rawHeaders, ok, err := w.extractCompleteHeaderSequence()
			if err != nil {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Header extraction failed for stream %d: %v", streamID, err))
				return err
			}
			if !ok {
				return nil
			}

			metaFrame, err := w.decodeMetaHeaders(rawHeaders)
			if err != nil {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Header decode failed for stream %d: %v", streamID, err))
				return err
			}
			//
			// Preserve wire-level HEADERS metadata before we rebuild the block.
			// HPACK decoding gives us the logical header fields, but flags like
			// PRIORITY/PADDED and the original pad length live on the frame itself.
			// If we rewrite headers without carrying those bits forward, some HTTP/2
			// clients or intermediaries may reject the rewritten stream with
			// PROTOCOL_ERROR.
			rewriteMeta, err := parseHTTP2HeaderRewriteMeta(rawHeaders, metaFrame)
			if err != nil {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Header metadata parse failed for stream %d: %v", streamID, err))
				return err
			}

			injectKey := ""
			injectValue := ""
			if isHTTP2InitialRequestHeaders(metaFrame.Fields) {
				injectKey = "authorization-inner"
				injectValue = user.GetCurrentAuthorizationHeader()
			}

			rewritten, rewrittenFields, err := w.rebuildReqHeadersWithInjectedField(
				metaFrame.Fields,
				streamID,
				rewriteMeta,
				injectKey,
				injectValue,
			)
			if err != nil {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Header rebuild failed for stream %d: %v", streamID, err))
				return err
			}

			w.streamsMu.Lock()
			stream := w.streams[streamID]
			if stream != nil {
				summary := summarizeHTTP2Request(rewrittenFields)
				stream.ReqHeaders = headerFieldsToMap(rewrittenFields)
				stream.ReqSummary = summary
				stream.AuthInjected = injectKey == "authorization-inner" && strings.TrimSpace(injectValue) != ""
				w.rememberRecentHTTP2RequestSummary(streamID, summary)
				if isHTTP2InitialRequestHeaders(rewrittenFields) {
					logger.Info(fmt.Sprintf(
						"[HTTP/2 REQ] stream=%d request %s auth_inner=%t end_stream=%t",
						streamID,
						summary,
						stream.AuthInjected,
						metaFrame.StreamEnded(),
					))
				}
				if metaFrame.StreamEnded() {
					stream.ReqEndStream = true
				}
			}
			w.streamsMu.Unlock()

			preBuff.Write(rewritten)
			w.reqBuf.Next(len(rawHeaders))

		case frameTypeSettings:
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))

		case frameTypeData:
			w.streamsMu.Lock()
			stream := w.streams[streamID]
			if stream != nil {
				stream.ReqBody.Write(payload)
				if flags&flagEndStream != 0 {
					stream.ReqEndStream = true
				}
			}
			w.streamsMu.Unlock()
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))

		case frameTypeRstStream:
			if len(payload) >= 4 {
				errorCode := binary.BigEndian.Uint32(payload[0:4])
				requestSummary := w.summarizeHTTP2RequestByStreamID(streamID)
				logger.Warn(fmt.Sprintf(
					"[HTTP/2 REQ] Stream %d reset by client, error code=%d(%s) %s",
					streamID,
					errorCode,
					http2ErrorCodeName(errorCode),
					requestSummary,
				))
				w.streamsMu.Lock()
				delete(w.streams, streamID)
				w.streamsMu.Unlock()
			} else {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Malformed RST_STREAM payload on stream %d: len=%d", streamID, len(payload)))
			}
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))

		case frameTypeGoaway:
			if len(payload) >= 8 {
				lastStreamID := binary.BigEndian.Uint32(payload[0:4]) & 0x7FFFFFFF
				errorCode := binary.BigEndian.Uint32(payload[4:8])
				activeSummaries := w.activeHTTP2RequestSummaries()
				logger.Warn(fmt.Sprintf(
					"[HTTP/2 REQ] GOAWAY received, last stream=%d, error code=%d(%s), active_streams=%d [%s]",
					lastStreamID,
					errorCode,
					http2ErrorCodeName(errorCode),
					len(activeSummaries),
					strings.Join(activeSummaries, "; "),
				))
				// GOAWAY: cleanup all streams with ID > lastStreamID
				w.streamsMu.Lock()
				for sid := range w.streams {
					if sid > lastStreamID {
						delete(w.streams, sid)
					}
				}
				w.streamsMu.Unlock()
			} else {
				logger.Warn(fmt.Sprintf("[HTTP/2 REQ] Malformed GOAWAY payload: len=%d", len(payload)))
			}
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))

		case frameTypeWindowUpdate:
			// Basic flow control: track window size, respond if needed
			if len(payload) >= 4 {
				windowSizeIncrement := binary.BigEndian.Uint32(payload[0:4]) & 0x7FFFFFFF
				logger.Debug(fmt.Sprintf("[HTTP/2 REQ] WINDOW_UPDATE stream=%d increment=%d", streamID, windowSizeIncrement))
				// TODO: Implement window size tracking and enforcement if needed
			}
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))

		default:
			preBuff.Write(frame)
			w.reqBuf.Next(len(frame))
		}
	}
}

func (w *WatcherWrapConn) observeHTTP2ResponseFrames(payload []byte) {
	w.respBuf.Write(payload)

	for {
		frame, ok := w.tryExtractFrameFromBuf(&w.respBuf, false)
		if !ok {
			return
		}

		ftype := frame[3]
		flags := frame[4]
		streamID := binary.BigEndian.Uint32(frame[5:9]) & 0x7FFFFFFF
		framePayload := frame[frameHeaderLen:]

		logger.Debug(fmt.Sprintf("[HTTP/2 RESP] Frame type=%d flags=0x%02x stream=%d len=%d", ftype, flags, streamID, len(framePayload)))

		switch ftype {
		case frameTypeSettings:
			if flags&flagAck == 0 {
				w.ParseSettingsFrame(framePayload, http2SettingsSourceServer)
			}

		case frameTypeHeaders:
			if flags&flagEndStream != 0 {
				w.streamsMu.Lock()
				if stream := w.streams[streamID]; stream != nil {
					stream.RespEndStream = true
				}
				delete(w.streams, streamID)
				w.streamsMu.Unlock()
			}

		case frameTypeData:
			if flags&flagEndStream != 0 {
				w.streamsMu.Lock()
				if stream := w.streams[streamID]; stream != nil {
					stream.RespEndStream = true
				}
				delete(w.streams, streamID)
				w.streamsMu.Unlock()
			}

		case frameTypeRstStream:
			if len(framePayload) >= 4 {
				errorCode := binary.BigEndian.Uint32(framePayload[:4])
				requestSummary := w.summarizeHTTP2RequestByStreamID(streamID)
				logger.Warn(fmt.Sprintf(
					"[HTTP/2 RESP] Stream %d reset by server, error code=%d(%s) %s",
					streamID,
					errorCode,
					http2ErrorCodeName(errorCode),
					requestSummary,
				))
				w.streamsMu.Lock()
				delete(w.streams, streamID)
				w.streamsMu.Unlock()
			} else {
				logger.Warn(fmt.Sprintf("[HTTP/2 RESP] Malformed RST_STREAM payload on stream %d: len=%d", streamID, len(framePayload)))
			}

		case frameTypeGoaway:
			if len(framePayload) >= 8 {
				lastStreamID := binary.BigEndian.Uint32(framePayload[0:4]) & 0x7FFFFFFF
				errorCode := binary.BigEndian.Uint32(framePayload[4:8])
				activeSummaries := w.activeHTTP2RequestSummaries()
				logger.Warn(fmt.Sprintf(
					"[HTTP/2 RESP] GOAWAY received from server, last stream=%d, error code=%d(%s), active_streams=%d [%s]",
					lastStreamID,
					errorCode,
					http2ErrorCodeName(errorCode),
					len(activeSummaries),
					strings.Join(activeSummaries, "; "),
				))
			} else {
				logger.Warn(fmt.Sprintf("[HTTP/2 RESP] Malformed GOAWAY payload: len=%d", len(framePayload)))
			}
		}

		w.respBuf.Next(len(frame))
	}
}

func (w *WatcherWrapConn) extractCompleteHeaderSequence() ([]byte, bool, error) {
	data := w.reqBuf.Bytes()
	if len(data) < frameHeaderLen {
		return nil, false, nil
	}

	totalLen, ok := http2FrameTotalLen(data)
	if !ok {
		return nil, false, nil
	}
	flags := data[4]
	streamID := binary.BigEndian.Uint32(data[5:9]) & 0x7FFFFFFF
	if flags&flagEndHeaders != 0 {
		raw := make([]byte, totalLen)
		copy(raw, data[:totalLen])
		return raw, true, nil
	}

	offset := totalLen
	for {
		if len(data[offset:]) < frameHeaderLen {
			return nil, false, nil
		}

		nextTotal, ok := http2FrameTotalLen(data[offset:])
		if !ok {
			return nil, false, nil
		}

		if data[offset+3] != frameTypeContinuation {
			return nil, false, fmt.Errorf("expected HTTP/2 CONTINUATION frame, got type=%d", data[offset+3])
		}

		nextStreamID := binary.BigEndian.Uint32(data[offset+5:offset+9]) & 0x7FFFFFFF
		if nextStreamID != streamID {
			return nil, false, fmt.Errorf("unexpected stream switch in HTTP/2 header block: got=%d want=%d", nextStreamID, streamID)
		}

		offset += nextTotal
		if data[offset-nextTotal+4]&flagEndHeaders != 0 {
			raw := make([]byte, offset)
			copy(raw, data[:offset])
			return raw, true, nil
		}
	}
}

func (w *WatcherWrapConn) decodeMetaHeaders(rawHeaders []byte) (*http2.MetaHeadersFrame, error) {
	reader := bytes.NewReader(rawHeaders)
	framer := http2.NewFramer(nil, reader)
	framer.ReadMetaHeaders = w.requestDecoderFromClient

	frame, err := framer.ReadFrame()
	if err != nil {
		return nil, err
	}

	metaFrame, ok := frame.(*http2.MetaHeadersFrame)
	if !ok {
		return nil, fmt.Errorf("expected HTTP/2 meta headers frame, got %T", frame)
	}
	return metaFrame, nil
}

func http2FrameTotalLen(data []byte) (int, bool) {
	if len(data) < frameHeaderLen {
		return 0, false
	}
	length := binary.BigEndian.Uint32(append([]byte{0}, data[0:3]...))
	totalLen := frameHeaderLen + int(length)
	if len(data) < totalLen {
		return 0, false
	}
	return totalLen, true
}

func parseHTTP2HeaderRewriteMeta(rawHeaders []byte, metaFrame *http2.MetaHeadersFrame) (http2HeaderRewriteMeta, error) {
	params := http2HeaderRewriteMeta{}
	if metaFrame != nil {
		params.endStream = metaFrame.StreamEnded()
		params.priority = metaFrame.HeadersFrame.Priority
	}

	if len(rawHeaders) < frameHeaderLen {
		return params, fmt.Errorf("short HTTP/2 header sequence: len=%d", len(rawHeaders))
	}

	firstFrameLen, ok := http2FrameTotalLen(rawHeaders)
	if !ok {
		return params, fmt.Errorf("incomplete first HTTP/2 HEADERS frame")
	}

	flags := rawHeaders[4]
	params.hasPriority = flags&flagPriority != 0
	if flags&flagPadded != 0 {
		firstPayload := rawHeaders[frameHeaderLen:firstFrameLen]
		if len(firstPayload) < 1 {
			return params, fmt.Errorf("padded HEADERS missing pad length")
		}
		params.padLength = firstPayload[0]
	}

	return params, nil
}

func computeHTTP2FirstHeaderChunkBudget(maxFrameSize int, rewriteMeta http2HeaderRewriteMeta) int {
	chunkBudget := maxFrameSize

	// chunkBudget/headerBlock 的单位都是 byte，这里计算的是：
	// “首个 HEADERS 帧里，还能留给 HPACK header block fragment 多少字节空间”。
	// 注意 maxFrameSize 限制的是整个 frame payload，而不是仅限制 fragment。
	// 因此像 padding/priority 这种帧级元信息，也必须先从预算里扣掉。
	if rewriteMeta.padLength != 0 {
		// PADDED HEADERS 的 payload 结构是:
		//   Pad Length(1 byte) + Header Block Fragment(N bytes) + Padding(padLength bytes)
		// 所以真正可用于 fragment 的空间，要减去 1 + padLength。
		chunkBudget -= 1 + int(rewriteMeta.padLength)
	}
	if rewriteMeta.hasPriority {
		// PRIORITY 信息在线上的 HEADERS payload 中固定占 5 byte:
		//   Stream Dependency(4 bytes) + Weight(1 byte)
		// 这里扣的是 HTTP/2 线协议编码长度，不是 Go 里 PriorityParam 结构体的内存大小。
		chunkBudget -= 5
	}
	if chunkBudget < 0 {
		// 这里先钳成 0，而不是立刻报错，是为了避免我们重写时继续生成
		// “首个 HEADERS payload 超过 maxFrameSize”的更坏结果。
		// 设成 0 后，首帧只保留必要的帧级元信息，不放 fragment；
		// 剩余 header block 会继续写进后续 CONTINUATION 帧。
		//
		// 这不是在说“输入一定合法”，而是在异常或极端组合下尽量做保守输出，
		// 避免本地重写逻辑额外制造新的协议错误。
		chunkBudget = 0
	}

	return chunkBudget
}

func (w *WatcherWrapConn) rebuildReqHeadersWithInjectedField(
	headerFields []hpack.HeaderField,
	streamID uint32,
	rewriteMeta http2HeaderRewriteMeta,
	keyToInject string,
	valueToInject string,
) ([]byte, []hpack.HeaderField, error) {
	normalizedInjectKey := strings.ToLower(strings.TrimSpace(keyToInject))
	if rewrittenHeaders, changed := rewriteAliangHTTP2HeaderFields(headerFields); changed {
		headerFields = rewrittenHeaders
	}
	rewrittenFields := make([]hpack.HeaderField, 0, len(headerFields)+1)
	for _, field := range headerFields {
		name := strings.ToLower(strings.TrimSpace(field.Name))
		if name == normalizedInjectKey || name == "authorization-inner" {
			continue
		}
		rewrittenFields = append(rewrittenFields, field)
	}
	if normalizedInjectKey != "" && strings.TrimSpace(valueToInject) != "" {
		rewrittenFields = append(rewrittenFields, hpack.HeaderField{
			Name:      normalizedInjectKey,
			Value:     valueToInject,
			Sensitive: true,
		})
	}

	if normalizedInjectKey == "authorization-inner" {
		if _, ok := getHTTP2HeaderFieldValue(rewrittenFields, normalizedInjectKey); !ok {
			logger.Warn(fmt.Sprintf(
				"WatcherWrapConn: missing authorization-inner after HTTP/2 header rewrite stream=%d %s",
				streamID,
				summarizeHTTP2Request(rewrittenFields),
			))
		} else if !version.IsProdBuild() {
			logger.Debug(fmt.Sprintf(
				"WatcherWrapConn: added authorization-inner for HTTP/2 stream=%d %s",
				streamID,
				summarizeHTTP2Request(rewrittenFields),
			))
		}
	}

	w.requestEncoderBuffer.Reset()
	for _, field := range rewrittenFields {
		toWrite := field
		if strings.EqualFold(field.Name, normalizedInjectKey) {
			toWrite.Sensitive = true
		}
		if err := w.requestEncoderToServer.WriteField(toWrite); err != nil {
			return nil, nil, fmt.Errorf("hpack encode error: %w", err)
		}
	}

	headerBlock := append([]byte(nil), w.requestEncoderBuffer.Bytes()...)
	maxFrameSize := 16384
	if val, ok := w.GetServerHTTP2Setting(SETTINGS_MAX_FRAME_SIZE); ok {
		maxFrameSize = int(val)
	}

	var out bytes.Buffer
	writer := http2.NewFramer(&out, nil)
	for first := true; len(headerBlock) > 0 || first; first = false {
		chunkBudget := maxFrameSize
		if first {
			chunkBudget = computeHTTP2FirstHeaderChunkBudget(maxFrameSize, rewriteMeta)
		}

		chunkSize := len(headerBlock)
		if chunkSize > chunkBudget {
			chunkSize = chunkBudget
		}
		chunk := headerBlock[:chunkSize]
		headerBlock = headerBlock[chunkSize:]
		endHeaders := len(headerBlock) == 0

		if first {
			if err := writeHTTP2HeadersFrame(&out, http2.HeadersFrameParam{
				StreamID:      streamID,
				BlockFragment: chunk,
				EndStream:     rewriteMeta.endStream,
				EndHeaders:    endHeaders,
				PadLength:     rewriteMeta.padLength,
				Priority:      rewriteMeta.priority,
			}, rewriteMeta.hasPriority); err != nil {
				return nil, nil, fmt.Errorf("write headers frame: %w", err)
			}
			continue
		}

		if err := writer.WriteContinuation(streamID, endHeaders, chunk); err != nil {
			return nil, nil, fmt.Errorf("write continuation frame: %w", err)
		}
	}

	return out.Bytes(), rewrittenFields, nil
}

func writeHTTP2HeadersFrame(out *bytes.Buffer, p http2.HeadersFrameParam, forcePriority bool) error {
	if !forcePriority {
		writer := http2.NewFramer(out, nil)
		return writer.WriteHeaders(p)
	}

	var flags byte
	if p.EndStream {
		flags |= flagEndStream
	}
	if p.EndHeaders {
		flags |= flagEndHeaders
	}
	if p.PadLength != 0 {
		flags |= flagPadded
	}
	flags |= flagPriority

	payloadLen := len(p.BlockFragment) + 5 + int(p.PadLength)
	if p.PadLength != 0 {
		payloadLen++
	}
	if payloadLen > 0xFFFFFF {
		return fmt.Errorf("headers payload too large: %d", payloadLen)
	}

	var header [frameHeaderLen]byte
	header[0] = byte(payloadLen >> 16)
	header[1] = byte(payloadLen >> 8)
	header[2] = byte(payloadLen)
	header[3] = frameTypeHeaders
	header[4] = flags
	binary.BigEndian.PutUint32(header[5:9], p.StreamID&0x7FFFFFFF)
	if _, err := out.Write(header[:]); err != nil {
		return err
	}

	if p.PadLength != 0 {
		if err := out.WriteByte(p.PadLength); err != nil {
			return err
		}
	}

	dependency := p.Priority.StreamDep & 0x7FFFFFFF
	if p.Priority.Exclusive {
		dependency |= 1 << 31
	}
	var dep [4]byte
	binary.BigEndian.PutUint32(dep[:], dependency)
	if _, err := out.Write(dep[:]); err != nil {
		return err
	}
	if err := out.WriteByte(p.Priority.Weight); err != nil {
		return err
	}
	if _, err := out.Write(p.BlockFragment); err != nil {
		return err
	}
	for i := uint8(0); i < p.PadLength; i++ {
		if err := out.WriteByte(0); err != nil {
			return err
		}
	}
	return nil
}

func (w *WatcherWrapConn) decodeHeaderBlock(block []byte, isRequest bool) (map[string]string, error) {
	headers := make(map[string]string)

	var decoder *hpack.Decoder
	if isRequest {
		decoder = w.requestDecoderFromClient
	} else {
		return nil, fmt.Errorf("response header decoding is not used in transparent response mode")
	}

	decoder.SetEmitFunc(func(f hpack.HeaderField) {
		headers[f.Name] = f.Value
	})

	_, err := decoder.Write(block)
	return headers, err
}
