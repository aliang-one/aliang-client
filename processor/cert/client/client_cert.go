package client

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
	"aliang.one/nursorgate/processor/cert/generator"

	"golang.org/x/net/http2"
)

var defaultCertificate *tls.Certificate
var certCache = sync.Map{}
var certAccessTime sync.Map // host -> time.Time of last access
var mu sync.RWMutex

func init() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			now := time.Now()
			certAccessTime.Range(func(key, value any) bool {
				if t, ok := value.(time.Time); ok && now.Sub(t) > time.Hour {
					certCache.Delete(key)
					certAccessTime.Delete(key)
				}
				return true
			})
		}
	}()
}

func buildHostLeafTemplate(host string) x509.Certificate {
	template := x509.Certificate{
		SerialNumber: big.NewInt(rand.Int63n(1 << 62)),
		Subject: pkix.Name{
			CommonName: host,
		},
		// Backdate slightly to avoid transient validation failures from small clock skew.
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  false,
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
		return template
	}

	template.DNSNames = []string{host}
	return template
}

// LoadMitmCACertificate loads the MITM CA certificate from filesystem
func LoadMitmCACertificate() (*tls.Certificate, error) {
	mu.RLock()
	if defaultCertificate != nil {
		mu.RUnlock()
		return defaultCertificate, nil
	}
	mu.RUnlock()

	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve MITM CA cert path: %w", err)
	}
	keyPath, err := cert_config.GetCertKeyPath(cert_config.CertTypeMitmCA)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve MITM CA key path: %w", err)
	}

	// Check if certificate files exist, if not generate them
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		logger.Warn("MITM CA certificate not found, generating new one")
		config := cert_config.GetCertConfig("mitm-ca")
		if config == nil {
			return nil, fmt.Errorf("MITM CA configuration not found")
		}
		if err := generator.GenerateCertificateFromConfig(config, certPath); err != nil {
			return nil, fmt.Errorf("failed to generate MITM CA certificate: %w", err)
		}
	}

	// Load the certificate
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load MITM CA certificate: %w", err)
	}

	mu.Lock()
	defaultCertificate = &cert
	mu.Unlock()

	return defaultCertificate, nil
}

// creatCertForHost creates a TLS certificate for the specified host signed by the local MITM CA.
func creatCertForHost(host string) (tls.Certificate, error) {
	var err error
	if strings.Contains(host, ":") {
		host, _, err = net.SplitHostPort(host)
		if err != nil {
			return tls.Certificate{}, err
		}
	}

	if cert, ok := certCache.Load(host); ok {
		certAccessTime.Store(host, time.Now())
		return cert.(tls.Certificate), nil
	}

	// Load the MITM CA certificate
	caCert, err := LoadMitmCACertificate()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to load MITM CA certificate: %w", err)
	}

	// Generate private key for the host
	priv, err := rsa.GenerateKey(crand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := buildHostLeafTemplate(host)

	// Parse CA certificate
	// caCert.Certificate[0] is already DER-encoded bytes (not PEM),
	// so we can parse it directly without PEM decoding
	// tls.LoadX509KeyPair returns Certificate field as DER-encoded bytes
	if len(caCert.Certificate) == 0 {
		return tls.Certificate{}, fmt.Errorf("CA certificate has no certificate chain")
	}

	ca, err := x509.ParseCertificate(caCert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("failed to parse CA certificate: %w", err)
	}
	if len(ca.SubjectKeyId) > 0 {
		template.AuthorityKeyId = append([]byte(nil), ca.SubjectKeyId...)
	}

	// Sign the certificate
	derBytes, err := x509.CreateCertificate(crand.Reader, &template, ca, &priv.PublicKey, caCert.PrivateKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Build certificate chain:
	// 1. Server certificate (signed by MITM CA)
	// 2. MITM CA certificate
	//
	// The local client is expected to trust this MITM CA directly via the OS trust store.
	certChain := [][]byte{derBytes, caCert.Certificate[0]}

	cert := tls.Certificate{
		Certificate: certChain,
		PrivateKey:  priv,
	}

	certCache.Store(host, cert)
	certAccessTime.Store(host, time.Now())
	return cert, nil
}

// CreateTlsConfigForHost creates a TLS configuration for the specified host
func CreateTlsConfigForHost(host string) *tls.Config {
	cert, err := creatCertForHost(host)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to create certificate for host %s: %v", host, err))
		return nil
	}

	certs := []tls.Certificate{
		cert,
	}

	return &tls.Config{
		Certificates:       certs,
		NextProtos:         []string{http2.NextProtoTLS, "http/1.1"},
		InsecureSkipVerify: true,
		MaxVersion:         tls.VersionTLS13,
		MinVersion:         tls.VersionTLS12,
	}
}
