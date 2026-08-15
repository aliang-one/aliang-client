package vless

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/inbound/tun/dialer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
	stls "github.com/sagernet/sing-box/common/tls"
	sopt "github.com/sagernet/sing-box/option"
	vlessSingBox "github.com/sagernet/sing-vmess/vless"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/json/badoption"
	smeta "github.com/sagernet/sing/common/metadata"
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

// VLESS 使用简化的 VLESS 实现，参考 xray-core 设计
type VLESS struct {
	*proxy.Base
	server  string
	port    uint16
	uuid    string
	sni     string
	flow    string
	reality *RealityConfig
	client  *vlessSingBox.Client

	mu sync.RWMutex
}

// RealityConfig REALITY 配置
type RealityConfig struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id"`
}

// VLESSConfig represents VLESS protocol configuration
type VLESSConfig struct {
	Server         string     `json:"server"`
	ServerPort     uint16     `json:"server_port"`
	UUID           string     `json:"uuid"`
	Flow           string     `json:"flow,omitempty"`
	TLSEnabled     bool       `json:"tls_enabled"`
	RealityEnabled bool       `json:"reality_enabled"`
	SNI            string     `json:"sni,omitempty"`
	PublicKey      string     `json:"public_key,omitempty"`
	ShortIDList    []string   `json:"short_id_list,omitempty"`
	TLS            *TLSConfig `json:"tls,omitempty"`
}

// TLSConfig TLS 配置
type TLSConfig struct {
	Enabled    bool           `json:"enabled"`
	ServerName string         `json:"server_name"`
	UTLS       *UTLSConfig    `json:"utls,omitempty"`
	Reality    *RealityConfig `json:"reality,omitempty"`
}

// UTLSConfig uTLS 配置
type UTLSConfig struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

// NewVLESS 创建基础 VLESS 客户端
func NewVLESS(server, uuid string) (*VLESS, error) {
	// 解析服务器地址
	host, port := server, uint16(443)
	if idx := strings.Index(server, ":"); idx != -1 {
		host = server[:idx]
		if p, err := strconv.ParseUint(server[idx+1:], 10, 16); err == nil {
			port = uint16(p)
		}
	}

	return NewVLESSWithConfig(&VLESSConfig{
		Server:     host,
		ServerPort: port,
		UUID:       uuid,
	})
}

// NewVLESSWithTLS 创建带 TLS 的 VLESS 客户端
func NewVLESSWithTLS(server, uuid, sni string) (*VLESS, error) {
	// 解析服务器地址
	host, port := server, uint16(443)
	if idx := strings.Index(server, ":"); idx != -1 {
		host = server[:idx]
		if p, err := strconv.ParseUint(server[idx+1:], 10, 16); err == nil {
			port = uint16(p)
		}
	}

	return NewVLESSWithConfig(&VLESSConfig{
		Server:     host,
		ServerPort: port,
		UUID:       uuid,
		TLS: &TLSConfig{
			Enabled:    true,
			ServerName: sni,
		},
	})
}

// NewVLESSWithVision 创建带 Vision 流的 VLESS 客户端
func NewVLESSWithVision(server, uuid, sni string) (*VLESS, error) {
	// 解析服务器地址
	host, port := server, uint16(443)
	if idx := strings.Index(server, ":"); idx != -1 {
		host = server[:idx]
		if p, err := strconv.ParseUint(server[idx+1:], 10, 16); err == nil {
			port = uint16(p)
		}
	}

	return NewVLESSWithConfig(&VLESSConfig{
		Server:     host,
		ServerPort: port,
		UUID:       uuid,
		Flow:       "xtls-rprx-vision",
		TLS: &TLSConfig{
			Enabled:    true,
			ServerName: sni,
		},
	})
}

// NewVLESSWithReality 创建带 REALITY 的 VLESS 客户端
func NewVLESSWithReality(Server string, ServerPort uint16, UUID string, Flow string, TLSEnabled bool, RealityEnabled bool, SNI string, PublicKey string, ShortIDList []string) (*VLESS, error) {
	// 如果没有提供 shortID，从默认列表随机选择
	if len(ShortIDList) == 0 {
		return nil, fmt.Errorf("shortIDList is empty")
	}
	return NewVLESSWithConfig(&VLESSConfig{
		Server:         Server,
		ServerPort:     uint16(ServerPort),
		UUID:           UUID,
		Flow:           Flow,
		TLSEnabled:     TLSEnabled,
		RealityEnabled: RealityEnabled,
		SNI:            SNI,
		PublicKey:      PublicKey,
		ShortIDList:    ShortIDList,
		TLS: &TLSConfig{
			Enabled:    bool(TLSEnabled),
			ServerName: SNI,
			Reality: &RealityConfig{
				Enabled:   bool(RealityEnabled),
				PublicKey: PublicKey,
				ShortID:   ShortIDList[rand.Intn(len(ShortIDList))],
			},
		},
	})
}

