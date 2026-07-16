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
	"os"
	"reflect"
	"strings"
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

func TestBuildDarwinPrivilegedCommandUsesDirectSecurityForRoot(t *testing.T) {
	name, args := buildDarwinPrivilegedCommand(true, "add-trusted-cert", "-k", darwinSystemKeychainPath, "/tmp/ca.pem")
	if name != darwinSecurityPath {
		t.Fatalf("command = %q, want %q", name, darwinSecurityPath)
	}
	want := []string{"add-trusted-cert", "-k", darwinSystemKeychainPath, "/tmp/ca.pem"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildDarwinPrivilegedCommandPassesArgumentsOutsideAppleScript(t *testing.T) {
	certificatePath := "/tmp/ca with ' quote.pem"
	name, args := buildDarwinPrivilegedCommand(false, "add-trusted-cert", "-k", darwinSystemKeychainPath, certificatePath)
	if name != darwinOSAScriptPath {
		t.Fatalf("command = %q, want %q", name, darwinOSAScriptPath)
	}
	if len(args) < 4 || args[0] != "-e" || args[2] != "--" {
		t.Fatalf("unexpected osascript args: %#v", args)
	}
	if strings.Contains(args[1], certificatePath) {
		t.Fatal("certificate path must not be interpolated into AppleScript source")
	}
	if args[len(args)-1] != certificatePath {
		t.Fatalf("certificate path argument = %q", args[len(args)-1])
	}
}

func TestDarwinInstallAddsTrustedCertificateAndVerifiesExactFingerprint(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	certPath := filepathForTestCertificate(t, certPEM)
	fingerprint, err := extractCertSHA256Fingerprint(certPEM)
	if err != nil {
		t.Fatal(err)
	}

	originalUID := darwinEffectiveUID
	originalRunner := runDarwinCommand
	t.Cleanup(func() {
		darwinEffectiveUID = originalUID
		runDarwinCommand = originalRunner
	})
	darwinEffectiveUID = func() int { return 0 }

	var calls [][]string
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		switch args[0] {
		case "add-trusted-cert":
			return nil, nil
		case "find-certificate":
			return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
		case "verify-cert":
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %#v", name, args)
			return nil, nil
		}
	}

	installer := &DarwinInstaller{}
	if err := installer.Install("mitm-ca", certPath); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("command count = %d, want 3: %#v", len(calls), calls)
	}
	if calls[0][0] != darwinSecurityPath || calls[0][1] != "add-trusted-cert" {
		t.Fatalf("install command = %#v", calls[0])
	}
	if strings.Contains(strings.Join(calls[0], " "), " open ") {
		t.Fatalf("install must not use open: %#v", calls[0])
	}
}

func TestDarwinRemoveIsIdempotentAndDeletesByFingerprint(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	fingerprint, err := extractCertSHA256Fingerprint(certPEM)
	if err != nil {
		t.Fatal(err)
	}

	originalUID := darwinEffectiveUID
	originalRunner := runDarwinCommand
	t.Cleanup(func() {
		darwinEffectiveUID = originalUID
		runDarwinCommand = originalRunner
	})
	darwinEffectiveUID = func() int { return 0 }

	findCalls := 0
	deleteCalls := 0
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "find-certificate":
			findCalls++
			if findCalls <= 2 {
				return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
			}
			return nil, nil
		case "remove-trusted-cert":
			return nil, nil
		case "delete-certificate":
			deleteCalls++
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "-Z "+fingerprint) || strings.Contains(joined, " -c ") {
				t.Fatalf("delete command must use exact fingerprint: %#v", args)
			}
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %#v", name, args)
			return nil, nil
		}
	}

	installer := &DarwinInstaller{}
	if err := installer.Remove("mitm-ca", certPEM); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete command count = %d, want 1", deleteCalls)
	}

	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		if args[0] != "find-certificate" {
			t.Fatalf("absent certificate removal ran mutating command: %s %#v", name, args)
		}
		return nil, nil
	}
	if err := installer.Remove("mitm-ca", certPEM); err != nil {
		t.Fatalf("idempotent Remove() error = %v", err)
	}
}

func filepathForTestCertificate(t *testing.T, certPEM []byte) string {
	t.Helper()
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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
