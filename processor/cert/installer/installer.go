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
	} else {
		certBytes = bytes.TrimSpace(certBytes)
	}

	if len(certBytes) == 0 {
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

	// When running as a Windows service (for example LocalSystem), CurrentUser points
	// at the service account profile. Prefer LocalMachine first in that context.
	if isWindowsDaemonRuntime() && len(targets) > 1 {
		targets[0], targets[1] = targets[1], targets[0]
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

func runWindowsPowerShell(script string) ([]byte, error) {
	cmd := newPlatformCommand("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.CombinedOutput()
}

func runWindowsCommand(name string, args ...string) ([]byte, error) {
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

func findWindowsInstalledStore(certType string, certBytes []byte) (windowsStoreTarget, bool, error) {
	thumbprint, thumbprintErr := extractCertThumbprint(certBytes)
	commonName, commonNameErr := extractCertCommonName(certBytes)
	if thumbprintErr != nil && commonNameErr != nil {
		return windowsStoreTarget{}, false, fmt.Errorf("failed to extract certificate identity: thumbprint=%v commonName=%v", thumbprintErr, commonNameErr)
	}

	for _, target := range getWindowsStoreTargets(certType) {
		var body string
		if thumbprintErr == nil {
			body = fmt.Sprintf(`
    $match = $store.Certificates | Where-Object { $_.Thumbprint -eq '%s' } | Select-Object -First 1
    if ($match) { Write-Output 'FOUND' }
`, escapePowerShellSingleQuoted(thumbprint))
		} else {
			body = fmt.Sprintf(`
    $match = $store.Certificates | Where-Object { $_.Subject -like '*%s*' } | Select-Object -First 1
    if ($match) { Write-Output 'FOUND' }
`, escapePowerShellSingleQuoted(commonName))
		}

		output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadOnly", body))
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to inspect Windows certificate store %s: %v, output: %s", target.PSPath, err, string(output)))
			continue
		}

		if strings.Contains(string(output), "FOUND") {
			return target, true, nil
		}
	}

	return windowsStoreTarget{}, false, nil
}

func countWindowsInstalledCopies(certType string, certBytes []byte) (int, error) {
	thumbprint, thumbprintErr := extractCertThumbprint(certBytes)
	commonName, commonNameErr := extractCertCommonName(certBytes)
	if thumbprintErr != nil && commonNameErr != nil {
		return 0, fmt.Errorf("failed to extract certificate identity: thumbprint=%v commonName=%v", thumbprintErr, commonNameErr)
	}

	total := 0
	for _, target := range getWindowsStoreTargets(certType) {
		var body string
		if thumbprintErr == nil {
			body = fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Thumbprint -eq '%s' })
    Write-Output $matches.Count
`, escapePowerShellSingleQuoted(thumbprint))
		} else {
			body = fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Subject -like '*%s*' })
    Write-Output $matches.Count
`, escapePowerShellSingleQuoted(commonName))
		}

		output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadOnly", body))
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to count Windows certificates in %s: %v, output: %s", target.PSPath, err, strings.TrimSpace(string(output))))
			continue
		}

		countText := strings.TrimSpace(string(output))
		if countText == "" {
			continue
		}

		var count int
		if _, scanErr := fmt.Sscanf(countText, "%d", &count); scanErr != nil {
			logger.Warn(fmt.Sprintf("Failed to parse Windows certificate count from %s output %q: %v", target.PSPath, countText, scanErr))
			continue
		}
		total += count
	}

	return total, nil
}

// ============= macOS (Darwin) Implementation =============

type DarwinInstaller struct{}

const (
	darwinSystemKeychainPath = "/Library/Keychains/System.keychain"
	darwinSecurityPath       = "/usr/bin/security"
	darwinOSAScriptPath      = "/usr/bin/osascript"
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
	darwinEffectiveUID = os.Geteuid
	runDarwinCommand   = func(name string, args ...string) ([]byte, error) {
		return newPlatformCommand(name, args...).CombinedOutput()
	}
)

