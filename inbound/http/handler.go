package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/tcp"
)

func HandleHttpConnection(conn net.Conn, reader *bufio.Reader, req *http.Request) {
	defer conn.Close()

	logger.Debug(fmt.Sprintf("Received non-CONNECT request: %s %s", req.Method, req.URL.String()))

	// Extract metadata from HTTP request for transparent proxy handling
	metadata, err := extractMetadataFromHTTP(req, conn)
	if err != nil {
		logger.Info(fmt.Sprintf("Rejected plain-HTTP request from %s: %v", conn.RemoteAddr(), err))
		writePlainHTTPErrorResponse(conn, http.StatusBadRequest, fmt.Sprintf("Failed to process request: %v", err))
		return
	}

	logger.Debug(fmt.Sprintf("HTTP transparent proxy: hostname=%s, port=%d, srcIP=%s, dstIP=%s",
		metadata.HostName, metadata.DstPort, metadata.SrcIP, metadata.DstIP))

	// 代理面目标守卫:非回环来源不得经代理面访问本机回环/内网目标。
	if err := tcp.CheckProxyFaceDestination(metadata); err != nil {
		logger.Info(fmt.Sprintf("Rejected plain-HTTP request from %s: %v", conn.RemoteAddr(), err))
		writePlainHTTPErrorResponse(conn, http.StatusForbidden, "Forbidden")
		return
	}

	// Import tcp handler for unified TCP processing
	tcpHandler := tcp.GetHandler()

	replayConn, err := newHTTPRequestReplayConn(conn, reader, req)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to rebuild HTTP request stream: %v", err))
		writePlainHTTPErrorResponse(conn, http.StatusBadGateway, fmt.Sprintf("Failed to rebuild request stream: %v", err))
		return
	}

	// Create context for the handler
	ctx := context.Background()

	// Delegate to processor/tcp for routing and relay
	// The handler will:
	// 1. Detect protocol (TLS on 443, HTTP on 80, direct for others)
	// 2. Route based on domain rules (cursor proxy, door proxy, or direct)
	// 3. Handle bidirectional relay with statistics
	logger.Debug(fmt.Sprintf("Routing HTTP request through TCP handler for %s:%d", metadata.HostName, metadata.DstPort))
	if err := tcpHandler.Handle(ctx, replayConn, metadata); err != nil {
		// 负缓存/在途秒拒是预期失败(直连闸治理,processor/tcp/dial_negcache.go),
		// 同款降 Debug,与 Handle 层口径一致。
		if errors.Is(err, tcp.ErrDialNegCached) {
			logger.Debug(fmt.Sprintf("TCP handler fast-failed for %s: %v", metadata.HostName, err))
		} else {
			logger.Error(fmt.Sprintf("TCP handler failed for %s: %v", metadata.HostName, err))
		}
		return
	}

	logger.Debug(fmt.Sprintf("HTTP connection closed: %s:%d", metadata.HostName, metadata.DstPort))
}

// writePlainHTTPErrorResponse 是明文 HTTP 面的错误应答:状态行、头、体一次
// 写成,Content-Length 与实际体一致。旧写法先 WriteHeader 再写体——头部发出
// 时 Content-Length 已定为 0,错误体被客户端按定长丢弃(回环同步管道下则
// 直接卡死在写体上)。
func writePlainHTTPErrorResponse(conn net.Conn, status int, body string) {
	resp := &http.Response{
		Status:        fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode:    status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Close:         true,
	}
	_ = resp.Write(conn)
}

// extractMetadataFromHTTP extracts connection metadata from HTTP request
func extractMetadataFromHTTP(req *http.Request, conn net.Conn) (*M.Metadata, error) {
	metadata := &M.Metadata{
		Network: M.TCP,
		// 代理面来源标记:回环/内网目标守卫据此判定(明文 HTTP 是统一入口的
		// 代理面之一)。
		IngressProxyFace: true,
	}

	// Extract source
	if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(remoteAddr.IP); err == nil {
			metadata.SrcIP = addr
		}
		metadata.SrcPort = uint16(remoteAddr.Port)
	}

	// Extract local address
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(localAddr.IP); err == nil {
			metadata.MidIP = addr
			metadata.DstIP = addr
		}
		metadata.MidPort = uint16(localAddr.Port)
		metadata.DstPort = uint16(localAddr.Port)
	}

	// Extract host and port from HTTP request. 缺 Host 一律拒绝(HTTP/1.1 的
	// 协议要求):旧实现回退到「监听地址」当目标——56432 绑 0.0.0.0 时那是本机
	// 局域网口,非回环来源的无 Host 请求会被原样重放回自己的 56432,自连接
	// 递归直到 fd 耗尽(2026-11-24 审计)。
	host := strings.TrimSpace(req.Host)
	if host == "" {
		host = strings.TrimSpace(req.URL.Host)
	}
	if host == "" {
		return nil, errors.New("missing Host header")
	}

	hostOnly, port, err := parseHTTPHostPort(host, metadata.DstPort)
	if err != nil {
		return nil, fmt.Errorf("invalid host %q: %w", host, err)
	}

	if !isLoopbackHost(hostOnly) {
		return nil, fmt.Errorf("explicit host %q is not a local loopback address", host)
	}

	if hostOnly != "" {
		metadata.SetHostName(hostOnly, M.BindingSourceHTTP, 10*time.Minute)
	}
	if port != 0 {
		metadata.DstPort = port
	}

	if ip := net.ParseIP(hostOnly); ip != nil {
		if addr, err := convertNetIPToNetipAddr(ip); err == nil {
			metadata.DstIP = addr
		}
	} else if strings.EqualFold(hostOnly, "localhost") && metadata.MidIP.IsValid() {
		metadata.DstIP = metadata.MidIP
	}

	return metadata, nil
}

