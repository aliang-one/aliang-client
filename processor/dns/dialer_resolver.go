package dns

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
)

// DialerResolverUtil 提供通过 dialer 进行 DNS 解析的工具函数
type DialerResolverUtil struct {
	dialer proxy.Dialer
	config *DNSConfig
}

// NewDialerResolverUtil 创建新的 dialer DNS 解析器
func NewDialerResolverUtil(dialer proxy.Dialer, config *DNSConfig) *DialerResolverUtil {
	return &DialerResolverUtil{
		dialer: dialer,
		config: config,
	}
}

// ResolveThroughDialer 通过 dialer 执行 DNS 解析
func (d *DialerResolverUtil) ResolveThroughDialer(ctx context.Context, domain string, preferIPv4 bool) ([]net.IP, time.Duration, error) {
	if d.dialer == nil {
		return nil, 0, fmt.Errorf("dialer not configured")
	}

	// 确保 domain 没有尾部点
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")

	// DNS 服务器地址
	dnsServer := d.config.PrimaryDNS
	if dnsServer == "" {
		dnsServer = "8.8.8.8:53"
	}

	host, port, err := parseDNSServerAddress(dnsServer)
	if err != nil {
		return nil, 0, err
	}

	// 解析 IP 地址
	dnsIP := net.ParseIP(host)
	if dnsIP == nil {
		// 尝试通过系统 DNS 解析 DNS 服务器地址
		addrs, err := net.LookupHost(host)
		if err != nil {
			logger.Error(fmt.Sprintf("Failed to resolve DNS server %s: %v", host, err))
			return nil, 0, fmt.Errorf("failed to resolve DNS server: %w", err)
		}
		if len(addrs) == 0 {
			return nil, 0, fmt.Errorf("DNS server %s resolved to no addresses", host)
		}
		dnsIP = net.ParseIP(addrs[0])
	}

	// 转换为 netip.Addr
	var dstAddr netip.Addr
	if v4 := dnsIP.To4(); v4 != nil {
		dstAddr, _ = netip.ParseAddr(v4.String())
	} else {
		dstAddr, _ = netip.ParseAddr(dnsIP.String())
	}

	// 创建 metadata 用于连接
	metadata := &M.Metadata{
		Network:  M.TCP, // DNS-over-TCP
		DstIP:    dstAddr,
		DstPort:  port,
		HostName: host,
	}

	// 通过 dialer 连接到 DNS 服务器
	conn, err := d.dialer.DialContext(ctx, metadata)
	if err != nil {
		logger.Debug(fmt.Sprintf("Failed to connect to DNS server %s via dialer: %v", dnsServer, err))
		return nil, 0, fmt.Errorf("failed to connect to DNS server: %w", err)
	}
	defer conn.Close()

	// 构建 DNS 查询
	query := d.buildDNSQuery(domain, preferIPv4)

	// 设置写超时
	if err := conn.SetWriteDeadline(time.Now().Add(d.config.Timeout)); err != nil {
		return nil, 0, fmt.Errorf("failed to set write deadline: %w", err)
	}

	// DNS over TCP uses a 2-byte length prefix before the DNS message.
	if len(query) > 65535 {
		return nil, 0, fmt.Errorf("DNS query too large: %d bytes", len(query))
	}
	framedQuery := make([]byte, 2+len(query))
	binary.BigEndian.PutUint16(framedQuery[:2], uint16(len(query)))
	copy(framedQuery[2:], query)

	if err := writeFull(conn, framedQuery); err != nil {
		return nil, 0, fmt.Errorf("failed to send DNS query: %w", err)
	}

	// 设置读超时
	if err := conn.SetReadDeadline(time.Now().Add(d.config.Timeout)); err != nil {
		return nil, 0, fmt.Errorf("failed to set read deadline: %w", err)
	}

	var lengthBuf [2]byte
	if _, err := io.ReadFull(conn, lengthBuf[:]); err != nil {
		return nil, 0, fmt.Errorf("failed to read DNS response length: %w", err)
	}

	responseLen := int(binary.BigEndian.Uint16(lengthBuf[:]))
	if responseLen == 0 {
		return nil, 0, fmt.Errorf("empty DNS response")
	}

	response := make([]byte, responseLen)
	if _, err := io.ReadFull(conn, response); err != nil {
		return nil, 0, fmt.Errorf("failed to read DNS response: %w", err)
	}

	// 解析 DNS 响应
	ips, ttl, err := d.parseDNSResponse(response, preferIPv4)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to parse DNS response: %w", err)
	}

	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no IP addresses found for %s", domain)
	}
	if ttl <= 0 {
		ttl = d.config.MaxTTL
	}

	logger.Debug(fmt.Sprintf("DNS resolved %s to %v via dialer", domain, ips))
	return ips, ttl, nil
}

