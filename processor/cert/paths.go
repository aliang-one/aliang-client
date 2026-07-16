package cert

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aliang.one/nursorgate/common/cache"
)

func GetCertDir() (string, error) {
	stateDir, err := cache.GetCacheDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve certificate directory: %w", err)
	}
	certDir := filepath.Join(stateDir, "certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create certificate directory: %w", err)
	}
	if err := os.Chmod(certDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to secure certificate directory: %w", err)
	}
	return certDir, nil
}

func GetCertPath(certType string) (string, error) {
	certDir, err := GetCertDir()
	if err != nil {
		return "", err
	}

	var filename string
	switch certType {
	case CertTypeMitmCA:
		filename = "mitm-ca.pem"
	case CertTypeMtlsClient:
		filename = "mtls-client.pem"
	default:
		return "", fmt.Errorf("unsupported certificate type: %s", certType)
	}

	certPath := filepath.Join(certDir, filename)
	if err := migrateLegacyPair(certPath); err != nil {
		return "", err
	}
	return certPath, nil
}

func GetCertKeyPath(certType string) (string, error) {
	certPath, err := GetCertPath(certType)
	if err != nil {
		return "", err
	}
	return certPath + ".key", nil
}

// migrateLegacyPair keeps existing installations working after certificates
// moved from the shared state root into the private certs directory.
func migrateLegacyPair(certPath string) error {
	if _, err := os.Stat(certPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	stateDir, err := cache.GetCacheDir()
	if err != nil {
		return err
	}
	legacyCert := filepath.Join(stateDir, filepath.Base(certPath))
	legacyKey := legacyCert + ".key"
	if _, err := os.Stat(legacyCert); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if _, err := os.Stat(legacyKey); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if err := copyLegacyFile(legacyKey, certPath+".key", 0o600); err != nil {
		return fmt.Errorf("migrate legacy certificate key: %w", err)
	}
	if err := copyLegacyFile(legacyCert, certPath, 0o644); err != nil {
		_ = os.Remove(certPath + ".key")
		return fmt.Errorf("migrate legacy certificate: %w", err)
	}
	return nil
}

func copyLegacyFile(sourcePath, targetPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	tempPath := targetPath + ".migrate"
	target, err := os.OpenFile(tempPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := target.Sync(); err != nil {
		target.Close()
		_ = os.Remove(tempPath)
		return err
	}
	if err := target.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return os.Rename(tempPath, targetPath)
}
