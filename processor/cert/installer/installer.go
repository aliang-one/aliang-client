package installer

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/cert"
)

// CertInfo holds information about a certificate
type CertInfo struct {
	Subject        string
	Issuer         string
	NotBefore      string
	NotAfter       string
	Fingerprint    string
	InstalledCount int
	InstallPath    string
}

// CertInstaller interface defines platform-specific certificate operations
type CertInstaller interface {
	// IsInstalled checks if a certificate is installed in system trust store
	// certBytes is used to extract the real certificate Common Name for accurate detection
	IsInstalled(certType string, certBytes []byte) (bool, error)

	// Install installs a certificate to system trust store (may require elevation)
	Install(certType string, certPath string) error

	// Remove removes a certificate from system trust store (may require elevation)
	// certBytes is used to extract the real certificate Common Name for accurate removal
	Remove(certType string, certBytes []byte) error

	// GetCertInfo retrieves certificate information
	GetCertInfo(certType string, certBytes []byte) (CertInfo, error)

	// GetInstallPath returns the system-specific installation path
	GetInstallPath(certType string) string

	// IsTrusted checks if a certificate is marked as globally trusted by the system
	// certBytes is used to extract the real certificate Common Name for accurate detection
	IsTrusted(certType string, certBytes []byte) (bool, error)

	// GetTrustStatus returns the detailed trust status of a certificate
	// Returns values like "not_found", "installed_not_trusted", "system_trusted"
	GetTrustStatus(certType string, certBytes []byte) (string, error)
}

// NewInstaller returns a platform-specific certificate installer
func NewInstaller() CertInstaller {
	switch runtime.GOOS {
	case "darwin":
		return &DarwinInstaller{}
	case "linux":
		return &LinuxInstaller{}
	case "windows":
		return &WindowsInstaller{}
	default:
		logger.Warn(fmt.Sprintf("Unsupported OS for certificate installation: %s", runtime.GOOS))
		return &UnimplementedInstaller{}
	}
}

// ============= Common Helper Functions =============

func parseCertificateBytes(certBytes []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certBytes)
	if block != nil {
		certBytes = block.Bytes
	}

	if len(bytes.TrimSpace(certBytes)) == 0 {
		return nil, fmt.Errorf("empty certificate data")
	}

	parsedCert, err := x509.ParseCertificate(certBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return parsedCert, nil
}

// extractCertCommonName extracts the Common Name from certificate PEM bytes
// This returns the actual certificate CN from the Subject, not a hardcoded value
func extractCertCommonName(certBytes []byte) (string, error) {
	cert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return "", err
	}

	if cert.Subject.CommonName == "" {
		return "", fmt.Errorf("certificate has no Common Name")
	}

	return cert.Subject.CommonName, nil
}

// parseCertificateInfo extracts certificate details from PEM bytes
func parseCertificateInfo(certBytes []byte) (CertInfo, error) {
	cert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return CertInfo{}, err
	}

	// Calculate SHA256 fingerprint
	hash := sha256.Sum256(cert.Raw)
	fingerprint := hex.EncodeToString(hash[:])

	return CertInfo{
		Subject:     cert.Subject.String(),
		Issuer:      cert.Issuer.String(),
		NotBefore:   cert.NotBefore.Format("2006-01-02"),
		NotAfter:    cert.NotAfter.Format("2006-01-02"),
		Fingerprint: fingerprint,
	}, nil
}

// extractCertThumbprint computes the SHA1 thumbprint used by Windows certificate stores.
func extractCertThumbprint(certBytes []byte) (string, error) {
	parsedCert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return "", err
	}

	thumbprint := sha1.Sum(parsedCert.Raw)
	return strings.ToUpper(hex.EncodeToString(thumbprint[:])), nil
}

func parsePEMCertificate(certBytes []byte) (*x509.Certificate, error) {
	return parseCertificateBytes(certBytes)
}

