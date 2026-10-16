package oidc

import (
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
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIdP is an in-process OpenID provider: a JWKS endpoint and a token
// endpoint that checks the code, PKCE verifier and client credentials and
// returns an RS256 ID token. Tests never contact real providers.
type fakeIdP struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	issuer string

	mu        sync.Mutex
	codes     map[string]codeGrant // code -> expected verifier and claims
	secret    string               // expected client_secret ("" = check skipped)
	jwksHits  int
	lastForm  url.Values
	tokenFail bool
}

type codeGrant struct {
	challenge string
	claims    map[string]any
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIdP{t: t, key: key, kid: "k1", codes: map[string]codeGrant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.jwksHits++
		k, kid := f.key, f.kid
		f.mu.Unlock()
		b64 := base64.RawURLEncoding
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
			"n": b64.EncodeToString(k.N.Bytes()), "e": b64.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastForm = r.PostForm
		g, ok := f.codes[r.PostForm.Get("code")]
		delete(f.codes, r.PostForm.Get("code"))
		bad := !ok || f.tokenFail || S256(r.PostForm.Get("code_verifier")) != g.challenge ||
			r.PostForm.Get("grant_type") != "authorization_code" || (f.secret != "" && r.PostForm.Get("client_secret") != f.secret)
		if bad {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "secret-ish detail"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "id_token": f.sign(g.claims), "token_type": "Bearer"})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.issuer = f.srv.URL
	return f
}

func (f *fakeIdP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": f.kid, "typ": "JWT"})
	c, _ := json.Marshal(claims)
	b64 := base64.RawURLEncoding
	input := b64.EncodeToString(h) + "." + b64.EncodeToString(c)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return input + "." + b64.EncodeToString(sig)
}

func (f *fakeIdP) provider(name string) *Provider {
	return &Provider{
		Name: name, ClientID: "client-1", RedirectURI: "https://reel.test/api/auth/oauth/" + name + "/callback",
		AuthURL: f.srv.URL + "/authorize", TokenURL: f.srv.URL + "/token", JWKSURL: f.srv.URL + "/jwks",
		Issuer: f.issuer, Scopes: []string{"openid", "email"}, HTTP: f.srv.Client(),
		secret: func(time.Time) (string, error) { return "s3cret", nil },
	}
}

func (f *fakeIdP) claims(nonce string) map[string]any {
	now := time.Now()
	return map[string]any{"iss": f.issuer, "sub": "user-42", "aud": "client-1", "exp": now.Add(time.Hour).Unix(),
		"iat": now.Unix(), "nonce": nonce, "email": "a@example.com", "email_verified": true, "name": "Ann"}
}

// authorize simulates the browser round trip: it parses the auth URL as the
// provider would and registers a code bound to the PKCE challenge.
func (f *fakeIdP) authorize(t *testing.T, authURL string, claims map[string]any) (code, state string) {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("auth URL misses PKCE / nonce / state: %s", authURL)
	}
	if q.Get("client_id") != "client-1" || q.Get("response_type") != "code" || !strings.HasSuffix(q.Get("redirect_uri"), "/callback") {
		t.Fatalf("auth URL params: %v", q)
	}
	if claims == nil {
		claims = f.claims(q.Get("nonce"))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code = "code-" + q.Get("state")
	f.codes[code] = codeGrant{challenge: q.Get("code_challenge"), claims: claims}
	return code, q.Get("state")
}

