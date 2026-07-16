package cert

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

const mitmValidationHostname = "mitm-import-check.invalid"

// MITMCAMaterial is a validated, canonical certificate and private-key pair.
type MITMCAMaterial struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
	Certificate    *x509.Certificate
	TLSCertificate tls.Certificate
	SourceFormat   string
	Fingerprint    string
	KeyAlgorithm   string
	Warnings       []string
}

type MITMCAImportInfo struct {
	SourceFormat string   `json:"source_format"`
	Fingerprint  string   `json:"fingerprint"`
	Subject      string   `json:"subject"`
	Issuer       string   `json:"issuer"`
	NotBefore    string   `json:"not_before"`
	NotAfter     string   `json:"not_after"`
	KeyAlgorithm string   `json:"key_algorithm"`
	Warnings     []string `json:"warnings"`
}

func ParseMITMCAPEM(certData, keyData []byte) (*MITMCAMaterial, error) {
	certPEM, err := normalizeCertificatePEM(certData)
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(keyData))) == 0 {
		return nil, fmt.Errorf("private key is empty")
	}
	return validateMITMCAMaterial(certPEM, keyData, "pem")
}

func ParseMITMCAPKCS12(bundle []byte, password string) (*MITMCAMaterial, error) {
	if len(bundle) == 0 {
		return nil, fmt.Errorf("PKCS#12 bundle is empty")
	}
	privateKey, certificate, chain, err := pkcs12.DecodeChain(bundle, password)
	if err != nil {
		return nil, fmt.Errorf("failed to decode PKCS#12 bundle: %w", err)
	}
	if certificate == nil {
		return nil, fmt.Errorf("PKCS#12 bundle has no certificate")
	}
	if len(chain) > 0 {
		return nil, fmt.Errorf("certificate chains are not supported; import a self-signed root CA")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encode PKCS#12 private key: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return validateMITMCAMaterial(certPEM, keyPEM, "pkcs12")
}

func (m *MITMCAMaterial) ImportInfo() MITMCAImportInfo {
	return MITMCAImportInfo{
		SourceFormat: m.SourceFormat,
		Fingerprint:  m.Fingerprint,
		Subject:      m.Certificate.Subject.String(),
		Issuer:       m.Certificate.Issuer.String(),
		NotBefore:    m.Certificate.NotBefore.Format(time.RFC3339),
		NotAfter:     m.Certificate.NotAfter.Format(time.RFC3339),
		KeyAlgorithm: m.KeyAlgorithm,
		Warnings:     append([]string(nil), m.Warnings...),
	}
}

func normalizeCertificatePEM(certData []byte) ([]byte, error) {
	remaining := certData
	var certificateBlocks []*pem.Block
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type == "CERTIFICATE" {
			certificateBlocks = append(certificateBlocks, block)
		}
	}
	if len(certificateBlocks) > 1 {
		return nil, fmt.Errorf("certificate chains are not supported; import a single self-signed root CA")
	}
	if len(certificateBlocks) == 1 {
		if _, err := x509.ParseCertificate(certificateBlocks[0].Bytes); err != nil {
			return nil, fmt.Errorf("failed to parse certificate: %w", err)
		}
		return pem.EncodeToMemory(certificateBlocks[0]), nil
	}

	der := []byte(strings.TrimSpace(string(certData)))
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("failed to decode certificate PEM or DER: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), nil
}

func validateMITMCAMaterial(certPEM, keyPEM []byte, sourceFormat string) (*MITMCAMaterial, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("certificate and private key do not form a valid pair: %w", err)
	}
	if len(pair.Certificate) != 1 {
		return nil, fmt.Errorf("exactly one root certificate is required")
	}
	certificate, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("failed to parse root certificate: %w", err)
	}
	pair.Leaf = certificate

	if !certificate.BasicConstraintsValid || !certificate.IsCA {
		return nil, fmt.Errorf("certificate is not a valid CA certificate")
	}
	if certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, fmt.Errorf("certificate key usage does not permit certificate signing")
	}
	now := time.Now()
	if now.Before(certificate.NotBefore) {
		return nil, fmt.Errorf("certificate is not valid before %s", certificate.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(certificate.NotAfter) {
		return nil, fmt.Errorf("certificate expired at %s", certificate.NotAfter.Format(time.RFC3339))
	}
	if err := certificate.CheckSignatureFrom(certificate); err != nil {
		return nil, fmt.Errorf("certificate must be a self-signed root CA: %w", err)
	}

	keyAlgorithm, err := validateMITMKey(certificate)
	if err != nil {
		return nil, err
	}
	if err := verifyMITMSigning(certificate, pair.PrivateKey); err != nil {
		return nil, err
	}

	fingerprintBytes := sha256.Sum256(certificate.Raw)
	warnings := make([]string, 0, 2)
	if certificate.NotAfter.Sub(now) < 30*24*time.Hour {
		warnings = append(warnings, "expires_soon")
	}
	switch certificate.SignatureAlgorithm {
	case x509.MD2WithRSA, x509.MD5WithRSA, x509.SHA1WithRSA, x509.ECDSAWithSHA1:
		warnings = append(warnings, "weak_signature_algorithm")
	}
	if certificate.PermittedDNSDomainsCritical || len(certificate.PermittedDNSDomains) > 0 || len(certificate.ExcludedDNSDomains) > 0 {
		warnings = append(warnings, "name_constraints_present")
	}

	return &MITMCAMaterial{
		CertificatePEM: append([]byte(nil), certPEM...),
		PrivateKeyPEM:  append([]byte(nil), keyPEM...),
		Certificate:    certificate,
		TLSCertificate: pair,
		SourceFormat:   sourceFormat,
		Fingerprint:    strings.ToUpper(hex.EncodeToString(fingerprintBytes[:])),
		KeyAlgorithm:   keyAlgorithm,
		Warnings:       warnings,
	}, nil
}

func validateMITMKey(certificate *x509.Certificate) (string, error) {
	switch publicKey := certificate.PublicKey.(type) {
	case *rsa.PublicKey:
		bits := publicKey.N.BitLen()
		if bits < 2048 {
			return "", fmt.Errorf("RSA key is too small: %d bits; at least 2048 bits are required", bits)
		}
		return fmt.Sprintf("RSA-%d", bits), nil
	case *ecdsa.PublicKey:
		bits := publicKey.Curve.Params().BitSize
		if bits < 256 {
			return "", fmt.Errorf("ECDSA curve is too small: %d bits", bits)
		}
		return "ECDSA-" + publicKey.Curve.Params().Name, nil
	case ed25519.PublicKey:
		return "Ed25519", nil
	default:
		return "", fmt.Errorf("unsupported CA public key type %T", certificate.PublicKey)
	}
}

func verifyMITMSigning(ca *x509.Certificate, privateKey any) error {
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return fmt.Errorf("failed to create validation serial number: %w", err)
	}
	now := time.Now()
	notAfter := now.Add(time.Hour)
	if ca.NotAfter.Before(notAfter) {
		notAfter = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: mitmValidationHostname},
		DNSNames:     []string{mitmValidationHostname},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return fmt.Errorf("failed to generate validation key: %w", err)
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, template, ca, &leafKey.PublicKey, privateKey)
	if err != nil {
		return fmt.Errorf("private key cannot sign MITM certificates: %w", err)
	}
	leaf, err := x509.ParseCertificate(leafDER)
	if err != nil {
		return fmt.Errorf("failed to parse validation certificate: %w", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: mitmValidationHostname}); err != nil {
		return fmt.Errorf("signed validation certificate could not be verified: %w", err)
	}
	return nil
}
