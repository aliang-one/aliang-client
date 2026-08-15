package user

import (
	"aliang.one/nursorgate/processor/crypto"
)

// EncryptField 加密单个字段
func EncryptField(plaintext string) (string, error) {
	return crypto.EncryptField(plaintext)
}

// DecryptField 解密单个字段
func DecryptField(ciphertext string) (string, error) {
	return crypto.DecryptField(ciphertext)
}
