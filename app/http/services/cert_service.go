package services

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"aliang.one/nursorgate/common/logger"
	cert_config "aliang.one/nursorgate/processor/cert"
	client_cert "aliang.one/nursorgate/processor/cert/client"
	cert_generator "aliang.one/nursorgate/processor/cert/generator"
	cert_installer "aliang.one/nursorgate/processor/cert/installer"
)

// CertStatusResult holds the status of a certificate
type CertStatusResult struct {
	CertType       string `json:"cert_type"`       // "mitm-ca", "mtls-cert"
	IsExported     bool   `json:"is_exported"`     // Whether exported to file
	IsInstalled    bool   `json:"is_installed"`    // Whether installed to system (file exists)
	IsTrusted      bool   `json:"is_trusted"`      // Whether marked as globally trusted (NEW)
	InstallPath    string `json:"install_path"`    // Installation path
	TrustStatus    string `json:"trust_status"`    // Trust status description (NEW)
	Subject        string `json:"subject"`         // Certificate subject
	Issuer         string `json:"issuer"`          // Certificate issuer
	NotBefore      string `json:"not_before"`      // Valid from date
	NotAfter       string `json:"not_after"`       // Valid until date
	Fingerprint    string `json:"fingerprint"`     // SHA256 fingerprint
	InstalledCount int    `json:"installed_count"` // Number of installed copies
	ExportedPath   string `json:"exported_path"`   // Path where exported
	Source         string `json:"source"`
	SourceFormat   string `json:"source_format,omitempty"`
	KeyAlgorithm   string `json:"key_algorithm,omitempty"`
	ActivatedAt    string `json:"activated_at,omitempty"`
	CanRollback    bool   `json:"can_rollback"`
}

type CertImportResult struct {
	cert_config.MITMCAImportInfo
	Source      string `json:"source"`
	IsInstalled bool   `json:"is_installed"`
	IsTrusted   bool   `json:"is_trusted"`
	CanRollback bool   `json:"can_rollback"`
}

type CertGenerationResult struct {
	CertType   string `json:"cert_type"`
	CertPath   string `json:"cert_path"`
	KeyPath    string `json:"key_path"`
	CN         string `json:"cn"`
	Issuer     string `json:"issuer"`
	ValidYears int    `json:"valid_years"`
}

type certificateOperationError struct {
	stage string
	err   error
}

func (e *certificateOperationError) Error() string { return e.err.Error() }
func (e *certificateOperationError) Unwrap() error { return e.err }

func wrapCertificateOperationError(stage string, err error) error {
	if err == nil || CertificateOperationStage(err) != "" {
		return err
	}
	return &certificateOperationError{stage: stage, err: err}
}

// CertificateOperationStage returns the failed certificate rotation phase.
func CertificateOperationStage(err error) string {
	var operationErr *certificateOperationError
	if errors.As(err, &operationErr) {
		return operationErr.stage
	}
	return ""
}

// SystemInfo holds system information
type SystemInfo struct {
	OS       string `json:"os"`        // "darwin", "linux", "windows"
	UserHome string `json:"user_home"` // User home directory
}

// CertService handles certificate operations
type CertService struct {
	installer cert_installer.CertInstaller
}

var mitmOperationMu sync.Mutex

// NewCertService creates a new certificate service
func NewCertService() *CertService {
	return &CertService{
		installer: cert_installer.NewInstaller(),
	}
}

// NewCertServiceWithInstaller allows platform adapters and tests to supply an
// installer while keeping all certificate lifecycle rules in CertService.
func NewCertServiceWithInstaller(installer cert_installer.CertInstaller) *CertService {
	if installer == nil {
		installer = cert_installer.NewInstaller()
	}
	return &CertService{installer: installer}
}

