package cert

import (
	"fmt"
	"path/filepath"

	"aliang.one/nursorgate/common/cache"
)

func GetCertDir() (string, error) {
	certDir, err := cache.GetCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve certificate directory: %w", err)
	}
	return certDir, nil
}

func GetCertPath(certType string) (string, error) {
	certDir, err := GetCertDir()
	if err != nil {
		return "", err
	}

	switch certType {
	case CertTypeMitmCA:
		return filepath.Join(certDir, "mitm-ca.pem"), nil
	case CertTypeMtlsClient:
		return filepath.Join(certDir, "mtls-client.pem"), nil
	default:
		return "", fmt.Errorf("unsupported certificate type: %s", certType)
	}
}

func GetCertKeyPath(certType string) (string, error) {
	certPath, err := GetCertPath(certType)
	if err != nil {
		return "", err
	}
	return certPath + ".key", nil
}
