package services

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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

// NewCertService creates a new certificate service
func NewCertService() *CertService {
	return &CertService{
		installer: cert_installer.NewInstaller(),
	}
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
	// First, export the certificate to a temporary location if not already exported
	certPath, err := cs.ExportCert(certType)
	if err != nil {
		return fmt.Errorf("failed to export certificate: %w", err)
	}

	// Install certificate
	if err := cs.installer.Install(certType, certPath); err != nil {
		return fmt.Errorf("failed to install certificate: %w", err)
	}

	logger.Info(fmt.Sprintf("Certificate %s install flow started successfully", certType))
	return nil
}

// RemoveCert removes a certificate from the system trust store
func (cs *CertService) RemoveCert(certType string) error {
	// Get certificate bytes for accurate CN extraction
	certBytes, err := cs.getCertBytes(certType)
	if err != nil {
		return fmt.Errorf("failed to get certificate bytes: %w", err)
	}

	if err := cs.installer.Remove(certType, certBytes); err != nil {
		return fmt.Errorf("failed to remove certificate: %w", err)
	}

	logger.Info(fmt.Sprintf("Certificate %s removed successfully", certType))
	return nil
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

// getMitmCACert returns the MITM CA certificate bytes from filesystem
func (cs *CertService) getMitmCACert() ([]byte, error) {
	certPath, err := cert_config.GetCertPath(cert_config.CertTypeMitmCA)
	if err != nil {
		return nil, err
	}

	// Check if file exists
	if _, err := os.Stat(certPath); os.IsNotExist(err) {
		// Try to generate it through client_cert
		certBytes, err := client_cert.LoadMitmCACertificate()
		if err != nil {
			return nil, fmt.Errorf("failed to load MITM CA certificate: %w", err)
		}
		return certBytes.Certificate[0], nil
	}

	return os.ReadFile(certPath)
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
	return nil
}
