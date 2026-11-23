package ipc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// stopWaitTimeout 是 Stop 等待在途连接排空的兜底上界。
const stopWaitTimeout = 2 * time.Second

// HandlerFunc is a function that handles an IPC action.
type HandlerFunc func(args json.RawMessage) (interface{}, error)

// Server represents the IPC server running in the Core daemon.
type Server struct {
	transport Transport
	handlers  map[string]HandlerFunc
	listener  net.Listener
	mu        sync.Mutex
	wg        sync.WaitGroup
	stopCh    chan struct{}
	conns     map[net.Conn]struct{}
	stopOnce  sync.Once
	stopErr   error
}

// NewServer creates a new IPC server.
func NewServer() *Server {
	return NewServerWithTransport(NewTransport())
}

// NewServerWithTransport creates a new IPC server over the given transport
// (测试注入用)。
func NewServerWithTransport(transport Transport) *Server {
	return &Server{
		transport: transport,
		handlers:  make(map[string]HandlerFunc),
		stopCh:    make(chan struct{}),
		conns:     make(map[net.Conn]struct{}),
	}
}

// Register registers a handler for an action.
func (s *Server) Register(action string, handler HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[action] = handler
}

// Start starts the IPC server.
func (s *Server) Start() error {
	// Ensure directories exist
	if err := EnsureCoreDirs(); err != nil {
		return fmt.Errorf("[IPC] failed to ensure directories: %w", err)
	}

	listener, err := s.transport.Listen()
	if err != nil {
		return fmt.Errorf("[IPC] failed to start listener: %w", err)
	}
	s.listener = listener

	logger.Info(fmt.Sprintf("[IPC] Server listening on %s", s.transport.SocketPath()))

	go s.acceptLoop()
	return nil
}

// Stop stops the IPC server gracefully. 幂等:重复调用返回首次结果
// (stopCh 不可重关)。
func (s *Server) Stop() error {
	s.stopOnce.Do(func() { s.stopErr = s.stop() })
	return s.stopErr
}

// stopping 报告服务是否已进入停止流程。stop() 拨读截止唤醒阻塞读之后,
// 各 handleConn 会以任意读失败形态退出——停机期的失败是预期路径,调用方
// 据此静默,不往日志里刷噪音。
func (s *Server) stopping() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *Server) stop() error {
	close(s.stopCh)

	var listenErr error
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			listenErr = fmt.Errorf("[IPC] failed to close listener: %w", err)
		}
	}

	// 唤醒全部存量连接的阻塞读:把读截止拨到过去,各 handleConn 的 Decode
	// 立即以超时失败返回,经它们自己的 defer Close 收尾——Close 永远发生在
	// 该连接的 I/O 排空之后。纪律性规避重叠 I/O 腐坏族崩溃(2026-11-23
	// win196 `fatal error: fault` 定谳于 go-winio asyncIO):winio 的
	// closeHandle 虽然自带 cancelIoEx+wg.Wait 排空设计,但「读侧主动退出、
	// 关闭只走读侧 defer」是对抗内核取消路径异常的更稳姿势。不关连接 wg.Wait
	// 结构性必挂的教训不变(2026-11-16 SCM StopPending 实锤)——deadline
	// 唤醒同样能让常驻轮询连接立即出读。
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.SetReadDeadline(time.Now())
	}

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(stopWaitTimeout):
		// 有界放弃:进程退出场景下泄漏安全;响亮日志留痕,不再无限挂死。
		logger.Warn(fmt.Sprintf("[IPC] stop timed out after %s waiting for in-flight connections (leaked goroutines tolerated)", stopWaitTimeout))
	}
	logger.Info("[IPC] Server stopped")
	return listenErr
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				logger.Error(fmt.Sprintf("[IPC] accept error: %v", err))
				continue
			}
		}

		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func(conn net.Conn) {
			defer s.wg.Done()
			s.handleConn(conn)
		}(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
	}()

	for {
		// Read request
		dec := json.NewDecoder(conn)
		var req Request
		if err := dec.Decode(&req); err != nil {
			// EOF=客户端正常收线;停机期(stop() 拨读截止唤醒)的失败是预期
			// 路径——两者都不留错误日志,防止停机刷屏淹没真异常。
			if err != io.EOF && !s.stopping() {
				logger.Error(fmt.Sprintf("[IPC] decode error: %v", err))
			}
			return
		}

		// Handle request
		resp := s.handleRequest(&req)

		// Write response
		enc := json.NewEncoder(conn)
		if err := enc.Encode(resp); err != nil {
			logger.Error(fmt.Sprintf("[IPC] encode error: %v", err))
			return
		}
	}
}

func (s *Server) handleRequest(req *Request) *Response {
	s.mu.Lock()
	handler, ok := s.handlers[req.Action]
	s.mu.Unlock()

	if !ok {
		return ErrorResponse(req.ID, fmt.Errorf("unknown action: %s", req.Action))
	}

	data, err := handler(req.Args)
	if err != nil {
		return ErrorResponse(req.ID, err)
	}

	return OKResponse(req.ID, data)
}