// NewVLESSWithConfig 使用配置创建 VLESS 客户端，参考 xray-core 初始化
func NewVLESSWithConfig(config *VLESSConfig) (*VLESS, error) {

	// 使用 SingBox 兼容的 logger
	singBoxLogger := logger.GetSingBoxLogger()
	client, err := vlessSingBox.NewClient(config.UUID, config.Flow, singBoxLogger)
	if err != nil {
		return nil, fmt.Errorf("failed to create vless client: %w", err)
	}

	v := &VLESS{
		Base: &proxy.Base{
			Address:  fmt.Sprintf("%s:%d", config.Server, config.ServerPort),
			Protocol: proto.VLESS,
		},
		server:  config.Server,
		port:    config.ServerPort,
		uuid:    config.UUID,
		sni:     config.SNI,
		flow:    config.Flow,
		reality: config.TLS.Reality,
		client:  client,
	}

	if config.TLS != nil && config.TLS.Enabled {
		v.sni = config.TLS.ServerName
		if config.TLS.Reality != nil {
			v.reality = config.TLS.Reality
			// 诊断日志：记录初始化时选择的 ShortID
			logger.Info(fmt.Sprintf("[VLESS] 初始化完成 - Server:%s, SNI:%s, ShortID:%s",
				config.Server, v.sni, v.reality.ShortID))
		}
	}
	v.flow = config.Flow

	return v, nil
}

// DialContext 实现 Proxy 接口的 DialContext 方法，参考 xray-core 连接复用机制
func (v *VLESS) DialContext(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	// 使用写锁而不是读锁，防止并发握手导致 REALITY 验证失败
	// 这确保每次只有一个 TLS 握手在进行
	v.mu.Lock()
	defer v.mu.Unlock()

	conn, err := v.establishNewConnection(ctx, metadata)
	if err != nil {
		return nil, err
	}

	return conn, nil
}