func TestCodeFlowWithFakeProvider(t *testing.T) {
	f := newFakeIdP(t)
	f.secret = "s3cret"
	p := f.provider(Google)
	verifier, challenge, err := PKCE()
	if err != nil {
		t.Fatal(err)
	}
	nonce, _ := RandomString(16)
	state, _ := RandomString(16)
	code, gotState := f.authorize(t, p.AuthCodeURL(state, nonce, challenge), nil)
	if gotState != state {
		t.Fatalf("state round trip: %q != %q", gotState, state)
	}
	raw, err := p.Exchange(context.Background(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if f.lastForm.Get("redirect_uri") != p.RedirectURI || f.lastForm.Get("client_id") != "client-1" {
		t.Errorf("token form: %v", f.lastForm)
	}
	c, err := p.Verify(context.Background(), raw, nonce)
	if err != nil {
		t.Fatal(err)
	}
	if c.Subject != "user-42" || c.Email != "a@example.com" || !c.EmailVerified || c.Name != "Ann" {
		t.Errorf("claims = %+v", c)
	}
	// The code is single use.
	if _, err := p.Exchange(context.Background(), code, verifier); !errors.Is(err, ErrToken) {
		t.Errorf("second exchange: %v, want ErrToken", err)
	}
}

func TestExchangeRejectsWrongVerifier(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(Google)
	_, challenge, _ := PKCE()
	code, _ := f.authorize(t, p.AuthCodeURL("st", "n", challenge), nil)
	other, _, _ := PKCE()
	_, err := p.Exchange(context.Background(), code, other)
	if !errors.Is(err, ErrToken) {
		t.Fatalf("err = %v, want ErrToken", err)
	}
	if strings.Contains(err.Error(), "secret-ish") {
		t.Errorf("error leaks provider body: %v", err)
	}
}

func TestVerifyRejects(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(Google)
	base := func() map[string]any { return f.claims("n1") }
	cases := map[string]func(map[string]any){
		"wrong nonce":    func(c map[string]any) { c["nonce"] = "other" },
		"wrong audience": func(c map[string]any) { c["aud"] = "someone-else" },
		"multi aud no azp": func(c map[string]any) {
			c["aud"] = []string{"client-1", "x"}
		},
		"wrong issuer":  func(c map[string]any) { c["iss"] = "https://evil.test" },
		"expired":       func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"future iat":    func(c map[string]any) { c["iat"] = time.Now().Add(time.Hour).Unix() },
		"empty subject": func(c map[string]any) { c["sub"] = "" },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			c := base()
			mut(c)
			if _, err := p.Verify(context.Background(), f.sign(c), "n1"); !errors.Is(err, ErrIDToken) {
				t.Errorf("err = %v, want ErrIDToken", err)
			}
		})
	}
	t.Run("multi aud with azp", func(t *testing.T) {
		c := base()
		c["aud"], c["azp"] = []string{"client-1", "x"}, "client-1"
		if _, err := p.Verify(context.Background(), f.sign(c), "n1"); err != nil {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("tampered payload", func(t *testing.T) {
		tok := strings.Split(f.sign(base()), ".")
		c := base()
		c["sub"] = "attacker"
		forged, _ := json.Marshal(c)
		tok[1] = base64.RawURLEncoding.EncodeToString(forged)
		if _, err := p.Verify(context.Background(), strings.Join(tok, "."), "n1"); !errors.Is(err, ErrIDToken) {
			t.Errorf("err = %v, want ErrIDToken", err)
		}
	})
	t.Run("alg none", func(t *testing.T) {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"k1"}`))
		c, _ := json.Marshal(base())
		tok := h + "." + base64.RawURLEncoding.EncodeToString(c) + "."
		if _, err := p.Verify(context.Background(), tok, "n1"); !errors.Is(err, ErrIDToken) {
			t.Errorf("err = %v, want ErrIDToken", err)
		}
	})
	t.Run("empty expected nonce", func(t *testing.T) {
		c := base()
		c["nonce"] = ""
		if _, err := p.Verify(context.Background(), f.sign(c), ""); !errors.Is(err, ErrIDToken) {
			t.Errorf("err = %v, want ErrIDToken", err)
		}
	})
}

func TestVerifyPicksUpKeyRotation(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(Google)
	now := time.Now()
	p.Now = func() time.Time { return now }
	if _, err := p.Verify(context.Background(), f.sign(f.claims("n")), "n"); err != nil {
		t.Fatal(err)
	}
	k2, _ := rsa.GenerateKey(rand.Reader, 2048)
	f.mu.Lock()
	f.key, f.kid = k2, "k2"
	f.mu.Unlock()
	// Within a minute of the last fetch an unknown kid is not refetched.
	if _, err := p.Verify(context.Background(), f.sign(f.claims("n")), "n"); !errors.Is(err, ErrIDToken) {
		t.Fatalf("err = %v, want ErrIDToken (refetch throttled)", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := p.Verify(context.Background(), f.sign(f.claims("n")), "n"); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if f.jwksHits != 2 {
		t.Errorf("jwks fetched %d times, want 2", f.jwksHits)
	}
}

func TestAppleEmailVerifiedString(t *testing.T) {
	f := newFakeIdP(t)
	p := f.provider(Apple)
	c := f.claims("n")
	c["email_verified"] = "true"
	got, err := p.Verify(context.Background(), f.sign(c), "n")
	if err != nil || !got.EmailVerified {
		t.Fatalf("got %+v %v", got, err)
	}
	c["email_verified"] = "false"
	got, _ = p.Verify(context.Background(), f.sign(c), "n")
	if got.EmailVerified {
		t.Error(`"false" read as verified`)
	}
}

func testP8(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestAppleClientSecret(t *testing.T) {
	k, _ := testP8(t)
	now := time.Unix(1_800_000_000, 0)
	tok, err := AppleClientSecret(k, "TEAM", "KEY", "com.example.web", now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	b64 := base64.RawURLEncoding
	hb, _ := b64.DecodeString(parts[0])
	cb, _ := b64.DecodeString(parts[1])
	sig, _ := b64.DecodeString(parts[2])
	var h map[string]string
	var c map[string]any
	json.Unmarshal(hb, &h)
	json.Unmarshal(cb, &c)
	if h["alg"] != "ES256" || h["kid"] != "KEY" {
		t.Errorf("header %v", h)
	}
	if c["iss"] != "TEAM" || c["sub"] != "com.example.web" || c["aud"] != "https://appleid.apple.com" || c["exp"].(float64)-c["iat"].(float64) != 300 {
		t.Errorf("claims %v", c)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&k.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("client secret signature does not verify")
	}
}

func TestFromEnv(t *testing.T) {
	_, p8 := testP8(t)
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	ps, err := FromEnv(env(nil))
	if err != nil || len(ps) != 0 {
		t.Fatalf("empty env: %v %v", ps, err)
	}
	if _, err := FromEnv(env(map[string]string{"GOOGLE_CLIENT_ID": "x", "BASE_URL": "https://reel.test"})); err == nil {
		t.Error("partial Google config accepted")
	}
	if _, err := FromEnv(env(map[string]string{"GOOGLE_CLIENT_ID": "x", "GOOGLE_CLIENT_SECRET": "y"})); err == nil {
		t.Error("missing BASE_URL accepted")
	}
	ps, err = FromEnv(env(map[string]string{"GOOGLE_CLIENT_ID": "x", "GOOGLE_CLIENT_SECRET": "y", "BASE_URL": "https://reel.test/",
		"APPLE_CLIENT_ID": "web", "APPLE_TEAM_ID": "T", "APPLE_KEY_ID": "K", "APPLE_PRIVATE_KEY": strings.ReplaceAll(p8, "\n", `\n`)}))
	if err != nil {
		t.Fatal(err)
	}
	if ps[Google].RedirectURI != "https://reel.test/api/auth/oauth/google/callback" || !ps[Apple].FormPost {
		t.Errorf("providers: %+v %+v", ps[Google], ps[Apple])
	}
	_, err = FromEnv(env(map[string]string{"APPLE_CLIENT_ID": "web", "APPLE_TEAM_ID": "T", "APPLE_KEY_ID": "K",
		"APPLE_PRIVATE_KEY": "-----BEGIN PRIVATE KEY-----\nTOPSECRET\n-----END PRIVATE KEY-----", "BASE_URL": "https://reel.test"}))
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") {
		t.Errorf("bad key: %v", err)
	}
	u, _ := url.Parse(ps[Apple].AuthCodeURL("s", "n", "c"))
	if u.Query().Get("response_mode") != "form_post" {
		t.Error("apple auth URL without form_post")
	}
}
