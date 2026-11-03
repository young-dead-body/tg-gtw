package password

import (
	"strings"
	"testing"
)

func TestHashAndVerify(t *testing.T) {
	h := New()
	plain := "correct horse battery staple"

	encoded, err := h.Hash(plain)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected non-empty hash")
	}

	ok, err := h.Verify(plain, encoded)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Error("expected password to verify")
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	h := New()
	encoded, err := h.Hash("right-password")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	ok, err := h.Verify("wrong-password", encoded)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if ok {
		t.Error("expected mismatch")
	}
}

func TestVerifyInvalidHash(t *testing.T) {
	h := New()
	if _, err := h.Verify("x", "not-an-argon2-hash"); err == nil {
		t.Error("expected error for malformed hash")
	}
}

func TestHashFormat(t *testing.T) {
	h := New()
	encoded, err := h.Hash("abc")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}

	wantPrefix := "$argon2id$v=19$m="
	if !strings.HasPrefix(encoded, wantPrefix) {
		t.Errorf("expected prefix %q, got %q", wantPrefix, encoded)
	}
}

func TestHashesAreUnique(t *testing.T) {
	h := New()
	a, _ := h.Hash("same")
	b, _ := h.Hash("same")
	if a == b {
		t.Error("expected unique salts to produce different hashes")
	}
}