func parseHTTPHostPort(rawHost string, fallbackPort uint16) (string, uint16, error) {
	rawHost = strings.TrimSpace(rawHost)
	if rawHost == "" {
		return "", fallbackPort, nil
	}

	if strings.Contains(rawHost, "://") {
		if parsedURL, err := url.Parse(rawHost); err == nil && parsedURL.Host != "" {
			rawHost = parsedURL.Host
		}
	}

	if host, portStr, err := net.SplitHostPort(rawHost); err == nil {
		port, parseErr := strconv.ParseUint(portStr, 10, 16)
		if parseErr != nil {
			return "", 0, parseErr
		}
		return strings.Trim(host, "[]"), uint16(port), nil
	}

	trimmedHost := strings.Trim(rawHost, "[]")
	if ip := net.ParseIP(trimmedHost); ip != nil {
		return trimmedHost, fallbackPort, nil
	}

	if host, portStr, ok := strings.Cut(rawHost, ":"); ok && !strings.Contains(host, ":") {
		port, parseErr := strconv.ParseUint(portStr, 10, 16)
		if parseErr != nil {
			return "", 0, parseErr
		}
		return strings.Trim(host, "[]"), uint16(port), nil
	}

	return trimmedHost, fallbackPort, nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// convertNetIPToNetipAddr converts net.IP to netip.Addr
func convertNetIPToNetipAddr(ip net.IP) (netip.Addr, error) {
	if ip == nil {
		return netip.Addr{}, fmt.Errorf("nil IP")
	}
	return netip.ParseAddr(ip.String())
}

type replayConn struct {
	net.Conn
	reader io.Reader
}

func (c *replayConn) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

func (c *replayConn) CloseRead() error {
	if closer, ok := c.Conn.(interface{ CloseRead() error }); ok {
		return closer.CloseRead()
	}
	return nil
}

func (c *replayConn) CloseWrite() error {
	if closer, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return closer.CloseWrite()
	}
	return nil
}

func newHTTPRequestReplayConn(conn net.Conn, reader *bufio.Reader, req *http.Request) (net.Conn, error) {
	if conn == nil {
		return nil, errors.New("conn is nil")
	}
	if reader == nil {
		return nil, errors.New("reader is nil")
	}
	head, err := serializeHTTPRequestHead(req)
	if err != nil {
		return nil, err
	}

	return &replayConn{
		Conn:   conn,
		reader: io.MultiReader(bytes.NewReader(head), reader),
	}, nil
}

func serializeHTTPRequestHead(req *http.Request) ([]byte, error) {
	if req == nil {
		return nil, errors.New("request is nil")
	}

	target := strings.TrimSpace(req.RequestURI)
	if target == "" && req.URL != nil {
		target = req.URL.String()
	}
	if target == "" {
		return nil, errors.New("request target is empty")
	}

	proto := strings.TrimSpace(req.Proto)
	if proto == "" {
		proto = "HTTP/1.1"
	}

	var buf bytes.Buffer
	if _, err := fmt.Fprintf(&buf, "%s %s %s\r\n", req.Method, target, proto); err != nil {
		return nil, err
	}

	hostWritten := false
	if req.Host != "" {
		if _, err := fmt.Fprintf(&buf, "Host: %s\r\n", req.Host); err != nil {
			return nil, err
		}
		hostWritten = true
	}

	contentLengthWritten := false
	transferEncodingWritten := false
	for key, values := range req.Header {
		if strings.EqualFold(key, "Host") {
			if !hostWritten && len(values) > 0 {
				if _, err := fmt.Fprintf(&buf, "Host: %s\r\n", values[0]); err != nil {
					return nil, err
				}
				hostWritten = true
			}
			continue
		}
		if strings.EqualFold(key, "Content-Length") {
			contentLengthWritten = true
		}
		if strings.EqualFold(key, "Transfer-Encoding") {
			transferEncodingWritten = true
		}
		for _, value := range values {
			if _, err := fmt.Fprintf(&buf, "%s: %s\r\n", key, value); err != nil {
				return nil, err
			}
		}
	}

	if !contentLengthWritten && req.ContentLength > 0 {
		if _, err := fmt.Fprintf(&buf, "Content-Length: %d\r\n", req.ContentLength); err != nil {
			return nil, err
		}
	}
	if !transferEncodingWritten && len(req.TransferEncoding) > 0 {
		if _, err := fmt.Fprintf(&buf, "Transfer-Encoding: %s\r\n", strings.Join(req.TransferEncoding, ", ")); err != nil {
			return nil, err
		}
	}

	if _, err := buf.WriteString("\r\n"); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
