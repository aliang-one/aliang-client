// Package tls provides TLS/HTTPS certificate management interfaces
package tls

import (
	"net"
)

// CertManager interface for certificate management
type CertManager interface {
	// GenerateCert generates a MITM certificate for the given hostname
	GenerateCert(hostname string) (cert []byte, key []byte, err error)

	// InstallCert installs a certificate to the system trust store
	InstallCert(cert []byte) error

	// UninstallCert removes a certificate from the system trust store
	UninstallCert() error
}

// SNIExtractor extracts SNI (Server Name Indication) from TLS connection
type SNIExtractor interface {
	// ExtractSNI extracts SNI from a TLS connection
	ExtractSNI(conn net.Conn) (sni string, buf []byte, err error)
}
