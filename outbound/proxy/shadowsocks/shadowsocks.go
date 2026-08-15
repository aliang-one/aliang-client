package shadowsocks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"

	"github.com/xjasonlyu/tun2socks/v2/transport/shadowsocks/core"

	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/dialer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

// isDNSError 检查错误是否为DNS解析相关错误
func isDNSError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()

	// 常见的DNS解析错误模式
	dnsErrorPatterns := []string{
		"lookup",
		"no such host",
		"server not found",
		"dns timeout",
		"i/o timeout",
		"timeout while waiting for response",
	}

	for _, pattern := range dnsErrorPatterns {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			return true
		}
	}

	// 检查是否为net.DNSError类型
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// Shadowsocks Shadowsocks 代理实现
// 基于 tun2socks 的核心加密库，提供改进的错误处理和稳定性
type Shadowsocks struct {
	*proxy.Base

	method   string
	password string
	username string
	obfsMode string
	obfsHost string

	mu sync.RWMutex
}

// ShadowsocksConfig Shadowsocks 配置选项
type ShadowsocksConfig struct {
	Server   string `json:"server"`
	Port     uint16 `json:"port"`
	Method   string `json:"method"`
	Password string `json:"password"`
	Username string `json:"username,omitempty"`
	ObfsMode string `json:"obfs_mode"`
	ObfsHost string `json:"obfs_host"`
}

// NewShadowsocksWithConfig 使用配置创建 Shadowsocks 客户端
func NewShadowsocksWithConfig(config *ShadowsocksConfig) (*Shadowsocks, error) {
	// 验证加密方式和密码
	if config.Method == "" {
		return nil, errors.New("shadowsocks method cannot be empty")
	}
	if config.Password == "" {
		return nil, errors.New("shadowsocks password cannot be empty")
	}

	ss := &Shadowsocks{
		Base: &proxy.Base{
			Address:  fmt.Sprintf("%s:%d", config.Server, config.Port),
			Protocol: proto.Shadowsocks,
		},
		method:   config.Method,
		password: config.Password,
		username: config.Username,
		obfsMode: config.ObfsMode,
		obfsHost: config.ObfsHost,
	}

	obfsInfo := ""
	if config.ObfsMode != "" {
		obfsInfo = fmt.Sprintf(", obfs: %s", config.ObfsMode)
	}

	userInfo := "-"
	if config.Username != "" {
		userInfo = config.Username
	}

	logger.Info(fmt.Sprintf("[Shadowsocks] ✓ 客户端已创建 服务器: %s:%d, 用户: %s, 加密方式: %s%s", config.Server, config.Port, userInfo, config.Method, obfsInfo))

	return ss, nil
}

