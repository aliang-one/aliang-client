//go:build !windows

package installer

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func TestParseCertificateBytesAcceptsPEMAndDER(t *testing.T) {
	pemBytes, rawBytes := mustCreateTestCertificate(t)

	fromPEM, err := parseCertificateBytes(pemBytes)
	if err != nil {
		t.Fatalf("parse PEM certificate returned error: %v", err)
	}
	fromDER, err := parseCertificateBytes(rawBytes)
	if err != nil {
		t.Fatalf("parse DER certificate returned error: %v", err)
	}

	if !bytes.Equal(fromPEM.Raw, rawBytes) {
		t.Fatal("PEM parse returned unexpected raw certificate")
	}
	if !bytes.Equal(fromDER.Raw, rawBytes) {
		t.Fatal("DER parse returned unexpected raw certificate")
	}
}

func TestCertificateBundleContainsFingerprintMatchesPEMBundle(t *testing.T) {
	pemBytes, _ := mustCreateTestCertificate(t)
	fingerprint, err := certificateSHA256Fingerprint(pemBytes)
	if err != nil {
		t.Fatalf("certificateSHA256Fingerprint returned error: %v", err)
	}

	bundle := append([]byte("# generated bundle\n"), pemBytes...)
	if !certificateBundleContainsFingerprint(bundle, fingerprint) {
		t.Fatal("expected PEM bundle to contain certificate fingerprint")
	}
}

func TestCertificateBundleContainsFingerprintDoesNotTreatFingerprintTextAsTrusted(t *testing.T) {
	pemBytes, _ := mustCreateTestCertificate(t)
	fingerprint, err := certificateSHA256Fingerprint(pemBytes)
	if err != nil {
		t.Fatalf("certificateSHA256Fingerprint returned error: %v", err)
	}

	if certificateBundleContainsFingerprint([]byte(fingerprint), fingerprint) {
		t.Fatal("fingerprint text alone must not be treated as a trusted certificate bundle entry")
	}
}

func TestLinuxCertFileNamesKeepsLegacyPEMForRemoval(t *testing.T) {
	names := linuxCertFileNames("mitm-ca")
	if len(names) != 2 || names[0] != "mitm-ca.crt" || names[1] != "mitm-ca.pem" {
		t.Fatalf("unexpected Linux cert filenames: %v", names)
	}
}

func mustCreateTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName:   "aliang-test",
			Organization: []string{"Aliang"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	rawBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: rawBytes,
	})

	return pemBytes, rawBytes
}
