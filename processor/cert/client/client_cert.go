package client

import (
	crand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"

	"golang.org/x/net/http2"
)

// certCache / certAccessTime 是域名证书缓存（按 host 缓存 MITM 签发的叶子证书）。
// CA 重载时由 CAManager.clearCertCache 整体清空（旧 CA 签的叶子对新 CA 无效）。
var certCache = sync.Map{}
var certAccessTime sync.Map // host -> time.Time of last access

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

func buildHostLeafTemplate(host string, caNotAfter time.Time) (x509.Certificate, error) {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := crand.Int(crand.Reader, serialLimit)
	if err != nil {
		return x509.Certificate{}, fmt.Errorf("failed to generate certificate serial number: %w", err)
	}
	notAfter := time.Now().Add(365 * 24 * time.Hour)
	if caNotAfter.Before(notAfter) {
		notAfter = caNotAfter
	}
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: host,
		},
		// Backdate slightly to avoid transient validation failures from small clock skew.
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  false,
		BasicConstraintsValid: true,
	}

	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = append(template.IPAddresses, ip)
		return template, nil
	}

	template.DNSNames = []string{host}
	return template, nil
}

// LoadMitmCACertificate 返回 MITM CA（委托 CAManager.Get，单一入口）。
// 保留导出函数名以兼容现有调用方；实际逻辑全部在 CAManager——文件即真相，
// 内存缓存通过 mtime 校验自动追随文件，CA 重载时联动清空 certCache。
func LoadMitmCACertificate() (*tls.Certificate, error) {
	return DefaultCAManager().Get()
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
	template, err := buildHostLeafTemplate(host, ca.NotAfter)
	if err != nil {
		return tls.Certificate{}, err
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