func extractCertSHA256Fingerprint(certBytes []byte) (string, error) {
	parsedCert, err := parsePEMCertificate(certBytes)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(parsedCert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func errorsIsNotExist(err error) bool {
	if err == nil {
		return false
	}
	if os.IsNotExist(err) {
		return true
	}
	var pathErr *fs.PathError
	return errors.As(err, &pathErr) && os.IsNotExist(pathErr.Err)
}

func escapePowerShellSingleQuoted(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

type windowsStoreTarget struct {
	StoreName string
	Location  string
	PSPath    string
}

func isWindowsDaemonRuntime() bool {
	return strings.TrimSpace(os.Getenv("ALIANG_DATA_DIR")) != "" ||
		strings.TrimSpace(os.Getenv("ALIANG_SOCKET_PATH")) != ""
}

func getWindowsStoreTargets(certType string) []windowsStoreTarget {
	var targets []windowsStoreTarget
	switch certType {
	case cert.CertTypeMtlsClient:
		targets = []windowsStoreTarget{
			{StoreName: "My", Location: "CurrentUser", PSPath: "Cert:\\CurrentUser\\My"},
			{StoreName: "My", Location: "LocalMachine", PSPath: "Cert:\\LocalMachine\\My"},
		}
	default:
		targets = []windowsStoreTarget{
			{StoreName: "Root", Location: "CurrentUser", PSPath: "Cert:\\CurrentUser\\Root"},
			{StoreName: "Root", Location: "LocalMachine", PSPath: "Cert:\\LocalMachine\\Root"},
		}
	}

	// A service account's CurrentUser store is not visible to the interactive user.
	// Daemon installs must therefore target LocalMachine only.
	if isWindowsDaemonRuntime() && len(targets) > 1 {
		targets = targets[1:]
	}

	return targets
}

func buildWindowsStoreScript(target windowsStoreTarget, openFlags string, body string) string {
	return fmt.Sprintf(`
$store = New-Object System.Security.Cryptography.X509Certificates.X509Store('%s', '%s')
$store.Open([System.Security.Cryptography.X509Certificates.OpenFlags]::%s)
try {
%s
} finally {
    $store.Close()
}
`, target.StoreName, target.Location, openFlags, body)
}

var runWindowsPowerShell = func(script string) ([]byte, error) {
	cmd := newPlatformCommand("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.CombinedOutput()
}

var runWindowsCommand = func(name string, args ...string) ([]byte, error) {
	cmd := newPlatformCommand(name, args...)
	return cmd.CombinedOutput()
}

func installWindowsCertWithX509Store(target windowsStoreTarget, certPath string) ([]byte, error) {
	psCmd := fmt.Sprintf(`
$cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2('%s')
%s
`, escapePowerShellSingleQuoted(certPath), buildWindowsStoreScript(target, "ReadWrite", "    $store.Add($cert)"))

	return runWindowsPowerShell(psCmd)
}

func installWindowsCertWithImportCertificate(target windowsStoreTarget, certPath string) ([]byte, error) {
	psCmd := fmt.Sprintf(
		"Import-Certificate -FilePath '%s' -CertStoreLocation '%s' -ErrorAction Stop | Out-Null",
		escapePowerShellSingleQuoted(certPath),
		escapePowerShellSingleQuoted(target.PSPath),
	)

	return runWindowsPowerShell(psCmd)
}

func installWindowsCertWithCertutil(target windowsStoreTarget, certPath string) ([]byte, error) {
	args := []string{}
	if target.Location == "CurrentUser" {
		args = append(args, "-user")
	}
	args = append(args, "-addstore", target.StoreName, certPath)

	return runWindowsCommand("certutil", args...)
}

func windowsStoreContainsThumbprint(target windowsStoreTarget, thumbprint string) (bool, error) {
	body := fmt.Sprintf(`
    $match = $store.Certificates | Where-Object { $_.Thumbprint -eq '%s' } | Select-Object -First 1
    if ($match) { Write-Output 'FOUND' }
	`, escapePowerShellSingleQuoted(thumbprint))
	output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadOnly", body))
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w, output: %s", target.PSPath, err, strings.TrimSpace(string(output)))
	}
	return strings.Contains(string(output), "FOUND"), nil
}

func findWindowsInstalledStore(certType string, certBytes []byte) (windowsStoreTarget, bool, error) {
	thumbprint, err := extractCertThumbprint(certBytes)
	if err != nil {
		return windowsStoreTarget{}, false, fmt.Errorf("failed to extract certificate thumbprint: %w", err)
	}

	var inspectErrs []string
	for _, target := range getWindowsStoreTargets(certType) {
		found, inspectErr := windowsStoreContainsThumbprint(target, thumbprint)
		if inspectErr != nil {
			inspectErrs = append(inspectErrs, inspectErr.Error())
			continue
		}
		if found {
			return target, true, nil
		}
	}
	if len(inspectErrs) > 0 {
		return windowsStoreTarget{}, false, errors.New(strings.Join(inspectErrs, "; "))
	}
	return windowsStoreTarget{}, false, nil
}

func countWindowsInstalledCopies(certType string, certBytes []byte) (int, error) {
	thumbprint, err := extractCertThumbprint(certBytes)
	if err != nil {
		return 0, fmt.Errorf("failed to extract certificate thumbprint: %w", err)
	}

	total := 0
	var inspectErrs []string
	for _, target := range getWindowsStoreTargets(certType) {
		body := fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Thumbprint -eq '%s' })
    Write-Output $matches.Count
	`, escapePowerShellSingleQuoted(thumbprint))

		output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadOnly", body))
		if err != nil {
			inspectErrs = append(inspectErrs, fmt.Sprintf("%s: %v, output: %s", target.PSPath, err, strings.TrimSpace(string(output))))
			continue
		}

		countText := strings.TrimSpace(string(output))
		if countText == "" {
			continue
		}

		var count int
		if _, scanErr := fmt.Sscanf(countText, "%d", &count); scanErr != nil {
			inspectErrs = append(inspectErrs, fmt.Sprintf("%s returned invalid count %q: %v", target.PSPath, countText, scanErr))
			continue
		}
		total += count
	}
	if len(inspectErrs) > 0 {
		return total, errors.New(strings.Join(inspectErrs, "; "))
	}
	return total, nil
}

// ============= macOS (Darwin) Implementation =============

type DarwinInstaller struct{}

const (
	darwinSystemKeychainPath = "/Library/Keychains/System.keychain"
	darwinSecurityPath       = "/usr/bin/security"
	darwinOSAScriptPath      = "/usr/bin/osascript"
	darwinLaunchctlPath      = "/bin/launchctl"
	darwinStatPath           = "/usr/bin/stat"
	darwinPrivilegedScript   = `on run argv
set commandText to "/usr/bin/security"
repeat with argValue in argv
set commandText to commandText & " " & quoted form of (contents of argValue)
end repeat
do shell script commandText with administrator privileges
end run`
)

type darwinKeychainMatch struct {
	Path  string
	Count int
}

var (
	darwinEffectiveUID   = os.Geteuid
	darwinConsoleUserUID = func() (string, error) {
		output, err := newPlatformCommand(darwinStatPath, "-f", "%u", "/dev/console").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("resolve macOS console user: %w, output: %s", err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	}
	runDarwinCommand = func(name string, args ...string) ([]byte, error) {
		return newPlatformCommand(name, args...).CombinedOutput()
	}
)

func buildDarwinPrivilegedCommand(isRoot bool, consoleUID string, securityArgs ...string) (string, []string, error) {
	if isRoot {
		uid, err := strconv.Atoi(strings.TrimSpace(consoleUID))
		if err != nil || uid <= 0 {
			return "", nil, fmt.Errorf("no logged-in macOS console user is available for certificate authorization")
		}
		args := []string{"asuser", strconv.Itoa(uid), darwinSecurityPath}
		args = append(args, securityArgs...)
		return darwinLaunchctlPath, args, nil
	}

	args := []string{"-e", darwinPrivilegedScript, "--"}
	args = append(args, securityArgs...)
	return darwinOSAScriptPath, args, nil
}

func runDarwinPrivilegedSecurity(securityArgs ...string) ([]byte, error) {
	isRoot := darwinEffectiveUID() == 0
	consoleUID := ""
	if isRoot {
		var err error
		consoleUID, err = darwinConsoleUserUID()
		if err != nil {
			return nil, err
		}
	}
	name, args, err := buildDarwinPrivilegedCommand(isRoot, consoleUID, securityArgs...)
	if err != nil {
		return nil, err
	}
	return runDarwinCommand(name, args...)
}

func countDarwinKeychainFingerprintMatches(certPath string, fingerprint string) (int, error) {
	output, err := runDarwinCommand(darwinSecurityPath, "find-certificate", "-a", "-Z", certPath)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderrText := strings.TrimSpace(string(output))
			if strings.Contains(stderrText, "could not be found in the keychain") ||
				strings.Contains(stderrText, "The specified item could not be found in the keychain") ||
				strings.Contains(stderrText, "Unable to delete certificate matching") {
				return 0, nil
			}
			logger.Debug(fmt.Sprintf("Failed to inspect macOS keychain %s: exit=%d output=%s", certPath, exitErr.ExitCode(), stderrText))
		}
		return 0, fmt.Errorf("security find-certificate %s failed: %w", certPath, err)
	}

	total := 0
	expected := strings.ToUpper(strings.TrimSpace(fingerprint))
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SHA-256 hash:") {
			continue
		}

		hash := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(line, "SHA-256 hash:")))
		if hash == expected {
			total++
		}
	}

	return total, nil
}

func findDarwinInstalledKeychains(certBytes []byte) ([]darwinKeychainMatch, error) {
	fingerprint, err := extractCertSHA256Fingerprint(certBytes)
	if err != nil {
		return nil, err
	}

	count, err := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	return []darwinKeychainMatch{{Path: darwinSystemKeychainPath, Count: count}}, nil
}

func verifyDarwinSystemTrust(certBytes []byte) (bool, error) {
	tempPath, cleanup, err := writeDarwinTemporaryCertificate(certBytes)
	if err != nil {
		return false, err
	}
	defer cleanup()

	output, err := runDarwinCommand(darwinSecurityPath, "verify-cert", "-c", tempPath,
		"-k", darwinSystemKeychainPath, "-L", "-l", "-q")
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		logger.Debug(fmt.Sprintf("macOS system trust verification failed: exit=%d output=%s", exitErr.ExitCode(), strings.TrimSpace(string(output))))
		return false, nil
	}
	return false, fmt.Errorf("security verify-cert failed: %w", err)
}

func writeDarwinTemporaryCertificate(certBytes []byte) (string, func(), error) {
	parsedCert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return "", nil, err
	}
	tempFile, err := os.CreateTemp("", "aliang-system-trust-*.pem")
	if err != nil {
		return "", nil, fmt.Errorf("create temporary certificate: %w", err)
	}
	tempPath := tempFile.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if err := tempFile.Chmod(0o600); err != nil {
		tempFile.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := tempFile.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parsedCert.Raw})); err != nil {
		tempFile.Close()
		cleanup()
		return "", nil, err
	}
	if err := tempFile.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return tempPath, cleanup, nil
}

func detectDarwinTrustStatus(certBytes []byte) (string, bool, error) {
	matches, err := findDarwinInstalledKeychains(certBytes)
	if err != nil {
		return "unknown", false, err
	}
	if len(matches) == 0 {
		return "not_found", false, nil
	}

	trusted, err := verifyDarwinSystemTrust(certBytes)
	if err != nil {
		return "unknown", false, err
	}
	if trusted {
		return "system_trusted", true, nil
	}
	return "installed_not_trusted", false, nil
}

func deleteDarwinCertificateCopiesUntil(fingerprint string, targetCount int) error {
	if targetCount < 0 {
		targetCount = 0
	}

	for {
		before, err := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
		if err != nil {
			return fmt.Errorf("inspect certificate copies before deletion: %w", err)
		}
		if before <= targetCount {
			return nil
		}

		output, removeErr := runDarwinPrivilegedSecurity("delete-certificate", "-Z", fingerprint, "-t", darwinSystemKeychainPath)
		after, inspectErr := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
		if inspectErr != nil {
			return errors.Join(removeErr, fmt.Errorf("verify certificate removal: %w", inspectErr))
		}
		if after <= targetCount {
			return nil
		}
		if removeErr != nil {
			return fmt.Errorf("security delete-certificate failed: %w, output: %s", removeErr, strings.TrimSpace(string(output)))
		}
		if after >= before {
			return fmt.Errorf("security delete-certificate completed but %d matching certificate(s) remain", after)
		}
	}
}

func (d *DarwinInstaller) restoreInstallState(certType string, certBytes []byte, fingerprint string, beforeCount, afterCount int) error {
	if afterCount <= beforeCount {
		return nil
	}
	if beforeCount == 0 {
		return d.Remove(certType, certBytes)
	}
	return deleteDarwinCertificateCopiesUntil(fingerprint, beforeCount)
}

// IsInstalled checks if certificate is installed in macOS System keychain
func (d *DarwinInstaller) IsInstalled(certType string, certBytes []byte) (bool, error) {
	matches, err := findDarwinInstalledKeychains(certBytes)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// Install adds a CA certificate to the macOS System keychain and marks it trusted.
func (d *DarwinInstaller) Install(certType string, certPath string) error {
	if certType != cert.CertTypeMitmCA {
		return fmt.Errorf("macOS system trust installation is only supported for %s", cert.CertTypeMitmCA)
	}

	absPath, err := filepath.Abs(certPath)
	if err != nil {
		return fmt.Errorf("failed to get absolute path: %w", err)
	}
	certBytes, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate for installation: %w", err)
	}
	parsedCert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return err
	}
	if !parsedCert.IsCA || parsedCert.KeyUsage&x509.KeyUsageCertSign == 0 {
		return fmt.Errorf("certificate is not a certificate-signing CA")
	}
	fingerprint, err := extractCertSHA256Fingerprint(certBytes)
	if err != nil {
		return err
	}
	beforeCount, err := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if err != nil {
		return fmt.Errorf("inspect certificate before installation: %w", err)
	}
	if beforeCount > 0 {
		alreadyTrusted, trustErr := verifyDarwinSystemTrust(certBytes)
		if trustErr != nil {
			return fmt.Errorf("inspect certificate trust before installation: %w", trustErr)
		}
		if alreadyTrusted {
			logger.Info(fmt.Sprintf("Certificate %s is already installed and trusted in macOS System keychain (SHA-256: %s)", certType, fingerprint))
			return nil
		}
	}

	logger.Debug(fmt.Sprintf("Installing certificate %s into macOS System keychain", certType))
	output, installErr := runDarwinPrivilegedSecurity("add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", darwinSystemKeychainPath, absPath)
	afterCount, countErr := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if countErr != nil {
		var commandErr error
		if installErr != nil {
			commandErr = fmt.Errorf("security add-trusted-cert failed: %w, output: %s", installErr, strings.TrimSpace(string(output)))
		}
		return errors.Join(commandErr, fmt.Errorf("verify installed certificate fingerprint: %w", countErr))
	}
	trusted := false
	var trustErr error
	if afterCount > 0 {
		trusted, trustErr = verifyDarwinSystemTrust(certBytes)
	}
	if trustErr == nil && trusted {
		if installErr != nil {
			logger.Warn(fmt.Sprintf("security add-trusted-cert returned an error after establishing trust; accepting verified final state: %v", installErr))
		}
		logger.Info(fmt.Sprintf("Certificate %s installed and trusted in macOS System keychain (SHA-256: %s)", certType, fingerprint))
		return nil
	}

	cleanupErr := d.restoreInstallState(certType, certBytes, fingerprint, beforeCount, afterCount)
	if trustErr != nil {
		return errors.Join(fmt.Errorf("verify installed certificate trust: %w", trustErr), cleanupErr)
	}
	if installErr != nil {
		outputText := strings.TrimSpace(string(output))
		if strings.Contains(strings.ToLower(outputText), "canceled") || strings.Contains(outputText, "(-128)") {
			logger.Warn("User canceled the certificate installation")
			return errors.Join(fmt.Errorf("installation canceled by user: %w", installErr), cleanupErr)
		}
		return errors.Join(fmt.Errorf("security add-trusted-cert failed: %w, output: %s", installErr, outputText), cleanupErr)
	}
	if afterCount == 0 {
		return errors.Join(fmt.Errorf("certificate installation command completed but the exact certificate was not found in the System keychain"), cleanupErr)
	}
	return errors.Join(fmt.Errorf("certificate was added to the System keychain but is not trusted as a root CA"), cleanupErr)
}

// Remove deletes the exact certificate and its trust settings from the System keychain.
func (d *DarwinInstaller) Remove(certType string, certBytes []byte) error {
	fingerprint, err := extractCertSHA256Fingerprint(certBytes)
	if err != nil {
		return err
	}
	count, err := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if err != nil {
		return err
	}
	if count == 0 {
		logger.Info(fmt.Sprintf("Certificate %s is not present in the macOS System keychain", certType))
		return nil
	}

	tempPath, cleanup, err := writeDarwinTemporaryCertificate(certBytes)
	if err != nil {
		return err
	}
	defer cleanup()
	trustOutput, trustErr := runDarwinPrivilegedSecurity("remove-trusted-cert", "-d", tempPath)
	afterTrustRemoval, inspectErr := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if inspectErr != nil {
		return fmt.Errorf("verify trust removal: %w", inspectErr)
	}
	if afterTrustRemoval == 0 {
		logger.Info(fmt.Sprintf("Certificate %s trust and System keychain entry removed (SHA-256: %s)", certType, fingerprint))
		return nil
	}
	if trustErr != nil {
		logger.Debug(fmt.Sprintf("remove-trusted-cert did not remove the certificate; deleting the exact keychain entry: %v, output: %s", trustErr, strings.TrimSpace(string(trustOutput))))
	}
	if err := deleteDarwinCertificateCopiesUntil(fingerprint, 0); err != nil {
		return err
	}
	logger.Info(fmt.Sprintf("Certificate %s removed from macOS System keychain (SHA-256: %s)", certType, fingerprint))
	return nil
}

// GetCertInfo retrieves certificate information
func (d *DarwinInstaller) GetCertInfo(certType string, certBytes []byte) (CertInfo, error) {
	info, err := parseCertificateInfo(certBytes)
	if err != nil {
		return info, err
	}
	matches, matchErr := findDarwinInstalledKeychains(certBytes)
	if matchErr == nil && len(matches) > 0 {
		paths := make([]string, 0, len(matches))
		total := 0
		for _, match := range matches {
			paths = append(paths, match.Path)
			total += match.Count
		}
		info.InstallPath = strings.Join(paths, ", ")
		info.InstalledCount = total
	} else {
		info.InstallPath = darwinSystemKeychainPath
	}
	return info, nil
}

// GetInstallPath returns the macOS installation path
func (d *DarwinInstaller) GetInstallPath(certType string) string {
	return darwinSystemKeychainPath
}

// IsTrusted checks if a certificate is marked as trusted on macOS
// It first checks if the certificate exists in the keychain, then verifies trust settings
func (d *DarwinInstaller) IsTrusted(certType string, certBytes []byte) (bool, error) {
	status, trusted, err := detectDarwinTrustStatus(certBytes)
	if err != nil {
		return false, err
	}
	logger.Debug(fmt.Sprintf("Certificate %s trust status on macOS: %s", certType, status))
	return trusted, nil
}

// GetTrustStatus returns detailed trust status for macOS certificates
func (d *DarwinInstaller) GetTrustStatus(certType string, certBytes []byte) (string, error) {
	status, _, err := detectDarwinTrustStatus(certBytes)
	if err != nil {
		return "unknown", err
	}
	return status, nil
}

// ============= Linux Implementation =============

type LinuxInstaller struct{}

type linuxTrustAnchor struct {
	Dir           string
	UpdateCommand []string
}

var linuxTrustAnchors = []linuxTrustAnchor{
	{Dir: "/usr/local/share/ca-certificates", UpdateCommand: []string{"update-ca-certificates"}},
	{Dir: "/etc/pki/ca-trust/source/anchors", UpdateCommand: []string{"update-ca-trust", "extract"}},
	{Dir: "/etc/ca-certificates/trust-source/anchors", UpdateCommand: []string{"trust", "extract-compat"}},
}

var linuxCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

var linuxLegacyCertDir = "/etc/ssl/certs"

func resolveLinuxExecutable(name string) (string, bool) {
	if strings.TrimSpace(name) == "" {
		return "", false
	}

	if filepath.IsAbs(name) || strings.Contains(name, string(os.PathSeparator)) {
		info, err := os.Stat(name)
		return name, err == nil && !info.IsDir() && info.Mode()&0111 != 0
	}

	if path, err := exec.LookPath(name); err == nil {
		return path, true
	}

	for _, dir := range []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
			return path, true
		}
	}

	return "", false
}

func linuxPrimaryCertFileName(certType string) string {
	certName := getCertFileName(certType)
	ext := filepath.Ext(certName)
	base := strings.TrimSuffix(certName, ext)
	if base == "" {
		base = strings.TrimSuffix(certName, ".")
	}
	return base + ".crt"
}

func linuxCertFileNames(certType string) []string {
	primary := linuxPrimaryCertFileName(certType)
	legacy := getCertFileName(certType)
	if legacy == primary {
		return []string{primary}
	}
	return []string{primary, legacy}
}

func linuxUserCertPaths(certType string) []string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		return nil
	}

	paths := make([]string, 0, len(linuxCertFileNames(certType)))
	for _, name := range linuxCertFileNames(certType) {
		paths = append(paths, filepath.Join(homeDir, ".local/share/ca-certificates/custom", name))
	}
	return paths
}

func linuxKnownCertPaths(certType string) []string {
	var paths []string
	for _, anchor := range linuxTrustAnchors {
		for _, name := range linuxCertFileNames(certType) {
			paths = append(paths, filepath.Join(anchor.Dir, name))
		}
	}
	for _, name := range linuxCertFileNames(certType) {
		paths = append(paths, filepath.Join(linuxLegacyCertDir, name))
	}
	paths = append(paths, linuxUserCertPaths(certType)...)
	return paths
}

func linuxPrimaryTrustAnchorPath(certType string) string {
	return filepath.Join(linuxTrustAnchors[0].Dir, linuxPrimaryCertFileName(certType))
}

func certificateSHA256Fingerprint(certBytes []byte) (string, error) {
	parsedCert, err := parseCertificateBytes(certBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(parsedCert.Raw)
	return strings.ToUpper(hex.EncodeToString(sum[:])), nil
}

func certificateFileMatchesFingerprint(path string, expectedFingerprint string) (bool, error) {
	certData, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	got, err := certificateSHA256Fingerprint(certData)
	if err != nil {
		return false, err
	}

	return got == expectedFingerprint, nil
}

func certificateBundleContainsFingerprint(bundleData []byte, expectedFingerprint string) bool {
	rest := bundleData
	foundPEM := false
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		foundPEM = true
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}

		parsedCert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(parsedCert.Raw)
		if strings.ToUpper(hex.EncodeToString(sum[:])) == expectedFingerprint {
			return true
		}
	}

	if foundPEM {
		return false
	}

	certs, err := x509.ParseCertificates(bytes.TrimSpace(bundleData))
	if err != nil {
		return false
	}
	for _, parsedCert := range certs {
		sum := sha256.Sum256(parsedCert.Raw)
		if strings.ToUpper(hex.EncodeToString(sum[:])) == expectedFingerprint {
			return true
		}
	}
	return false
}

func linuxCABundleContainsFingerprint(expectedFingerprint string) (bool, error) {
	var errs []string
	for _, caFile := range linuxCABundlePaths {
		caData, err := os.ReadFile(caFile)
		if err != nil {
			if !errorsIsNotExist(err) {
				errs = append(errs, fmt.Sprintf("%s: %v", caFile, err))
			}
			continue
		}

		if certificateBundleContainsFingerprint(caData, expectedFingerprint) {
			return true, nil
		}
	}

	if len(errs) > 0 {
		return false, errors.New(strings.Join(errs, "; "))
	}
	return false, nil
}

var runLinuxPrivilegedCommand = func(name string, args ...string) ([]byte, error) {
	resolvedName, ok := resolveLinuxExecutable(name)
	if !ok {
		return nil, fmt.Errorf("command %s not found", name)
	}

	if os.Geteuid() == 0 {
		return newPlatformCommand(resolvedName, args...).CombinedOutput()
	}

	output, err := newPlatformCommand(resolvedName, args...).CombinedOutput()
	if err == nil {
		return output, nil
	}

	sudoPath, sudoOK := resolveLinuxExecutable("sudo")
	if !sudoOK {
		return output, err
	}

	sudoArgs := append([]string{"-n", resolvedName}, args...)
	sudoOutput, sudoRunErr := newPlatformCommand(sudoPath, sudoArgs...).CombinedOutput()
	if sudoRunErr == nil {
		return sudoOutput, nil
	}

	if pkexecPath, pkexecOK := resolveLinuxExecutable("pkexec"); pkexecOK {
		pkexecArgs := append([]string{resolvedName}, args...)
		pkexecOutput, pkexecErr := newPlatformCommand(pkexecPath, pkexecArgs...).CombinedOutput()
		if pkexecErr == nil {
			return pkexecOutput, nil
		}
		combined := append(output, sudoOutput...)
		return append(combined, pkexecOutput...), pkexecErr
	}

	return append(output, sudoOutput...), sudoRunErr
}

func writeLinuxSystemCert(targetPath string, certData []byte) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err == nil {
		if err := os.WriteFile(targetPath, certData, 0644); err == nil {
			return nil
		}
	}

	_, sudoOK := resolveLinuxExecutable("sudo")
	_, pkexecOK := resolveLinuxExecutable("pkexec")
	if !sudoOK && !pkexecOK && os.Geteuid() != 0 {
		return fmt.Errorf("system certificate directory is not writable and neither sudo nor pkexec is available")
	}

	output, err := runLinuxPrivilegedCommand("mkdir", "-p", filepath.Dir(targetPath))
	if err != nil {
		return fmt.Errorf("create system certificate directory failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}

	tmpFile, err := os.CreateTemp("", "aliang-cert-*.crt")
	if err != nil {
		return fmt.Errorf("create temporary certificate file failed: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.Write(certData); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write temporary certificate file failed: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temporary certificate file failed: %w", err)
	}

	output, err = runLinuxPrivilegedCommand("install", "-m", "0644", tmpPath, targetPath)
	if err != nil {
		return fmt.Errorf("install system certificate failed: %w, output: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func removeLinuxSystemPath(path string) error {
	if err := os.Remove(path); err == nil || errorsIsNotExist(err) {
		return nil
	}

	output, err := runLinuxPrivilegedCommand("rm", "-f", path)
	if err != nil {
		return fmt.Errorf("remove %s failed: %w, output: %s", path, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func snapshotLinuxSystemCert(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		return data, true, nil
	}
	if errorsIsNotExist(err) {
		return nil, false, nil
	}
	return nil, false, err
}

func restoreLinuxSystemCert(path string, previousData []byte, existed bool) error {
	if existed {
		return writeLinuxSystemCert(path, previousData)
	}
	return removeLinuxSystemPath(path)
}

type linuxRemovedSystemCert struct {
	path string
	data []byte
}

func restoreRemovedLinuxSystemCerts(removed []linuxRemovedSystemCert, updateCommands [][]string) error {
	var restoreErrs []error
	for _, item := range removed {
		if err := writeLinuxSystemCert(item.path, item.data); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("restore %s: %w", item.path, err))
		}
	}
	if len(restoreErrs) == 0 && len(updateCommands) > 0 {
		if err := runLinuxTrustUpdate(updateCommands); err != nil {
			restoreErrs = append(restoreErrs, fmt.Errorf("refresh trust after restoring certificate files: %w", err))
		}
	}
	return errors.Join(restoreErrs...)
}

func runLinuxTrustUpdate(commands [][]string) error {
	seen := make(map[string]bool)
	var errs []string
	var attempted bool

	for _, command := range commands {
		commandKey := strings.Join(command, "\x00")
		if len(command) == 0 || seen[commandKey] {
			continue
		}
		seen[commandKey] = true

		if _, ok := resolveLinuxExecutable(command[0]); !ok {
			continue
		}

		attempted = true
		output, err := runLinuxPrivilegedCommand(command[0], command[1:]...)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v, output: %s", strings.Join(command, " "), err, strings.TrimSpace(string(output))))
		}
	}

	if !attempted {
		return fmt.Errorf("no supported Linux CA trust update command found")
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// IsInstalled checks if certificate is installed in system or user CA directories.
// It verifies the actual certificate content (via SHA256 fingerprint), not just file existence,
// so that a file with the same name but different content is correctly reported as not installed.
func (l *LinuxInstaller) IsInstalled(certType string, certBytes []byte) (bool, error) {
	ourFingerprint, err := certificateSHA256Fingerprint(certBytes)
	if err != nil {
		return false, err
	}

	trusted, err := linuxCABundleContainsFingerprint(ourFingerprint)
	if err == nil && trusted {
		logger.Debug(fmt.Sprintf("Certificate %s found in Linux CA bundle", certType))
		return true, nil
	}
	var inspectErrs []string
	if err != nil {
		inspectErrs = append(inspectErrs, err.Error())
	}

	for _, path := range linuxKnownCertPaths(certType) {
		matches, matchErr := certificateFileMatchesFingerprint(path, ourFingerprint)
		if matchErr != nil {
			if !errorsIsNotExist(matchErr) {
				logger.Debug(fmt.Sprintf("Failed to inspect Linux certificate path %s: %v", path, matchErr))
				inspectErrs = append(inspectErrs, fmt.Sprintf("%s: %v", path, matchErr))
			}
			continue
		}
		if matches {
			logger.Debug(fmt.Sprintf("Certificate %s found in Linux certificate path: %s", certType, path))
			return true, nil
		}
	}
	if len(inspectErrs) > 0 {
		return false, errors.New(strings.Join(inspectErrs, "; "))
	}

	logger.Debug(fmt.Sprintf("Certificate %s not found in Linux system", certType))
	return false, nil
}

// Install adds the certificate to a system trust anchor and verifies the CA bundle.
func (l *LinuxInstaller) Install(certType string, certPath string) error {
	logger.Debug(fmt.Sprintf("Installing certificate %s to Linux system", certType))

	certData, readErr := os.ReadFile(certPath)
	if readErr != nil {
		return fmt.Errorf("failed to read certificate file: %w", readErr)
	}
	if _, parseErr := parseCertificateBytes(certData); parseErr != nil {
		return parseErr
	}
	fingerprint, err := certificateSHA256Fingerprint(certData)
	if err != nil {
		return err
	}
	if trusted, trustErr := linuxCABundleContainsFingerprint(fingerprint); trustErr == nil && trusted {
		logger.Info(fmt.Sprintf("Certificate %s is already trusted by the Linux CA bundle", certType))
		return nil
	}

	var installErrs []string
	var attempted bool
	for _, anchor := range linuxTrustAnchors {
		if len(anchor.UpdateCommand) == 0 {
			continue
		}
		if _, ok := resolveLinuxExecutable(anchor.UpdateCommand[0]); !ok {
			continue
		}
		attempted = true

		targetPath := filepath.Join(anchor.Dir, linuxPrimaryCertFileName(certType))
		logger.Debug(fmt.Sprintf("Attempting system-level installation to %s", targetPath))
		previousData, existed, snapshotErr := snapshotLinuxSystemCert(targetPath)
		if snapshotErr != nil {
			installErrs = append(installErrs, fmt.Sprintf("snapshot %s: %v", targetPath, snapshotErr))
			continue
		}

		if err := writeLinuxSystemCert(targetPath, certData); err != nil {
			installErrs = append(installErrs, fmt.Sprintf("%s: %v", targetPath, err))
			continue
		}

		updateErr := runLinuxTrustUpdate([][]string{anchor.UpdateCommand})
		trusted := false
		var verifyErr error
		if updateErr == nil {
			trusted, verifyErr = linuxCABundleContainsFingerprint(fingerprint)
		}
		if updateErr != nil || verifyErr != nil || !trusted {
			restoreErr := restoreLinuxSystemCert(targetPath, previousData, existed)
			var restoreUpdateErr error
			if restoreErr == nil {
				restoreUpdateErr = runLinuxTrustUpdate([][]string{anchor.UpdateCommand})
			}
			failure := errors.Join(updateErr, verifyErr, restoreErr, restoreUpdateErr)
			if failure == nil {
				failure = fmt.Errorf("exact certificate fingerprint was not found in the Linux CA bundle after trust refresh")
			}
			installErrs = append(installErrs, fmt.Sprintf("install %s: %v", targetPath, failure))
			continue
		}

		logger.Info(fmt.Sprintf("Certificate installed to Linux system trust anchor %s and CA certificates updated", targetPath))
		return nil
	}
	if !attempted {
		return fmt.Errorf("no supported Linux system CA trust anchor found")
	}
	return fmt.Errorf("certificate was not installed into the Linux system trust store: %s", strings.Join(installErrs, "; "))
}

// Remove deletes certificate from system or user CA directory
func (l *LinuxInstaller) Remove(certType string, certBytes []byte) error {
	config := cert.GetCertConfig(certType)
	if config == nil {
		return fmt.Errorf("unknown certificate type: %s", certType)
	}

	logger.Debug(fmt.Sprintf("Removing certificate %s from Linux system", certType))
	fingerprint, err := certificateSHA256Fingerprint(certBytes)
	if err != nil {
		return err
	}

	var removedSystem []linuxRemovedSystemCert
	var removedUserAny bool
	var removeErrs []string
	var updateCommands [][]string

	for _, anchor := range linuxTrustAnchors {
		for _, name := range linuxCertFileNames(certType) {
			path := filepath.Join(anchor.Dir, name)
			matches, matchErr := certificateFileMatchesFingerprint(path, fingerprint)
			if matchErr != nil {
				if !errorsIsNotExist(matchErr) {
					removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, matchErr))
				}
				continue
			}
			if !matches {
				continue
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, readErr))
				continue
			}

			if err := removeLinuxSystemPath(path); err != nil {
				removeErrs = append(removeErrs, err.Error())
				continue
			}

			removedSystem = append(removedSystem, linuxRemovedSystemCert{path: path, data: data})
			updateCommands = append(updateCommands, anchor.UpdateCommand)
			logger.Debug(fmt.Sprintf("Certificate removed from Linux system certificate path: %s", path))
		}
	}

	for _, name := range linuxCertFileNames(certType) {
		legacyPath := filepath.Join(linuxLegacyCertDir, name)
		matches, matchErr := certificateFileMatchesFingerprint(legacyPath, fingerprint)
		if matchErr != nil {
			if !errorsIsNotExist(matchErr) {
				removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", legacyPath, matchErr))
			}
			continue
		}
		if !matches {
			continue
		}
		data, readErr := os.ReadFile(legacyPath)
		if readErr != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", legacyPath, readErr))
			continue
		}
		if err := removeLinuxSystemPath(legacyPath); err != nil {
			removeErrs = append(removeErrs, err.Error())
			continue
		}
		removedSystem = append(removedSystem, linuxRemovedSystemCert{path: legacyPath, data: data})
		updateCommands = append(updateCommands, []string{"update-ca-certificates"})
		logger.Debug(fmt.Sprintf("Certificate removed from legacy Linux certificate path: %s", legacyPath))
	}

	if len(removeErrs) > 0 {
		restoreErr := restoreRemovedLinuxSystemCerts(removedSystem, updateCommands)
		return errors.Join(errors.New(strings.Join(removeErrs, "; ")), restoreErr)
	}
	if len(removedSystem) > 0 {
		if len(updateCommands) > 0 {
			if err := runLinuxTrustUpdate(updateCommands); err != nil {
				restoreErr := restoreRemovedLinuxSystemCerts(removedSystem, updateCommands)
				return errors.Join(fmt.Errorf("certificate files were removed but Linux CA trust refresh failed: %w", err), restoreErr)
			}
		}
	}

	for _, path := range linuxUserCertPaths(certType) {
		matches, matchErr := certificateFileMatchesFingerprint(path, fingerprint)
		if matchErr != nil {
			if !errorsIsNotExist(matchErr) {
				removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, matchErr))
			}
			continue
		}
		if !matches {
			continue
		}
		if err := os.Remove(path); err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		removedUserAny = true
		logger.Debug(fmt.Sprintf("Certificate removed from Linux user certificate path: %s", path))
	}
	if len(removeErrs) > 0 {
		return errors.New(strings.Join(removeErrs, "; "))
	}
	if len(removedSystem) > 0 || removedUserAny {
		logger.Info("Certificate removal completed across available Linux certificate paths")
		return nil
	}

	logger.Info(fmt.Sprintf("Certificate %s was not present in Linux system or user certificate paths", certType))
	return nil
}

// GetCertInfo retrieves certificate information
func (l *LinuxInstaller) GetCertInfo(certType string, certBytes []byte) (CertInfo, error) {
	info, err := parseCertificateInfo(certBytes)
	if err != nil {
		return info, err
	}

	certFingerprint, fingerprintErr := certificateSHA256Fingerprint(certBytes)
	if fingerprintErr == nil {
		for _, path := range linuxKnownCertPaths(certType) {
			matches, matchErr := certificateFileMatchesFingerprint(path, certFingerprint)
			if matchErr == nil && matches {
				info.InstallPath = path
				info.InstalledCount++
			}
		}
	}

	if info.InstallPath == "" {
		info.InstallPath = linuxPrimaryTrustAnchorPath(certType)
	}
	return info, nil
}

// GetInstallPath returns the Linux installation path
func (l *LinuxInstaller) GetInstallPath(certType string) string {
	return linuxPrimaryTrustAnchorPath(certType)
}

// IsTrusted checks if a certificate is in Linux's trusted CA bundle
func (l *LinuxInstaller) IsTrusted(certType string, certBytes []byte) (bool, error) {
	fingerprint, err := certificateSHA256Fingerprint(certBytes)
	if err != nil {
		return false, err
	}

	return linuxCABundleContainsFingerprint(fingerprint)
}

// GetTrustStatus returns detailed trust status for Linux certificates
func (l *LinuxInstaller) GetTrustStatus(certType string, certBytes []byte) (string, error) {
	// Check if installed
	installed, err := l.IsInstalled(certType, certBytes)
	if err != nil {
		return "unknown", err
	}
	if !installed {
		return "not_found", nil
	}

	// Check if trusted
	trusted, err := l.IsTrusted(certType, certBytes)
	if err != nil {
		return "unknown", err
	}
	if trusted {
		return "system_trusted", nil
	}

	return "installed_not_trusted", nil
}

// ============= Windows Implementation =============

type WindowsInstaller struct{}

// IsInstalled checks if certificate is installed in Windows certificate store
func (w *WindowsInstaller) IsInstalled(certType string, certBytes []byte) (bool, error) {
	target, found, err := findWindowsInstalledStore(certType, certBytes)
	if err != nil {
		return false, err
	}

	if found {
		logger.Debug(fmt.Sprintf("Certificate %s found in Windows certificate store %s", certType, target.PSPath))
		return true, nil
	}

	logger.Debug(fmt.Sprintf("Certificate %s not found in Windows certificate stores", certType))
	return false, nil
}

// Install adds certificate to Windows certificate store
func (w *WindowsInstaller) Install(certType string, certPath string) error {
	logger.Debug(fmt.Sprintf("Installing certificate %s to Windows certificate store", certType))
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate file: %w", err)
	}
	thumbprint, err := extractCertThumbprint(certBytes)
	if err != nil {
		return fmt.Errorf("failed to extract certificate thumbprint: %w", err)
	}

	// Convert path to Windows format
	certPath = strings.ReplaceAll(certPath, "/", "\\")
	targets := getWindowsStoreTargets(certType)
	failures := make([]string, 0, len(targets))
	strategies := []struct {
		name string
		run  func(windowsStoreTarget, string) ([]byte, error)
	}{
		{name: "x509store", run: installWindowsCertWithX509Store},
		{name: "import-certificate", run: installWindowsCertWithImportCertificate},
		{name: "certutil", run: installWindowsCertWithCertutil},
	}

	for _, target := range targets {
		for _, strategy := range strategies {
			output, installErr := strategy.run(target, certPath)
			found, inspectErr := windowsStoreContainsThumbprint(target, thumbprint)
			if inspectErr == nil && found {
				if installErr != nil {
					logger.Warn(fmt.Sprintf("Windows certificate command returned an error after the exact certificate appeared in %s via %s: %v", target.PSPath, strategy.name, installErr))
				}
				logger.Debug(fmt.Sprintf("Certificate installed and verified in Windows certificate store %s via %s", target.PSPath, strategy.name))
				return nil
			}

			trimmedOutput := strings.TrimSpace(string(output))
			failure := errors.Join(installErr, inspectErr)
			if failure == nil {
				failure = fmt.Errorf("command completed but the exact certificate was not found")
			}
			logger.Warn(fmt.Sprintf("Failed to install certificate %s to %s via %s: %v, output: %s", certType, target.PSPath, strategy.name, failure, trimmedOutput))
			failures = append(failures, fmt.Sprintf("%s via %s: %v, output: %s", target.PSPath, strategy.name, failure, trimmedOutput))
		}
	}

	return fmt.Errorf("certificate installation failed in all Windows stores: %s", strings.Join(failures, "; "))
}

// Remove deletes certificate from Windows certificate store
func (w *WindowsInstaller) Remove(certType string, certBytes []byte) error {
	thumbprint, err := extractCertThumbprint(certBytes)
	if err != nil {
		return fmt.Errorf("failed to extract certificate thumbprint for removal: %w", err)
	}

	logger.Debug(fmt.Sprintf("Removing certificate %s from Windows certificate stores", certType))

	var removeErrs []string
	for _, target := range getWindowsStoreTargets(certType) {
		body := fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Thumbprint -eq '%s' })
    foreach ($match in $matches) {
        $store.Remove($match)
    }
    Write-Output $matches.Count
	`, escapePowerShellSingleQuoted(thumbprint))

		output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadWrite", body))
		if err != nil {
			removeErrs = append(removeErrs, fmt.Sprintf("%s: %v, output: %s", target.PSPath, err, strings.TrimSpace(string(output))))
			continue
		}
		found, inspectErr := windowsStoreContainsThumbprint(target, thumbprint)
		if inspectErr != nil {
			removeErrs = append(removeErrs, inspectErr.Error())
			continue
		}
		if found {
			removeErrs = append(removeErrs, fmt.Sprintf("exact certificate remains in %s after removal", target.PSPath))
		}
	}
	if len(removeErrs) > 0 {
		return fmt.Errorf("certificate removal failed in one or more Windows stores: %s", strings.Join(removeErrs, "; "))
	}
	logger.Info("Certificate removal verified across Windows certificate stores")
	return nil
}

