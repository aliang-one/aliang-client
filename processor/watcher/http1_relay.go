package tls

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/common/version"
	user "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

const (
	httpRelayCaptureLimit       = 128 * 1024
	http1DropMaxDrainBodyBytes  = 1 << 20
	http1UnknownRequestBodySize = -1
	// http1ModelRewriteMaxBodyBytes bounds how much of an HTTP/1 AI request body
	// we buffer in memory to rewrite the "model" field. Larger bodies are
	// forwarded unchanged.
	http1ModelRewriteMaxBodyBytes = 1 << 20
)

type HTTP1RelayStats struct {
	StartedAt          time.Time
	FirstResponseAt    time.Time
	CompletedAt        time.Time
	ClientToServerByte int64
	ServerToClientByte int64
	RequestPayload     []byte
	ResponsePayload    []byte
}

type httpRelayCaptureBuffer struct {
	buf   bytes.Buffer
	limit int
}

func newHTTPRelayCaptureBuffer(limit int) *httpRelayCaptureBuffer {
	if limit <= 0 {
		limit = httpRelayCaptureLimit
	}
	return &httpRelayCaptureBuffer{limit: limit}
}

func (p *httpRelayCaptureBuffer) Write(data []byte) {
	if p == nil || len(data) == 0 {
		return
	}
	remaining := p.limit - p.buf.Len()
	if remaining <= 0 {
		return
	}
	if len(data) > remaining {
		data = data[:remaining]
	}
	_, _ = p.buf.Write(data)
}

func (p *httpRelayCaptureBuffer) Bytes() []byte {
	if p == nil {
		return nil
	}
	out := make([]byte, p.buf.Len())
	copy(out, p.buf.Bytes())
	return out
}

type httpRelayCountingWriter struct {
	writer      io.Writer
	capture     *httpRelayCaptureBuffer
	onFirstData func()
	written     *int64
}

func (w *httpRelayCountingWriter) Write(p []byte) (int, error) {
	if w.onFirstData != nil && len(p) > 0 {
		w.onFirstData()
		w.onFirstData = nil
	}
	if w.capture != nil && len(p) > 0 {
		w.capture.Write(p)
	}
	n, err := w.writer.Write(p)
	if n > 0 && w.written != nil {
		atomic.AddInt64(w.written, int64(n))
	}
	return n, err
}

type httpRequestResult struct {
	req *http.Request
	err error
}

type http1RemoteStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	events chan error
}

func startHTTP1RemoteStream(
	remoteConn net.Conn,
	capture *httpRelayCaptureBuffer,
	onFirstData func(),
	written *int64,
) *http1RemoteStream {
	pipeReader, pipeWriter := io.Pipe()
	stream := &http1RemoteStream{
		reader: pipeReader,
		writer: pipeWriter,
		events: make(chan error, 1),
	}

	go func() {
		defer close(stream.events)

		buf := make([]byte, 32*1024)
		localOnFirstData := onFirstData
		for {
			n, err := remoteConn.Read(buf)
			if n > 0 {
				if localOnFirstData != nil {
					localOnFirstData()
					localOnFirstData = nil
				}
				if capture != nil {
					capture.Write(buf[:n])
				}
				if written != nil {
					atomic.AddInt64(written, int64(n))
				}
				if _, writeErr := pipeWriter.Write(buf[:n]); writeErr != nil {
					stream.events <- writeErr
					_ = pipeWriter.CloseWithError(writeErr)
					return
				}
			}
			if err != nil {
				stream.events <- err
				_ = pipeWriter.CloseWithError(err)
				return
			}
		}
	}()

	return stream
}

func (s *http1RemoteStream) Reader() io.Reader {
	if s == nil {
		return nil
	}
	return s.reader
}

func (s *http1RemoteStream) Events() <-chan error {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *http1RemoteStream) Close() {
	if s == nil {
		return
	}
	_ = s.reader.Close()
	_ = s.writer.Close()
}

