package http

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/config"
)

// HTTP server state management
var (
	httpListener  net.Listener
	httpCtx       context.Context
	httpCancel    context.CancelFunc
	httpMutex     sync.Mutex
	isHttpRunning bool
	connIDCounter int64
)

// StartHttpProxy starts the HTTP CONNECT proxy server
// This is a blocking function that runs until stopped
func StartMitmHttp() {
	httpMutex.Lock()
	if isHttpRunning {
		httpMutex.Unlock()
		logger.Warn("HTTP proxy is already running")
		return
	}
	if !auth.GetSessionAuthority().CanProxy() {
		httpMutex.Unlock()
		logger.Warn("HTTP proxy start rejected: active session required")
		return
	}

	// Bind while holding the lifecycle lock. Logout first demotes authority, then
	// StopHttpProxy takes this same lock; this prevents a delayed start from
	// binding after the stop path observed an empty listener.
	listenAddr := config.HTTPProxyListenAddr()
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		isHttpRunning = false
		httpMutex.Unlock()
		logger.GetMainLogger().Fatal(fmt.Sprintf("Failed to listen on %s: %v", listenAddr, err))
	}

	httpCtx, httpCancel = context.WithCancel(context.Background())
	isHttpRunning = true
	httpListener = listener
	httpMutex.Unlock()

	logger.Info(fmt.Sprintf("HTTP CONNECT proxy server starting on %s", listenAddr))

	// Accept connections in a loop until context is cancelled
	for {
		// Accept will block until a connection arrives or listener is closed
		conn, err := listener.Accept()
		if err != nil {
			// Check if context was cancelled (listener was closed for shutdown)
			select {
			case <-httpCtx.Done():
				logger.Info("HTTP proxy server stopped")
				httpMutex.Lock()
				isHttpRunning = false
				httpMutex.Unlock()
				return
			default:
				// Other errors (should be rare, e.g., network issues)
				logger.Debug(fmt.Sprintf("Accept error: %v", err))
				continue
			}
		}

		// Handle connection in a separate goroutine
		go handleRawConnection(conn)
	}
}

// StopHttpProxy stops the HTTP proxy server gracefully
func StopHttpProxy() {
	httpMutex.Lock()
	defer httpMutex.Unlock()

	if !isHttpRunning {
		logger.Warn("HTTP proxy is not running")
		return
	}

	logger.Info("Stopping HTTP proxy server...")

	// Cancel context to signal shutdown
	if httpCancel != nil {
		httpCancel()
	}

	// Close listener to interrupt Accept() call
	// This must be done outside the mutex to avoid deadlock
	listener := httpListener
	httpMutex.Unlock()

	if listener != nil {
		if err := listener.Close(); err != nil {
			logger.Debug(fmt.Sprintf("Error closing listener: %v", err))
		}
	}

	// Re-acquire lock to update state
	httpMutex.Lock()
	isHttpRunning = false
	logger.Info("HTTP proxy server stopped")
}

// IsHttpRunning returns the current state of HTTP proxy
func IsHttpRunning() bool {
	httpMutex.Lock()
	defer httpMutex.Unlock()
	return isHttpRunning
}

// 处理客户端连接
func handleRawConnection(conn net.Conn) {
	defer conn.Close()

	// 为每个连接分配唯一ID
	connID := atomic.AddInt64(&connIDCounter, 1)
	logger.Debug(fmt.Sprintf("[CONN#%d] 新连接建立 - %s → %s", connID, conn.RemoteAddr(), config.HTTPProxyListenAddr()))

	// 读取客户端初始数据，检查是否为 CONNECT 请求
	reader := bufio.NewReader(conn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		logger.Error(fmt.Sprintf("[CONN#%d] 读取请求失败: %v", connID, err))
		return
	}

	lease, err := auth.GetSessionAuthority().AcquireProxyLease(func() { _ = conn.Close() })
	if err != nil {
		response := &http.Response{
			Status:        "503 Service Unavailable",
			StatusCode:    http.StatusServiceUnavailable,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Body:          io.NopCloser(strings.NewReader("active session required")),
			ContentLength: int64(len("active session required")),
			Close:         true,
		}
		_ = response.Write(conn)
		return
	}
	defer lease.Release()

	if req.Method == "CONNECT" {
		logger.Debug(fmt.Sprintf("[CONN#%d] CONNECT请求: %s", connID, req.Host))

		// Extract metadata from CONNECT request
		metadata, err := ExtractMetadataFromCONNECT(req, conn)
		if err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] 提取元数据失败: %v", connID, err))
			resp := &http.Response{
				Status:        "400 Bad Request",
				StatusCode:    http.StatusBadRequest,
				Proto:         "HTTP/1.1",
				ProtoMajor:    1,
				ProtoMinor:    1,
				Body:          io.NopCloser(strings.NewReader("")),
				ContentLength: 0,
			}
			resp.Write(conn)
			return
		}

		logger.Debug(fmt.Sprintf("[CONN#%d] 目标信息: %s:%d (IP:%s)", connID, metadata.HostName, metadata.DstPort, metadata.DstIP))

		// Send 200 Connection Established
		// IMPORTANT: Use raw write instead of http.Response for proper formatting
		response200 := "HTTP/1.1 200 Connection Established\r\n\r\n"
		if _, err := conn.Write([]byte(response200)); err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] 发送200应答失败: %v", connID, err))
			return
		}
		logger.Debug(fmt.Sprintf("[CONN#%d] ✓ 已发送200 Connection Established", connID))

		// Handle CONNECT tunnel - establishes direct tunnel between client and target
		logger.Debug(fmt.Sprintf("[CONN#%d] 开始建立隧道...", connID))
		if err := HandleRawConnect(conn, metadata); err != nil {
			logger.Error(fmt.Sprintf("[CONN#%d] ❌ 隧道建立失败: %v", connID, err))
		} else {
			logger.Debug(fmt.Sprintf("[CONN#%d] ✓ 隧道建立成功并已关闭", connID))
		}
	} else {
		// 透明代理，基本不存在
		HandleHttpConnection(conn, reader, req)
	}
}