// GetCertInfo retrieves certificate information
func (w *WindowsInstaller) GetCertInfo(certType string, certBytes []byte) (CertInfo, error) {
	info, err := parseCertificateInfo(certBytes)
	if err != nil {
		return info, err
	}

	if count, err := countWindowsInstalledCopies(certType, certBytes); err == nil {
		info.InstalledCount = count
	}

	target, found, err := findWindowsInstalledStore(certType, certBytes)
	if err == nil && found {
		info.InstallPath = target.PSPath
	} else {
		info.InstallPath = w.GetInstallPath(certType)
	}

	return info, nil
}

// GetInstallPath returns the Windows installation path
func (w *WindowsInstaller) GetInstallPath(certType string) string {
	return getWindowsStoreTargets(certType)[0].PSPath
}

// IsTrusted checks if a certificate is trusted on Windows (equivalent to IsInstalled for Root store)
func (w *WindowsInstaller) IsTrusted(certType string, certBytes []byte) (bool, error) {
	if certType == cert.CertTypeMtlsClient {
		// Personal client certificates are installable, but they are not root-trusted CAs.
		return false, nil
	}

	// On Windows, certificates in Root store are automatically trusted.
	return w.IsInstalled(certType, certBytes)
}

// GetTrustStatus returns detailed trust status for Windows certificates
func (w *WindowsInstaller) GetTrustStatus(certType string, certBytes []byte) (string, error) {
	// Check if installed
	installed, err := w.IsInstalled(certType, certBytes)
	if err != nil {
		return "unknown", err
	}
	if !installed {
		return "not_found", nil
	}

	if certType == cert.CertTypeMtlsClient {
		return "installed_not_trusted", nil
	}

	// On Windows, being in Root store means it's trusted.
	return "system_trusted", nil
}