func RelayHTTP1(ctx context.Context, clientConn, remoteConn net.Conn) (*HTTP1RelayStats, error) {
	stats := &HTTP1RelayStats{StartedAt: time.Now()}
	requestCapture := newHTTPRelayCaptureBuffer(httpRelayCaptureLimit)
	responseCapture := newHTTPRelayCaptureBuffer(httpRelayCaptureLimit)

	var firstResponseNano int64
	markFirstResponse := func() {
		if atomic.CompareAndSwapInt64(&firstResponseNano, 0, time.Now().UnixNano()) {
			stats.FirstResponseAt = time.Unix(0, atomic.LoadInt64(&firstResponseNano))
		}
	}

	clientReader := bufio.NewReader(clientConn)
	requestWriter := &httpRelayCountingWriter{
		writer:  remoteConn,
		capture: requestCapture,
		written: &stats.ClientToServerByte,
	}
	responseWriter := &httpRelayCountingWriter{
		writer: clientConn,
	}

	remoteStream := startHTTP1RemoteStream(remoteConn, responseCapture, markFirstResponse, &stats.ServerToClientByte)
	defer remoteStream.Close()
	remoteReader := bufio.NewReader(remoteStream.Reader())

	var relayErr error
	for {
		reqCh := make(chan httpRequestResult, 1)
		go func() {
			req, err := http.ReadRequest(clientReader)
			reqCh <- httpRequestResult{req: req, err: err}
		}()

		select {
		case <-ctx.Done():
			relayErr = ctx.Err()
			goto done
		case remoteErr, ok := <-remoteStream.Events():
			if ok {
				relayErr = normalizeIdleRemoteClose(remoteErr)
			}
			goto done
		case reqRes := <-reqCh:
			if reqRes.err != nil {
				if errors.Is(reqRes.err, io.EOF) {
					goto done
				}
				relayErr = reqRes.err
				goto done
			}

			if shouldDropHTTP1Request(reqRes.req) {
				closeAfterDrop := shouldCloseAfterDroppedHTTP1Request(reqRes.req)
				if !closeAfterDrop {
					if err := drainAndCloseHTTP1RequestBody(reqRes.req); err != nil {
						relayErr = err
						goto done
					}
				} else if reqRes.req.Body != nil {
					_ = reqRes.req.Body.Close()
				}
				if err := writeHTTP1DroppedResponse(responseWriter, reqRes.req, closeAfterDrop); err != nil {
					relayErr = err
					goto done
				}
				logger.Info(fmt.Sprintf(
					"WatcherWrapConn: dropped HTTP/1 request locally request=%q host=%q",
					http1RequestLine(reqRes.req),
					reqRes.req.Host,
				))
				if closeAfterDrop {
					goto done
				}
				continue
			}

			injectHTTP1AuthorizationHeader(reqRes.req)
			applyHTTP1AIModelRewrite(reqRes.req)

			if err := reqRes.req.Write(requestWriter); err != nil {
				relayErr = err
				goto done
			}

			resp, err := http.ReadResponse(remoteReader, reqRes.req)
			if err != nil {
				if isNetTimeout(err) {
					if writeErr := writeHTTP1GatewayTimeout(responseWriter, reqRes.req); writeErr != nil {
						relayErr = fmt.Errorf("http1 relay timeout while writing local gateway timeout: %w", writeErr)
					} else {
						relayErr = err
					}
				} else {
					relayErr = err
				}
				goto done
			}

			if err := resp.Write(responseWriter); err != nil {
				relayErr = err
				goto done
			}

			if shouldCloseHTTP1Relay(reqRes.req, resp) {
				goto done
			}
		}
	}

done:
	closeHTTPWrite(clientConn)
	closeHTTPRead(clientConn)
	closeHTTPWrite(remoteConn)
	closeHTTPRead(remoteConn)

	stats.CompletedAt = time.Now()
	stats.RequestPayload = requestCapture.Bytes()
	stats.ResponsePayload = responseCapture.Bytes()

	return stats, normalizeIdleRemoteClose(relayErr)
}

func shouldDropHTTP1Request(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}

	cfg := config.GetGlobalConfig().EffectiveHTTP1Drop()
	if cfg == nil || !cfg.IsEnabled() {
		return false
	}

	path := strings.ToLower(strings.TrimSpace(req.URL.Path))
	if path == "" {
		return false
	}

	for _, pattern := range cfg.EffectivePathContains() {
		needle := strings.ToLower(strings.TrimSpace(pattern))
		if needle != "" && strings.Contains(path, needle) {
			return true
		}
	}
	return false
}

func drainAndCloseHTTP1RequestBody(req *http.Request) error {
	if req == nil || req.Body == nil {
		return nil
	}
	_, copyErr := io.Copy(io.Discard, req.Body)
	closeErr := req.Body.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func shouldCloseAfterDroppedHTTP1Request(req *http.Request) bool {
	if req == nil {
		return true
	}
	if req.Close || strings.EqualFold(req.Header.Get("Connection"), "close") {
		return true
	}
	if hasHTTP1ExpectContinue(req) {
		return true
	}
	if req.ContentLength == http1UnknownRequestBodySize {
		return true
	}
	return req.ContentLength > http1DropMaxDrainBodyBytes
}

func hasHTTP1ExpectContinue(req *http.Request) bool {
	if req == nil {
		return false
	}
	for _, value := range req.Header.Values("Expect") {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "100-continue") {
				return true
			}
		}
	}
	return false
}

func writeHTTP1DroppedResponse(w io.Writer, req *http.Request, closeAfter bool) error {
	resp := &http.Response{
		Status:        "204 No Content",
		StatusCode:    http.StatusNoContent,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          http.NoBody,
		ContentLength: 0,
		Request:       req,
		Close:         closeAfter,
	}
	resp.Header.Set("X-Aliang-Dropped", "http1-path")
	if closeAfter {
		resp.Header.Set("Connection", "close")
	}
	return resp.Write(w)
}

