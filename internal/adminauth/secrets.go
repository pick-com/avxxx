package adminauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// KeyEnv names the key that seals TOTP secrets at rest (AES-256-GCM). Any
// string works; it is stretched with SHA-256. Without it, secrets are stored
// as "plain:<base32>" and re-sealed on the next login once a key is set.
const KeyEnv = "ADMIN_TOTP_KEY"

// Sealer seals and opens TOTP secrets. The zero value has no key.
type Sealer struct{ aead cipher.AEAD }

// NewSealer builds a Sealer from key ("" = no key: plain storage).
func NewSealer(key string) Sealer {
	if key == "" {
		return Sealer{}
	}
	k := sha256.Sum256([]byte("avxxx admin totp v1\x00" + key))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		panic(err) // 32-byte key cannot fail
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return Sealer{aead: aead}
}

// HasKey reports whether secrets are encrypted.
func (s Sealer) HasKey() bool { return s.aead != nil }

// Seal returns the stored form of a base32 secret.
func (s Sealer) Seal(secret string) (string, error) {
	if s.aead == nil {
		return "plain:" + secret, nil
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, []byte(secret), []byte("totp"))
	return "v1:" + base64.RawStdEncoding.EncodeToString(ct), nil
}

// ErrNoKey is returned when a sealed secret is opened without ADMIN_TOTP_KEY.
var ErrNoKey = errors.New("TOTP secret is sealed but " + KeyEnv + " is not set")

// Open returns the base32 secret of a stored value; plain is true when it
// was stored unencrypted (callers re-seal it once a key is configured).
func (s Sealer) Open(stored string) (secret string, plain bool, err error) {
	switch {
	case strings.HasPrefix(stored, "plain:"):
		return strings.TrimPrefix(stored, "plain:"), true, nil
	case strings.HasPrefix(stored, "v1:"):
		if s.aead == nil {
			return "", false, ErrNoKey
		}
		raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, "v1:"))
		if err != nil || len(raw) < s.aead.NonceSize() {
			return "", false, errors.New("corrupt TOTP secret")
		}
		pt, err := s.aead.Open(nil, raw[:s.aead.NonceSize()], raw[s.aead.NonceSize():], []byte("totp"))
		if err != nil {
			return "", false, errors.New("TOTP secret does not open with " + KeyEnv)
		}
		return string(pt), false, nil
	}
	return "", false, errors.New("no TOTP secret")
}

// NewToken returns a random URL-safe token with a prefix (session, login
// challenge, invite). 32 random bytes.
func NewToken(prefix string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is the stored digest of a high-entropy token (SHA-256 hex). A
// slow hash is unnecessary: the token has 256 random bits.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// RecoveryCodeCount is how many one-time recovery codes an admin gets.
const RecoveryCodeCount = 10

var recoveryAlphabet = []byte("abcdefghjkmnpqrstuvwxyz23456789") // no 0/o/1/i/l

// NewRecoveryCodes returns n codes shaped xxxxx-xxxxx (≈ 49 bits each).
func NewRecoveryCodes(n int) ([]string, error) {
	out := make([]string, n)
	for i := range out {
		b := make([]byte, 10)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c := make([]byte, 0, 11)
		for j, v := range b {
			if j == 5 {
				c = append(c, '-')
			}
			c = append(c, recoveryAlphabet[int(v)%len(recoveryAlphabet)])
		}
		out[i] = string(c)
	}
	return out, nil
}

// NormalizeRecoveryCode lowercases and strips spaces so "ABCDE FGHJK" and
// "abcde-fghjk" match.
func NormalizeRecoveryCode(c string) string {
	c = strings.ToLower(strings.NewReplacer(" ", "", "-", "").Replace(strings.TrimSpace(c)))
	if len(c) == 10 {
		c = c[:5] + "-" + c[5:]
	}
	return c
}

// Lockout policy: after LockoutThreshold consecutive failures (password or
// second factor) the account is locked for LockoutDuration(failures), which
// doubles with every further failure up to LockoutMax.
const (
	LockoutThreshold = 5
	LockoutBase      = time.Minute
	LockoutMax       = time.Hour
)

// LockoutDuration is how long the account stays locked after the given
// number of consecutive failures; 0 below the threshold.
func LockoutDuration(failures int) time.Duration {
	if failures < LockoutThreshold {
		return 0
	}
	d := LockoutBase
	for i := LockoutThreshold; i < failures; i++ {
		d *= 2
		if d >= LockoutMax {
			return LockoutMax
		}
	}
	return d
}

// Session lifetimes: a session ends after SessionIdle without a request or
// SessionMax after sign-in, whichever comes first. A login challenge (the
// step between password and TOTP) lives ChallengeTTL; an invite InviteTTL.
const (
	SessionIdle  = 30 * time.Minute
	SessionMax   = 12 * time.Hour
	ChallengeTTL = 5 * time.Minute
	InviteTTL    = 72 * time.Hour
	ApprovalTTL  = 72 * time.Hour
)
