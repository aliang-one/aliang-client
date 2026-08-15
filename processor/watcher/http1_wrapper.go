package tls

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	user "aliang.one/nursorgate/processor/auth"
)

type http1BodyMode int

const (
	http1BodyModeNone http1BodyMode = iota
	http1BodyModeContentLength
	http1BodyModeChunked
)

type http1ChunkPhase int

const (
	http1ChunkPhaseSizeLine http1ChunkPhase = iota
	http1ChunkPhaseData
	http1ChunkPhaseDataCRLF
	http1ChunkPhaseTrailers
)

type http1ChunkState struct {
	phase          http1ChunkPhase
	lineBuf        bytes.Buffer
	chunkRemaining int64
	crlfProgress   int
}

type http1BodyTracker struct {
	mode       http1BodyMode
	remaining  int64
	chunkState http1ChunkState
}

func serializeHTTPRequestHead(req *http.Request) ([]byte, error) {
	var rebuilt bytes.Buffer

	requestURI := req.RequestURI
	if requestURI == "" && req.URL != nil {
		requestURI = req.URL.RequestURI()
	}
	if requestURI == "" {
		return nil, fmt.Errorf("invalid HTTP/1 request: empty request URI")
	}

	if _, err := fmt.Fprintf(&rebuilt, "%s %s %s\r\n", req.Method, requestURI, req.Proto); err != nil {
		return nil, err
	}

	headers := req.Header.Clone()
	headers.Del("Host")
	if req.Host != "" {
		if _, err := fmt.Fprintf(&rebuilt, "Host: %s\r\n", req.Host); err != nil {
			return nil, err
		}
	}
	if err := headers.Write(&rebuilt); err != nil {
		return nil, err
	}
	if _, err := rebuilt.WriteString("\r\n"); err != nil {
		return nil, err
	}

	return rebuilt.Bytes(), nil
}

func newHTTP1BodyTracker(req *http.Request) *http1BodyTracker {
	if req == nil {
		return nil
	}

	if len(req.TransferEncoding) > 0 && strings.EqualFold(req.TransferEncoding[len(req.TransferEncoding)-1], "chunked") {
		return &http1BodyTracker{
			mode: http1BodyModeChunked,
			chunkState: http1ChunkState{
				phase: http1ChunkPhaseSizeLine,
			},
		}
	}

	if req.ContentLength > 0 {
		return &http1BodyTracker{
			mode:      http1BodyModeContentLength,
			remaining: req.ContentLength,
		}
	}

	return nil
}

func (w *WatcherWrapConn) consumeHTTP1Body() ([]byte, bool, error) {
	if w.http1BodyTracker == nil {
		return nil, false, nil
	}

	data := w.reqBuf.Bytes()
	if len(data) == 0 {
		return nil, false, nil
	}

	consumed, done, err := w.http1BodyTracker.consume(data)
	if err != nil {
		logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/1 body tracker failed: %v", err))
		return nil, false, err
	}
	if consumed == 0 {
		return nil, false, nil
	}

	out := make([]byte, consumed)
	copy(out, data[:consumed])
	w.reqBuf.Next(consumed)
	if done {
		w.http1BodyTracker = nil
	}

	return out, true, nil
}

func (t *http1BodyTracker) consume(data []byte) (int, bool, error) {
	if t == nil || len(data) == 0 {
		return 0, t == nil, nil
	}

	switch t.mode {
	case http1BodyModeContentLength:
		if t.remaining <= 0 {
			return 0, true, nil
		}
		n := len(data)
		if int64(n) > t.remaining {
			n = int(t.remaining)
		}
		t.remaining -= int64(n)
		return n, t.remaining == 0, nil
	case http1BodyModeChunked:
		return t.chunkState.consume(data)
	default:
		return 0, true, nil
	}
}