// GetCertStatus returns the current status of a certificate
func (cs *CertService) GetCertStatus(certType string) (CertStatusResult, error) {
	result := CertStatusResult{
		CertType: certType,
	}

	// Get certificate bytes
	certBytes, err := cs.getCertBytes(certType)
	if err != nil {
		logger.Error(fmt.Sprintf("Failed to get certificate bytes: %v", err))
		return result, err
	}

	// Parse certificate info
	block, _ := pem.Decode(certBytes)
	if block != nil {
		cert, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			result.Subject = cert.Subject.String()
			result.Issuer = cert.Issuer.String()
			result.NotBefore = cert.NotBefore.Format("2006-01-02")
			result.NotAfter = cert.NotAfter.Format("2006-01-02")
		}
	}

	// Get certificate info from installer (includes fingerprint)
	certInfo, err := cs.installer.GetCertInfo(certType, certBytes)
	if err == nil {
		result.Fingerprint = certInfo.Fingerprint
		result.InstallPath = certInfo.InstallPath
		result.InstalledCount = certInfo.InstalledCount
	}

	// Check if exported
	exportedPath := cs.getExportPath(certType)
	if _, err := os.Stat(exportedPath); err == nil {
		result.IsExported = true
		result.ExportedPath = exportedPath
	}
	if certType == cert_config.CertTypeMitmCA {
		result.CanRollback = cert_config.CanRollbackMITMCA()
		if metadata, metadataErr := cert_config.GetMITMCAMetadata(); metadataErr == nil {
			result.Source = metadata.Source
			result.SourceFormat = metadata.SourceFormat
			result.KeyAlgorithm = metadata.KeyAlgorithm
			result.ActivatedAt = metadata.ActivatedAt
		} else {
			result.Source = "legacy"
		}
	}

	// Check if installed (pass certBytes so installer can extract the real CN from the certificate)
	isInstalled, err := cs.installer.IsInstalled(certType, certBytes)
	if err == nil {
		result.IsInstalled = isInstalled
	}

	// Check if trusted (NEW)
	if isInstalled {
		isTrusted, err := cs.installer.IsTrusted(certType, certBytes)
		if err == nil {
			result.IsTrusted = isTrusted
		}
		// Get detailed trust status (NEW)
		trustStatus, err := cs.installer.GetTrustStatus(certType, certBytes)
		if err == nil {
			result.TrustStatus = trustStatus
		}
	} else {
		result.IsTrusted = false
		result.TrustStatus = "not_found"
	}

	logger.Debug(fmt.Sprintf("Certificate %s status: exported=%v, installed=%v, trusted=%v, trustStatus=%s",
		certType, result.IsExported, result.IsInstalled, result.IsTrusted, result.TrustStatus))
	return result, nil
}

// ExportCert exports a certificate to ~/.aliang/ directory
// If the certificate doesn't exist, it will be generated
func (cs *CertService) ExportCert(certType string) (string, error) {
	// Get certificate configuration
	config := cert_config.GetCertConfig(certType)
	if config == nil {
		return "", fmt.Errorf("unsupported certificate type: %s", certType)
	}

	certPath, err := cert_config.GetCertPath(config.CertType)
	if err != nil {
		return "", err
	}

	// Check if certificate already exists in filesystem
	if _, err := os.Stat(certPath); err == nil {
		logger.Debug(fmt.Sprintf("Certificate %s already exists at %s", certType, certPath))
		return certPath, nil
	}

	// Certificate doesn't exist, need to generate it
	logger.Debug(fmt.Sprintf("Certificate %s not found, generating new one at %s", certType, certPath))

	// For all certificate types, generate new certificates
	if err := cs.generateAndExportCert(config, certPath); err != nil {
		return "", fmt.Errorf("failed to generate certificate: %w", err)
	}

	logger.Debug(fmt.Sprintf("Certificate %s exported to %s", certType, certPath))
	return certPath, nil
}

// DownloadCert returns the certificate bytes for download
// If the certificate is installed in the system, download the installed certificate
// Otherwise, ensure the certificate exists in filesystem and return it
func (cs *CertService) DownloadCert(certType string) ([]byte, error) {
	// Check if certificate is installed in system
	certBytes, err := cs.getCertBytes(certType)
	if err != nil {
		return nil, fmt.Errorf("failed to get certificate bytes: %w", err)
	}

	isInstalled, err := cs.installer.IsInstalled(certType, certBytes)
	if err == nil && isInstalled {
		// Certificate is installed, return the installed certificate
		logger.Debug(fmt.Sprintf("Certificate %s is installed, returning installed certificate", certType))
		return certBytes, nil
	}

	// Certificate is not installed, ensure it exists in filesystem and return it
	// This will generate a new certificate if it doesn't exist
	certPath, err := cs.ExportCert(certType)
	if err != nil {
		return nil, fmt.Errorf("failed to export certificate: %w", err)
	}

	// Read the certificate from filesystem
	certBytes, err = os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate from %s: %w", certPath, err)
	}

	logger.Debug(fmt.Sprintf("Certificate %s is not installed, returning generated certificate", certType))
	return certBytes, nil
}

