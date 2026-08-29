package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	EnvPostgresDSN       = "MOINA_POSTGRES_DSN"
	EnvBootstrapAdmin    = "MOINA_BOOTSTRAP_ADMIN"
	EnvBootstrapPassword = "MOINA_BOOTSTRAP_ADMIN_PASSWORD"
	EnvEncryptionKey     = "MOINA_ENCRYPTION_KEY"
)

// Config intentionally represents the complete runtime environment contract.
// Every setting other than these four bootstrap values is stored in PostgreSQL
// and managed through the administrator API.
type Config struct {
	PostgresDSN       string
	BootstrapAdmin    string
	BootstrapPassword string
	EncryptionKey     []byte
}

func Load() (Config, error) {
	cfg := Config{
		PostgresDSN:       strings.TrimSpace(os.Getenv(EnvPostgresDSN)),
		BootstrapAdmin:    strings.TrimSpace(os.Getenv(EnvBootstrapAdmin)),
		BootstrapPassword: os.Getenv(EnvBootstrapPassword),
	}
	var missing []string
	if cfg.PostgresDSN == "" {
		missing = append(missing, EnvPostgresDSN)
	}
	if cfg.BootstrapAdmin == "" {
		missing = append(missing, EnvBootstrapAdmin)
	}
	if cfg.BootstrapPassword == "" {
		missing = append(missing, EnvBootstrapPassword)
	}
	if os.Getenv(EnvEncryptionKey) == "" {
		missing = append(missing, EnvEncryptionKey)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("필수 환경변수가 없습니다: %s", strings.Join(missing, ", "))
	}
	if !validIdentifier(cfg.BootstrapAdmin) {
		return Config{}, errors.New("bootstrap 관리자 아이디는 줄바꿈 없이 1~120자여야 합니다")
	}
	if !utf8.ValidString(cfg.BootstrapPassword) || utf8.RuneCountInString(cfg.BootstrapPassword) < 12 || len([]byte(cfg.BootstrapPassword)) > 72 {
		return Config{}, errors.New("bootstrap 관리자 비밀번호는 UTF-8 기준 12자 이상, 72바이트 이하여야 합니다")
	}
	key, err := ParseEncryptionKey(os.Getenv(EnvEncryptionKey))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", EnvEncryptionKey, err)
	}
	cfg.EncryptionKey = key
	return cfg, nil
}

func validIdentifier(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 120 && !strings.ContainsAny(value, "\x00\r\n")
}

func ParseEncryptionKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("값이 비어 있습니다")
	}
	decoders := []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString, hex.DecodeString}
	for _, decode := range decoders {
		if key, err := decode(raw); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	return nil, errors.New("base64 또는 hex로 인코딩한 정확히 32바이트 키여야 합니다")
}