func buildDarwinPrivilegedCommand(isRoot bool, securityArgs ...string) (string, []string) {
	if isRoot {
		return darwinSecurityPath, append([]string(nil), securityArgs...)
	}

	args := []string{"-e", darwinPrivilegedScript, "--"}
	args = append(args, securityArgs...)
	return darwinOSAScriptPath, args
}

func runDarwinPrivilegedSecurity(securityArgs ...string) ([]byte, error) {
	name, args := buildDarwinPrivilegedCommand(darwinEffectiveUID() == 0, securityArgs...)
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

	logger.Debug(fmt.Sprintf("Installing certificate %s into macOS System keychain", certType))
	output, err := runDarwinPrivilegedSecurity("add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", darwinSystemKeychainPath, absPath)
	if err != nil {
		outputText := strings.TrimSpace(string(output))
		if strings.Contains(strings.ToLower(outputText), "canceled") || strings.Contains(outputText, "(-128)") {
			logger.Warn("User canceled the certificate installation")
			return fmt.Errorf("installation canceled by user")
		}
		return fmt.Errorf("security add-trusted-cert failed: %w, output: %s", err, outputText)
	}

	fingerprint, err := extractCertSHA256Fingerprint(certBytes)
	if err != nil {
		return err
	}
	count, err := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
	if err != nil {
		return fmt.Errorf("verify installed certificate fingerprint: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("certificate installation command completed but the exact certificate was not found in the System keychain")
	}
	trusted, err := verifyDarwinSystemTrust(certBytes)
	if err != nil {
		return fmt.Errorf("verify installed certificate trust: %w", err)
	}
	if !trusted {
		return fmt.Errorf("certificate was added to the System keychain but is not trusted as a root CA")
	}

	logger.Info(fmt.Sprintf("Certificate %s installed and trusted in macOS System keychain (SHA-256: %s)", certType, fingerprint))
	return nil
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
	count = afterTrustRemoval

	for remaining := count; remaining > 0; {
		output, removeErr := runDarwinPrivilegedSecurity("delete-certificate", "-Z", fingerprint, "-t", darwinSystemKeychainPath)
		after, inspectErr := countDarwinKeychainFingerprintMatches(darwinSystemKeychainPath, fingerprint)
		if inspectErr != nil {
			return fmt.Errorf("verify certificate removal: %w", inspectErr)
		}
		if after == 0 {
			logger.Info(fmt.Sprintf("Certificate %s removed from macOS System keychain (SHA-256: %s)", certType, fingerprint))
			return nil
		}
		if removeErr != nil {
			return fmt.Errorf("security delete-certificate failed: %w, output: %s", removeErr, strings.TrimSpace(string(output)))
		}
		if after >= remaining {
			return fmt.Errorf("security delete-certificate completed but %d matching certificate(s) remain", after)
		}
		remaining = after
	}

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
		paths = append(paths, filepath.Join("/etc/ssl/certs", name))
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

func runLinuxPrivilegedCommand(name string, args ...string) ([]byte, error) {
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

	for _, path := range linuxKnownCertPaths(certType) {
		matches, matchErr := certificateFileMatchesFingerprint(path, ourFingerprint)
		if matchErr != nil {
			if !errorsIsNotExist(matchErr) {
				logger.Debug(fmt.Sprintf("Failed to inspect Linux certificate path %s: %v", path, matchErr))
			}
			continue
		}
		if matches {
			logger.Debug(fmt.Sprintf("Certificate %s found in Linux certificate path: %s", certType, path))
			return true, nil
		}
	}

	logger.Debug(fmt.Sprintf("Certificate %s not found in Linux system", certType))
	return false, nil
}

// Install attempts to install certificate to system or user CA directory
func (l *LinuxInstaller) Install(certType string, certPath string) error {
	logger.Debug(fmt.Sprintf("Installing certificate %s to Linux system", certType))

	certData, readErr := os.ReadFile(certPath)
	if readErr != nil {
		return fmt.Errorf("failed to read certificate file: %w", readErr)
	}
	if _, parseErr := parseCertificateBytes(certData); parseErr != nil {
		return parseErr
	}

	var installErrs []string
	for _, anchor := range linuxTrustAnchors {
		if len(anchor.UpdateCommand) == 0 {
			continue
		}
		if _, ok := resolveLinuxExecutable(anchor.UpdateCommand[0]); !ok {
			continue
		}

		targetPath := filepath.Join(anchor.Dir, linuxPrimaryCertFileName(certType))
		logger.Debug(fmt.Sprintf("Attempting system-level installation to %s", targetPath))

		if err := writeLinuxSystemCert(targetPath, certData); err != nil {
			installErrs = append(installErrs, fmt.Sprintf("%s: %v", targetPath, err))
			continue
		}

		if err := runLinuxTrustUpdate([][]string{anchor.UpdateCommand}); err != nil {
			installErrs = append(installErrs, fmt.Sprintf("refresh trust after writing %s: %v", targetPath, err))
			continue
		}

		logger.Info(fmt.Sprintf("Certificate installed to Linux system trust anchor %s and CA certificates updated", targetPath))
		return nil
	}

	logger.Info("System-level installation failed, attempting user-level installation")

	// Attempt 2: User-level installation (no sudo needed)
	homeDir, _ := os.UserHomeDir()
	userCertDir := filepath.Join(homeDir, ".local/share/ca-certificates/custom")

	if err := os.MkdirAll(userCertDir, 0700); err != nil {
		logger.Error(fmt.Sprintf("Failed to create certificate directory: %v", err))
		return fmt.Errorf("failed to create certificate directory: %w", err)
	}

	userCertPath := filepath.Join(userCertDir, linuxPrimaryCertFileName(certType))

	if writeErr := os.WriteFile(userCertPath, certData, 0644); writeErr != nil {
		if len(installErrs) > 0 {
			return fmt.Errorf("system install failed (%s); user install failed: %w", strings.Join(installErrs, "; "), writeErr)
		}
		return fmt.Errorf("failed to write certificate file: %w", writeErr)
	}

	if len(installErrs) > 0 {
		logger.Warn(fmt.Sprintf("Certificate installed only to user path after system install failed: %s", strings.Join(installErrs, "; ")))
	}

	logger.Debug(fmt.Sprintf("Certificate installed to Linux user certificate path at %s", userCertPath))
	return nil
}

// Remove deletes certificate from system or user CA directory
func (l *LinuxInstaller) Remove(certType string, certBytes []byte) error {
	config := cert.GetCertConfig(certType)
	if config == nil {
		return fmt.Errorf("unknown certificate type: %s", certType)
	}

	logger.Debug(fmt.Sprintf("Removing certificate %s from Linux system", certType))

	var removedAny bool
	var removeErrs []string
	var updateCommands [][]string

	for _, anchor := range linuxTrustAnchors {
		for _, name := range linuxCertFileNames(certType) {
			path := filepath.Join(anchor.Dir, name)
			if _, err := os.Stat(path); err != nil {
				if !errorsIsNotExist(err) {
					removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, err))
				}
				continue
			}

			if err := removeLinuxSystemPath(path); err != nil {
				removeErrs = append(removeErrs, err.Error())
				continue
			}

			removedAny = true
			updateCommands = append(updateCommands, anchor.UpdateCommand)
			logger.Debug(fmt.Sprintf("Certificate removed from Linux system certificate path: %s", path))
		}
	}

	for _, path := range linuxUserCertPaths(certType) {
		if err := os.Remove(path); err != nil {
			if !errorsIsNotExist(err) {
				removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", path, err))
			}
			continue
		}
		removedAny = true
		logger.Debug(fmt.Sprintf("Certificate removed from Linux user certificate path: %s", path))
	}

	for _, name := range linuxCertFileNames(certType) {
		legacyPath := filepath.Join("/etc/ssl/certs", name)
		if _, err := os.Stat(legacyPath); err != nil {
			if !errorsIsNotExist(err) {
				removeErrs = append(removeErrs, fmt.Sprintf("%s: %v", legacyPath, err))
			}
			continue
		}
		if err := removeLinuxSystemPath(legacyPath); err != nil {
			removeErrs = append(removeErrs, err.Error())
			continue
		}
		removedAny = true
		updateCommands = append(updateCommands, []string{"update-ca-certificates"})
		logger.Debug(fmt.Sprintf("Certificate removed from legacy Linux certificate path: %s", legacyPath))
	}

	if removedAny {
		if len(updateCommands) > 0 {
			if err := runLinuxTrustUpdate(updateCommands); err != nil {
				logger.Warn(fmt.Sprintf("Certificate removed, but failed to refresh Linux CA certificates: %v", err))
			}
		}
		logger.Info("Certificate removal completed across available Linux certificate paths")
		return nil
	}

	if len(removeErrs) > 0 {
		return errors.New(strings.Join(removeErrs, "; "))
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
	installed, _ := l.IsInstalled(certType, certBytes)
	if !installed {
		return "not_found", nil
	}

	// Check if trusted
	trusted, _ := l.IsTrusted(certType, certBytes)
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
			output, err := strategy.run(target, certPath)
			if err == nil {
				logger.Debug(fmt.Sprintf("Certificate installed successfully to Windows certificate store %s via %s", target.PSPath, strategy.name))
				return nil
			}

			trimmedOutput := strings.TrimSpace(string(output))
			logger.Warn(fmt.Sprintf("Failed to install certificate %s to %s via %s: %v, output: %s", certType, target.PSPath, strategy.name, err, trimmedOutput))
			if trimmedOutput == "" {
				trimmedOutput = err.Error()
			}
			failures = append(failures, fmt.Sprintf("%s via %s: %s", target.PSPath, strategy.name, trimmedOutput))
		}
	}

	return fmt.Errorf("certificate installation failed in all Windows stores: %s", strings.Join(failures, "; "))
}