// InstallCert installs a certificate to the system trust store
func (cs *CertService) InstallCert(certType string) error {
	if certType == cert_config.CertTypeMitmCA {
		mitmOperationMu.Lock()
		defer mitmOperationMu.Unlock()
	}
	return cs.installCertUnlocked(certType)
}

func (cs *CertService) installCertUnlocked(certType string) error {
	// First, export the certificate to a temporary location if not already exported
	certPath, err := cs.ExportCert(certType)
	if err != nil {
		return fmt.Errorf("failed to export certificate: %w", err)
	}

	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return fmt.Errorf("failed to read certificate before installation: %w", err)
	}
	if certType == cert_config.CertTypeMitmCA {
		previousState, installErr := cs.ensureCertificateTrusted(certBytes)
		if installErr != nil {
			cleanupErr := cs.cleanupNewlyInstalledCertificate(certBytes, previousState)
			return fmt.Errorf("failed to install certificate: %w", errors.Join(installErr, cleanupErr))
		}
	} else {
		if err := cs.installer.Install(certType, certPath); err != nil {
			return fmt.Errorf("failed to install certificate: %w", err)
		}
		installed, inspectErr := cs.installer.IsInstalled(certType, certBytes)
		if inspectErr != nil {
			return fmt.Errorf("verify installed certificate: %w", inspectErr)
		}
		if !installed {
			return fmt.Errorf("certificate installer completed but the exact certificate is not installed")
		}
	}

	logger.Info(fmt.Sprintf("Certificate %s installed and verified successfully", certType))
	return nil
}

// RemoveCert removes a certificate from the system trust store
func (cs *CertService) RemoveCert(certType string) error {
	if certType == cert_config.CertTypeMitmCA {
		mitmOperationMu.Lock()
		defer mitmOperationMu.Unlock()
	}
	return cs.removeCertUnlocked(certType)
}

func (cs *CertService) removeCertUnlocked(certType string) error {
	// Get certificate bytes for accurate CN extraction
	certBytes, err := cs.getCertBytes(certType)
	if err != nil {
		return fmt.Errorf("failed to get certificate bytes: %w", err)
	}

	if err := cs.installer.Remove(certType, certBytes); err != nil {
		return fmt.Errorf("failed to remove certificate: %w", err)
	}
	installed, inspectErr := cs.installer.IsInstalled(certType, certBytes)
	if inspectErr != nil {
		return fmt.Errorf("verify certificate removal: %w", inspectErr)
	}
	if installed {
		return fmt.Errorf("certificate removal completed but the exact certificate is still installed")
	}
	if certType == cert_config.CertTypeMitmCA {
		trusted, trustErr := cs.installer.IsTrusted(certType, certBytes)
		if trustErr != nil {
			return fmt.Errorf("verify certificate trust removal: %w", trustErr)
		}
		if trusted {
			return fmt.Errorf("certificate removal completed but the exact certificate is still trusted")
		}
	}

	logger.Info(fmt.Sprintf("Certificate %s removed successfully", certType))
	return nil
}

func (cs *CertService) ValidateMITMCAImport(certPEM, keyPEM, bundle []byte, password string) (CertImportResult, error) {
	material, err := parseMITMCAImport(certPEM, keyPEM, bundle, password)
	if err != nil {
		return CertImportResult{}, err
	}
	return cs.importResult(material), nil
}

func (cs *CertService) ImportMITMCA(certPEM, keyPEM, bundle []byte, password string) (CertImportResult, error) {
	material, err := parseMITMCAImport(certPEM, keyPEM, bundle, password)
	if err != nil {
		return CertImportResult{}, err
	}

	mitmOperationMu.Lock()
	defer mitmOperationMu.Unlock()
	if err := cs.activateTrustedMITMCA(material, "imported"); err != nil {
		return CertImportResult{}, err
	}
	result := cs.importResult(material)
	if metadata, metadataErr := cert_config.GetMITMCAMetadata(); metadataErr == nil && metadata.Source != "" {
		result.Source = metadata.Source
	}
	return result, nil
}

