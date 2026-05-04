package aliang

import (
	"sync"
	"time"
)

// ConnectionPool manages pooled TLS connections
type ConnectionPool struct {
	mu            sync.RWMutex
	conns         map[string]*PooledConn
	config        *ConnectionPoolConfig
	cleanupTicker *time.Ticker
	closeChan     chan struct{}
	statsLock     sync.RWMutex
	totalCreated  int64
	totalReused   int64
	totalClosed   int64
}

// NewConnectionPool creates a new connection pool
func NewConnectionPool(config *ConnectionPoolConfig) *ConnectionPool {
	if config == nil {
		config = &ConnectionPoolConfig{
			MaxConnPerHost:  4,
			MaxIdleTime:     5 * time.Minute,
			CleanupInterval: 1 * time.Minute,
		}
	}

	cp := &ConnectionPool{
		conns:        make(map[string]*PooledConn),
		config:       config,
		closeChan:    make(chan struct{}),
		totalCreated: 0,
		totalReused:  0,
		totalClosed:  0,
	}

	// Start cleanup goroutine
	cp.cleanupTicker = time.NewTicker(config.CleanupInterval)
	go cp.cleanupRoutine()

	return cp
}

// Get retrieves a connection from the pool
func (cp *ConnectionPool) Get(key string) *PooledConn {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	conn, exists := cp.conns[key]
	if exists && conn != nil {
		conn.LastUsed = time.Now()
		cp.updateStats(false)
		return conn
	}

	return nil
}

// Put returns a connection to the pool
func (cp *ConnectionPool) Put(key string, conn *PooledConn) error {
	if conn == nil {
		return NewErrorf(ErrInvalidConfig, "cannot put nil connection")
	}

	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Check if we should store this connection
	// For now, store it if there's space or replace
	conn.LastUsed = time.Now()
	cp.conns[key] = conn

	return nil
}

// Remove removes a connection from the pool
func (cp *ConnectionPool) Remove(key string) {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	conn, exists := cp.conns[key]
	if exists && conn != nil {
		conn.Conn.Close()
		delete(cp.conns, key)
		cp.updateStats(true)
	}
}

// Stats returns pool statistics
func (cp *ConnectionPool) Stats() map[string]interface{} {
	cp.mu.RLock()
	connCount := len(cp.conns)
	cp.mu.RUnlock()

	cp.statsLock.RLock()
	defer cp.statsLock.RUnlock()

	return map[string]interface{}{
		"total_connections": connCount,
		"total_created":     cp.totalCreated,
		"total_reused":      cp.totalReused,
		"total_closed":      cp.totalClosed,
	}
}

// ClearAllConnections closes all pooled connections without closing the pool
// This is useful for clearing stale connections when switching proxies
func (cp *ConnectionPool) ClearAllConnections() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Close all connections
	for key, conn := range cp.conns {
		if conn != nil && conn.Conn != nil {
			conn.Conn.Close()
		}
		delete(cp.conns, key)
	}

	return nil
}

// Close closes all connections in the pool
func (cp *ConnectionPool) Close() error {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	// Stop cleanup goroutine
	if cp.cleanupTicker != nil {
		cp.cleanupTicker.Stop()
	}
	close(cp.closeChan)

	// Close all connections
	for key, conn := range cp.conns {
		if conn != nil && conn.Conn != nil {
			conn.Conn.Close()
		}
		delete(cp.conns, key)
	}

	return nil
}

// cleanupRoutine periodically removes idle connections
func (cp *ConnectionPool) cleanupRoutine() {
	for {
		select {
		case <-cp.closeChan:
			return
		case <-cp.cleanupTicker.C:
			cp.cleanup()
		}
	}
}

// cleanup removes idle connections from the pool
func (cp *ConnectionPool) cleanup() {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	now := time.Now()
	for key, conn := range cp.conns {
		if conn != nil && now.Sub(conn.LastUsed) > cp.config.MaxIdleTime {
			conn.Conn.Close()
			delete(cp.conns, key)
			cp.updateStats(true)
		}
	}
}

// updateStats updates connection pool statistics
func (cp *ConnectionPool) updateStats(closed bool) {
	cp.statsLock.Lock()
	defer cp.statsLock.Unlock()

	if closed {
		cp.totalClosed++
	} else {
		cp.totalReused++
	}
}