func parseDNSServerAddress(dnsServer string) (string, uint16, error) {
	dnsServer = strings.TrimSpace(dnsServer)
	if dnsServer == "" {
		return "", 0, fmt.Errorf("DNS server cannot be empty")
	}

	host, portText, err := net.SplitHostPort(dnsServer)
	if err == nil {
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			return "", 0, fmt.Errorf("invalid DNS server port %q: %w", portText, err)
		}
		return strings.Trim(host, "[]"), uint16(port), nil
	}

	if strings.Count(dnsServer, ":") == 1 {
		host, portText, _ := strings.Cut(dnsServer, ":")
		port, err := strconv.ParseUint(portText, 10, 16)
		if err != nil {
			return "", 0, fmt.Errorf("invalid DNS server port %q: %w", portText, err)
		}
		return strings.TrimSpace(host), uint16(port), nil
	}

	return strings.Trim(dnsServer, "[]"), 53, nil
}

func writeFull(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// buildDNSQuery 构建 DNS 查询包
func (d *DialerResolverUtil) buildDNSQuery(domain string, isA bool) []byte {
	buf := make([]byte, 512)
	pos := 0

	// Transaction ID (使用时间戳的低 16 位作为伪随机数)
	txID := uint16(time.Now().UnixNano() & 0xFFFF)
	binary.BigEndian.PutUint16(buf[pos:], txID)
	pos += 2

	// Flags: standard query (0x0100)
	// Bit 0: QR (0 = query)
	// Bit 1-4: Opcode (0 = standard query)
	// Bit 5: AA (0 = not authoritative)
	// Bit 6: TC (0 = not truncated)
	// Bit 7: RD (1 = recursion desired)
	// Bit 8-11: Reserved (0)
	// Bit 12-15: RCODE (0 = no error)
	binary.BigEndian.PutUint16(buf[pos:], 0x0100)
	pos += 2

	// Questions: 1
	binary.BigEndian.PutUint16(buf[pos:], 1)
	pos += 2

	// Answer RRs: 0
	binary.BigEndian.PutUint16(buf[pos:], 0)
	pos += 2

	// Authority RRs: 0
	binary.BigEndian.PutUint16(buf[pos:], 0)
	pos += 2

	// Additional RRs: 0
	binary.BigEndian.PutUint16(buf[pos:], 0)
	pos += 2

	// Question section: domain name
	// 格式：length + label + length + label + ... + 0
	labels := strings.Split(domain, ".")
	for _, label := range labels {
		if label == "" {
			continue
		}
		if len(label) > 63 {
			label = label[:63] // DNS 标签最大 63 字节
		}
		buf[pos] = byte(len(label))
		pos++
		copy(buf[pos:], label)
		pos += len(label)
	}
	buf[pos] = 0 // End of domain name
	pos++

	// QTYPE: A (1) or AAAA (28)
	if isA {
		binary.BigEndian.PutUint16(buf[pos:], 1) // A record
	} else {
		binary.BigEndian.PutUint16(buf[pos:], 28) // AAAA record
	}
	pos += 2

	// QCLASS: IN (1)
	binary.BigEndian.PutUint16(buf[pos:], 1)
	pos += 2

	return buf[:pos]
}

// parseDNSResponse 解析 DNS 响应包
func (d *DialerResolverUtil) parseDNSResponse(response []byte, preferIPv4 bool) ([]net.IP, time.Duration, error) {
	if len(response) < 12 {
		return nil, 0, fmt.Errorf("DNS response too short: %d bytes", len(response))
	}

	// 检查响应代码 (RCODE in flags)
	// 字节 2-3: Flags
	// Bit 0: QR (1 = response)
	// Bit 1-4: Opcode
	// Bit 5: AA
	// Bit 6: TC
	// Bit 7: RD
	// Bit 8: RA
	// Bit 9-11: Reserved
	// Bit 12-15: RCODE (响应代码)
	flags := binary.BigEndian.Uint16(response[2:4])
	rcode := flags & 0x000F
	if rcode != 0 {
		return nil, 0, fmt.Errorf("DNS query failed with rcode %d", rcode)
	}

	// 检查是否是响应
	if flags&0x8000 == 0 {
		return nil, 0, fmt.Errorf("received DNS query instead of response")
	}

	// 字节 6-7: Answer count
	answerCount := binary.BigEndian.Uint16(response[6:8])
	if answerCount == 0 {
		return nil, 0, fmt.Errorf("no answers in DNS response")
	}

	// 跳过 Question section
	pos := 12
	for pos < len(response) && response[pos] != 0 {
		if response[pos] >= 0xC0 {
			// DNS 压缩指针
			pos += 2
			break
		}
		// 标签长度 + 标签
		pos += int(response[pos]) + 1
	}
	if pos < len(response) && response[pos] == 0 {
		pos++ // 跳过域名终止符
	}
	pos += 4 // QTYPE (2 bytes) + QCLASS (2 bytes)

	if pos >= len(response) {
		return nil, 0, fmt.Errorf("malformed DNS response")
	}

	// 解析 Answer section
	var ips []net.IP
	var ttlSeconds uint32
	ttlSet := false
	for i := 0; i < int(answerCount) && pos < len(response); i++ {
		// 跳过 RR Name (可能是压缩指针)
		if pos >= len(response) {
			break
		}
		if response[pos] >= 0xC0 {
			pos += 2 // 压缩指针
		} else {
			for pos < len(response) && response[pos] != 0 {
				pos += int(response[pos]) + 1
			}
			if pos < len(response) {
				pos++ // 跳过终止符
			}
		}

		// 检查是否还有足够的字节用于 Type, Class, TTL, RDLENGTH
		if pos+10 > len(response) {
			break
		}

		// RR Type (2 bytes)
		recordType := binary.BigEndian.Uint16(response[pos:])
		pos += 2

		// RR Class (2 bytes)
		pos += 2

		// TTL (4 bytes)
		recordTTL := binary.BigEndian.Uint32(response[pos:])
		pos += 4

		// RDLENGTH (2 bytes)
		dataLen := binary.BigEndian.Uint16(response[pos:])
		pos += 2

		// 检查是否有足够的字节用于 RDATA
		if pos+int(dataLen) > len(response) {
			break
		}

		// 根据 record type 提取 IP
		if recordType == 1 && dataLen == 4 {
			// A record (IPv4)
			if preferIPv4 || len(ips) == 0 {
				ip := net.IPv4(response[pos], response[pos+1], response[pos+2], response[pos+3])
				ips = append(ips, ip)
				if !ttlSet || recordTTL < ttlSeconds {
					ttlSeconds = recordTTL
					ttlSet = true
				}
			}
		} else if recordType == 28 && dataLen == 16 {
			// AAAA record (IPv6)
			if !preferIPv4 || len(ips) == 0 {
				ip := net.IP(make([]byte, 16))
				copy(ip, response[pos:pos+16])
				ips = append(ips, ip)
				if !ttlSet || recordTTL < ttlSeconds {
					ttlSeconds = recordTTL
					ttlSet = true
				}
			}
		}

		pos += int(dataLen)
	}

	if len(ips) == 0 {
		return nil, 0, fmt.Errorf("no A/AAAA records found in DNS response")
	}

	return ips, time.Duration(ttlSeconds) * time.Second, nil
}