func (cs *CertService) RollbackMITMCA() (CertImportResult, error) {
	mitmOperationMu.Lock()
	defer mitmOperationMu.Unlock()

	currentCert, err := cs.getMitmCACert()
	if err != nil {
		return CertImportResult{}, err
	}
	currentState, err := cs.certificateTrustState(currentCert)
	if err != nil {
		return CertImportResult{}, err
	}
	if _, err := cert_config.RollbackMITMCA(); err != nil {
		return CertImportResult{}, err
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		_, rollbackErr := cert_config.RollbackMITMCA()
		var reloadRecoveryErr error
		if rollbackErr == nil {
			reloadRecoveryErr = client_cert.DefaultCAManager().Reload()
		}
		return CertImportResult{}, errors.Join(fmt.Errorf("previous MITM CA was restored on disk but could not be reloaded: %w", err), rollbackErr, reloadRecoveryErr)
	}
	restoredCert, err := cs.getMitmCACert()
	if err != nil {
		return CertImportResult{}, err
	}
	restoredPreviousState, err := cs.ensureCertificateTrusted(restoredCert)
	if err != nil {
		_, rollbackErr := cert_config.RollbackMITMCA()
		var reloadErr, cleanupErr error
		if rollbackErr == nil {
			reloadErr = client_cert.DefaultCAManager().Reload()
			if reloadErr == nil {
				cleanupErr = cs.cleanupNewlyInstalledCertificate(restoredCert, restoredPreviousState)
			}
		}
		return CertImportResult{}, errors.Join(fmt.Errorf("failed to restore previous CA trust: %w", err), rollbackErr, reloadErr, cleanupErr)
	}
	if !sameCertificateFingerprint(currentCert, restoredCert) && currentState.installed {
		if err := cs.installer.Remove(cert_config.CertTypeMitmCA, currentCert); err != nil {
			recoveryErr := cs.restoreCurrentAfterFailedRollback(currentCert, currentState, restoredCert, restoredPreviousState)
			return CertImportResult{}, errors.Join(fmt.Errorf("failed to remove superseded CA during rollback: %w", err), recoveryErr)
		}
	}
	material, err := cs.activeMITMMaterial()
	if err != nil {
		return CertImportResult{}, err
	}
	result := cs.importResult(material)
	if metadata, metadataErr := cert_config.GetMITMCAMetadata(); metadataErr == nil && metadata.Source != "" {
		result.Source = metadata.Source
	}
	return result, nil
}

func (cs *CertService) RegenerateCert(certType string) (CertGenerationResult, error) {
	config := cert_config.GetCertConfig(certType)
	if config == nil {
		return CertGenerationResult{}, wrapCertificateOperationError("generate", fmt.Errorf("unsupported certificate type: %s", certType))
	}
	certPath, err := cert_config.GetCertPath(config.CertType)
	if err != nil {
		return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
	}
	if certType == cert_config.CertTypeMitmCA {
		mitmOperationMu.Lock()
		defer mitmOperationMu.Unlock()

		tempPath := certPath + ".generated"
		defer os.Remove(tempPath)
		defer os.Remove(tempPath + ".key")
		if err := cert_generator.GenerateCertificateFromConfig(config, tempPath); err != nil {
			return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
		}
		certPEM, err := os.ReadFile(tempPath)
		if err != nil {
			return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
		}
		keyPEM, err := os.ReadFile(tempPath + ".key")
		if err != nil {
			return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
		}
		material, err := cert_config.ParseMITMCAPEM(certPEM, keyPEM)
		if err != nil {
			return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
		}
		if err := cs.activateTrustedMITMCA(material, "generated"); err != nil {
			return CertGenerationResult{}, err
		}
	} else if err := cs.generateAndExportCert(config, certPath); err != nil {
		return CertGenerationResult{}, wrapCertificateOperationError("generate", err)
	}
	return CertGenerationResult{
		CertType: certType, CertPath: certPath, KeyPath: certPath + ".key",
		CN: config.CN, Issuer: config.Issuer, ValidYears: config.ValidityYears,
	}, nil
}

