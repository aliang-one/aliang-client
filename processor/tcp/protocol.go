package tcp

// DefaultProtocolDetector implements the ProtocolDetector interface.
// It determines connection protocol based on destination port.
type DefaultProtocolDetector struct{}

// NewDefaultProtocolDetector creates a new protocol detector.
func NewDefaultProtocolDetector() *DefaultProtocolDetector {
	return &DefaultProtocolDetector{}
}

// Detect returns the protocol type based on destination port.
// - Port 443: ProtocolTLS (HTTPS with SNI extraction and possible MITM)
// - Port 80: ProtocolHTTP (plain HTTP, may need special handling)
// - Others: ProtocolDirect (pass-through without inspection)
func (d *DefaultProtocolDetector) Detect(port uint16) Protocol {
	switch port {
	case PortHTTPS: // 443
		return ProtocolTLS
	case PortHTTP: // 80
		return ProtocolHTTP
	default:
		return ProtocolDirect
	}
}

// IsProtocolTLS returns true if protocol is TLS
func IsProtocolTLS(proto Protocol) bool {
	return proto == ProtocolTLS
}

// IsProtocolHTTP returns true if protocol is HTTP
func IsProtocolHTTP(proto Protocol) bool {
	return proto == ProtocolHTTP
}

// IsProtocolDirect returns true if protocol is Direct
func IsProtocolDirect(proto Protocol) bool {
	return proto == ProtocolDirect
}

// String returns human-readable protocol name
func (p Protocol) String() string {
	switch p {
	case ProtocolTLS:
		return "TLS"
	case ProtocolHTTP:
		return "HTTP"
	case ProtocolDirect:
		return "Direct"
	default:
		return "Unknown"
	}
}

// PortRequiresSNI returns true if port typically requires SNI extraction
func PortRequiresSNI(port uint16) bool {
	// Only port 443 (HTTPS) has SNI
	return port == PortHTTPS
}

// PortIsWebTraffic returns true if port is web traffic (HTTP/HTTPS)
func PortIsWebTraffic(port uint16) bool {
	return port == PortHTTP || port == PortHTTPS
}
