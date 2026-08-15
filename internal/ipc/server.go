package ipc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"

	"aliang.one/nursorgate/common/logger"
)

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
}

// NewServer creates a new IPC server.
func NewServer() *Server {
	return &Server{
		transport: NewTransport(),
		handlers:  make(map[string]HandlerFunc),
		stopCh:    make(chan struct{}),
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

// Stop stops the IPC server gracefully.
func (s *Server) Stop() error {
	close(s.stopCh)

	if s.listener != nil {
		if err := s.listener.Close(); err != nil {
			return fmt.Errorf("[IPC] failed to close listener: %w", err)
		}
	}

	s.wg.Wait()
	logger.Info("[IPC] Server stopped")
	return nil
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

		s.wg.Add(1)
		go func(conn net.Conn) {
			defer s.wg.Done()
			s.handleConn(conn)
		}(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()

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
