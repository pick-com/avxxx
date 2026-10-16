package webpush

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := DecodeB64(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

// RFC 8291 Appendix A.
const (
	rfcPlaintext = "When I grow up, I want to be a watermelon"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncryptRFC8291Vector(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := EncodeB64(as.PublicKey().Bytes()); got != rfcASPublic {
		t.Fatalf("as_public = %s", got)
	}
	ua, auth, err := Keys{P256dh: rfcUAPublic, Auth: rfcAuth}.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := encrypt([]byte(rfcPlaintext), ua, auth, as, b64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if EncodeB64(got) != rfcMessage {
		t.Fatalf("message mismatch:\n got %s\nwant %s", EncodeB64(got), rfcMessage)
	}

	uaPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Decrypt(b64(t, rfcMessage), uaPriv, auth)
	if err != nil || string(pt) != rfcPlaintext {
		t.Fatalf("Decrypt = %q, %v", pt, err)
	}
}

func TestEncryptRoundTripAndLimits(t *testing.T) {
	ua, keys := newUA(t)
	msg := []byte(`{"title":"新作"}`)
	a, err := Encrypt(msg, keys)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Encrypt(msg, keys)
	if bytes.Equal(a, b) {
		t.Fatal("two encryptions must use fresh salt / ephemeral keys")
	}
	auth := b64(t, keys.Auth)
	if pt, err := Decrypt(a, ua, auth); err != nil || !bytes.Equal(pt, msg) {
		t.Fatalf("round trip = %q, %v", pt, err)
	}
	if _, err := Decrypt(a, ua, make([]byte, 16)); err == nil {
		t.Fatal("wrong auth secret must fail")
	}
	if _, err := Encrypt(make([]byte, MaxPayload+1), keys); err != ErrPayloadTooLarge {
		t.Fatalf("oversize err = %v", err)
	}
	if _, err := Encrypt(make([]byte, MaxPayload), keys); err != nil {
		t.Fatalf("max payload: %v", err)
	}
	if _, err := Encrypt(msg, Keys{P256dh: "AAAA", Auth: keys.Auth}); err == nil {
		t.Fatal("bad p256dh must fail")
	}
	if _, err := Encrypt(msg, Keys{P256dh: keys.P256dh, Auth: "AAAA"}); err == nil {
		t.Fatal("short auth must fail")
	}
}

func newUA(t *testing.T) (*ecdh.PrivateKey, Keys) {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return ua, Keys{P256dh: EncodeB64(ua.PublicKey().Bytes()), Auth: EncodeB64(auth)}
}

func newVAPID(t *testing.T) *VAPID {
	t.Helper()
	pub, priv, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVAPID(pub, priv, "mailto:ops@example.com")
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNewVAPIDValidation(t *testing.T) {
	pub, priv, _ := GenerateVAPID()
	other, _, _ := GenerateVAPID()
	if _, err := NewVAPID(other, priv, "mailto:a@b"); err == nil {
		t.Error("mismatched public key must fail")
	}
	if _, err := NewVAPID(pub, priv, "a@b"); err == nil {
		t.Error("subject without mailto:/https: must fail")
	}
	if _, err := NewVAPID(pub, "zz", "mailto:a@b"); err == nil {
		t.Error("bad private key must fail")
	}
	if v, err := NewVAPID(pub, priv, "https://example.com"); err != nil || v.PublicKey() != pub {
		t.Errorf("valid keys: %v", err)
	}
}

// verifyVAPID checks an RFC 8292 Authorization header and returns the claims.
func verifyVAPID(t *testing.T, header string, wantKey string) map[string]any {
	t.Helper()
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		t.Fatalf("Authorization = %q", header)
	}
	params := map[string]string{}
	for _, p := range strings.Split(rest, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		params[k] = v
	}
	if params["k"] != wantKey {
		t.Fatalf("k = %q, want %q", params["k"], wantKey)
	}
	parts := strings.Split(params["t"], ".")
	if len(parts) != 3 {
		t.Fatalf("jwt = %q", params["t"])
	}
	var hdr map[string]string
	json.Unmarshal(b64(t, parts[0]), &hdr)
	if hdr["alg"] != "ES256" || hdr["typ"] != "JWT" {
		t.Fatalf("jwt header = %v", hdr)
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), b64(t, params["k"]))
	if err != nil {
		t.Fatal(err)
	}
	sig := b64(t, parts[2])
	if len(sig) != 64 {
		t.Fatalf("signature length %d", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("jwt signature does not verify")
	}
	var claims map[string]any
	if err := json.Unmarshal(b64(t, parts[1]), &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestSendAgainstFakePushService(t *testing.T) {
	v := newVAPID(t)
	ua, keys := newUA(t)
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	status := http.StatusCreated
	var gotBody []byte
	var gotReq *http.Request
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq = r
		gotBody, _ = io.ReadAll(r.Body)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "120")
		}
		w.WriteHeader(status)
	}))
	defer ts.Close()
	c := &Client{VAPID: v, HTTP: ts.Client(), TTL: time.Hour, Urgency: "normal", Now: func() time.Time { return now }}
	sub := Subscription{Endpoint: ts.URL + "/push/abc123", Keys: keys}

	res, err := c.Send(context.Background(), sub, []byte(`{"title":"hi"}`))
	if err != nil || res.Outcome != Delivered || res.Status != 201 {
		t.Fatalf("Send = %+v, %v", res, err)
	}
	if gotReq.Method != http.MethodPost || gotReq.URL.Path != "/push/abc123" {
		t.Fatalf("request %s %s", gotReq.Method, gotReq.URL.Path)
	}
	for k, want := range map[string]string{
		"Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream",
		"TTL": "3600", "Urgency": "normal",
	} {
		if got := gotReq.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	claims := verifyVAPID(t, gotReq.Header.Get("Authorization"), v.PublicKey())
	if claims["aud"] != ts.URL || claims["sub"] != "mailto:ops@example.com" {
		t.Errorf("claims = %v (aud must be the endpoint origin %s)", claims, ts.URL)
	}
	if exp := int64(claims["exp"].(float64)); exp <= now.Unix() || exp > now.Add(24*time.Hour).Unix() {
		t.Errorf("exp %d not within (now, now+24h]", exp)
	}
	if pt, err := Decrypt(gotBody, ua, b64(t, keys.Auth)); err != nil || string(pt) != `{"title":"hi"}` {
		t.Fatalf("push service body decrypts to %q, %v", pt, err)
	}

	for _, tc := range []struct {
		status int
		want   Outcome
	}{
		{404, Gone}, {410, Gone}, {429, Retry}, {500, Retry}, {503, Retry}, {400, Rejected}, {403, Rejected}, {413, Rejected},
	} {
		status = tc.status
		res, err := c.Send(context.Background(), sub, []byte("x"))
		if err != nil || res.Outcome != tc.want || res.Status != tc.status {
			t.Errorf("status %d: %+v, %v; want %v", tc.status, res, err, tc.want)
		}
		if tc.status == 429 && res.RetryAfter != 2*time.Minute {
			t.Errorf("Retry-After = %v", res.RetryAfter)
		}
	}

	// Transport errors are retryable and never echo the endpoint path.
	ts.Close()
	res, err = c.Send(context.Background(), sub, []byte("x"))
	if err == nil || res.Outcome != Retry {
		t.Fatalf("closed server: %+v, %v", res, err)
	}
	if strings.Contains(err.Error(), "abc123") {
		t.Errorf("transport error leaks endpoint: %v", err)
	}
}

func TestParseRetryAfterDate(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if d := parseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now); d != 90*time.Second {
		t.Errorf("date Retry-After = %v", d)
	}
	if d := parseRetryAfter("soon", now); d != 0 {
		t.Errorf("junk Retry-After = %v", d)
	}
}