// GetSystemInfo returns system information
func (cs *CertService) GetSystemInfo() (SystemInfo, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		cacheDir, cacheErr := cert_config.GetCertDir()
		if cacheErr == nil {
			homeDir = cacheDir
		} else {
			homeDir = "~"
		}
	}

	return SystemInfo{
		OS:       runtime.GOOS,
		UserHome: homeDir,
	}, nil
}

// ============= Private Helper Methods =============

// getCertBytes returns the certificate bytes for a given certificate type
func (cs *CertService) getCertBytes(certType string) ([]byte, error) {
	switch certType {
	case "mitm-ca":
		return cs.getMitmCACert()
	case "mtls-cert":
		return cs.getMTLSCert()
	default:
		return nil, fmt.Errorf("unsupported certificate type: %s", certType)
	}
}

// getMitmCACert returns the MITM CA certificate bytes.
// 统一走 CAManager.Get —— 装证书与 MITM 签证书从此读同一个 CA（文件即真相 + mtime
// 自动追随），不再旁路 os.ReadFile 造成与 MITM 内存缓存不一致。
func (cs *CertService) getMitmCACert() ([]byte, error) {
	caCert, err := client_cert.DefaultCAManager().Get()
	if err != nil {
		return nil, fmt.Errorf("failed to load MITM CA certificate: %w", err)
	}
	if len(caCert.Certificate) == 0 {
		return nil, fmt.Errorf("MITM CA certificate has empty chain")
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Certificate[0]}), nil
}

// getMTLSCert returns the mTLS certificate bytes from filesystem
func (cs *CertService) getMTLSCert() ([]byte, error) {
	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMtlsClient)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		if _, exportErr := cs.ExportCert("mtls-cert"); exportErr != nil {
			return nil, fmt.Errorf("mTLS certificate not found at %s and auto-export failed: %w", certPath, exportErr)
		}
	}

	return os.ReadFile(certPath)
}

// getExportPath returns the export path for a certificate type
func (cs *CertService) getExportPath(certType string) string {
	certDir, err := cert_config.GetCertDir()
	if err != nil {
		certDir = filepath.Join(os.TempDir(), "aliang")
	}

	switch certType {
	case "mitm-ca":
		return filepath.Join(certDir, "mitm-ca.pem")
	case "mtls-cert":
		return filepath.Join(certDir, "mtls-client.pem")
	default:
		return filepath.Join(certDir, certType+".pem")
	}
}

// generateAndExportCert generates a new certificate and exports it to the specified path
func (cs *CertService) generateAndExportCert(config *cert_config.CertConfig, certPath string) error {
	// Ensure directory exists
	certDir := filepath.Dir(certPath)
	if err := os.MkdirAll(certDir, 0700); err != nil {
		return fmt.Errorf("failed to create certificate directory: %w", err)
	}

	// Generate certificate using the generator
	if err := cert_generator.GenerateCertificateFromConfig(config, certPath); err != nil {
		return fmt.Errorf("failed to generate certificate: %w", err)
	}

	logger.Debug(fmt.Sprintf("Generated new certificate for %s at %s", config.CertType, certPath))

	// 若生成的是 MITM CA，立即刷新 CAManager：文件已变，让内存追随 + 清空域名证书缓存，
	// 避免「装证书读新 CA、MITM 还用旧 CA 缓存」的不一致（无需重启进程）。
	if config.CertType == cert_config.CertTypeMitmCA {
		if err := cert_config.RecordMITMCASource("generated"); err != nil {
			logger.Warn(fmt.Sprintf("Failed to record generated MITM CA metadata: %v", err))
		}
		if err := client_cert.DefaultCAManager().Reload(); err != nil {
			logger.Warn(fmt.Sprintf("CAManager reload after regenerating mitm-ca failed: %v", err))
		}
	}
	return nil
}

func parseMITMCAImport(certPEM, keyPEM, bundle []byte, password string) (*cert_config.MITMCAMaterial, error) {
	if len(bundle) > 0 {
		if len(certPEM) > 0 || len(keyPEM) > 0 {
			return nil, fmt.Errorf("provide either a PKCS#12 bundle or a PEM certificate pair, not both")
		}
		return cert_config.ParseMITMCAPKCS12(bundle, password)
	}
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return nil, fmt.Errorf("both certificate and private key files are required")
	}
	return cert_config.ParseMITMCAPEM(certPEM, keyPEM)
}