// establishNewConnection 建立新连接并准备握手数据，参考 xray-core 连接建立流程
func (v *VLESS) establishNewConnection(ctx context.Context, metadata *M.Metadata) (net.Conn, error) {
	// 连接到 VLESS 服务器，使用默认 dialer
	logger.Debug(fmt.Sprintf("DEBUG: 连接到 VLESS 服务器 %s\n", v.Addr()))
	conn, err := dialer.DialContext(ctx, "tcp", v.Addr())
	if err != nil {
		// 改进错误处理，区分DNS解析错误和连接错误
		if isDNSError(err) {
			return nil, fmt.Errorf("DNS resolution failed for VLESS server %s: %w", v.Addr(), err)
		} else {
			return nil, fmt.Errorf("failed to connect to VLESS server %s: %w", v.Addr(), err)
		}
	}

	// 诊断：验证 TCP 连接真实状态
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		// 检查连接是否可写（发送一个空的 TCP keepalive 探测）
		if err := tcpConn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			logger.Error(fmt.Sprintf("[VLESS] TCP 连接可能无效: SetWriteDeadline 失败: %v", err))
		} else {
			tcpConn.SetWriteDeadline(time.Time{}) // 清除 deadline
		}
		logger.Debug(fmt.Sprintf("[VLESS] TCP 连接已建立 - LocalAddr:%s, RemoteAddr:%s",
			tcpConn.LocalAddr(), tcpConn.RemoteAddr()))
	}

	// 如果启用了 REALITY，进行 REALITY 握手
	if v.reality != nil && v.reality.Enabled {
		logger.Debug(fmt.Sprintf("DEBUG: 开始 REALITY 握手，SNI: %s\n", v.sni))

		// 使用自动生成的 TLS 配置
		defaultOptions := v.GetDefaultTLSOptions()

		// REALITY 握手的关键：
		// 1. TCP 连接已经建立到代理服务器 (v.server:v.port)

		// 诊断日志：Context 状态
		deadline, hasDeadline := ctx.Deadline()
		if hasDeadline {
			logger.Debug(fmt.Sprintf("[VLESS] Context deadline: %v (剩余: %v)", deadline, deadline.Sub(time.Now())))
		} else {
			logger.Debug("[VLESS] Context 无超时限制")
		}

		// 诊断日志：TCP 连接状态
		logger.Debug(fmt.Sprintf("[VLESS] TCP连接 - Local:%s, Remote:%s", conn.LocalAddr(), conn.RemoteAddr()))
		logger.Debug(fmt.Sprintf("[VLESS] 代理服务器: %s:%d, SNI伪装目标: %s", v.server, v.port, v.sni))
		logger.Debug(fmt.Sprintf("[VLESS] REALITY参数 - PublicKey:%s, ShortID:%s", v.reality.PublicKey, v.reality.ShortID))

		// 关键修复：使用 SNI 作为 serverName，而不是代理服务器地址
		logger.Debug("[VLESS] 创建 TLS 客户端配置...")
		tlsConf, err := stls.NewClient(ctx, v.sni, common.PtrValueOrDefault(defaultOptions))
		if err != nil {
			logger.Error(fmt.Sprintf("[VLESS] 创建TLS配置失败: %v", err))
			return nil, fmt.Errorf("failed to create TLS client: %w", err)
		}
		logger.Debug("[VLESS] TLS 配置创建成功")

		// 开始 TLS 握手
		logger.Debug(fmt.Sprintf("[VLESS] 开始TLS握手 - 目标:%s:%d, SNI:%s", v.server, v.port, v.sni))
		startTime := time.Now()
		tlsConn, err := stls.ClientHandshake(ctx, conn, tlsConf)
		handshakeDuration := time.Since(startTime)

		if err != nil {
			logger.Error(fmt.Sprintf("[VLESS] ❌ TLS握手失败 (耗时:%v)", handshakeDuration))
			logger.Error(fmt.Sprintf("[VLESS]    错误类型: %T", err))
			logger.Error(fmt.Sprintf("[VLESS]    错误详情: %v", err))
			logger.Error(fmt.Sprintf("[VLESS]    Server:%s:%d, SNI:%s", v.server, v.port, v.sni))
			logger.Error(fmt.Sprintf("[VLESS]    PublicKey:%s, ShortID:%s", v.reality.PublicKey, v.reality.ShortID))
			return nil, fmt.Errorf("failed to handshake with VLESS server: %w", err)
		}
		logger.Debug(fmt.Sprintf("[VLESS] ✓ TLS握手成功 (耗时:%v) - 目标: %s:%d", handshakeDuration, metadata.HostName, metadata.DstPort))

		// 创建目标地址
		// 重要：如果有域名，优先使用域名而不是IP，这样目标服务器才能正确处理TLS SNI
		destination := smeta.Socksaddr{
			Port: metadata.DstPort,
		}

		// 优先使用 Fqdn（域名）
		if metadata.HostName != "" {
			destination.Fqdn = metadata.HostName
			logger.Debug(fmt.Sprintf("DEBUG: VLESS 目标（使用域名）: %s:%d\n", metadata.HostName, metadata.DstPort))
		} else {
			destination.Addr = metadata.DstIP
			logger.Debug(fmt.Sprintf("DEBUG: VLESS 目标（使用IP）: %s:%d\n", metadata.DstIP, metadata.DstPort))
		}

		visionConn, err := v.client.DialEarlyConn(tlsConn, destination)
		if err != nil {
			// 关键诊断：记录错误的具体类型
			errMsg := fmt.Sprintf("[VLESS] ❌ Early Dial失败 目标: %s:%d, 错误: %v", metadata.HostName, metadata.DstPort, err)

			if err == io.EOF {
				errMsg += " [EOF - 可能原因: 服务器不支持0-RTT或握手失败]"
			} else if err.Error() == "context canceled" {
				errMsg += " [Context取消]"
			}

			logger.Error(errMsg)
			return nil, fmt.Errorf("failed to dial early connection: %w", err)
		}

		logger.Debug(fmt.Sprintf("[VLESS] ✓ Early Dial成功 目标: %s:%d, 连接类型: %T", metadata.HostName, metadata.DstPort, visionConn))
		return visionConn, nil
	}

	// 如果启用了 TLS，进行 TLS 握手
	if v.sni != "" {
		tlsConfig := &tls.Config{
			ServerName:         v.sni,
			InsecureSkipVerify: false,
			MinVersion:         tls.VersionTLS12,
			MaxVersion:         tls.VersionTLS13,
		}
		conn = tls.Client(conn, tlsConfig)
	}

	logger.Debug("连接建立成功")
	return conn, nil
}