func normalizeIdleRemoteClose(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func injectHTTP1AuthorizationHeader(req *http.Request) {
	if req == nil {
		return
	}

	rewriteAliangHTTPRequestHost(req)

	if authHeader := strings.TrimSpace(user.GetCurrentAuthorizationHeader()); authHeader != "" {
		req.Header.Set("Authorization-Inner", authHeader)
	}

	requestLine := http1RequestLine(req)
	if req.Header.Get("Authorization-Inner") == "" {
		logger.Warn(fmt.Sprintf(
			"WatcherWrapConn: missing authorization-inner after HTTP/1 relay request=%q host=%q",
			requestLine,
			req.Host,
		))
	} else if !version.IsProdBuild() {
		logger.Debug(fmt.Sprintf(
			"WatcherWrapConn: added authorization-inner for HTTP/1 relay request=%q host=%q",
			requestLine,
			req.Host,
		))
	}
}

// applyHTTP1AIModelRewrite rewrites the top-level "model" field of an HTTP/1 AI
// request body in place according to the configured ModelMapping rules. It is
// best-effort: disabled/empty rules, non-JSON bodies, oversized or unparseable
// bodies are all forwarded unchanged. Once any bytes are consumed from
// req.Body the body is always restored (rewritten, original-buffered, or as a
// reconstructed stream) so the downstream relay never loses data.
func applyHTTP1AIModelRewrite(req *http.Request) {
	if req == nil || req.Body == nil {
		return
	}
	rules := config.GetGlobalConfig().EffectiveModelMapping().EffectiveRules()
	if len(rules) == 0 {
		return
	}
	if !isHTTP1JSONRequest(req) {
		return
	}

	raw, err := io.ReadAll(io.LimitReader(req.Body, http1ModelRewriteMaxBodyBytes+1))
	if err != nil || len(raw) > http1ModelRewriteMaxBodyBytes {
		// Body too large to buffer safely or read error: forward the original
		// stream untouched by re-prepending whatever we already consumed.
		restoreHTTP1RequestBodyStream(req, raw)
		return
	}

	if rewritten, ok := rewriteHTTP1AIModelField(raw, rules); ok {
		setHTTP1RequestBody(req, rewritten)
		if !version.IsProdBuild() {
			logger.Debug(fmt.Sprintf(
				"WatcherWrapConn: rewrote HTTP/1 AI model for request=%q",
				http1RequestLine(req),
			))
		}
		return
	}

	// Model absent / not in rules / JSON unparseable: forward original bytes.
	setHTTP1RequestBody(req, raw)
}

// rewriteHTTP1AIModelField parses a JSON object and, if it has a top-level
// string "model" field present in rules, returns the re-serialized body with
// the mapped value substituted. ok is false when nothing should change.
func rewriteHTTP1AIModelField(raw []byte, rules map[string]string) ([]byte, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	if obj == nil || dec.More() {
		return nil, false
	}
	current, ok := obj["model"].(string)
	if !ok {
		return nil, false
	}
	target, ok := rules[current]
	if !ok {
		return nil, false
	}
	obj["model"] = target
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return rewritten, true
}

func isHTTP1JSONRequest(req *http.Request) bool {
	if req == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	return strings.Contains(contentType, "application/json")
}

// setHTTP1RequestBody replaces req.Body with a fixed-length buffer of body and
// normalizes framing to an explicit Content-Length (dropping Transfer-Encoding).
func setHTTP1RequestBody(req *http.Request, body []byte) {
	if req == nil {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.TransferEncoding = nil
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	req.Header.Del("Transfer-Encoding")
}

// restoreHTTP1RequestBodyStream reconstructs the original body stream after a
// partial read by prepending the already-consumed bytes. Framing headers are
// left untouched so the original Content-Length / chunked semantics still hold.
func restoreHTTP1RequestBodyStream(req *http.Request, consumed []byte) {
	if req == nil || req.Body == nil || len(consumed) == 0 {
		return
	}
	req.Body = io.NopCloser(io.MultiReader(bytes.NewReader(consumed), req.Body))
}

func http1RequestLine(req *http.Request) string {
	if req == nil {
		return ""
	}
	return fmt.Sprintf("%s %s %s", req.Method, req.RequestURI, req.Proto)
}

func shouldCloseHTTP1Relay(req *http.Request, resp *http.Response) bool {
	if req == nil || resp == nil {
		return true
	}
	if req.Close || resp.Close {
		return true
	}
	if strings.EqualFold(req.Header.Get("Connection"), "close") {
		return true
	}
	if strings.EqualFold(resp.Header.Get("Connection"), "close") {
		return true
	}
	return false
}

func writeHTTP1GatewayTimeout(w io.Writer, req *http.Request) error {
	body := "gateway timeout"
	resp := &http.Response{
		Status:        "504 Gateway Timeout",
		StatusCode:    http.StatusGatewayTimeout,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
		Request:       req,
	}
	resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
	resp.Header.Set("Connection", "close")
	return resp.Write(w)
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func closeHTTPWrite(conn net.Conn) {
	if conn == nil {
		return
	}
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func closeHTTPRead(conn net.Conn) {
	if conn == nil {
		return
	}
	if cr, ok := conn.(interface{ CloseRead() error }); ok {
		_ = cr.CloseRead()
	}
}
