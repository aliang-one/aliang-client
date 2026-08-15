package socks5

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	"aliang.one/nursorgate/inbound/tun/dialer"
	M "aliang.one/nursorgate/inbound/tun/metadata"
	"aliang.one/nursorgate/outbound/proxy"
	"aliang.one/nursorgate/outbound/proxy/proto"
)

const (
	socksVersion5 = 0x05

	authNone     = 0x00
	authUserPass = 0x02
	authNoAccept = 0xFF

	cmdConnect = 0x01

	atypIPv4   = 0x01
	atypDomain = 0x03
	atypIPv6   = 0x04
)

// Socks5 implements a simple SOCKS5 client (TCP CONNECT only).
type Socks5 struct {
	*proxy.Base

	user string
	pass string
}

// New creates a SOCKS5 proxy instance.
func New(addr, user, pass string) (*Socks5, error) {
	return &Socks5{
		Base: &proxy.Base{
			Address:  addr,
			Protocol: proto.Socks5,
		},
		user: user,
		pass: pass,
	}, nil
}

func (s *Socks5) DialContext(ctx context.Context, metadata *M.Metadata) (c net.Conn, err error) {
	c, err = dialer.DialContext(ctx, "tcp", s.Addr())
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", s.Addr(), err)
	}
	proxy.SetKeepAlive(c)

	defer func() {
		proxy.SafeConnClose(c, err)
	}()

	if err = s.handshake(c); err != nil {
		return nil, err
	}

	if err = s.connect(c, metadata); err != nil {
		return nil, err
	}

	return c, nil
}

func (s *Socks5) DialUDP(*M.Metadata) (net.PacketConn, error) {
	return nil, errors.ErrUnsupported
}

func (s *Socks5) handshake(rw io.ReadWriter) error {
	methods := []byte{authNone}
	if s.user != "" || s.pass != "" {
		methods = []byte{authUserPass}
	}
	req := []byte{socksVersion5, byte(len(methods))}
	req = append(req, methods...)

	if _, err := rw.Write(req); err != nil {
		return fmt.Errorf("socks5 handshake write: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(rw, resp); err != nil {
		return fmt.Errorf("socks5 handshake read: %w", err)
	}
	if resp[0] != socksVersion5 {
		return fmt.Errorf("socks5 unsupported version: %d", resp[0])
	}
	if resp[1] == authNoAccept {
		return errors.New("socks5 no acceptable auth method")
	}
	if resp[1] == authUserPass {
		return s.userPassAuth(rw)
	}
	return nil
}

func (s *Socks5) userPassAuth(rw io.ReadWriter) error {
	if s.user == "" || s.pass == "" {
		return errors.New("socks5 auth required but username/password not provided")
	}
	if len(s.user) > 255 || len(s.pass) > 255 {
		return errors.New("socks5 username/password too long")
	}
	// RFC1929: version 0x01
	buf := make([]byte, 0, 3+len(s.user)+len(s.pass))
	buf = append(buf, 0x01, byte(len(s.user)))
	buf = append(buf, []byte(s.user)...)
	buf = append(buf, byte(len(s.pass)))
	buf = append(buf, []byte(s.pass)...)

	if _, err := rw.Write(buf); err != nil {
		return fmt.Errorf("socks5 auth write: %w", err)
	}

	resp := make([]byte, 2)
	if _, err := io.ReadFull(rw, resp); err != nil {
		return fmt.Errorf("socks5 auth read: %w", err)
	}
	if resp[1] != 0x00 {
		return errors.New("socks5 auth failed")
	}
	return nil
}

func (s *Socks5) connect(rw io.ReadWriter, metadata *M.Metadata) error {
	addr := metadata.HostName
	useDomain := addr != "" && !isIPString(addr)

	buf := make([]byte, 0, 300)
	buf = append(buf, socksVersion5, cmdConnect, 0x00)

	if useDomain {
		if len(addr) > 255 {
			addr = addr[:255]
		}
		buf = append(buf, atypDomain, byte(len(addr)))
		buf = append(buf, []byte(addr)...)
	} else if metadata.DstIP.Is4() {
		buf = append(buf, atypIPv4)
		ip4 := metadata.DstIP.As4()
		buf = append(buf, ip4[:]...)
	} else {
		buf = append(buf, atypIPv6)
		ip6 := metadata.DstIP.As16()
		buf = append(buf, ip6[:]...)
	}

	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, metadata.DstPort)
	buf = append(buf, port...)

	if _, err := rw.Write(buf); err != nil {
		return fmt.Errorf("socks5 connect write: %w", err)
	}

	br := bufio.NewReader(rw)
	// Read reply: VER, REP, RSV, ATYP
	head := make([]byte, 4)
	if _, err := io.ReadFull(br, head); err != nil {
		return fmt.Errorf("socks5 connect read: %w", err)
	}
	if head[0] != socksVersion5 {
		return fmt.Errorf("socks5 connect invalid version: %d", head[0])
	}
	if head[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed, rep=%d", head[1])
	}

	// Consume BND.ADDR and BND.PORT
	switch head[3] {
	case atypIPv4:
		if _, err := io.ReadFull(br, make([]byte, 4+2)); err != nil {
			return fmt.Errorf("socks5 connect read ipv4 bind: %w", err)
		}
	case atypIPv6:
		if _, err := io.ReadFull(br, make([]byte, 16+2)); err != nil {
			return fmt.Errorf("socks5 connect read ipv6 bind: %w", err)
		}
	case atypDomain:
		lenBuf := make([]byte, 1)
		if _, err := io.ReadFull(br, lenBuf); err != nil {
			return fmt.Errorf("socks5 connect read domain len: %w", err)
		}
		if _, err := io.ReadFull(br, make([]byte, int(lenBuf[0])+2)); err != nil {
			return fmt.Errorf("socks5 connect read domain bind: %w", err)
		}
	default:
		return fmt.Errorf("socks5 connect unknown atyp: %d", head[3])
	}

	return nil
}

func isIPString(host string) bool {
	return net.ParseIP(host) != nil
}
