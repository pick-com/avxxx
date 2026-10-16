// Package webpush is a small, dependency-free Web Push sender built on the Go
// standard library:
//
//   - RFC 8291 message encryption (aes128gcm content coding, RFC 8188),
//   - RFC 8292 VAPID application-server identification (ES256 JWT),
//   - RFC 8030 delivery (POST to the subscription endpoint with TTL/Urgency).
//
// It only implements what an application server needs: one record per
// message, no padding beyond the mandatory delimiter.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// recordSize is the aes128gcm record size advertised in the header. One
	// record carries the whole message.
	recordSize = 4096
	saltLen    = 16
	keyLen     = 65 // uncompressed P-256 point
	authLen    = 16
	tagLen     = 16
	headerLen  = saltLen + 4 + 1 + keyLen // 86
	// MaxPayload is the largest plaintext that fits one record
	// (record size minus the GCM tag and the 0x02 delimiter).
	MaxPayload = recordSize - tagLen - 1
)

// ErrPayloadTooLarge is returned by Encrypt for plaintexts above MaxPayload.
var ErrPayloadTooLarge = errors.New("webpush: payload too large")

// Keys are the client keys of a PushSubscription (base64url, as browsers
// return them from PushSubscription.toJSON()).
type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Decode validates and decodes the client keys.
func (k Keys) Decode() (*ecdh.PublicKey, []byte, error) {
	pub, err := DecodeB64(k.P256dh)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	ua, err := ecdh.P256().NewPublicKey(pub)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	auth, err := DecodeB64(k.Auth)
	if err != nil {
		return nil, nil, fmt.Errorf("webpush: auth: %w", err)
	}
	if len(auth) != authLen {
		return nil, nil, fmt.Errorf("webpush: auth must be %d bytes, got %d", authLen, len(auth))
	}
	return ua, auth, nil
}

// DecodeB64 accepts base64url with or without padding (and, leniently,
// standard base64), as browsers and libraries differ.
func DecodeB64(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

// EncodeB64 is unpadded base64url.
func EncodeB64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// Encrypt encrypts plaintext for the subscription keys with a fresh
// ephemeral key and salt, returning the aes128gcm message body.
func Encrypt(plaintext []byte, keys Keys) ([]byte, error) {
	ua, auth, err := keys.Decode()
	if err != nil {
		return nil, err
	}
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, err
	}
	return encrypt(plaintext, ua, auth, as, salt)
}

// encrypt is RFC 8291 section 3 with explicit ephemeral key and salt (test vectors).
func encrypt(plaintext []byte, ua *ecdh.PublicKey, auth []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	cek, nonce, err := deriveKeys(as, ua, ua.Bytes(), as.PublicKey().Bytes(), auth, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	out := make([]byte, headerLen, headerLen+len(plaintext)+1+tagLen)
	copy(out, salt)
	binary.BigEndian.PutUint32(out[saltLen:], recordSize)
	out[saltLen+4] = keyLen
	copy(out[saltLen+5:], as.PublicKey().Bytes())
	record := append(append([]byte{}, plaintext...), 0x02) // last-record delimiter
	return gcm.Seal(out, nonce, record, nil), nil
}

// Decrypt is the user-agent side of RFC 8291: it decrypts body with the
// subscription's private key and auth secret. The sender never needs it; it
// exists for tests and fake push services.
func Decrypt(body []byte, ua *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < headerLen+tagLen+1 {
		return nil, errors.New("webpush: message too short")
	}
	salt := body[:saltLen]
	rs := binary.BigEndian.Uint32(body[saltLen:])
	idlen := int(body[saltLen+4])
	if idlen != keyLen || len(body) < saltLen+5+idlen {
		return nil, errors.New("webpush: bad keyid length")
	}
	asPub, err := ecdh.P256().NewPublicKey(body[saltLen+5 : saltLen+5+idlen])
	if err != nil {
		return nil, err
	}
	ct := body[saltLen+5+idlen:]
	if uint32(len(ct)) > rs {
		return nil, errors.New("webpush: multi-record messages not supported")
	}
	cek, nonce, err := deriveKeys(ua, asPub, ua.PublicKey().Bytes(), asPub.Bytes(), auth, salt)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(cek)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0 {
		i--
	}
	if i < 0 || pt[i] != 0x02 {
		return nil, errors.New("webpush: missing last-record delimiter")
	}
	return pt[:i], nil
}

// deriveKeys implements the RFC 8291 section 3.3/3.4 key schedule.
func deriveKeys(priv *ecdh.PrivateKey, peer *ecdh.PublicKey, uaPub, asPub, auth, salt []byte) (cek, nonce []byte, err error) {
	ecdhSecret, err := priv.ECDH(peer)
	if err != nil {
		return nil, nil, err
	}
	keyInfo := "WebPush: info\x00" + string(uaPub) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, ecdhSecret, auth, keyInfo, 32)
	if err != nil {
		return nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, err
	}
	if cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16); err != nil {
		return nil, nil, err
	}
	if nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12); err != nil {
		return nil, nil, err
	}
	return cek, nonce, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
