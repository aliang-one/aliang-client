# Cursor H2 Proxy Module

A minimal mTLS connection establishment and pooling module for the Nursor VPN gateway, implementing the standard `Proxy` interface.

## Overview

The `cursor_h2` module focuses on a single core responsibility: **establishing and managing mTLS connections to the cursor server**.

### Core Features

- **mTLS Connection Establishment** - Secure connections using hardcoded client certificates
- **Connection Pooling** - TLS connection reuse with configurable concurrency limits
- **Simple and Focused** - Only core connection management, no protocol handling or token injection
- **Thread Safe** - All components use proper synchronization primitives

## Architecture

```
outbound/proxy/cursor_h2/
├── types.go              # Core data structures (PooledConn)
├── config.go             # Configuration management
├── errors.go             # Error definitions
├── proxy.go              # CursorH2 Proxy interface implementation
├── handshaker.go         # mTLS connection establishment (CursorServerConnector)
├── connection_pool.go    # TLS connection pooling and reuse
├── factory.go            # Factory function for creating proxy instances
└── README.md             # This file
```

## Quick Start

### Basic Usage

```go
package main

import (
	"context"
	"fmt"
	"time"

	"aliang.one/nursorgate/outbound/proxy/cursor_h2"
)

func main() {
	// Create configuration
	config := &cursor_h2.CursorH2Config{
		Addr:                 "cursor.example.com:443",
		DialTimeout:          10 * time.Second,
		ReadTimeout:          30 * time.Second,
		WriteTimeout:         30 * time.Second,
		MaxConcurrentStreams: 250,
		ConnectionPool: &cursor_h2.ConnectionPoolConfig{
			MaxConnPerHost:  4,
			MaxIdleTime:     5 * time.Minute,
			CleanupInterval: 1 * time.Minute,
		},
	}

	// Create proxy instance (simplified - only requires config)
	proxy, err := cursor_h2.New(config)
	if err != nil {
		fmt.Printf("Failed to create proxy: %v\n", err)
		return
	}
	defer proxy.Close()

	// Use proxy to establish mTLS connection
	ctx := context.Background()
	conn, err := proxy.DialContext(ctx, "tcp", "example.com:443")
	if err != nil {
		fmt.Printf("Failed to dial: %v\n", err)
		return
	}
	defer conn.Close()

	fmt.Println("mTLS connection established successfully!")
}
```

## Configuration

The `CursorH2Config` struct provides configuration for mTLS connections:

```go
type CursorH2Config struct {
	// Cursor server address (required)
	Addr string

	// Timeout options
	DialTimeout  time.Duration // TLS handshake timeout
	ReadTimeout  time.Duration // Read timeout
	WriteTimeout time.Duration // Write timeout

	// Connection pooling
	ConnectionPool *ConnectionPoolConfig {
		MaxConnPerHost  int           // Max connections per host
		MaxIdleTime     time.Duration // Idle connection timeout
		CleanupInterval time.Duration // Cleanup check interval
	}

	// Stream settings
	MaxConcurrentStreams uint32
}
```

## Key Components

### 1. CursorH2 (proxy.go)
Main proxy implementation. Implements the `Proxy` interface with:
- `DialContext()` - Establish or reuse mTLS connection
- `Close()` - Clean shutdown
- `Addr()` - Return server address
- `Proto()` - Return "cursor_h2"

### 2. CursorServerConnector (handshaker.go)
Handles mTLS handshake using hardcoded client certificates:
- Loads client certificate from `processor/cert/client`
- Establishes TLS connection
- Handles handshake errors

### 3. ConnectionPool (connection_pool.go)
Manages pooled TLS connections:
- Stores connections by address
- Supports configurable pool size
- Idle timeout tracking and cleanup

## Interfaces

### Proxy Interface (standard implementation)

```go
type Proxy interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	DialUDP(ctx context.Context, network, address string) (net.Conn, error)
	Addr() string
	Proto() string
}
```

## Error Handling

The module defines specific error codes:

- `ErrInvalidConfig` - Invalid configuration
- `ErrTLSHandshakeFailed` - mTLS handshake failure
- `ErrConnectionPoolFull` - Connection pool is full
- `ErrConnectionTimeout` - Connection timeout

## Thread Safety

All components are thread-safe:
- Connection pool uses `sync.RWMutex` for safe concurrent access
- Configuration is immutable after creation
- No shared mutable state between goroutines

## What This Module Does NOT Include

This module intentionally excludes:
- Protocol handling (HTTP/1 vs HTTP/2 detection)
- Frame parsing or processing
- Token injection
- Stream management
- HPACK compression

These concerns are delegated to higher-level components that consume the raw mTLS connection.

## Integration

To use cursor_h2 with the proxy registry:

```go
import (
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/cursor_h2"
)

// Create proxy instance
proxy, err := cursor_h2.NewCursorH2(config)
if err != nil {
	log.Fatal(err)
}

// Register or use directly
// proxy.DialContext(...) to establish connections
```

## Testing

Run the test suite:

```bash
cd /Users/mac/MyProgram/GoProgram/nursor/nursorgate2
go test ./outbound/proxy/cursor_h2/...
```

Test Coverage:
- Proxy creation and configuration validation
- Connection pooling behavior
- Error handling for invalid inputs
- Proxy lifecycle (close, reuse)

## Performance

Connection Pooling Benefits:
- **Reduced Handshake Overhead**: TLS handshakes (100-200ms) are expensive; reusing connections avoids repeated handshakes
- **Throughput Improvement**: Reusing connections provides 20-30% throughput improvement in high-concurrency scenarios
- **Resource Efficiency**: Fewer open connections = lower memory and file descriptor usage

## Design Principles

1. **Single Responsibility**: Focus only on mTLS connection establishment and pooling
2. **Simplicity**: Minimal code, minimal dependencies
3. **Clarity**: Clear separation between connection establishment and protocol handling
4. **Thread Safety**: Safe for concurrent use
5. **Standard Interface**: Implements the `Proxy` interface for integration

## Contributing

When modifying this module:
1. Maintain the single responsibility principle
2. Do not add protocol-handling logic
3. Keep error handling consistent
4. Add tests for new features
5. Update this documentation

## Related Modules

- `processor/cert/client` - Client certificate management
- `processor/proxy` - Proxy registry and interface definitions
- For protocol handling: Implement using the raw connection from this module

## License

Same as the Nursor project license.
