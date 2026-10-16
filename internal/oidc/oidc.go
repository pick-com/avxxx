// Package oidc implements the OpenID Connect authorization code flow with
// PKCE (S256) and nonce for Google and Apple sign-in (tech plan 3.6).
//
// Only what the two providers need is implemented: building the
// authorization URL, exchanging the code at the token endpoint and verifying
// the ID token (RS256 / ES256 against the provider's JWKS; iss, aud, exp, iat
// and nonce). Endpoints are fixed per provider and overridable for tests;
// nothing here is ever pointed at a real provider in tests.
//
// Configuration (all from the environment, never logged):
//
//	GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET
//	APPLE_CLIENT_ID (Services ID), APPLE_TEAM_ID, APPLE_KEY_ID,
//	APPLE_PRIVATE_KEY (the .p8 PEM; "\n" escapes are accepted)
//	BASE_URL  public site origin; the redirect URI is
//	          BASE_URL/api/auth/oauth/<provider>/callback
//
// A provider whose variables are all empty is disabled; a partial set is a
// configuration error.
package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Provider names.
const (
	Google = "google"
	Apple  = "apple"
)

// Errors returned by Exchange and Verify. Messages never contain tokens.
var (
	ErrToken    = errors.New("oidc: token request failed")
	ErrIDToken  = errors.New("oidc: invalid id token")
	ErrNoConfig = errors.New("oidc: provider not configured")
)

// clockSkew is the leeway for exp / iat checks.
const clockSkew = 2 * time.Minute

// Provider is one configured OpenID provider.
type Provider struct {
	Name     string
	ClientID string
	AuthURL  string
	TokenURL string
	JWKSURL  string
	Issuer   string
	Scopes   []string
	// FormPost: the provider answers with response_mode=form_post (Apple,
	// required when requesting name / email).
	FormPost bool
	// RedirectURI is BASE_URL/api/auth/oauth/<name>/callback.
	RedirectURI string
	HTTP        *http.Client
	Now         func() time.Time

	secret func(now time.Time) (string, error)

	mu        sync.Mutex
	keys      map[string]crypto.PublicKey
	fetchedAt time.Time
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Provider) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// FromEnv returns the configured providers keyed by name (possibly empty).
func FromEnv(getenv func(string) string) (map[string]*Provider, error) {
	out := map[string]*Provider{}
	var errs []error
	base := strings.TrimRight(strings.TrimSpace(getenv("BASE_URL")), "/")
	redirect := func(name string) string { return base + "/api/auth/oauth/" + name + "/callback" }

	gid, gsec := strings.TrimSpace(getenv("GOOGLE_CLIENT_ID")), getenv("GOOGLE_CLIENT_SECRET")
	switch {
	case gid == "" && gsec == "":
	case gid == "" || gsec == "":
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET must be set together"))
	default:
		out[Google] = NewGoogle(gid, gsec, redirect(Google))
	}

	aid, team, kid, key := strings.TrimSpace(getenv("APPLE_CLIENT_ID")), strings.TrimSpace(getenv("APPLE_TEAM_ID")),
		strings.TrimSpace(getenv("APPLE_KEY_ID")), getenv("APPLE_PRIVATE_KEY")
	switch {
	case aid == "" && team == "" && kid == "" && key == "":
	case aid == "" || team == "" || kid == "" || key == "":
		errs = append(errs, errors.New("APPLE_CLIENT_ID, APPLE_TEAM_ID, APPLE_KEY_ID and APPLE_PRIVATE_KEY must be set together"))
	default:
		p, err := NewApple(aid, team, kid, key, redirect(Apple))
		if err != nil {
			errs = append(errs, err) // never includes key material
		} else {
			out[Apple] = p
		}
	}
	if len(out) > 0 {
		if u, err := url.Parse(base); err != nil || base == "" || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, errors.New("BASE_URL must be an absolute http(s) URL when Google or Apple sign-in is configured"))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

// NewGoogle returns the Google provider.
func NewGoogle(clientID, clientSecret, redirectURI string) *Provider {
	return &Provider{
		Name: Google, ClientID: clientID, RedirectURI: redirectURI,
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		JWKSURL:  "https://www.googleapis.com/oauth2/v3/certs",
		Issuer:   "https://accounts.google.com",
		Scopes:   []string{"openid", "email", "profile"},
		secret:   func(time.Time) (string, error) { return clientSecret, nil },
	}
}

// NewApple returns the Apple provider. The client secret is an ES256 JWT
// signed with the .p8 key, minted per token request.
func NewApple(clientID, teamID, keyID, privateKeyPEM, redirectURI string) (*Provider, error) {
	key, err := parseECKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	p := &Provider{
		Name: Apple, ClientID: clientID, RedirectURI: redirectURI,
		AuthURL:  "https://appleid.apple.com/auth/authorize",
		TokenURL: "https://appleid.apple.com/auth/token",
		JWKSURL:  "https://appleid.apple.com/auth/keys",
		Issuer:   "https://appleid.apple.com",
		Scopes:   []string{"openid", "email", "name"},
		FormPost: true,
	}
	p.secret = func(now time.Time) (string, error) {
		return AppleClientSecret(key, teamID, keyID, clientID, now)
	}
	return p, nil
}

func parseECKey(s string) (*ecdsa.PrivateKey, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), `\n`, "\n")
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("APPLE_PRIVATE_KEY is not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if ek, err2 := x509.ParseECPrivateKey(block.Bytes); err2 == nil {
			return ek, nil
		}
		return nil, errors.New("APPLE_PRIVATE_KEY: cannot parse the key")
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok || ek.Curve != elliptic.P256() {
		return nil, errors.New("APPLE_PRIVATE_KEY must be a P-256 (ES256) key")
	}
	return ek, nil
}