// Remove deletes certificate from Windows certificate store
func (w *WindowsInstaller) Remove(certType string, certBytes []byte) error {
	thumbprint, thumbprintErr := extractCertThumbprint(certBytes)
	commonName, commonNameErr := extractCertCommonName(certBytes)
	if thumbprintErr != nil && commonNameErr != nil {
		logger.Warn(fmt.Sprintf("Failed to extract certificate identity for removal, falling back to config CN: thumbprint=%v commonName=%v", thumbprintErr, commonNameErr))
		config := cert.GetCertConfig(certType)
		if config != nil {
			commonName = config.CN
			commonNameErr = nil
		}
	}

	logger.Debug(fmt.Sprintf("Removing certificate %s from Windows certificate stores", certType))

	removedAny := false
	for _, target := range getWindowsStoreTargets(certType) {
		var body string
		if thumbprintErr == nil {
			body = fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Thumbprint -eq '%s' })
    foreach ($match in $matches) {
        $store.Remove($match)
    }
    Write-Output $matches.Count
`, escapePowerShellSingleQuoted(thumbprint))
		} else if commonNameErr == nil {
			body = fmt.Sprintf(`
    $matches = @($store.Certificates | Where-Object { $_.Subject -like '*%s*' })
    foreach ($match in $matches) {
        $store.Remove($match)
    }
    Write-Output $matches.Count
`, escapePowerShellSingleQuoted(commonName))
		} else {
			continue
		}

		output, err := runWindowsPowerShell(buildWindowsStoreScript(target, "ReadWrite", body))
		if err != nil {
			logger.Warn(fmt.Sprintf("Failed to remove certificate %s from %s: %v, output: %s", certType, target.PSPath, err, strings.TrimSpace(string(output))))
			continue
		}

		if strings.TrimSpace(string(output)) != "0" {
			removedAny = true
		}
	}

	if removedAny {
		logger.Info("Certificate removed successfully from Windows certificate store")
		return nil
	}

	logger.Info("Certificate was not present in Windows certificate stores")
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
	installed, _ := w.IsInstalled(certType, certBytes)
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
