package dns

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	M "aliang.one/nursorgate/inbound/tun/metadata"
)

type tcpDNSDialer struct {
	conn     net.Conn
	metadata *M.Metadata
}

func (d *tcpDNSDialer) DialContext(_ context.Context, metadata *M.Metadata) (net.Conn, error) {
	d.metadata = metadata
	return d.conn, nil
}

func (d *tcpDNSDialer) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, errors.New("unused")
}

func TestResolveThroughDialerUsesDNSOverTCPFramingAndTTL(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		serverErr <- serveFramedDNSAResponse(server, net.IPv4(1, 2, 3, 4), 60)
	}()

	dialer := &tcpDNSDialer{conn: client}
	util := NewDialerResolverUtil(dialer, &DNSConfig{
		PrimaryDNS:   "8.8.8.8:53",
		Timeout:      time.Second,
		MaxTTL:       5 * time.Minute,
		CacheEnabled: true,
	})

	ips, ttl, err := util.ResolveThroughDialer(context.Background(), "example.com", true)
	if err != nil {
		t.Fatalf("ResolveThroughDialer failed: %v", err)
	}
	if len(ips) != 1 || !ips[0].Equal(net.IPv4(1, 2, 3, 4)) {
		t.Fatalf("ips = %v, want [1.2.3.4]", ips)
	}
	if ttl != 60*time.Second {
		t.Fatalf("ttl = %v, want 60s", ttl)
	}
	if dialer.metadata == nil {
		t.Fatal("dialer metadata was not captured")
	}
	if dialer.metadata.Network != M.TCP ||
		dialer.metadata.DstIP != netip.MustParseAddr("8.8.8.8") ||
		dialer.metadata.DstPort != 53 {
		t.Fatalf("metadata = %+v, want TCP 8.8.8.8:53", dialer.metadata)
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server failed: %v", err)
	}
}

func serveFramedDNSAResponse(conn net.Conn, ip net.IP, ttl uint32) error {
	var lengthBuf [2]byte
	if _, err := io.ReadFull(conn, lengthBuf[:]); err != nil {
		return fmt.Errorf("read query length: %w", err)
	}
	queryLen := int(binary.BigEndian.Uint16(lengthBuf[:]))
	if queryLen == 0 {
		return fmt.Errorf("query length is zero")
	}

	query := make([]byte, queryLen)
	if _, err := io.ReadFull(conn, query); err != nil {
		return fmt.Errorf("read query: %w", err)
	}
	if len(query) < 12 {
		return fmt.Errorf("query too short: %d", len(query))
	}

	questionEnd, err := dnsQuestionEnd(query)
	if err != nil {
		return err
	}

	response := make([]byte, 0, questionEnd+16)
	response = append(response, query[0], query[1])
	response = append(response, 0x81, 0x80)
	response = append(response, 0x00, 0x01)
	response = append(response, 0x00, 0x01)
	response = append(response, 0x00, 0x00)
	response = append(response, 0x00, 0x00)
	response = append(response, query[12:questionEnd]...)
	response = append(response, 0xc0, 0x0c)
	response = append(response, 0x00, 0x01)
	response = append(response, 0x00, 0x01)
	var ttlBuf [4]byte
	binary.BigEndian.PutUint32(ttlBuf[:], ttl)
	response = append(response, ttlBuf[:]...)
	response = append(response, 0x00, 0x04)
	response = append(response, ip.To4()...)

	framed := make([]byte, 2+len(response))
	binary.BigEndian.PutUint16(framed[:2], uint16(len(response)))
	copy(framed[2:], response)
	return writeFull(conn, framed)
}

func dnsQuestionEnd(query []byte) (int, error) {
	pos := 12
	for pos < len(query) {
		labelLen := int(query[pos])
		pos++
		if labelLen == 0 {
			if pos+4 > len(query) {
				return 0, fmt.Errorf("query question missing type/class")
			}
			return pos + 4, nil
		}
		if labelLen > 63 {
			return 0, fmt.Errorf("invalid label length %d", labelLen)
		}
		pos += labelLen
	}
	return 0, fmt.Errorf("unterminated query name")
}
