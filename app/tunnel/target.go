package tunnel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

var (
	private10  = netip.MustParsePrefix("10.0.0.0/8")
	private172 = netip.MustParsePrefix("172.16.0.0/12")
	private192 = netip.MustParsePrefix("192.168.0.0/16")
)

func validateTarget(host string, port int) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return errors.New("target host is required")
	}
	if port < 1 || port > 65535 {
		return errors.New("target port must be between 1 and 65535")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}

	addr, err := netip.ParseAddr(host)
	if err != nil {
		return errors.New("target host must be localhost or an IP literal")
	}
	addr = addr.Unmap()
	if addr.IsLoopback() {
		return nil
	}
	if addr.Is4() && (private10.Contains(addr) || private172.Contains(addr) || private192.Contains(addr)) {
		return nil
	}
	return errors.New("target host is outside the allowed loopback and private IPv4 ranges")
}

func targetAddress(host string, port int) string {
	return net.JoinHostPort(strings.TrimSpace(host), strconv.Itoa(port))
}

func secureDialContext(timeout time.Duration) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, rawPort, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split target address: %w", err)
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			return nil, errors.New("invalid target port")
		}
		if err := validateTarget(host, port); err != nil {
			return nil, err
		}

		// Pin localhost to loopback instead of consulting DNS or the hosts file.
		if strings.EqualFold(host, "localhost") {
			address = net.JoinHostPort("127.0.0.1", rawPort)
		}
		return dialer.DialContext(ctx, network, address)
	}
}
