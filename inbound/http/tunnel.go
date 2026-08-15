package http

import (
	"context"
	"fmt"
	"net"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/processor/tcp"
)

// HandleRawConnect handles HTTP CONNECT tunneling
// It delegates to processor/tcp for unified TCP handling with routing decisions
func HandleRawConnect(clientConn net.Conn, metadata *M.Metadata) error {
	// 从连接信息中提取ClientAddr以便与server.go日志关联
	clientAddr := "unknown"
	if tcpConn, ok := clientConn.(*net.TCPConn); ok {
		if addr := tcpConn.RemoteAddr(); addr != nil {
			clientAddr = addr.String()
		}
	} else if addr := clientConn.RemoteAddr(); addr != nil {
		clientAddr = addr.String()
	}

	logger.Debug(fmt.Sprintf("[TUNNEL] 隧道参数 - 客户端:%s, 目标:%s:%d",
		clientAddr, metadata.HostName, metadata.DstPort))

	// Create context for the handler
	ctx := context.Background()

	// Get the unified TCP handler
	handler := tcp.GetHandler()

	// Delegate to processor/tcp for routing and relay
	// The handler will:
	// 1. Detect protocol (TLS on 443, HTTP on 80, direct for others)
	// 2. Route based on domain rules (cursor proxy, door proxy, or direct)
	// 3. Handle SNI extraction if TLS
	// 4. Perform bidirectional relay with statistics
	logger.Debug(fmt.Sprintf("[TUNNEL] 调用TCP Handler处理 %s:%d", metadata.HostName, metadata.DstPort))
	if err := handler.Handle(ctx, clientConn, metadata); err != nil {
		logger.Error(fmt.Sprintf("[TUNNEL] TCP Handler失败: 客户端:%s, 目标:%s, 错误:%v",
			clientAddr, metadata.HostName, err))
		return err
	}

	logger.Debug(fmt.Sprintf("[TUNNEL] 隧道已关闭: %s:%d", metadata.HostName, metadata.DstPort))
	return nil
}
