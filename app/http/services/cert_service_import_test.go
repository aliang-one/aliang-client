package services

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"aliang.one/nursorgate/common/cache"
	cert_config "aliang.one/nursorgate/processor/cert"
	client_cert "aliang.one/nursorgate/processor/cert/client"
	cert_installer "aliang.one/nursorgate/processor/cert/installer"
)

type importTestInstaller struct{}

func (importTestInstaller) IsInstalled(string, []byte) (bool, error) { return true, nil }
func (importTestInstaller) Install(string, string) error             { return nil }
func (importTestInstaller) Remove(string, []byte) error              { return nil }
func (importTestInstaller) GetCertInfo(string, []byte) (cert_installer.CertInfo, error) {
	return cert_installer.CertInfo{}, nil
}

type transactionTestInstaller struct {
	trusted           map[string]bool
	failInstall       bool
	failRemoveForCert string
}

func newTransactionTestInstaller() *transactionTestInstaller {
	return &transactionTestInstaller{trusted: make(map[string]bool)}
}

func (i *transactionTestInstaller) IsInstalled(_ string, certBytes []byte) (bool, error) {
	fingerprint, err := x509CertificateFingerprint(certBytes)
	if err != nil {
		return false, err
	}
	_, installed := i.trusted[fingerprint]
	return installed, nil
}

func (i *transactionTestInstaller) Install(_ string, certPath string) error {
	if i.failInstall {
		return fmt.Errorf("injected install failure")
	}
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	fingerprint, err := x509CertificateFingerprint(certBytes)
	if err != nil {
		return err
	}
	i.trusted[fingerprint] = true
	return nil
}

func (i *transactionTestInstaller) Remove(_ string, certBytes []byte) error {
	fingerprint, err := x509CertificateFingerprint(certBytes)
	if err != nil {
		return err
	}
	if fingerprint == i.failRemoveForCert {
		return fmt.Errorf("injected remove failure")
	}
	delete(i.trusted, fingerprint)
	return nil
}

func (i *transactionTestInstaller) GetCertInfo(string, []byte) (cert_installer.CertInfo, error) {
	return cert_installer.CertInfo{}, nil
}

func (i *transactionTestInstaller) GetInstallPath(string) string { return "system-test" }

func (i *transactionTestInstaller) IsTrusted(_ string, certBytes []byte) (bool, error) {
	fingerprint, err := x509CertificateFingerprint(certBytes)
	if err != nil {
		return false, err
	}
	return i.trusted[fingerprint], nil
}

func (i *transactionTestInstaller) GetTrustStatus(_ string, certBytes []byte) (string, error) {
	trusted, err := i.IsTrusted("", certBytes)
	if err != nil {
		return "unknown", err
	}
	if trusted {
		return "system_trusted", nil
	}
	return "not_found", nil
}
func (importTestInstaller) GetInstallPath(string) string           { return "" }
func (importTestInstaller) IsTrusted(string, []byte) (bool, error) { return true, nil }
func (importTestInstaller) GetTrustStatus(string, []byte) (string, error) {
	return "system_trusted", nil
}

func TestCertServiceImportAndRollback(t *testing.T) {
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	service := &CertService{installer: importTestInstaller{}}

	if _, err := service.RegenerateCert(cert_config.CertTypeMitmCA); err != nil {
		t.Fatalf("RegenerateCert() error = %v", err)
	}
	certPEM, keyPEM := generateImportTestCA(t, "custom-import-root")
	validated, err := service.ValidateMITMCAImport(certPEM, keyPEM, nil, "")
	if err != nil {
		t.Fatalf("ValidateMITMCAImport() error = %v", err)
	}
	if !validated.IsInstalled || !validated.IsTrusted {
		t.Fatalf("validated trust status = %#v", validated)
	}
	imported, err := service.ImportMITMCA(certPEM, keyPEM, nil, "")
	if err != nil {
		t.Fatalf("ImportMITMCA() error = %v", err)
	}
	if imported.Source != "imported" || !imported.CanRollback {
		t.Fatalf("import result = %#v", imported)
	}
	status, err := service.GetCertStatus(cert_config.CertTypeMitmCA)
	if err != nil {
		t.Fatalf("GetCertStatus() error = %v", err)
	}
	if status.Source != "imported" || status.Subject == "" || !status.CanRollback {
		t.Fatalf("status after import = %#v", status)
	}

	rolledBack, err := service.RollbackMITMCA()
	if err != nil {
		t.Fatalf("RollbackMITMCA() error = %v", err)
	}
	if rolledBack.Source != "generated" {
		t.Fatalf("rollback source = %q, want generated", rolledBack.Source)
	}
}

