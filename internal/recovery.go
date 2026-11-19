package internal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// encryptRecoveryCode protects the recoverable device code at rest. The
// database still uses recovery_hash for authentication; this ciphertext is
// only returned to devices that already possess a valid device token.
func encryptRecoveryCode(secret, code string) (string, error) {
	key := sha256.Sum256([]byte("law-study-recovery-code:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(code), []byte("law-study-recovery-code:v1"))
	payload := append(nonce, sealed...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decryptRecoveryCode(secret, encrypted string) (string, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encrypted)
	if err != nil {
		return "", err
	}
	key := sha256.Sum256([]byte("law-study-recovery-code:v1:" + secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(payload) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("encrypted recovery code is too short")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, ciphertext, []byte("law-study-recovery-code:v1"))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