// AppleClientSecret builds the client_secret JWT Apple expects (5 minutes).
func AppleClientSecret(key *ecdsa.PrivateKey, teamID, keyID, clientID string, now time.Time) (string, error) {
	header := map[string]string{"alg": "ES256", "kid": keyID, "typ": "JWT"}
	claims := map[string]any{"iss": teamID, "iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"aud": "https://appleid.apple.com", "sub": clientID}
	return signES256(key, header, claims)
}

func signES256(key *ecdsa.PrivateKey, header map[string]string, claims map[string]any) (string, error) {
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	b64 := base64.RawURLEncoding
	input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64.EncodeToString(sig), nil
}

// PKCE returns a random code verifier and its S256 challenge.
func PKCE() (verifier, challenge string, err error) {
	verifier, err = RandomString(32)
	if err != nil {
		return "", "", err
	}
	return verifier, S256(verifier), nil
}

// S256 is the PKCE S256 transform.
func S256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RandomString returns n random bytes, base64url without padding.
func RandomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// AuthCodeURL is the authorization endpoint URL for one request.
func (p *Provider) AuthCodeURL(state, nonce, challenge string) string {
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {p.ClientID},
		"redirect_uri":          {p.RedirectURI},
		"scope":                 {strings.Join(p.Scopes, " ")},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if p.FormPost {
		q.Set("response_mode", "form_post")
	}
	if p.Name == Google {
		q.Set("prompt", "select_account")
	}
	sep := "?"
	if strings.Contains(p.AuthURL, "?") {
		sep = "&"
	}
	return p.AuthURL + sep + q.Encode()
}

// Exchange redeems an authorization code and returns the raw ID token.
func (p *Provider) Exchange(ctx context.Context, code, verifier string) (string, error) {
	secret, err := p.secret(p.now())
	if err != nil {
		return "", fmt.Errorf("%w: client secret", ErrToken)
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.RedirectURI},
		"client_id":     {p.ClientID},
		"client_secret": {secret},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrToken, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %s unreachable", ErrToken, p.Name)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var tr struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(body, &tr)
	if resp.StatusCode != http.StatusOK || tr.IDToken == "" {
		// The provider's error code is safe to log; the body may echo secrets.
		return "", fmt.Errorf("%w: %s HTTP %d %s", ErrToken, p.Name, resp.StatusCode, safeCode(tr.Error))
	}
	return tr.IDToken, nil
}

func safeCode(s string) string {
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return ""
		}
	}
	if len(s) > 40 {
		return ""
	}
	return s
}

// Claims are the verified ID token claims the app uses.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

type rawClaims struct {
	Iss           string          `json:"iss"`
	Sub           string          `json:"sub"`
	Aud           json.RawMessage `json:"aud"`
	Azp           string          `json:"azp"`
	Exp           json.Number     `json:"exp"`
	Iat           json.Number     `json:"iat"`
	Nonce         string          `json:"nonce"`
	Email         string          `json:"email"`
	EmailVerified any             `json:"email_verified"`
	Name          string          `json:"name"`
}

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrIDToken, fmt.Sprintf(format, a...))
}

