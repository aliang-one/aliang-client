package cert

import (
	"os"
	"path/filepath"
	"testing"

	"aliang.one/nursorgate/common/cache"
)

func TestActivateAndRollbackMITMCA(t *testing.T) {
	useTemporaryCertificateStore(t)
	certA, keyA, _, _ := generateTestCA(t, "root-a", true)
	certB, keyB, _, _ := generateTestCA(t, "root-b", true)
	materialA, err := ParseMITMCAPEM(certA, keyA)
	if err != nil {
		t.Fatal(err)
	}
	materialB, err := ParseMITMCAPEM(certB, keyB)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ActivateMITMCA(materialA, "generated"); err != nil {
		t.Fatalf("activate first CA: %v", err)
	}
	if CanRollbackMITMCA() {
		t.Fatal("first activation should not have a rollback target")
	}
	if _, err := ActivateMITMCA(materialB, "imported"); err != nil {
		t.Fatalf("activate second CA: %v", err)
	}
	if !CanRollbackMITMCA() {
		t.Fatal("second activation should retain previous CA")
	}
	assertActiveCertificateCN(t, "root-b")

	if _, err := RollbackMITMCA(); err != nil {
		t.Fatalf("rollback CA: %v", err)
	}
	assertActiveCertificateCN(t, "root-a")
	metadata, err := GetMITMCAMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Source != "generated" {
		t.Fatalf("rollback metadata source = %q", metadata.Source)
	}
}

func TestRecoverInterruptedMITMCAActivation(t *testing.T) {
	useTemporaryCertificateStore(t)
	certA, keyA, _, _ := generateTestCA(t, "root-a", true)
	certB, keyB, _, _ := generateTestCA(t, "root-b", true)
	materialA, _ := ParseMITMCAPEM(certA, keyA)
	materialB, _ := ParseMITMCAPEM(certB, keyB)
	if _, err := ActivateMITMCA(materialA, "generated"); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateMITMCA(materialB, "imported"); err != nil {
		t.Fatal(err)
	}
	certDir, _ := GetCertDir()
	certPath, _ := GetCertPath(CertTypeMitmCA)
	if err := os.WriteFile(certPath+".key", []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, mitmTransaction), []byte("pending"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverInterruptedMITMCA(); err != nil {
		t.Fatalf("recover interrupted activation: %v", err)
	}
	assertActiveCertificateCN(t, "root-a")
}

func TestGetCertPathMigratesLegacyPair(t *testing.T) {
	stateDir := t.TempDir()
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", stateDir)
	t.Cleanup(cache.ResetCacheDirForTest)
	certPEM, keyPEM, _, _ := generateTestCA(t, "legacy-root", true)
	if err := os.WriteFile(filepath.Join(stateDir, "mitm-ca.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "mitm-ca.pem.key"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		t.Fatalf("GetCertPath() error = %v", err)
	}
	if filepath.Dir(certPath) != filepath.Join(stateDir, "certs") {
		t.Fatalf("certificate path = %q", certPath)
	}
	assertActiveCertificateCN(t, "legacy-root")
}

func useTemporaryCertificateStore(t *testing.T) {
	t.Helper()
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
}

func assertActiveCertificateCN(t *testing.T, expected string) {
	t.Helper()
	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(certPath + ".key")
	if err != nil {
		t.Fatal(err)
	}
	material, err := ParseMITMCAPEM(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if material.Certificate.Subject.CommonName != expected {
		t.Fatalf("active certificate CN = %q, want %q", material.Certificate.Subject.CommonName, expected)
	}
}
