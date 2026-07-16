package cert

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

func TestParseMITMCAPEMValidatesPair(t *testing.T) {
	certPEM, keyPEM, _, _ := generateTestCA(t, "custom-root", true)
	material, err := ParseMITMCAPEM(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("ParseMITMCAPEM() error = %v", err)
	}
	if material.Certificate.Subject.CommonName != "custom-root" {
		t.Fatalf("subject CN = %q", material.Certificate.Subject.CommonName)
	}
	if material.KeyAlgorithm != "RSA-2048" || material.Fingerprint == "" {
		t.Fatalf("unexpected material info: %#v", material.ImportInfo())
	}
}

func TestParseMITMCAPEMRejectsMismatchedKey(t *testing.T) {
	certPEM, _, _, _ := generateTestCA(t, "root-a", true)
	_, keyPEM, _, _ := generateTestCA(t, "root-b", true)
	if _, err := ParseMITMCAPEM(certPEM, keyPEM); err == nil {
		t.Fatal("expected mismatched key error")
	}
}

func TestParseMITMCAPEMRejectsNonCA(t *testing.T) {
	certPEM, keyPEM, _, _ := generateTestCA(t, "leaf", false)
	if _, err := ParseMITMCAPEM(certPEM, keyPEM); err == nil {
		t.Fatal("expected non-CA certificate error")
	}
}

func TestParseMITMCAPKCS12(t *testing.T) {
	_, _, certificate, privateKey := generateTestCA(t, "charles-style-root", true)
	bundle, err := pkcs12.Modern.Encode(privateKey, certificate, nil, "secret")
	if err != nil {
		t.Fatalf("encode PKCS#12: %v", err)
	}
	material, err := ParseMITMCAPKCS12(bundle, "secret")
	if err != nil {
		t.Fatalf("ParseMITMCAPKCS12() error = %v", err)
	}
	if material.SourceFormat != "pkcs12" || material.Certificate.Subject.CommonName != "charles-style-root" {
		t.Fatalf("unexpected PKCS#12 material: %#v", material.ImportInfo())
	}
	if _, err := ParseMITMCAPKCS12(bundle, "wrong"); err == nil {
		t.Fatal("expected incorrect password error")
	}
}

func generateTestCA(t *testing.T, commonName string, isCA bool) ([]byte, []byte, *x509.Certificate, *rsa.PrivateKey) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyUsage := x509.KeyUsageDigitalSignature
	if isCA {
		keyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: commonName},
		Issuer:                pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              keyUsage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), certificate, privateKey
}