// DialContext 实现 Proxy 接口的 DialContext 方法
func (ss *Shadowsocks) DialContext(ctx context.Context, metadata *M.Metadata) (c net.Conn, err error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	logger.Debug(fmt.Sprintf("[Shadowsocks] 连接到 Shadowsocks 服务器 %s, 目标: %s:%d", ss.Addr(), metadata.HostName, metadata.DstPort))

	// 连接到 Shadowsocks 服务器
	var connErr error
	c, connErr = dialer.DialContext(ctx, "tcp", ss.Addr())
	if connErr != nil {
		// 改进错误处理，区分DNS解析错误和连接错误
		if isDNSError(connErr) {
			logger.Error(fmt.Sprintf("[Shadowsocks] ✗ DNS解析失败 %s: %v", ss.Addr(), connErr))
			return nil, fmt.Errorf("DNS resolution failed for shadowsocks server %s: %w", ss.Addr(), connErr)
		} else {
			logger.Error(fmt.Sprintf("[Shadowsocks] ✗ 连接失败 %s: %v", ss.Addr(), connErr))
			return nil, fmt.Errorf("failed to connect to shadowsocks server %s: %w", ss.Addr(), connErr)
		}
	}

	// 设置 KeepAlive
	proxy.SetKeepAlive(c)

	// 为每个连接创建新的cipher实例，完全隔离状态
	cipher, err := core.PickCipher(ss.method, nil, ss.password)
	if err != nil {
		logger.Error(fmt.Sprintf("[Shadowsocks] ✗ 创建cipher失败: %v", err))
		c.Close()
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// 保存原始TCP连接，以便错误时清理
	rawConn := c

	// 延迟清理连接（如果出错）
	defer func() {
		if err != nil {
			// 只关闭原始TCP连接，让cipher.StreamConn()返回的包装器自行清理
			if rawConn != nil {
				rawConn.Close()
			}
		}
	}()

	// 使用加密包装连接
	c = cipher.StreamConn(rawConn)

	// 构建 SOCKS5 地址
	socksAddr := proxy.SerializeSocksAddr(metadata)

	// 调试日志：显示metadata和SOCKS5地址详情
	logger.Debug(fmt.Sprintf("[Shadowsocks] DEBUG - metadata.DstIP: %v (valid: %v), DstPort: %d, HostName: %s",
		metadata.DstIP, metadata.DstIP.IsValid(), metadata.DstPort, metadata.HostName))
	logger.Debug(fmt.Sprintf("[Shadowsocks] DEBUG - socksAddr bytes: %v (len: %d)", socksAddr, len(socksAddr)))

	// 发送目标地址信息到服务器
	n, err := c.Write(socksAddr)
	logger.Debug(fmt.Sprintf("[Shadowsocks] DEBUG - Write result: n=%d, err=%v", n, err))

	if err != nil {
		logger.Error(fmt.Sprintf("[Shadowsocks] ✗ 发送地址信息失败: %v", err))
		return nil, fmt.Errorf("failed to write socks addr: %w", err)
	}

	userInfo := "-"
	if ss.username != "" {
		userInfo = ss.username
	}

	logger.Debug(fmt.Sprintf("[Shadowsocks] ✓ TCP连接成功 用户: %s, 目标: %s:%d", userInfo, metadata.HostName, metadata.DstPort))

	return c, err
}

// DialUDP 实现 Proxy 接口的 DialUDP 方法
func (ss *Shadowsocks) DialUDP(metadata *M.Metadata) (net.PacketConn, error) {
	ss.mu.RLock()
	defer ss.mu.RUnlock()

	logger.Debug(fmt.Sprintf("[Shadowsocks] 创建 UDP 连接到 %s, 目标: %s:%d", ss.Addr(), metadata.HostName, metadata.DstPort))

	// 创建本地 UDP 套接字
	pc, err := dialer.ListenPacket("udp", "")
	if err != nil {
		logger.Error(fmt.Sprintf("[Shadowsocks] ✗ UDP 监听失败: %v", err))
		return nil, fmt.Errorf("failed to listen packet: %w", err)
	}

	// 解析服务器地址
	udpAddr, err := net.ResolveUDPAddr("udp", ss.Addr())
	if err != nil {
		pc.Close()
		logger.Error(fmt.Sprintf("[Shadowsocks] ✗ 解析 UDP 地址失败 %s: %v", ss.Addr(), err))
		return nil, fmt.Errorf("resolve udp address %s: %w", ss.Addr(), err)
	}

	// 为这个UDP连接创建新的cipher实例
	cipher, err := core.PickCipher(ss.method, nil, ss.password)
	if err != nil {
		pc.Close()
		logger.Error(fmt.Sprintf("[Shadowsocks] ✗ UDP cipher创建失败: %v", err))
		return nil, fmt.Errorf("failed to create cipher for UDP: %w", err)
	}

	// 使用加密包装 UDP 连接
	pc = cipher.PacketConn(pc)

	logger.Debug(fmt.Sprintf("[Shadowsocks] ✓ UDP连接成功 目标: %s:%d", metadata.HostName, metadata.DstPort))

	return &ssPacketConn{
		PacketConn: pc,
		rAddr:      udpAddr,
		metadata:   metadata,
	}, nil
}

// ssPacketConn 包装 UDP 连接，处理 SOCKS5 地址格式
type ssPacketConn struct {
	net.PacketConn
	rAddr    net.Addr
	metadata *M.Metadata
}

// WriteTo 实现 PacketConn 接口
func (pc *ssPacketConn) WriteTo(b []byte, addr net.Addr) (n int, err error) {
	logger.Debug(fmt.Sprintf("[Shadowsocks UDP] 写入数据到 %v", addr))

	// 构建 SOCKS5 格式的 UDP 数据包
	var packet []byte

	// 如果是 M.Addr 类型，使用元数据构建地址
	if ma, ok := addr.(*M.Addr); ok {
		socksAddr := proxy.SerializeSocksAddr(ma.Metadata())
		packet = make([]byte, len(socksAddr)+len(b))
		copy(packet, socksAddr)
		copy(packet[len(socksAddr):], b)
	} else if udpAddr, ok := addr.(*net.UDPAddr); ok {
		// 将 net.IP 转换为 netip.Addr
		ip, err := netip.ParseAddr(udpAddr.IP.String())
		if err != nil {
			ip = netip.IPv4Unspecified()
		}
		socksAddr := proxy.SerializeSocksAddr(&M.Metadata{
			DstIP:    ip,
			DstPort:  uint16(udpAddr.Port),
			HostName: "",
		})
		packet = make([]byte, len(socksAddr)+len(b))
		copy(packet, socksAddr)
		copy(packet[len(socksAddr):], b)
	} else {
		// 默认情况，直接发送
		packet = b
	}

	// 写入加密后的数据到服务器
	return pc.PacketConn.WriteTo(packet, pc.rAddr)
}

// 修改后的 ReadFrom
func (pc *ssPacketConn) ReadFrom(b []byte) (n int, addr net.Addr, err error) {
	// 1. 读取解密后的完整数据（包含地址头 + 载荷）
	// 注意：这里最好用一个足够大的缓冲区，或者确保 b 足够大
	buf := make([]byte, 65535)
	n, _, err = pc.PacketConn.ReadFrom(buf)
	if err != nil {
		return 0, nil, err
	}

	// 2. 解析 SOCKS5 地址头，确定载荷的起始位置
	// Shadowsocks UDP 地址头格式与 SOCKS5 类似
	// [1-byte type] [variable length host] [2-byte port]
	tx := 0
	if len(buf[:n]) < 1 {
		return 0, nil, errors.New("packet too short")
	}

	addrType := buf[0]
	switch addrType {
	case 0x01: // IPv4
		tx = 1 + 4 + 2
	case 0x03: // Domain
		if len(buf[:n]) < 2 {
			return 0, nil, errors.New("packet too short for domain")
		}
		domainLen := int(buf[1])
		tx = 1 + 1 + domainLen + 2
	case 0x04: // IPv6
		tx = 1 + 16 + 2
	default:
		return 0, nil, fmt.Errorf("unknown address type: %d", addrType)
	}

	if n <= tx {
		return 0, nil, errors.New("packet contains no payload")
	}

	// 3. 将真正的载荷复制到传入的 b 中
	// 注意：这里不仅要剥离头部，还要防止 b 长度不足
	payload := buf[tx:n]
	copy(b, payload)

	logger.Debug(fmt.Sprintf("[Shadowsocks UDP] 读取数据 (剥离头后) %d 字节", len(payload)))

	// 4. 返回地址处理
	// 如果是基于 Session 的 UDP（常见于 Tun 模式），通常返回固定的目标地址即可
	if pc.metadata != nil {
		ip := pc.metadata.DstIP
		port := pc.metadata.DstPort
		return len(payload), &net.UDPAddr{
			IP:   net.ParseIP(ip.String()),
			Port: int(port),
		}, nil
	}

	return len(payload), pc.rAddr, nil
}

// Getter methods for configuration details
// GetMethod returns the encryption method
func (ss *Shadowsocks) GetMethod() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.method
}

// GetPassword returns the password
func (ss *Shadowsocks) GetPassword() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.password
}

// GetUsername returns the username
func (ss *Shadowsocks) GetUsername() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.username
}

// GetObfsMode returns the obfs mode
func (ss *Shadowsocks) GetObfsMode() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.obfsMode
}

// GetObfsHost returns the obfs host
func (ss *Shadowsocks) GetObfsHost() string {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.obfsHost
}
