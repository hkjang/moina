package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

// Manager provides purpose-bound AES-256-GCM encryption and keyed hashes. A
// different sub-key is derived for encryption and token verification so the
// bootstrap key is never used directly for both primitives.
type Manager struct {
	aead    cipher.AEAD
	hashKey [32]byte
}

func New(master []byte) (*Manager, error) {
	if len(master) != 32 {
		return nil, errors.New("ENCRYPTION_KEY는 정확히 32바이트여야 합니다")
	}
	encKey := hmacSum(master, []byte("moina:v1:encryption"))
	block, err := aes.NewCipher(encKey[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Manager{aead: aead, hashKey: hmacSum(master, []byte("moina:v1:token-hash"))}, nil
}

func (m *Manager) Encrypt(plain []byte, purpose string) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	body := m.aead.Seal(nil, nonce, plain, []byte(purpose))
	return append(nonce, body...), nil
}

func (m *Manager) Decrypt(value []byte, purpose string) ([]byte, error) {
	if len(value) < m.aead.NonceSize() {
		return nil, errors.New("암호문이 너무 짧습니다")
	}
	nonce, body := value[:m.aead.NonceSize()], value[m.aead.NonceSize():]
	plain, err := m.aead.Open(nil, nonce, body, []byte(purpose))
	if err != nil {
		return nil, fmt.Errorf("복호화 실패: %w", err)
	}
	return plain, nil
}

func (m *Manager) HashToken(token string) string {
	h := hmac.New(sha256.New, m.hashKey[:])
	_, _ = io.WriteString(h, token)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func RandomToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func NewID(prefix string) string {
	value, err := RandomToken(18)
	if err != nil {
		panic(err)
	}
	return prefix + "_" + value
}

func hmacSum(key, value []byte) [32]byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(value)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
