package secure

import (
	"bytes"
	"testing"
)

func TestPurposeBoundEncryption(t *testing.T) {
	m, err := New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := m.Encrypt([]byte("secret"), "setting:oidc")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := m.Decrypt(ciphertext, "setting:oidc")
	if err != nil || string(plain) != "secret" {
		t.Fatalf("round trip failed: %q %v", plain, err)
	}
	if _, err := m.Decrypt(ciphertext, "setting:ai"); err == nil {
		t.Fatal("ciphertext decrypted under another purpose")
	}
}

func TestTokenHashesAreStableAndKeyed(t *testing.T) {
	a, _ := New(bytes.Repeat([]byte{1}, 32))
	b, _ := New(bytes.Repeat([]byte{2}, 32))
	first, repeated := a.HashToken("token"), a.HashToken("token")
	if first != repeated {
		t.Fatal("hashing the same token twice produced different values")
	}
	if first == b.HashToken("token") {
		t.Fatal("different root keys produced the same token hash")
	}
}