func (cs *CertService) importResult(material *cert_config.MITMCAMaterial) CertImportResult {
	result := CertImportResult{
		MITMCAImportInfo: material.ImportInfo(),
		Source:           "imported",
		CanRollback:      cert_config.CanRollbackMITMCA(),
	}
	if installed, err := cs.installer.IsInstalled(cert_config.CertTypeMitmCA, material.CertificatePEM); err == nil {
		result.IsInstalled = installed
	}
	if trusted, err := cs.installer.IsTrusted(cert_config.CertTypeMitmCA, material.CertificatePEM); err == nil {
		result.IsTrusted = trusted
	}
	return result
}

type certificateTrustState struct {
	installed bool
	trusted   bool
}

func (cs *CertService) certificateTrustState(certBytes []byte) (certificateTrustState, error) {
	installed, err := cs.installer.IsInstalled(cert_config.CertTypeMitmCA, certBytes)
	if err != nil {
		return certificateTrustState{}, fmt.Errorf("inspect certificate installation: %w", err)
	}
	if !installed {
		return certificateTrustState{}, nil
	}
	trusted, err := cs.installer.IsTrusted(cert_config.CertTypeMitmCA, certBytes)
	if err != nil {
		return certificateTrustState{}, fmt.Errorf("inspect certificate trust: %w", err)
	}
	return certificateTrustState{installed: true, trusted: trusted}, nil
}

func (cs *CertService) ensureCertificateTrusted(certBytes []byte) (certificateTrustState, error) {
	previousState, err := cs.certificateTrustState(certBytes)
	if err != nil {
		return certificateTrustState{}, err
	}
	if previousState.trusted {
		return previousState, nil
	}

	tempFile, err := os.CreateTemp("", "aliang-install-ca-*.pem")
	if err != nil {
		return previousState, err
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)
	if err := tempFile.Chmod(0o600); err != nil {
		tempFile.Close()
		return previousState, err
	}
	if _, err := tempFile.Write(certBytes); err != nil {
		tempFile.Close()
		return previousState, err
	}
	if err := tempFile.Close(); err != nil {
		return previousState, err
	}
	installErr := cs.installer.Install(cert_config.CertTypeMitmCA, tempPath)
	after, inspectErr := cs.certificateTrustState(certBytes)
	if inspectErr != nil {
		return previousState, errors.Join(installErr, inspectErr)
	}
	if after.installed && after.trusted {
		return previousState, nil
	}
	if installErr != nil {
		return previousState, installErr
	}
	if !after.installed || !after.trusted {
		return previousState, fmt.Errorf("certificate installer completed without establishing system trust")
	}
	return previousState, nil
}

func (cs *CertService) activateTrustedMITMCA(material *cert_config.MITMCAMaterial, source string) error {
	oldCert, err := cs.getMitmCACert()
	if err != nil {
		return wrapCertificateOperationError("verify", fmt.Errorf("load current MITM CA before activation: %w", err))
	}
	oldState, err := cs.certificateTrustState(oldCert)
	if err != nil {
		return wrapCertificateOperationError("verify", err)
	}
	newPreviousState, err := cs.ensureCertificateTrusted(material.CertificatePEM)
	if err != nil {
		cleanupErr := cs.cleanupNewlyInstalledCertificate(material.CertificatePEM, newPreviousState)
		return wrapCertificateOperationError("install", errors.Join(fmt.Errorf("install new MITM CA before activation: %w", err), cleanupErr))
	}

	if _, err := cert_config.ActivateMITMCA(material, source); err != nil {
		cleanupErr := cs.cleanupNewlyInstalledCertificate(material.CertificatePEM, newPreviousState)
		return wrapCertificateOperationError("activate", errors.Join(fmt.Errorf("activate %s MITM CA: %w", source, err), cleanupErr))
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		recoveryErr := cs.recoverPreviousMITMCA(oldCert, oldState, material.CertificatePEM, newPreviousState)
		return wrapCertificateOperationError("activate", errors.Join(fmt.Errorf("reload %s MITM CA: %w", source, err), recoveryErr))
	}

	if !sameCertificateFingerprint(oldCert, material.CertificatePEM) && oldState.installed {
		if err := cs.installer.Remove(cert_config.CertTypeMitmCA, oldCert); err != nil {
			recoveryErr := cs.recoverPreviousMITMCA(oldCert, oldState, material.CertificatePEM, newPreviousState)
			return wrapCertificateOperationError("remove", errors.Join(fmt.Errorf("remove superseded MITM CA: %w", err), recoveryErr))
		}
	}

	newState, err := cs.certificateTrustState(material.CertificatePEM)
	if err != nil {
		return wrapCertificateOperationError("verify", err)
	}
	if !newState.installed || !newState.trusted {
		recoveryErr := cs.recoverPreviousMITMCA(oldCert, oldState, material.CertificatePEM, newPreviousState)
		return wrapCertificateOperationError("verify", errors.Join(fmt.Errorf("new MITM CA is active but system trust verification failed"), recoveryErr))
	}
	return nil
}

