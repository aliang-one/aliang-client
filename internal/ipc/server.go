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

func (s *Server) stop() error {
	close(s.stopCh)

	var listenErr error
	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			listenErr = fmt.Errorf("[IPC] failed to close listener: %w", err)
		}
	}

	// 关闭全部存量连接:handleConn 读无 deadline,而托盘 companion 持常驻
	// 连接(get_status 2s 轮询)——不关连接 wg.Wait 结构性必挂,SCM 停止
	// 就会卡 StopPending 直至强杀(2026-11-16 生产实锤)。主动断开让各
	// handleConn 的 Decode 立即出错返回。
	s.mu.Lock()
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
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
			if err != io.EOF {
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
