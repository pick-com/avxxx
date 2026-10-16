package store

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/argon2"
)

func TestHashPasswordFormatAndVerify(t *testing.T) {
	h, err := hashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected PHC prefix: %s", h)
	}
	if parts := strings.Split(h, "$"); len(parts) != 6 {
		t.Fatalf("expected 6 PHC segments, got %d", len(parts))
	}
	if isLegacyHash(h) {
		t.Fatal("argon2id hash detected as legacy")
	}
	ok, rehash := verifyPassword(h, "correct horse battery")
	if !ok || rehash {
		t.Fatalf("verify correct: ok=%v rehash=%v", ok, rehash)
	}
	if ok, _ := verifyPassword(h, "wrong password"); ok {
		t.Fatal("wrong password verified")
	}
}

func TestHashPasswordIsSalted(t *testing.T) {
	a, _ := hashPassword("same password")
	b, _ := hashPassword("same password")
	if a == b {
		t.Fatal("two hashes of the same password are identical; salt missing")
	}
}

func TestLegacyHashDetection(t *testing.T) {
	sum := sha256.Sum256([]byte("password123"))
	legacy := hex.EncodeToString(sum[:])
	cases := map[string]bool{
		legacy:                        true,
		strings.ToUpper(legacy):       true,
		legacy[:63]:                   false,
		legacy + "0":                  false,
		strings.Repeat("z", 64):       false,
		"$argon2id$v=19$m=1,t=1,p=1$": false,
		"":                            false,
	}
	for in, want := range cases {
		if got := isLegacyHash(in); got != want {
			t.Errorf("isLegacyHash(%q)=%v want %v", in, got, want)
		}
	}
}

func TestLegacyHashUpgradeDecision(t *testing.T) {
	legacy := legacySHA256Hash("password123")
	ok, rehash := verifyPassword(legacy, "password123")
	if !ok || !rehash {
		t.Fatalf("legacy match should verify and request rehash: ok=%v rehash=%v", ok, rehash)
	}
	ok, rehash = verifyPassword(legacy, "password124")
	if ok || rehash {
		t.Fatalf("legacy mismatch must not verify or rehash: ok=%v rehash=%v", ok, rehash)
	}
}

func TestOutdatedArgonParamsRequestRehash(t *testing.T) {
	// Hash produced with weaker parameters still verifies but must be upgraded.
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("pw-12345678"), salt, 1, 8*1024, 1, 32)
	b64 := base64.RawStdEncoding
	weak := fmt.Sprintf("$argon2id$v=19$m=8192,t=1,p=1$%s$%s", b64.EncodeToString(salt), b64.EncodeToString(key))
	if ok, rehash := verifyPassword(weak, "pw-12345678"); !ok || !rehash {
		t.Fatalf("weak params: ok=%v rehash=%v", ok, rehash)
	}
	h, _ := hashPassword("pw-12345678")
	// Changing the encoded params without re-deriving must fail verification.
	tampered := strings.Replace(h, "t=3", "t=1", 1)
	if ok, _ := verifyPassword(tampered, "pw-12345678"); ok {
		t.Fatal("tampered params verified")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=65536,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=3,p=2$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$!!!$aGFzaA",
		"$argon2id$v=19$m=65536,t=3,p=2$c2FsdA$",
	} {
		if ok, _ := verifyPassword(s, "anything"); ok {
			t.Errorf("malformed %q verified", s)
		}
	}
}

func TestHashToken(t *testing.T) {
	token := strings.Repeat("ab", 32)
	got := hashToken(token)
	sum := sha256.Sum256([]byte(token))
	if want := hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("hashToken=%s want %s", got, want)
	}
	if len(got) != 64 || got == token {
		t.Fatalf("unexpected token hash %q", got)
	}
	if hashToken(token) != got {
		t.Fatal("hashToken not deterministic")
	}
	if hashToken(token+"x") == got {
		t.Fatal("different tokens share a hash")
	}
}

func TestErrEmailTakenWrapping(t *testing.T) {
	err := fmt.Errorf("%w: %w", ErrEmailTaken, errors.New("duplicate key value violates unique constraint"))
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatal("wrapped error does not match ErrEmailTaken")
	}
}