func (cs *CertService) recoverPreviousMITMCA(oldCert []byte, oldState certificateTrustState, newCert []byte, newPreviousState certificateTrustState) error {
	if _, err := cert_config.RollbackMITMCA(); err != nil {
		return fmt.Errorf("rollback active MITM CA: %w", err)
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		_, forwardErr := cert_config.RollbackMITMCA()
		var forwardReloadErr error
		if forwardErr == nil {
			forwardReloadErr = client_cert.DefaultCAManager().Reload()
		}
		return errors.Join(fmt.Errorf("reload previous MITM CA: %w", err), forwardErr, forwardReloadErr)
	}
	if err := cs.restoreTrustState(oldCert, oldState); err != nil {
		_, forwardErr := cert_config.RollbackMITMCA()
		var forwardReloadErr error
		if forwardErr == nil {
			forwardReloadErr = client_cert.DefaultCAManager().Reload()
		}
		return errors.Join(fmt.Errorf("restore previous MITM CA trust: %w", err), forwardErr, forwardReloadErr)
	}
	return cs.cleanupNewlyInstalledCertificate(newCert, newPreviousState)
}

func (cs *CertService) restoreCurrentAfterFailedRollback(currentCert []byte, currentState certificateTrustState, restoredCert []byte, restoredPreviousState certificateTrustState) error {
	if _, err := cert_config.RollbackMITMCA(); err != nil {
		return fmt.Errorf("restore current MITM CA after failed rollback: %w", err)
	}
	if err := client_cert.DefaultCAManager().Reload(); err != nil {
		return fmt.Errorf("reload current MITM CA after failed rollback: %w", err)
	}
	if err := cs.restoreTrustState(currentCert, currentState); err != nil {
		return fmt.Errorf("restore current MITM CA trust after failed rollback: %w", err)
	}
	return cs.cleanupNewlyInstalledCertificate(restoredCert, restoredPreviousState)
}

func (cs *CertService) restoreTrustState(certBytes []byte, state certificateTrustState) error {
	if !state.trusted {
		return nil
	}
	_, err := cs.ensureCertificateTrusted(certBytes)
	return err
}

func (cs *CertService) cleanupNewlyInstalledCertificate(certBytes []byte, previousState certificateTrustState) error {
	if previousState.installed {
		return nil
	}
	return cs.installer.Remove(cert_config.CertTypeMitmCA, certBytes)
}

func (cs *CertService) activeMITMMaterial() (*cert_config.MITMCAMaterial, error) {
	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		return nil, err
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(certPath + ".key")
	if err != nil {
		return nil, err
	}
	return cert_config.ParseMITMCAPEM(certPEM, keyPEM)
}

func sameCertificateFingerprint(left, right []byte) bool {
	leftCert, leftErr := x509CertificateFingerprint(left)
	rightCert, rightErr := x509CertificateFingerprint(right)
	return leftErr == nil && rightErr == nil && leftCert == rightCert
}

func x509CertificateFingerprint(certBytes []byte) (string, error) {
	block, _ := pem.Decode(certBytes)
	if block == nil {
		return "", fmt.Errorf("failed to decode certificate PEM")
	}
	parsed, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return string(parsed.Raw), nil
}
