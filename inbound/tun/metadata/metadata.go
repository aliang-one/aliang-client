package metadata

import (
	"net"
	"net/netip"
	"time"
)

// BindingSource represents the source of domain name binding
type BindingSource string

const (
	BindingSourceSNI     BindingSource = "sni"
	BindingSourceHTTP    BindingSource = "http_host"
	BindingSourceDNS     BindingSource = "dns"
	BindingSourceCONNECT BindingSource = "connect"
)

// Standard TTL values for different binding sources
const (
	DefaultSNITTL     = 5 * time.Minute  // SNI extraction TTL
	DefaultHTTPTTL    = 10 * time.Minute // HTTP Host header TTL
	DefaultCONNECTTTL = 10 * time.Minute // HTTP CONNECT TTL
	DefaultDNSTTL     = 30 * time.Minute // DNS resolution TTL
)

// DNSInfo contains DNS-related binding information
type DNSInfo struct {
	// BindingSource indicates where the domain name comes from
	BindingSource BindingSource `json:"bindingSource"`
	// BindingTime is when the domain was bound to the IP
	BindingTime time.Time `json:"bindingTime"`
	// CacheTTL suggests how long to cache this binding
	CacheTTL time.Duration `json:"cacheTTL"`
	// ShouldCache indicates if this binding should be cached
	ShouldCache bool `json:"shouldCache"`
}

// Metadata contains metadata of transport protocol sessions.
type Metadata struct {
	Network  Network    `json:"network"`
	ConnID   string     `json:"connId,omitempty"`
	SrcIP    netip.Addr `json:"sourceIP"`
	MidIP    netip.Addr `json:"dialerIP"`
	DstIP    netip.Addr `json:"destinationIP"`
	SrcPort  uint16     `json:"sourcePort"`
	MidPort  uint16     `json:"dialerPort"`
	DstPort  uint16     `json:"destinationPort"`
	HostName string     `json:"hostName"`
	AppProto string     `json:"appProto,omitempty"`
	DNSInfo  *DNSInfo   `json:"dnsInfo"`
	Route    string     `json:"route"` // Final routing decision for this connection
}

func (m *Metadata) DestinationAddrPort() netip.AddrPort {
	return netip.AddrPortFrom(m.DstIP, m.DstPort)
}

func (m *Metadata) DestinationAddress() string {
	return m.DestinationAddrPort().String()
}

func (m *Metadata) SourceAddrPort() netip.AddrPort {
	return netip.AddrPortFrom(m.SrcIP, m.SrcPort)
}

func (m *Metadata) SourceAddress() string {
	return m.SourceAddrPort().String()
}

func (m *Metadata) Addr() net.Addr {
	return &Addr{metadata: m}
}

func (m *Metadata) TCPAddr() *net.TCPAddr {
	if m.Network != TCP || !m.DstIP.IsValid() {
		return nil
	}
	return net.TCPAddrFromAddrPort(m.DestinationAddrPort())
}

func (m *Metadata) UDPAddr() *net.UDPAddr {
	if m.Network != UDP || !m.DstIP.IsValid() {
		return nil
	}
	return net.UDPAddrFromAddrPort(m.DestinationAddrPort())
}

// Addr implements the net.Addr interface.
type Addr struct {
	metadata *Metadata
}

func (a *Addr) Metadata() *Metadata {
	return a.metadata
}

func (a *Addr) Network() string {
	return a.metadata.Network.String()
}

func (a *Addr) String() string {
	return a.metadata.DestinationAddress()
}

// SetHostName sets the hostname with its binding source information.
// This method ensures all hostname assignments are properly tracked for caching.
// It automatically creates DNSInfo with the specified source and TTL.
func (m *Metadata) SetHostName(hostname string, source BindingSource, cacheTTL time.Duration) {
	m.HostName = hostname

	// Only set DNSInfo if we have a valid hostname and source
	if hostname != "" && source != "" {
		m.DNSInfo = &DNSInfo{
			BindingSource: source,
			BindingTime:   time.Now(),
			CacheTTL:      cacheTTL,
			ShouldCache:   true,
		}
	}
}

// SetHostNameFromCacheEntry sets hostname from a DNS cache entry.
// It preserves the original binding source information from the cache.
// Parameters are extracted from cache.CacheEntry to avoid circular dependency:
//   - domain: entry.Domain
//   - source: entry.BindingSources[0] (first source if multiple exist)
//   - bindingTime: entry.CreatedAt
//   - cacheTTL: entry.TimeToLive()
func (m *Metadata) SetHostNameFromCacheEntry(domain string, source BindingSource, bindingTime time.Time, cacheTTL time.Duration) {
	if domain == "" {
		return
	}

	m.HostName = domain

	// Use the cached binding source information
	if source != "" {
		m.DNSInfo = &DNSInfo{
			BindingSource: source,
			BindingTime:   bindingTime,
			CacheTTL:      cacheTTL,
			ShouldCache:   true,
		}
	}
}
