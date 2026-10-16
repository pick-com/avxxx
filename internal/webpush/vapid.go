package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"
)

// VAPID is an application server key pair (RFC 8292) plus the contact
// subject ("mailto:..." or "https://...") sent in every JWT.
type VAPID struct {
	key       *ecdsa.PrivateKey
	publicB64 string
	Subject   string
}

// NewVAPID parses the base64url keys in the format web-push tooling emits:
// the private key is the raw 32-byte scalar, the public key the 65-byte
// uncompressed point. The public key must belong to the private key.
func NewVAPID(publicB64, privateB64, subject string) (*VAPID, error) {
	if !strings.HasPrefix(subject, "mailto:") && !strings.HasPrefix(subject, "https://") {
		return nil, errors.New("webpush: VAPID subject must be a mailto: or https: URL")
	}
	priv, err := DecodeB64(privateB64)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key: %w", err)
	}
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), priv)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID private key: %w", err)
	}
	pub, err := DecodeB64(publicB64)
	if err != nil {
		return nil, fmt.Errorf("webpush: VAPID public key: %w", err)
	}
	derived, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, err
	}
	if string(pub) != string(derived) {
		return nil, errors.New("webpush: VAPID public key does not match the private key")
	}
	return &VAPID{key: key, publicB64: EncodeB64(derived), Subject: subject}, nil
}

// GenerateVAPID creates a new key pair, returned as (public, private) base64url.
func GenerateVAPID() (publicB64, privateB64 string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	pub, err := key.PublicKey.Bytes()
	if err != nil {
		return "", "", err
	}
	priv, err := key.Bytes()
	if err != nil {
		return "", "", err
	}
	return EncodeB64(pub), EncodeB64(priv), nil
}

// PublicKey is the base64url application server key browsers pass to
// pushManager.subscribe({applicationServerKey}).
func (v *VAPID) PublicKey() string { return v.publicB64 }

// Authorization returns the RFC 8292 "vapid t=<jwt>, k=<key>" header value
// for a push endpoint. The JWT audience is the endpoint's origin.
func (v *VAPID) Authorization(endpoint string, exp time.Time) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("webpush: bad endpoint URL")
	}
	header := EncodeB64([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": u.Scheme + "://" + u.Host,
		"exp": exp.Unix(),
		"sub": v.Subject,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + EncodeB64(claims)
	sum := sha256.Sum256([]byte(signing))
	der, err := ecdsa.SignASN1(rand.Reader, v.key, sum[:])
	if err != nil {
		return "", err
	}
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil {
		return "", err
	}
	raw := make([]byte, 64) // JWS ES256: fixed-width R || S
	sig.R.FillBytes(raw[:32])
	sig.S.FillBytes(raw[32:])
	return "vapid t=" + signing + "." + EncodeB64(raw) + ", k=" + v.publicB64, nil
}
