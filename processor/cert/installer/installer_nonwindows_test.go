//go:build !windows

package installer

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
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

func TestLinuxInstallRequiresVerifiedSystemTrustAndRestoresPreviousAnchor(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	certPath := filepathForTestCertificate(t, certPEM)
	root := t.TempDir()
	anchorDir := filepath.Join(root, "anchors")
	bundlePath := filepath.Join(root, "ca-bundle.pem")
	targetPath := filepath.Join(anchorDir, linuxPrimaryCertFileName("mitm-ca"))
	previous := []byte("previous anchor contents")
	if err := os.MkdirAll(anchorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundlePath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	restoreLinuxTestHooks(t, []linuxTrustAnchor{{Dir: anchorDir, UpdateCommand: []string{"true"}}}, []string{bundlePath})

	if err := (&LinuxInstaller{}).Install("mitm-ca", certPath); err == nil {
		t.Fatal("expected installation to fail when trust bundle does not contain the certificate")
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, previous) {
		t.Fatalf("previous anchor was not restored: got %q", got)
	}
	userFallback := filepath.Join(homeDir, ".local", "share", "ca-certificates", "custom", linuxPrimaryCertFileName("mitm-ca"))
	if _, err := os.Stat(userFallback); err == nil {
		t.Fatalf("unexpected untrusted user fallback at %s", userFallback)
	}
}

func TestLinuxInstallReturnsSuccessOnlyAfterBundleContainsFingerprint(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	certPath := filepathForTestCertificate(t, certPEM)
	root := t.TempDir()
	anchorDir := filepath.Join(root, "anchors")
	bundlePath := filepath.Join(root, "ca-bundle.pem")
	updateScript := filepath.Join(root, "refresh-ca")
	script := "#!/bin/sh\ncp \"$1\" \"$2\"\n"
	if err := os.WriteFile(updateScript, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(anchorDir, linuxPrimaryCertFileName("mitm-ca"))
	restoreLinuxTestHooks(t, []linuxTrustAnchor{{Dir: anchorDir, UpdateCommand: []string{updateScript, targetPath, bundlePath}}}, []string{bundlePath})

	if err := (&LinuxInstaller{}).Install("mitm-ca", certPath); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	trusted, err := (&LinuxInstaller{}).IsTrusted("mitm-ca", certPEM)
	if err != nil || !trusted {
		t.Fatalf("trusted=%v error=%v", trusted, err)
	}
}

func TestLinuxRemoveReturnsTrustRefreshError(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	root := t.TempDir()
	anchorDir := filepath.Join(root, "anchors")
	targetPath := filepath.Join(anchorDir, linuxPrimaryCertFileName("mitm-ca"))
	if err := os.MkdirAll(anchorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	restoreLinuxTestHooks(t, []linuxTrustAnchor{{Dir: anchorDir, UpdateCommand: []string{"true"}}}, nil)
	originalRunner := runLinuxPrivilegedCommand
	runLinuxPrivilegedCommand = func(name string, args ...string) ([]byte, error) {
		return []byte("injected refresh failure"), errors.New("exit status 1")
	}
	t.Cleanup(func() { runLinuxPrivilegedCommand = originalRunner })

	if err := (&LinuxInstaller{}).Remove("mitm-ca", certPEM); err == nil {
		t.Fatal("expected trust refresh failure")
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("removed anchor was not restored: %v", err)
	}
	if !bytes.Equal(got, certPEM) {
		t.Fatal("restored anchor content does not match the original certificate")
	}
}

func TestLinuxRemovePreservesDifferentCertificateAtKnownPath(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	otherCertPEM, _ := mustCreateTestCertificate(t)
	root := t.TempDir()
	anchorDir := filepath.Join(root, "anchors")
	targetPath := filepath.Join(anchorDir, linuxPrimaryCertFileName("mitm-ca"))
	if err := os.MkdirAll(anchorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetPath, otherCertPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	restoreLinuxTestHooks(t, []linuxTrustAnchor{{Dir: anchorDir, UpdateCommand: []string{"true"}}}, nil)

	if err := (&LinuxInstaller{}).Remove("mitm-ca", certPEM); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, otherCertPEM) {
		t.Fatal("different certificate at the known path was modified")
	}
}

func restoreLinuxTestHooks(t *testing.T, anchors []linuxTrustAnchor, bundles []string) {
	t.Helper()
	originalAnchors := linuxTrustAnchors
	originalBundles := linuxCABundlePaths
	originalLegacyDir := linuxLegacyCertDir
	linuxTrustAnchors = anchors
	linuxCABundlePaths = bundles
	linuxLegacyCertDir = filepath.Join(t.TempDir(), "legacy")
	t.Cleanup(func() {
		linuxTrustAnchors = originalAnchors
		linuxCABundlePaths = originalBundles
		linuxLegacyCertDir = originalLegacyDir
	})
}

func TestBuildDarwinPrivilegedCommandUsesConsoleAuditSessionForRoot(t *testing.T) {
	name, args, err := buildDarwinPrivilegedCommand(true, "502", "add-trusted-cert", "-k", darwinSystemKeychainPath, "/tmp/ca.pem")
	if err != nil {
		t.Fatal(err)
	}
	if name != darwinLaunchctlPath {
		t.Fatalf("command = %q, want %q", name, darwinLaunchctlPath)
	}
	want := []string{"asuser", "502", darwinSecurityPath, "add-trusted-cert", "-k", darwinSystemKeychainPath, "/tmp/ca.pem"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildDarwinPrivilegedCommandRejectsRootWithoutConsoleUser(t *testing.T) {
	if _, _, err := buildDarwinPrivilegedCommand(true, "0", "add-trusted-cert"); err == nil {
		t.Fatal("expected missing console user error")
	}
}

func TestBuildDarwinPrivilegedCommandPassesArgumentsOutsideAppleScript(t *testing.T) {
	certificatePath := "/tmp/ca with ' quote.pem"
	name, args, err := buildDarwinPrivilegedCommand(false, "", "add-trusted-cert", "-k", darwinSystemKeychainPath, certificatePath)
	if err != nil {
		t.Fatal(err)
	}
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
	originalConsoleUID := darwinConsoleUserUID
	originalRunner := runDarwinCommand
	t.Cleanup(func() {
		darwinEffectiveUID = originalUID
		darwinConsoleUserUID = originalConsoleUID
		runDarwinCommand = originalRunner
	})
	darwinEffectiveUID = func() int { return 0 }
	darwinConsoleUserUID = func() (string, error) { return "502", nil }

	var calls [][]string
	installed := false
	trusted := false
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		commandArgs := args
		if name == darwinLaunchctlPath {
			commandArgs = args[3:]
		}
		switch commandArgs[0] {
		case "add-trusted-cert":
			installed = true
			trusted = true
			return nil, nil
		case "find-certificate":
			if installed {
				return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
			}
			return nil, nil
		case "verify-cert":
			if trusted {
				return nil, nil
			}
			return nil, mustExitError(t)
		default:
			t.Fatalf("unexpected command: %s %#v", name, args)
			return nil, nil
		}
	}

	installer := &DarwinInstaller{}
	if err := installer.Install("mitm-ca", certPath); err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if len(calls) != 4 {
		t.Fatalf("command count = %d, want 4: %#v", len(calls), calls)
	}
	if calls[1][0] != darwinLaunchctlPath || calls[1][1] != "asuser" || calls[1][3] != darwinSecurityPath || calls[1][4] != "add-trusted-cert" {
		t.Fatalf("install command = %#v", calls[1])
	}
	if strings.Contains(strings.Join(calls[1], " "), " open ") {
		t.Fatalf("install must not use open: %#v", calls[1])
	}
}

func TestDarwinInstallAcceptsVerifiedTrustAfterCommandError(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	certPath := filepathForTestCertificate(t, certPEM)
	fingerprint, err := extractCertSHA256Fingerprint(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	restoreDarwinTestHooks(t)

	installed := false
	trusted := false
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		commandArgs := unwrapDarwinTestCommand(name, args)
		switch commandArgs[0] {
		case "find-certificate":
			if installed {
				return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
			}
			return nil, nil
		case "add-trusted-cert":
			installed = true
			trusted = true
			return []byte("authorization result arrived late"), errors.New("exit status 1")
		case "verify-cert":
			if trusted {
				return nil, nil
			}
			return nil, mustExitError(t)
		default:
			t.Fatalf("unexpected command: %s %#v", name, args)
			return nil, nil
		}
	}

	if err := (&DarwinInstaller{}).Install("mitm-ca", certPath); err != nil {
		t.Fatalf("Install() must accept verified final trust after command error: %v", err)
	}
}

func TestDarwinInstallRollsBackNewUntrustedCertificateAfterCommandError(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	certPath := filepathForTestCertificate(t, certPEM)
	fingerprint, err := extractCertSHA256Fingerprint(certPEM)
	if err != nil {
		t.Fatal(err)
	}
	restoreDarwinTestHooks(t)

	installed := false
	deleteCalls := 0
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		commandArgs := unwrapDarwinTestCommand(name, args)
		switch commandArgs[0] {
		case "find-certificate":
			if installed {
				return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
			}
			return nil, nil
		case "add-trusted-cert":
			installed = true
			return []byte("SecTrustSettingsSetTrustSettings: authorization denied"), errors.New("exit status 1")
		case "verify-cert":
			return nil, mustExitError(t)
		case "remove-trusted-cert":
			return nil, nil
		case "delete-certificate":
			deleteCalls++
			installed = false
			return nil, nil
		default:
			t.Fatalf("unexpected command: %s %#v", name, args)
			return nil, nil
		}
	}

	if err := (&DarwinInstaller{}).Install("mitm-ca", certPath); err == nil {
		t.Fatal("expected installation error")
	}
	if installed || deleteCalls != 1 {
		t.Fatalf("partial certificate was not rolled back: installed=%v deleteCalls=%d", installed, deleteCalls)
	}
}

func TestDarwinRemoveIsIdempotentAndDeletesByFingerprint(t *testing.T) {
	certPEM, _ := mustCreateTestCertificate(t)
	fingerprint, err := extractCertSHA256Fingerprint(certPEM)
	if err != nil {
		t.Fatal(err)
	}

	originalUID := darwinEffectiveUID
	originalConsoleUID := darwinConsoleUserUID
	originalRunner := runDarwinCommand
	t.Cleanup(func() {
		darwinEffectiveUID = originalUID
		darwinConsoleUserUID = originalConsoleUID
		runDarwinCommand = originalRunner
	})
	darwinEffectiveUID = func() int { return 0 }
	darwinConsoleUserUID = func() (string, error) { return "502", nil }

	installed := true
	deleteCalls := 0
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		commandArgs := args
		if name == darwinLaunchctlPath {
			commandArgs = args[3:]
		}
		switch commandArgs[0] {
		case "find-certificate":
			if installed {
				return []byte("SHA-256 hash: " + fingerprint + "\n"), nil
			}
			return nil, nil
		case "remove-trusted-cert":
			return nil, nil
		case "delete-certificate":
			deleteCalls++
			installed = false
			joined := strings.Join(commandArgs, " ")
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

func restoreDarwinTestHooks(t *testing.T) {
	t.Helper()
	originalUID := darwinEffectiveUID
	originalConsoleUID := darwinConsoleUserUID
	originalRunner := runDarwinCommand
	t.Cleanup(func() {
		darwinEffectiveUID = originalUID
		darwinConsoleUserUID = originalConsoleUID
		runDarwinCommand = originalRunner
	})
	darwinEffectiveUID = func() int { return 0 }
	darwinConsoleUserUID = func() (string, error) { return "502", nil }
}

func unwrapDarwinTestCommand(name string, args []string) []string {
	if name == darwinLaunchctlPath {
		return args[3:]
	}
	return args
}

func mustExitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("false").Run()
	if err == nil {
		t.Fatal("false command unexpectedly succeeded")
	}
	return err
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