// DialUDP 实现 Proxy 接口的 DialUDP 方法
func (v *VLESS) DialUDP(metadata *M.Metadata) (net.PacketConn, error) {
	// VLESS 的 UDP 支持需要先建立 TCP 连接
	// 这里返回一个错误，表示不支持 UDP
	return nil, fmt.Errorf("VLESS UDP not supported")
}

// GetDefaultTLSOptions 从 VLESS 配置生成 Sing-box 的 OutboundTLSOptions
// 包含完整的 TLS、REALITY、UTLS 等配置
func (v *VLESS) GetDefaultTLSOptions() *sopt.OutboundTLSOptions {
	// 如果没有 SNI，返回 nil（表示不使用 TLS）
	if v.sni == "" {
		return nil
	}

	opts := &sopt.OutboundTLSOptions{
		Enabled:    true,
		ServerName: v.sni,
		Insecure:   false, // REALITY 必须跳过证书验证（服务器返回自签名证书）
	}

	// 配置 TLS 版本
	// Vision 模式推荐使用 TLS 1.3
	if v.flow == "xtls-rprx-vision" {
		opts.MinVersion = "1.3"
		opts.MaxVersion = "1.3"
	} else {
		opts.MinVersion = "1.2"
		opts.MaxVersion = "1.3"
	}

	// 配置 ALPN（应用层协议协商）
	// HTTP/2 和 HTTP/1.1 是常用的协议
	opts.ALPN = badoption.Listable[string]([]string{"h2", "http/1.1"})

	// 如果启用了 REALITY，配置 REALITY 选项
	if v.reality != nil && v.reality.Enabled {
		opts.Reality = &sopt.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: v.reality.PublicKey,
			ShortID:   v.reality.ShortID,
		}

		// REALITY 使用 uTLS 来模拟真实浏览器指纹
		// 使用 firefox 指纹，比 chrome 更稳定（chrome 指纹有随机性可能导致验证失败）
		opts.UTLS = &sopt.OutboundUTLSOptions{
			Enabled:     true,
			Fingerprint: "firefox", // 使用 Firefox 指纹，更稳定
		}
	}

	// 推荐的密码套件（优先使用更安全的）
	opts.CipherSuites = badoption.Listable[string]([]string{
		"TLS_AES_128_GCM_SHA256",
		"TLS_AES_256_GCM_SHA384",
		"TLS_CHACHA20_POLY1305_SHA256",
		"TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256",
		"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
	})

	return opts
}

// GetSimpleTLSOptions 获取简化的 TLS 配置（用于快速配置）
func (v *VLESS) GetSimpleTLSOptions() *sopt.OutboundTLSOptions {
	if v.sni == "" {
		return nil
	}

	opts := &sopt.OutboundTLSOptions{
		Enabled:    true,
		ServerName: v.sni,
		Insecure:   true,
		MinVersion: "1.2",
		MaxVersion: "1.3",
	}

	// 如果有 REALITY 配置
	if v.reality != nil && v.reality.Enabled {
		opts.Reality = &sopt.OutboundRealityOptions{
			Enabled:   true,
			PublicKey: v.reality.PublicKey,
			ShortID:   v.reality.ShortID,
		}
	}

	return opts
}

// GetCustomTLSOptions 获取自定义的 TLS 配置
// 允许覆盖特定字段
func (v *VLESS) GetCustomTLSOptions(customize func(*sopt.OutboundTLSOptions)) *sopt.OutboundTLSOptions {
	opts := v.GetDefaultTLSOptions()
	if opts != nil && customize != nil {
		customize(opts)
	}
	return opts
}

// Getter methods for configuration details
// GetServer returns the server address
func (v *VLESS) GetServer() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.server
}

// GetUUID returns the UUID
func (v *VLESS) GetUUID() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.uuid
}

// GetSNI returns the SNI
func (v *VLESS) GetSNI() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.sni
}

// GetFlow returns the flow
func (v *VLESS) GetFlow() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.flow
}

// GetRealityConfig returns the REALITY configuration
func (v *VLESS) GetRealityConfig() *RealityConfig {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.reality
}
