package generator

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
)

// GenerateCertificateFromConfig generates a certificate based on configuration
func GenerateCertificateFromConfig(config *cert_config.CertConfig, exportPath string) error {
	if config == nil {
		return fmt.Errorf("certificate configuration is nil")
	}

	logger.Debug(fmt.Sprintf("Generating certificate for %s (CN=%s)", config.CertType, config.CN))

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, config.KeySize)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   config.CN,
			Organization: []string{config.Organization},
			Country:      []string{config.Country},
		},
		Issuer: pkix.Name{
			CommonName:   config.Issuer,
			Organization: []string{config.Organization},
			Country:      []string{config.Country},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(config.ValidityYears, 0, 0),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		// 扩展用途 (可选，但推荐)
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}

	// Self-sign the certificate
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Ensure export directory exists
	if err := os.MkdirAll(filepath.Dir(exportPath), 0700); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Export certificate
	certFile := exportPath
	certOut, err := os.OpenFile(certFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create certificate file: %w", err)
	}
	defer certOut.Close()

	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		return fmt.Errorf("failed to encode certificate: %w", err)
	}

	// Export private key
	keyFile := exportPath + ".key"
	keyOut, err := os.OpenFile(keyFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyOut.Close()

	privBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to encode private key: %w", err)
	}

	logger.Debug(fmt.Sprintf("Certificate generated successfully: %s", certFile))
	logger.Debug(fmt.Sprintf("Private key saved: %s", keyFile))

	return nil
}

// GenerateSignedCertificate generates a certificate signed by a CA
func GenerateSignedCertificate(caCertPath, caKeyPath string, config *cert_config.CertConfig, exportPath string, hostnames ...string) error {
	if config == nil {
		return fmt.Errorf("certificate configuration is nil")
	}

	logger.Debug(fmt.Sprintf("Generating signed certificate for %s", config.CN))

	// Read CA certificate
	caCertPEM, err := os.ReadFile(caCertPath)
	if err != nil {
		return fmt.Errorf("failed to read CA certificate: %w", err)
	}

	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		return fmt.Errorf("failed to decode CA certificate PEM")
	}

	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate: %w", err)
	}

	// Read CA private key
	caKeyPEM, err := os.ReadFile(caKeyPath)
	if err != nil {
		return fmt.Errorf("failed to read CA private key: %w", err)
	}

	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		return fmt.Errorf("failed to decode CA private key PEM")
	}

	caKey, err := x509.ParsePKCS8PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return fmt.Errorf("failed to parse CA private key: %w", err)
	}

	// Generate private key for the new certificate
	privateKey, err := rsa.GenerateKey(rand.Reader, config.KeySize)
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   config.CN,
			Organization: []string{config.Organization},
			Country:      []string{config.Country},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(config.ValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		DNSNames:              hostnames,
	}

	// Sign the certificate with CA
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, caCert, &privateKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	// Ensure export directory exists
	if err := os.MkdirAll(filepath.Dir(exportPath), 0700); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Export certificate
	certFile := exportPath
	certOut, err := os.OpenFile(certFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("failed to create certificate file: %w", err)
	}
	defer certOut.Close()

	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		return fmt.Errorf("failed to encode certificate: %w", err)
	}

	// Export private key
	keyFile := exportPath + ".key"
	keyOut, err := os.OpenFile(keyFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create key file: %w", err)
	}
	defer keyOut.Close()

	privBytes, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := pem.Encode(keyOut, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		return fmt.Errorf("failed to encode private key: %w", err)
	}

	logger.Debug(fmt.Sprintf("Signed certificate generated: %s", certFile))

	return nil
}

// LoadCertificate loads a certificate from PEM file
func LoadCertificate(certPath, keyPath string) (*tls.Certificate, error) {
	tlsCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load certificate: %w", err)
	}

	if len(tlsCert.Certificate) == 0 {
		return nil, fmt.Errorf("certificate has no chains")
	}

	tlsCert.Leaf, err = x509.ParseCertificate(tlsCert.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return &tlsCert, nil
}

// GetPrivateKey reads and parses a private key from file
func GetPrivateKey(keyPath string) (crypto.PrivateKey, error) {
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key: %w", err)
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("failed to decode private key PEM")
	}

	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	return key, nil
}