func (s *http1ChunkState) consume(data []byte) (int, bool, error) {
	idx := 0

	for idx < len(data) {
		switch s.phase {
		case http1ChunkPhaseSizeLine:
			b := data[idx]
			s.lineBuf.WriteByte(b)
			idx++
			line := s.lineBuf.Bytes()
			lineLen := len(line)
			if lineLen < 2 || line[lineLen-2] != '\r' || line[lineLen-1] != '\n' {
				continue
			}

			sizeLine := strings.TrimSpace(string(line[:lineLen-2]))
			s.lineBuf.Reset()
			sizeToken := sizeLine
			if cut := strings.IndexByte(sizeToken, ';'); cut >= 0 {
				sizeToken = sizeToken[:cut]
			}
			sizeToken = strings.TrimSpace(sizeToken)
			if sizeToken == "" {
				return idx, false, fmt.Errorf("invalid HTTP/1 chunk size line")
			}

			size, err := strconv.ParseInt(sizeToken, 16, 64)
			if err != nil {
				return idx, false, fmt.Errorf("parse HTTP/1 chunk size %q: %w", sizeToken, err)
			}
			if size == 0 {
				s.phase = http1ChunkPhaseTrailers
				continue
			}

			s.chunkRemaining = size
			s.phase = http1ChunkPhaseData

		case http1ChunkPhaseData:
			if s.chunkRemaining <= 0 {
				s.phase = http1ChunkPhaseDataCRLF
				continue
			}

			toConsume := len(data) - idx
			if int64(toConsume) > s.chunkRemaining {
				toConsume = int(s.chunkRemaining)
			}
			idx += toConsume
			s.chunkRemaining -= int64(toConsume)
			if s.chunkRemaining == 0 {
				s.phase = http1ChunkPhaseDataCRLF
			}

		case http1ChunkPhaseDataCRLF:
			for s.crlfProgress < 2 && idx < len(data) {
				expected := byte('\r')
				if s.crlfProgress == 1 {
					expected = '\n'
				}
				if data[idx] != expected {
					return idx, false, fmt.Errorf("invalid HTTP/1 chunk terminator")
				}
				s.crlfProgress++
				idx++
			}
			if s.crlfProgress < 2 {
				return idx, false, nil
			}
			s.crlfProgress = 0
			s.phase = http1ChunkPhaseSizeLine

		case http1ChunkPhaseTrailers:
			b := data[idx]
			s.lineBuf.WriteByte(b)
			idx++
			line := s.lineBuf.Bytes()
			lineLen := len(line)
			if lineLen < 2 || line[lineLen-2] != '\r' || line[lineLen-1] != '\n' {
				continue
			}
			if lineLen == 2 {
				s.lineBuf.Reset()
				return idx, true, nil
			}
			s.lineBuf.Reset()
		}
	}

	return idx, false, nil
}

func (w *WatcherWrapConn) processH1ReqHeaders() ([]byte, bool, error) {
	dataOrigin := append([]byte(nil), w.reqBuf.Bytes()...)
	headersEndIdx := bytes.Index(dataOrigin, []byte("\r\n\r\n"))
	if headersEndIdx == -1 {
		return nil, false, nil
	}

	headersData := dataOrigin[:headersEndIdx+4]
	bodyData := dataOrigin[headersEndIdx+4:]
	w.http1ReqContent = string(dataOrigin)

	req, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(headersData)))
	if err != nil {
		logger.Warn(fmt.Sprintf("WatcherWrapConn: invalid HTTP/1 request headers: %v", err))
		return nil, false, fmt.Errorf("invalid HTTP/1 request: %w", err)
	}

	// 将localhost:56432上监听到的，别家的host，改成openai.com等，改完后继续往下走正常的流程，最终发给aliang，不然后端要考虑处理各种第三方的host
	rewriteAliangHTTPRequestHost(req)

	if authHeader := strings.TrimSpace(user.GetCurrentAuthorizationHeader()); authHeader != "" {
		req.Header.Set("Authorization-Inner", authHeader)
	}

	requestLine := fmt.Sprintf("%s %s %s", req.Method, req.RequestURI, req.Proto)
	if req.Header.Get("Authorization-Inner") == "" {
		logger.Warn(fmt.Sprintf(
			"WatcherWrapConn: missing authorization-inner after HTTP/1 header rewrite request=%q host=%q",
			requestLine,
			req.Host,
		))
	} else if !version.IsProdBuild() {
		logger.Debug(fmt.Sprintf(
			"WatcherWrapConn: added authorization-inner for HTTP/1 request=%q host=%q",
			requestLine,
			req.Host,
		))
	}

	headBytes, err := serializeHTTPRequestHead(req)
	if err != nil {
		logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/1 request rebuild failed: %v", err))
		return nil, false, err
	}

	bodyTracker := newHTTP1BodyTracker(req)
	currentBody := bodyData
	leftoverBody := []byte(nil)
	if bodyTracker == nil {
		currentBody = nil
		leftoverBody = bodyData
	} else {
		consumed, done, consumeErr := bodyTracker.consume(bodyData)
		if consumeErr != nil {
			logger.Warn(fmt.Sprintf("WatcherWrapConn: HTTP/1 inline body parse failed: %v", consumeErr))
			return nil, false, consumeErr
		}
		currentBody = bodyData[:consumed]
		if consumed < len(bodyData) {
			leftoverBody = append([]byte(nil), bodyData[consumed:]...)
		}
		if done {
			bodyTracker = nil
		}
	}

	var rebuilt bytes.Buffer
	rebuilt.Write(headBytes)
	rebuilt.Write(currentBody)

	w.http1BodyTracker = bodyTracker
	w.reqBuf.Reset()
	if len(leftoverBody) > 0 {
		w.reqBuf.Write(leftoverBody)
	}

	logger.Debug(fmt.Sprintf("rewritten HTTP/1 request: method=%s host=%s uri=%s header_bytes=%d inline_body_bytes=%d",
		req.Method,
		req.Host,
		req.RequestURI,
		len(headBytes),
		len(currentBody),
	))
	return rebuilt.Bytes(), true, nil
}
