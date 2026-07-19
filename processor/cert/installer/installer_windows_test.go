package installer

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cert_config "aliang.one/nursorgate/processor/cert"
)

func TestGetWindowsStoreTargets(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", "")
	t.Setenv("ALIANG_SOCKET_PATH", "")

	rootTargets := getWindowsStoreTargets(cert_config.CertTypeMitmCA)
	if len(rootTargets) != 2 {
		t.Fatalf("expected 2 root targets, got %d", len(rootTargets))
	}
	if rootTargets[0].PSPath != "Cert:\\CurrentUser\\Root" {
		t.Fatalf("unexpected primary root store: %s", rootTargets[0].PSPath)
	}
	if rootTargets[1].PSPath != "Cert:\\LocalMachine\\Root" {
		t.Fatalf("unexpected fallback root store: %s", rootTargets[1].PSPath)
	}

	mtlsTargets := getWindowsStoreTargets(cert_config.CertTypeMtlsClient)
	if len(mtlsTargets) != 2 {
		t.Fatalf("expected 2 mTLS targets, got %d", len(mtlsTargets))
	}
	if mtlsTargets[0].PSPath != "Cert:\\CurrentUser\\My" {
		t.Fatalf("unexpected primary mTLS store: %s", mtlsTargets[0].PSPath)
	}
	if mtlsTargets[1].PSPath != "Cert:\\LocalMachine\\My" {
		t.Fatalf("unexpected fallback mTLS store: %s", mtlsTargets[1].PSPath)
	}
}

func TestGetWindowsStoreTargetsPrefersLocalMachineInDaemonMode(t *testing.T) {
	t.Setenv("ALIANG_DATA_DIR", `C:\ProgramData\Aliang`)
	t.Setenv("ALIANG_SOCKET_PATH", `\\.\pipe\aliang-core`)

	rootTargets := getWindowsStoreTargets(cert_config.CertTypeMitmCA)
	if len(rootTargets) != 1 || rootTargets[0].PSPath != "Cert:\\LocalMachine\\Root" {
		t.Fatalf("unexpected root stores in daemon mode: %#v", rootTargets)
	}

	mtlsTargets := getWindowsStoreTargets(cert_config.CertTypeMtlsClient)
	if len(mtlsTargets) != 1 || mtlsTargets[0].PSPath != "Cert:\\LocalMachine\\My" {
		t.Fatalf("unexpected mTLS stores in daemon mode: %#v", mtlsTargets)
	}
}

func TestExtractCertThumbprint(t *testing.T) {
	pemBytes, rawBytes := mustCreateWindowsTestCertificate(t)

	got, err := extractCertThumbprint(pemBytes)
	if err != nil {
		t.Fatalf("extractCertThumbprint returned error: %v", err)
	}

	sum := sha1.Sum(rawBytes)
	want := strings.ToUpper(hex.EncodeToString(sum[:]))
	if got != want {
		t.Fatalf("unexpected thumbprint: got %s want %s", got, want)
	}
}

func TestEscapePowerShellSingleQuoted(t *testing.T) {
	got := escapePowerShellSingleQuoted("C:\\ProgramData\\Aliang\\it's.pem")
	want := "C:\\ProgramData\\Aliang\\it''s.pem"
	if got != want {
		t.Fatalf("unexpected escaped string: got %q want %q", got, want)
	}
}

func TestWindowsInstallerIsTrustedReturnsFalseForMtls(t *testing.T) {
	installer := &WindowsInstaller{}

	trusted, err := installer.IsTrusted(cert_config.CertTypeMtlsClient, nil)
	if err != nil {
		t.Fatalf("IsTrusted returned error: %v", err)
	}
	if trusted {
		t.Fatal("expected mTLS certificate to not be treated as root-trusted")
	}
}

func TestWindowsInstallRequiresExactCertificatePostcondition(t *testing.T) {
	certPEM, _ := mustCreateWindowsTestCertificate(t)
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreWindowsTestHooks(t)
	runWindowsPowerShell = func(string) ([]byte, error) { return nil, nil }
	runWindowsCommand = func(string, ...string) ([]byte, error) { return nil, nil }

	if err := (&WindowsInstaller{}).Install(cert_config.CertTypeMitmCA, certPath); err == nil {
		t.Fatal("expected install failure when commands succeed without adding the exact certificate")
	}
}

func TestWindowsInstallAcceptsVerifiedFinalStateAfterCommandError(t *testing.T) {
	certPEM, _ := mustCreateWindowsTestCertificate(t)
	certPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	restoreWindowsTestHooks(t)
	runWindowsPowerShell = func(script string) ([]byte, error) {
		if strings.Contains(script, "::ReadOnly") {
			return []byte("FOUND\n"), nil
		}
		return []byte("command reported failure"), errors.New("exit status 1")
	}

	if err := (&WindowsInstaller{}).Install(cert_config.CertTypeMitmCA, certPath); err != nil {
		t.Fatalf("Install() must accept verified Root-store state: %v", err)
	}
}

func TestWindowsRemoveDoesNotSwallowStoreErrors(t *testing.T) {
	certPEM, _ := mustCreateWindowsTestCertificate(t)
	restoreWindowsTestHooks(t)
	runWindowsPowerShell = func(string) ([]byte, error) {
		return []byte("access denied"), errors.New("exit status 1")
	}

	if err := (&WindowsInstaller{}).Remove(cert_config.CertTypeMitmCA, certPEM); err == nil {
		t.Fatal("expected Windows store removal error")
	}
}

func TestWindowsInstalledStatusDoesNotTurnInspectionErrorsIntoNotFound(t *testing.T) {
	certPEM, _ := mustCreateWindowsTestCertificate(t)
	restoreWindowsTestHooks(t)
	runWindowsPowerShell = func(string) ([]byte, error) {
		return []byte("access denied"), errors.New("exit status 1")
	}

	if _, err := (&WindowsInstaller{}).IsInstalled(cert_config.CertTypeMitmCA, certPEM); err == nil {
		t.Fatal("expected Windows store inspection error")
	}
	if status, err := (&WindowsInstaller{}).GetTrustStatus(cert_config.CertTypeMitmCA, certPEM); err == nil || status != "unknown" {
		t.Fatalf("status=%q error=%v, want unknown with error", status, err)
	}
}

func restoreWindowsTestHooks(t *testing.T) {
	t.Helper()
	originalPowerShell := runWindowsPowerShell
	originalCommand := runWindowsCommand
	t.Cleanup(func() {
		runWindowsPowerShell = originalPowerShell
		runWindowsCommand = originalCommand
	})
	t.Setenv("ALIANG_DATA_DIR", "")
	t.Setenv("ALIANG_SOCKET_PATH", "")
}

func mustCreateWindowsTestCertificate(t *testing.T) ([]byte, []byte) {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
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
