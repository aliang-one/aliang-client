package proxy

import (
	"bytes"
	"encoding/binary"
	"net"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

const (
	tcpKeepAlivePeriod = 30 * time.Second
)

// setKeepAlive sets tcp keepalive option for tcp connection.
func SetKeepAlive(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
		tcp.SetKeepAlivePeriod(tcpKeepAlivePeriod)
	}
}

// safeConnClose closes tcp connection safely.
func SafeConnClose(c net.Conn, err error) {
	if c != nil && err != nil {
		c.Close()
	}
}

// serializeSocksAddr 将目的地址序列化为 SOCKS5 地址格式，使用 tun2socks 的 socks5.SerializeAddr
func SerializeSocksAddr(metadata *M.Metadata) []byte {
	buf := bytes.NewBuffer(make([]byte, 0, 300))

	// ---------------------------------------------------------
	// 关键逻辑：优先使用域名 (Type 3) 以避免 DNS 污染
	// ---------------------------------------------------------
	// 条件：HostName 不为空，且 HostName 不是 IP 字符串
	if metadata.HostName != "" && !isIPString(metadata.HostName) {
		// [Type 3: Domain]
		buf.WriteByte(3)

		// SOCKS5 协议限制域名长度最大为 255 字节
		hostLen := len(metadata.HostName)
		if hostLen > 255 {
			// 如果超长，截断处理（虽然极少见）
			buf.WriteByte(255)
			buf.WriteString(metadata.HostName[:255])
		} else {
			buf.WriteByte(byte(hostLen))
			buf.WriteString(metadata.HostName)
		}
	} else {
		// ---------------------------------------------------------
		// 降级逻辑：使用 IP (Type 1 或 Type 4)
		// ---------------------------------------------------------
		// 如果没有域名，或者 HostName 本身就是个 IP，则使用 metadata.DstIP

		if metadata.DstIP.Is4() {
			// [Type 1: IPv4]
			buf.WriteByte(1)
			// netip.Addr 转 [4]byte
			bytes4 := metadata.DstIP.As4()
			buf.Write(bytes4[:])
		} else if metadata.DstIP.Is6() {
			// [Type 4: IPv6]
			buf.WriteByte(4)
			// netip.Addr 转 [16]byte
			bytes16 := metadata.DstIP.As16()
			buf.Write(bytes16[:])
		} else {
			// 异常情况：既没有域名，IP 也是无效的
			// 为了防止协议错乱，发送一个空的 IPv4 (0.0.0.0)
			buf.WriteByte(1)
			buf.Write([]byte{0, 0, 0, 0})
		}
	}

	// ---------------------------------------------------------
	// 写入端口 (Big Endian / 网络字节序)
	// ---------------------------------------------------------
	binary.Write(buf, binary.BigEndian, metadata.DstPort)

	return buf.Bytes()
}

// isIPString 判断字符串是否为合法的 IP 地址 (IPv4 或 IPv6)
func isIPString(host string) bool {
	// net.ParseIP 会尝试解析 IP，如果不是合法的 IP 字符串（例如是域名），则返回 nil
	return net.ParseIP(host) != nil
}
