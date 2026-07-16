package cert

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	mitmMetadataName   = "mitm-ca.metadata.json"
	mitmPreviousSuffix = ".previous"
	mitmTransaction    = "mitm-ca.importing"
	mitmLockName       = "mitm-ca.lock"
	mitmLockStaleAfter = 30 * time.Second
)

type MITMCAMetadata struct {
	Source         string `json:"source"`
	SourceFormat   string `json:"source_format,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	KeyAlgorithm   string `json:"key_algorithm,omitempty"`
	ActivatedAt    string `json:"activated_at,omitempty"`
	PreviousSource string `json:"previous_source,omitempty"`
}

var mitmStoreMu sync.Mutex

func ActivateMITMCA(material *MITMCAMaterial, source string) (MITMCAMetadata, error) {
	if material == nil || material.Certificate == nil {
		return MITMCAMetadata{}, fmt.Errorf("validated MITM CA material is required")
	}
	mitmStoreMu.Lock()
	defer mitmStoreMu.Unlock()

	release, err := acquireMITMStoreLock()
	if err != nil {
		return MITMCAMetadata{}, err
	}
	defer release()

	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		return MITMCAMetadata{}, err
	}
	keyPath := certPath + ".key"
	metadataPath := filepath.Join(filepath.Dir(certPath), mitmMetadataName)
	stagedCert := certPath + ".next"
	stagedKey := keyPath + ".next"
	stagedMetadata := metadataPath + ".next"
	cleanupPaths := []string{stagedCert, stagedKey, stagedMetadata}
	defer func() {
		for _, path := range cleanupPaths {
			_ = os.Remove(path)
		}
	}()

	if err := writeFileSync(stagedKey, material.PrivateKeyPEM, 0o600); err != nil {
		return MITMCAMetadata{}, fmt.Errorf("stage private key: %w", err)
	}
	if err := writeFileSync(stagedCert, material.CertificatePEM, 0o644); err != nil {
		return MITMCAMetadata{}, fmt.Errorf("stage certificate: %w", err)
	}
	if _, err := ParseMITMCAPEM(material.CertificatePEM, material.PrivateKeyPEM); err != nil {
		return MITMCAMetadata{}, fmt.Errorf("staged certificate pair failed validation: %w", err)
	}

	previousMetadata, _ := GetMITMCAMetadata()
	metadata := MITMCAMetadata{
		Source:         source,
		SourceFormat:   material.SourceFormat,
		Fingerprint:    material.Fingerprint,
		KeyAlgorithm:   material.KeyAlgorithm,
		ActivatedAt:    time.Now().UTC().Format(time.RFC3339),
		PreviousSource: previousMetadata.Source,
	}
	metadataBytes, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return MITMCAMetadata{}, err
	}
	if err := writeFileSync(stagedMetadata, metadataBytes, 0o600); err != nil {
		return MITMCAMetadata{}, fmt.Errorf("stage certificate metadata: %w", err)
	}

	if err := backupCurrentMITMCA(certPath, keyPath, metadataPath); err != nil {
		return MITMCAMetadata{}, err
	}
	transactionPath := filepath.Join(filepath.Dir(certPath), mitmTransaction)
	if err := writeFileSync(transactionPath, []byte(metadata.ActivatedAt), 0o600); err != nil {
		return MITMCAMetadata{}, fmt.Errorf("create certificate transaction marker: %w", err)
	}

	// The certificate is replaced last because CAManager uses its mtime as the
	// commit signal. Readers continue using their cached pair until this step.
	if err := replaceFile(stagedKey, keyPath); err != nil {
		_ = restorePreviousMITMCALocked(certPath, keyPath, metadataPath)
		return MITMCAMetadata{}, fmt.Errorf("activate private key: %w", err)
	}
	if err := replaceFile(stagedCert, certPath); err != nil {
		_ = restorePreviousMITMCALocked(certPath, keyPath, metadataPath)
		return MITMCAMetadata{}, fmt.Errorf("activate certificate: %w", err)
	}
	if err := replaceFile(stagedMetadata, metadataPath); err != nil {
		_ = restorePreviousMITMCALocked(certPath, keyPath, metadataPath)
		return MITMCAMetadata{}, fmt.Errorf("activate certificate metadata: %w", err)
	}
	if err := syncDirectory(filepath.Dir(certPath)); err != nil {
		_ = restorePreviousMITMCALocked(certPath, keyPath, metadataPath)
		return MITMCAMetadata{}, fmt.Errorf("sync certificate directory: %w", err)
	}
	if err := os.Remove(transactionPath); err != nil && !os.IsNotExist(err) {
		return MITMCAMetadata{}, fmt.Errorf("finish certificate transaction: %w", err)
	}
	return metadata, nil
}

func RollbackMITMCA() (MITMCAMetadata, error) {
	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		return MITMCAMetadata{}, err
	}
	previousCert, err := os.ReadFile(certPath + mitmPreviousSuffix)
	if err != nil {
		if os.IsNotExist(err) {
			return MITMCAMetadata{}, fmt.Errorf("no previous MITM CA is available")
		}
		return MITMCAMetadata{}, err
	}
	previousKey, err := os.ReadFile(certPath + ".key" + mitmPreviousSuffix)
	if err != nil {
		return MITMCAMetadata{}, fmt.Errorf("read previous MITM CA key: %w", err)
	}
	material, err := ParseMITMCAPEM(previousCert, previousKey)
	if err != nil {
		return MITMCAMetadata{}, fmt.Errorf("previous MITM CA is invalid: %w", err)
	}
	previousMetadata, _ := readMITMCAMetadata(filepath.Join(filepath.Dir(certPath), mitmMetadataName) + mitmPreviousSuffix)
	source := previousMetadata.Source
	if source == "" {
		source = "legacy"
	}
	return ActivateMITMCA(material, source)
}

func CanRollbackMITMCA() bool {
	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		return false
	}
	if _, err := os.Stat(certPath + mitmPreviousSuffix); err != nil {
		return false
	}
	_, err = os.Stat(certPath + ".key" + mitmPreviousSuffix)
	return err == nil
}

func GetMITMCAMetadata() (MITMCAMetadata, error) {
	certDir, err := GetCertDir()
	if err != nil {
		return MITMCAMetadata{}, err
	}
	return readMITMCAMetadata(filepath.Join(certDir, mitmMetadataName))
}

func RecordMITMCASource(source string) error {
	certPath, err := GetCertPath(CertTypeMitmCA)
	if err != nil {
		return err
	}
	keyData, err := os.ReadFile(certPath + ".key")
	if err != nil {
		return err
	}
	certData, err := os.ReadFile(certPath)
	if err != nil {
		return err
	}
	material, err := ParseMITMCAPEM(certData, keyData)
	if err != nil {
		return err
	}
	metadata := MITMCAMetadata{
		Source:       source,
		SourceFormat: material.SourceFormat,
		Fingerprint:  material.Fingerprint,
		KeyAlgorithm: material.KeyAlgorithm,
		ActivatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSync(filepath.Join(filepath.Dir(certPath), mitmMetadataName), data, 0o600)
}

// RecoverInterruptedMITMCA restores the previous pair when a process stopped
// between the key and certificate commit steps.
func RecoverInterruptedMITMCA() error {
	mitmStoreMu.Lock()
	defer mitmStoreMu.Unlock()
	certDir, err := GetCertDir()
	if err != nil {
		return err
	}
	transactionPath := filepath.Join(certDir, mitmTransaction)
	if _, err := os.Stat(transactionPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lockPath := filepath.Join(certDir, mitmLockName)
	if info, lockErr := os.Stat(lockPath); lockErr == nil {
		if time.Since(info.ModTime()) <= mitmLockStaleAfter {
			return fmt.Errorf("MITM CA activation is currently in progress")
		}
		_ = os.Remove(lockPath)
	} else if !os.IsNotExist(lockErr) {
		return lockErr
	}
	certPath := filepath.Join(certDir, "mitm-ca.pem")
	keyPath := certPath + ".key"
	metadataPath := filepath.Join(certDir, mitmMetadataName)
	if err := restorePreviousMITMCALocked(certPath, keyPath, metadataPath); err != nil {
		return fmt.Errorf("recover interrupted MITM CA activation: %w", err)
	}
	_ = os.Remove(certPath + ".next")
	_ = os.Remove(keyPath + ".next")
	_ = os.Remove(metadataPath + ".next")
	return os.Remove(transactionPath)
}

func backupCurrentMITMCA(certPath, keyPath, metadataPath string) error {
	if _, certErr := os.Stat(certPath); certErr != nil {
		if os.IsNotExist(certErr) {
			_ = os.Remove(certPath + mitmPreviousSuffix)
			_ = os.Remove(keyPath + mitmPreviousSuffix)
			_ = os.Remove(metadataPath + mitmPreviousSuffix)
			return nil
		}
		return certErr
	}
	if _, err := os.Stat(keyPath); err != nil {
		return fmt.Errorf("current MITM CA private key is unavailable: %w", err)
	}
	if err := copyFileSync(certPath, certPath+mitmPreviousSuffix, 0o644); err != nil {
		return fmt.Errorf("backup current MITM CA: %w", err)
	}
	if err := copyFileSync(keyPath, keyPath+mitmPreviousSuffix, 0o600); err != nil {
		return fmt.Errorf("backup current MITM CA key: %w", err)
	}
	if _, err := os.Stat(metadataPath); err == nil {
		if err := copyFileSync(metadataPath, metadataPath+mitmPreviousSuffix, 0o600); err != nil {
			return fmt.Errorf("backup current MITM CA metadata: %w", err)
		}
	} else if os.IsNotExist(err) {
		_ = os.Remove(metadataPath + mitmPreviousSuffix)
	} else {
		return err
	}
	return nil
}

func restorePreviousMITMCALocked(certPath, keyPath, metadataPath string) error {
	previousCert := certPath + mitmPreviousSuffix
	previousKey := keyPath + mitmPreviousSuffix
	if _, err := os.Stat(previousCert); err != nil {
		if os.IsNotExist(err) {
			_ = os.Remove(certPath)
			_ = os.Remove(keyPath)
			_ = os.Remove(metadataPath)
			return nil
		}
		return err
	}
	if err := copyFileSync(previousKey, keyPath+".restore", 0o600); err != nil {
		return err
	}
	if err := copyFileSync(previousCert, certPath+".restore", 0o644); err != nil {
		return err
	}
	if err := replaceFile(keyPath+".restore", keyPath); err != nil {
		return err
	}
	if err := replaceFile(certPath+".restore", certPath); err != nil {
		return err
	}
	previousMetadata := metadataPath + mitmPreviousSuffix
	if _, err := os.Stat(previousMetadata); err == nil {
		if err := copyFileSync(previousMetadata, metadataPath+".restore", 0o600); err != nil {
			return err
		}
		if err := replaceFile(metadataPath+".restore", metadataPath); err != nil {
			return err
		}
	}
	return nil
}

func readMITMCAMetadata(path string) (MITMCAMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MITMCAMetadata{}, err
	}
	var metadata MITMCAMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return MITMCAMetadata{}, err
	}
	return metadata, nil
}

func acquireMITMStoreLock() (func(), error) {
	certDir, err := GetCertDir()
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(certDir, mitmLockName)
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil && errors.Is(err, os.ErrExist) {
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > mitmLockStaleAfter {
			_ = os.Remove(lockPath)
			lock, err = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("another certificate operation is in progress: %w", err)
	}
	_, _ = fmt.Fprintf(lock, "%d\n", os.Getpid())
	_ = lock.Close()
	return func() { _ = os.Remove(lockPath) }, nil
}

func writeFileSync(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func copyFileSync(sourcePath, targetPath string, mode os.FileMode) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(target, source); err != nil {
		target.Close()
		return err
	}
	if err := target.Sync(); err != nil {
		target.Close()
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	return os.Chmod(targetPath, mode)
}

func replaceFile(sourcePath, targetPath string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(sourcePath, targetPath)
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