// Verify checks an ID token's signature and claims, including the nonce.
func (p *Provider) Verify(ctx context.Context, raw, nonce string) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, invalidf("malformed")
	}
	b64 := base64.RawURLEncoding
	hb, err1 := b64.DecodeString(parts[0])
	cb, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return Claims{}, invalidf("bad encoding")
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if json.Unmarshal(hb, &h) != nil {
		return Claims{}, invalidf("bad header")
	}
	if h.Alg != "RS256" && h.Alg != "ES256" {
		return Claims{}, invalidf("unsupported alg %q", h.Alg)
	}
	key, err := p.key(ctx, h.Kid)
	if err != nil {
		return Claims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch k := key.(type) {
	case *rsa.PublicKey:
		if h.Alg != "RS256" || rsa.VerifyPKCS1v15(k, crypto.SHA256, sum[:], sig) != nil {
			return Claims{}, invalidf("bad signature")
		}
	case *ecdsa.PublicKey:
		if h.Alg != "ES256" || len(sig) != 64 ||
			!ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			return Claims{}, invalidf("bad signature")
		}
	default:
		return Claims{}, invalidf("unsupported key")
	}

	var c rawClaims
	dec := json.NewDecoder(bytes.NewReader(cb))
	dec.UseNumber()
	if dec.Decode(&c) != nil {
		return Claims{}, invalidf("bad claims")
	}
	if c.Iss != p.Issuer && !(p.Name == Google && c.Iss == "accounts.google.com") {
		return Claims{}, invalidf("wrong issuer")
	}
	if !audienceOK(c.Aud, c.Azp, p.ClientID) {
		return Claims{}, invalidf("wrong audience")
	}
	now := p.now()
	exp, err := c.Exp.Int64()
	if err != nil || now.After(time.Unix(exp, 0).Add(clockSkew)) {
		return Claims{}, invalidf("expired")
	}
	if iat, err := c.Iat.Int64(); err != nil || time.Unix(iat, 0).After(now.Add(clockSkew)) {
		return Claims{}, invalidf("issued in the future")
	}
	if nonce == "" || c.Nonce != nonce {
		return Claims{}, invalidf("nonce mismatch")
	}
	if c.Sub == "" {
		return Claims{}, invalidf("no subject")
	}
	verified := false
	switch v := c.EmailVerified.(type) {
	case bool:
		verified = v
	case string: // Apple sends "true" / "false"
		verified = v == "true"
	}
	return Claims{Subject: c.Sub, Email: c.Email, EmailVerified: verified, Name: c.Name}, nil
}

func audienceOK(raw json.RawMessage, azp, clientID string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == clientID
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return false
	}
	found := false
	for _, a := range many {
		if a == clientID {
			found = true
		}
	}
	// With several audiences the token must have been issued to us (azp).
	return found && (len(many) == 1 || azp == clientID)
}

// key returns the JWKS key for kid, refetching the set (at most once a
// minute) when kid is unknown, so provider key rotation is picked up.
func (p *Provider) key(ctx context.Context, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if k, ok := p.keys[kid]; ok {
		return k, nil
	}
	if !p.fetchedAt.IsZero() && p.now().Sub(p.fetchedAt) < time.Minute {
		return nil, invalidf("unknown key id")
	}
	keys, err := p.fetchJWKS(ctx)
	p.fetchedAt = p.now()
	if err != nil {
		return nil, err
	}
	p.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, invalidf("unknown key id")
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

func (p *Provider) fetchJWKS(ctx context.Context) (map[string]crypto.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.JWKSURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: jwks: %v", ErrIDToken, err)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: jwks unreachable", ErrIDToken)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: jwks HTTP %d", ErrIDToken, resp.StatusCode)
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("%w: jwks: bad json", ErrIDToken)
	}
	out := map[string]crypto.PublicKey{}
	b64 := base64.RawURLEncoding
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := b64.DecodeString(k.N)
			e, err2 := b64.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
				continue
			}
			pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
			if pub.N.BitLen() < 2048 {
				continue
			}
			out[k.Kid] = pub
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := b64.DecodeString(k.X)
			y, err2 := b64.DecodeString(k.Y)
			if err1 != nil || err2 != nil {
				continue
			}
			pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
			out[k.Kid] = pub
		}
	}
	return out, nil
}