// ============= Unimplemented Installer =============

type UnimplementedInstaller struct{}

func (u *UnimplementedInstaller) IsInstalled(certType string, certBytes []byte) (bool, error) {
	return false, fmt.Errorf("certificate operations not supported on this platform")
}

func (u *UnimplementedInstaller) Install(certType string, certPath string) error {
	return fmt.Errorf("certificate operations not supported on this platform")
}

func (u *UnimplementedInstaller) Remove(certType string, certBytes []byte) error {
	return fmt.Errorf("certificate operations not supported on this platform")
}

func (u *UnimplementedInstaller) GetCertInfo(certType string, certBytes []byte) (CertInfo, error) {
	return CertInfo{}, fmt.Errorf("certificate operations not supported on this platform")
}

func (u *UnimplementedInstaller) GetInstallPath(certType string) string {
	return ""
}

// IsTrusted returns error for unsupported platforms
func (u *UnimplementedInstaller) IsTrusted(certType string, certBytes []byte) (bool, error) {
	return false, fmt.Errorf("IsTrusted not implemented for this platform")
}

// GetTrustStatus returns error for unsupported platforms
func (u *UnimplementedInstaller) GetTrustStatus(certType string, certBytes []byte) (string, error) {
	return "unsupported_platform", fmt.Errorf("GetTrustStatus not implemented for this platform")
}

// ============= Helper Functions =============

// getCertCommonName returns the certificate common name from configuration
// Deprecated: Use cert.GetCertConfig(certType).CN instead
func getCertCommonName(certType string) string {
	config := cert.GetCertConfig(certType)
	if config != nil {
		return config.CN
	}
	// Fallback for unknown types
	return certType
}

// getCertFileName returns the certificate file name from configuration
// Deprecated: Use cert.GetCertConfig(certType).FileName instead
func getCertFileName(certType string) string {
	config := cert.GetCertConfig(certType)
	if config != nil {
		return config.FileName + ".pem"
	}
	// Fallback for unknown types
	return certType + ".pem"
}