func TestCertServiceRejectsMismatchedImportWithoutChangingActiveCA(t *testing.T) {
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	service := &CertService{installer: importTestInstaller{}}
	if _, err := service.RegenerateCert(cert_config.CertTypeMitmCA); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(mustCertPath(t))
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _ := generateImportTestCA(t, "root-a")
	_, keyPEM := generateImportTestCA(t, "root-b")
	if _, err := service.ImportMITMCA(certPEM, keyPEM, nil, ""); err == nil {
		t.Fatal("expected mismatched key error")
	}
	after, err := os.ReadFile(mustCertPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("active certificate changed after rejected import")
	}
}

func TestImportInstallFailureKeepsPreviousCAActive(t *testing.T) {
	prepareActiveTestCA(t, "previous-root")
	installer := newTransactionTestInstaller()
	oldCert, err := os.ReadFile(mustCertPath(t))
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint, err := x509CertificateFingerprint(oldCert)
	if err != nil {
		t.Fatal(err)
	}
	installer.trusted[oldFingerprint] = true
	installer.failInstall = true

	service := NewCertServiceWithInstaller(installer)
	newCert, newKey := generateImportTestCA(t, "replacement-root")
	if _, err := service.ImportMITMCA(newCert, newKey, nil, ""); err == nil {
		t.Fatal("expected injected installation failure")
	}
	assertServiceActiveCertificateCN(t, "previous-root")
}

func TestImportRemoveFailureRollsBackActiveCAAndTrust(t *testing.T) {
	prepareActiveTestCA(t, "previous-root")
	installer := newTransactionTestInstaller()
	oldCert, err := os.ReadFile(mustCertPath(t))
	if err != nil {
		t.Fatal(err)
	}
	oldFingerprint, err := x509CertificateFingerprint(oldCert)
	if err != nil {
		t.Fatal(err)
	}
	installer.trusted[oldFingerprint] = true
	installer.failRemoveForCert = oldFingerprint

	service := NewCertServiceWithInstaller(installer)
	newCert, newKey := generateImportTestCA(t, "replacement-root")
	newFingerprint, err := x509CertificateFingerprint(newCert)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportMITMCA(newCert, newKey, nil, ""); err == nil {
		t.Fatal("expected injected old-certificate removal failure")
	}
	assertServiceActiveCertificateCN(t, "previous-root")
	if !installer.trusted[oldFingerprint] {
		t.Fatal("previous CA trust was not preserved after rollback")
	}
	if installer.trusted[newFingerprint] {
		t.Fatal("new CA trust was not cleaned up after rollback")
	}
}

func TestManagedSystemServiceCopiesPrivateCertificateStore(t *testing.T) {
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	service := &CertService{installer: importTestInstaller{}}
	if _, err := service.RegenerateCert(cert_config.CertTypeMitmCA); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := ensureManagedCertificateAssets("", target); err != nil {
		t.Fatalf("ensureManagedCertificateAssets() error = %v", err)
	}
	certPath := filepath.Join(target, "certs", "mitm-ca.pem")
	keyPath := certPath + ".key"
	if _, err := os.Stat(certPath); err != nil {
		t.Fatalf("managed certificate missing: %v", err)
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("managed private key missing: %v", err)
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("managed private key mode = %o, want 600", keyInfo.Mode().Perm())
	}
}

func generateImportTestCA(t *testing.T, commonName string) ([]byte, []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName}, Issuer: pkix.Name{CommonName: commonName},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		BasicConstraintsValid: true, IsCA: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func mustCertPath(t *testing.T) string {
	t.Helper()
	path, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func prepareActiveTestCA(t *testing.T, commonName string) {
	t.Helper()
	cache.ResetCacheDirForTest()
	t.Setenv("ALIANG_CACHE_DIR", t.TempDir())
	t.Cleanup(cache.ResetCacheDirForTest)
	certPEM, keyPEM := generateImportTestCA(t, commonName)
	material, err := cert_config.ParseMITMCAPEM(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cert_config.ActivateMITMCA(material, "generated"); err != nil {
		t.Fatal(err)
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		t.Fatal(err)
	}
}

func assertServiceActiveCertificateCN(t *testing.T, expected string) {
	t.Helper()
	certPEM, err := os.ReadFile(mustCertPath(t))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("active certificate is not PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Subject.CommonName != expected {
		t.Fatalf("active certificate CN = %q, want %q", parsed.Subject.CommonName, expected)
	}
}
