package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
)

// encryptionKey encryption key (32 bytes for AES-256)
// For stability, uses fixed key. In production, should be from environment variables or secure storage
var encryptionKey = []byte("nursorgate-secret-key-2025-token!")[:32]

// EncryptField encrypts a single field string
func EncryptField(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}

	ciphertext, err := encryptBytes([]byte(plaintext))
	if err != nil {
		return "", err
	}

	// Base64 encode for JSON storage
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptField decrypts a single field string
func DecryptField(ciphertext string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}

	// Base64 decode
	decoded, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", fmt.Errorf("failed to decode ciphertext: %w", err)
	}

	plaintext, err := decryptBytes(decoded)
	if err != nil {
		return "", err
	}

	return string(plaintext), nil
}

// encryptBytes encrypts bytes using AES-256-GCM
func encryptBytes(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Use GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decryptBytes decrypts bytes using AES-256-GCM
func decryptBytes(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	// Use GCM mode
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	// Extract nonce (first gcm.NonceSize() bytes)
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}
