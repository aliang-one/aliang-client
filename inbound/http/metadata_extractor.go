package http

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
	M "aliang.one/nursorgate/inbound/tun/metadata"
)

// ExtractMetadataFromCONNECT extracts connection metadata from a CONNECT request
func ExtractMetadataFromCONNECT(req *http.Request, conn net.Conn) (*M.Metadata, error) {
	metadata := &M.Metadata{
		Network: M.TCP,
	}

	// Extract target from CONNECT request
	// Format: CONNECT host:port HTTP/1.1
	host, port, err := ParseConnectTarget(req.RequestURI)
	if err != nil {
		return nil, err
	}

	// Set hostname with CONNECT binding source
	if host != "" {
		metadata.SetHostName(host, M.BindingSourceCONNECT, 10*time.Minute)
	}
	metadata.DstPort = port

	// Try to parse host as IP address
	if ip := net.ParseIP(host); ip != nil {
		if addr, err := convertNetIPToNetipAddr(ip); err == nil {
			metadata.DstIP = addr
		}
	} else {
		// Host is a domain name, try to resolve it
		ips, err := net.LookupIP(host)
		if err != nil {
			// DNS resolution failed, but that's OK
			// We'll let the tunnel handler try with hostname
			// and it will do DNS resolution when dialing
			logger.Debug(fmt.Sprintf("DNS resolution failed for %s: %v (will retry at tunnel time)", host, err))
		} else if len(ips) > 0 {
			if addr, err := convertNetIPToNetipAddr(ips[0]); err == nil {
				metadata.DstIP = addr
				logger.Debug(fmt.Sprintf("Resolved %s to %s", host, metadata.DstIP.String()))
			}
		}
	}

	// Extract source from connection
	if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(remoteAddr.IP); err == nil {
			metadata.SrcIP = addr
		}
		metadata.SrcPort = uint16(remoteAddr.Port)
	}

	// Extract local address
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(localAddr.IP); err == nil {
			metadata.MidIP = addr
		}
		metadata.MidPort = uint16(localAddr.Port)
	}

	return metadata, nil
}

// ExtractMetadataFromHTTP extracts metadata from a regular HTTP request
func ExtractMetadataFromHTTP(req *http.Request, conn net.Conn) (*M.Metadata, error) {
	metadata := &M.Metadata{
		Network: M.TCP,
	}

	// Extract host and port from HTTP request
	host := req.Header.Get("Host")
	if host == "" {
		host = req.URL.Host
	}

	if host == "" {
		return nil, fmt.Errorf("cannot determine host from HTTP request")
	}

	// Parse host:port
	var hostOnly string
	var port uint16 = 80 // Default HTTP port

	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		hostOnly = parts[0]
		portNum, err := strconv.ParseUint(parts[1], 10, 16)
		if err == nil {
			port = uint16(portNum)
		}
	} else {
		hostOnly = host
	}

	// Set hostname with HTTP binding source
	if hostOnly != "" {
		metadata.SetHostName(hostOnly, M.BindingSourceHTTP, 10*time.Minute)
	}
	metadata.DstPort = port

	// Try to parse host as IP
	if ip := net.ParseIP(hostOnly); ip != nil {
		if addr, err := convertNetIPToNetipAddr(ip); err == nil {
			metadata.DstIP = addr
		}
	} else {
		// Resolve hostname
		ips, err := net.LookupIP(hostOnly)
		if err != nil {
			return metadata, fmt.Errorf("failed to resolve hostname %s: %w", hostOnly, err)
		}
		if len(ips) > 0 {
			if addr, err := convertNetIPToNetipAddr(ips[0]); err == nil {
				metadata.DstIP = addr
			}
		}
	}

	// Extract source
	if remoteAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(remoteAddr.IP); err == nil {
			metadata.SrcIP = addr
		}
		metadata.SrcPort = uint16(remoteAddr.Port)
	}

	// Extract local address
	if localAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		if addr, err := convertNetIPToNetipAddr(localAddr.IP); err == nil {
			metadata.MidIP = addr
		}
		metadata.MidPort = uint16(localAddr.Port)
	}

	return metadata, nil
}

// ParseConnectTarget parses a CONNECT request target in format "host:port"
func ParseConnectTarget(target string) (string, uint16, error) {
	// CONNECT requests have target in format "host:port"
	target = strings.TrimSpace(target)

	// Find the last colon (to handle IPv6)
	lastColon := strings.LastIndex(target, ":")
	if lastColon == -1 {
		return "", 0, fmt.Errorf("invalid CONNECT target format (missing port): %s", target)
	}

	host := target[:lastColon]
	portStr := target[lastColon+1:]

	// Remove brackets from IPv6 addresses
	host = strings.Trim(host, "[]")

	// Parse port
	portNum, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("invalid port %s: %w", portStr, err)
	}

	if portNum == 0 || portNum > 65535 {
		return "", 0, fmt.Errorf("port out of range: %d", portNum)
	}

	return host, uint16(portNum), nil
}

// ExtractSourceAddress gets the source IP and port from a connection
func ExtractSourceAddress(conn net.Conn) (net.IP, uint16, error) {
	if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return tcpAddr.IP, uint16(tcpAddr.Port), nil
	}
	return nil, 0, fmt.Errorf("cannot extract source from non-TCP connection")
}

// ExtractDestinationAddress gets the destination IP and port from a connection
func ExtractDestinationAddress(conn net.Conn) (net.IP, uint16, error) {
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		return tcpAddr.IP, uint16(tcpAddr.Port), nil
	}
	return nil, 0, fmt.Errorf("cannot extract destination from non-TCP connection")
}
