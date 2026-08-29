package config

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestLoadOnlyFourEnvironmentValues(t *testing.T) {
	t.Setenv(EnvPostgresDSN, "postgres://moina:test@localhost/moina")
	t.Setenv(EnvBootstrapAdmin, "admin")
	t.Setenv(EnvBootstrapPassword, "안전한-bootstrap-password")
	t.Setenv(EnvEncryptionKey, base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BootstrapAdmin != "admin" || len(cfg.EncryptionKey) != 32 {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestParseEncryptionKeyRejectsWeakInput(t *testing.T) {
	if _, err := ParseEncryptionKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Fatal("short key was accepted")
	}
}