func TestValidateEndpoint(t *testing.T) {
	for _, tc := range []struct {
		ep string
		ok bool
	}{
		{"https://fcm.googleapis.com/fcm/send/abc", true},
		{"https://updates.push.services.mozilla.com/wpush/v2/abc", true},
		{"https://web.push.apple.com/abc", true},
		{"https://db5p.notify.windows.com/w/?token=abc", true},
		{"http://fcm.googleapis.com/fcm/send/abc", false},
		{"https://evil.com/fcm.googleapis.com", false},
		{"https://fcm.googleapis.com.evil.com/x", false},
		{"https://127.0.0.1/x", false},
		{"https://user:pw@fcm.googleapis.com/x", false},
		{"fcm.googleapis.com/x", false},
		{"https://fcm.googleapis.com/" + strings.Repeat("a", MaxEndpointLen), false},
	} {
		if err := ValidateEndpoint(tc.ep, DefaultAllowedHosts); (err == nil) != tc.ok {
			t.Errorf("ValidateEndpoint(%.60q) = %v, want ok=%v", tc.ep, err, tc.ok)
		}
	}
	if err := ValidateEndpoint("https://push.example.org/x", nil); err != nil {
		t.Errorf("nil allowlist must accept any https host: %v", err)
	}
	if EndpointHost("https://fcm.googleapis.com/fcm/send/secret") != "fcm.googleapis.com" {
		t.Error("EndpointHost")
	}
}
