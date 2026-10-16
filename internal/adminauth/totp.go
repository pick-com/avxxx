// Package adminauth holds the back-office account primitives that do not
// touch the database: RFC 6238 TOTP, sealing of TOTP secrets at rest,
// recovery codes, the login lockout schedule and the role → permission
// matrix (tech plan 3.6, artboard AdminUsers).
//
// TOTP is implemented here (about 60 lines over crypto/hmac) instead of
// pulling a third-party module: RFC 6238 is small, the test vectors are in
// the RFC, and it keeps the dependency list unchanged.
package adminauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters used for every admin account: the authenticator-app
// defaults (SHA-1, 6 digits, 30 s), which every app supports.
const (
	TOTPPeriod = 30
	TOTPDigits = 6
	// TOTPSkew is how many steps either side of now are accepted (clock drift).
	TOTPSkew = 1
	// SecretBytes is the shared secret length (RFC 4226 recommends ≥ 160 bits).
	SecretBytes = 20
	// Issuer is shown in the authenticator app.
	Issuer = "REEL Admin"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random TOTP secret, base32 without padding.
func NewSecret() (string, error) {
	b := make([]byte, SecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// DecodeSecret parses a base32 secret (case-insensitive, spaces and padding ignored).
func DecodeSecret(s string) ([]byte, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "=", "").Replace(s))
	k, err := b32.DecodeString(s)
	if err != nil || len(k) == 0 {
		return nil, errors.New("invalid TOTP secret")
	}
	return k, nil
}

// HOTP is RFC 4226 section 5.3 with a configurable hash and digit count.
func HOTP(key []byte, counter uint64, digits int, h func() hash.Hash) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(h, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// Step is the RFC 6238 time step T for t (T0 = 0).
func Step(t time.Time) uint64 { return uint64(t.Unix()) / TOTPPeriod }

// Hash returns the HMAC hash for an RFC 6238 algorithm name.
func Hash(alg string) func() hash.Hash {
	switch alg {
	case "SHA256":
		return sha256.New
	case "SHA512":
		return sha512.New
	default:
		return sha1.New
	}
}

// TOTP returns the code for key at t with the given digits and algorithm
// ("SHA1", "SHA256", "SHA512"); 30 s steps.
func TOTP(key []byte, t time.Time, digits int, alg string) string {
	return HOTP(key, Step(t), digits, Hash(alg))
}

// Code returns the 6-digit SHA-1 code for a base32 secret at t.
func Code(secret string, t time.Time) (string, error) {
	k, err := DecodeSecret(secret)
	if err != nil {
		return "", err
	}
	return HOTP(k, Step(t), TOTPDigits, sha1.New), nil
}

// Verify checks code against secret at now ± TOTPSkew steps and returns the
// matching step. Callers must reject a step at or below the last accepted
// one (replay protection). Every candidate is compared in constant time and
// all of them are always computed.
func Verify(secret, code string, now time.Time) (uint64, bool) {
	code = strings.TrimSpace(strings.ReplaceAll(code, " ", ""))
	if len(code) != TOTPDigits {
		return 0, false
	}
	k, err := DecodeSecret(secret)
	if err != nil {
		return 0, false
	}
	cur := Step(now)
	var hit uint64
	ok := 0
	for d := -TOTPSkew; d <= TOTPSkew; d++ {
		s := uint64(int64(cur) + int64(d))
		want := HOTP(k, s, TOTPDigits, sha1.New)
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			// Keep the latest matching step so a replay window cannot reopen.
			hit, ok = s, 1
		}
	}
	return hit, ok == 1
}

// URI is the otpauth:// enrollment URI (Key Uri Format) for an account.
func URI(account, secret string) string {
	label := url.PathEscape(Issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", Issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(TOTPDigits))
	q.Set("period", fmt.Sprint(TOTPPeriod))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
