package objstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config configures the S3 client (ConfigFromEnv).
type Config struct {
	// Endpoint is the S3 API base URL, e.g. http://minio:9000 or
	// https://s3.eu-central-1.amazonaws.com.
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	// PathStyle addresses the bucket as {endpoint}/{bucket}/{key} (MinIO,
	// most self-hosted stores); false uses {bucket}.{endpoint host}.
	PathStyle bool
	// PresignEndpoint is the base URL browsers reach the store at, used for
	// presigned URLs (default Endpoint). The host is part of the signature.
	PresignEndpoint string
	// CacheControl is stored on every object (served by S3 / the CDN).
	CacheControl string
}

// Enabled reports whether the storage is configured at all.
func (c Config) Enabled() bool { return c.Endpoint != "" }

// ConfigFromEnv reads S3_ENDPOINT, S3_BUCKET, S3_ACCESS_KEY, S3_SECRET_KEY,
// S3_REGION (default us-east-1), S3_PATH_STYLE (default true) and
// S3_PRESIGN_ENDPOINT. An empty S3_ENDPOINT disables storage (zero Config,
// nil error); a partial configuration is an error.
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	c := Config{
		Endpoint:        strings.TrimRight(strings.TrimSpace(getenv("S3_ENDPOINT")), "/"),
		Bucket:          strings.TrimSpace(getenv("S3_BUCKET")),
		AccessKey:       strings.TrimSpace(getenv("S3_ACCESS_KEY")),
		SecretKey:       strings.TrimSpace(getenv("S3_SECRET_KEY")),
		Region:          strings.TrimSpace(getenv("S3_REGION")),
		PathStyle:       true,
		PresignEndpoint: strings.TrimRight(strings.TrimSpace(getenv("S3_PRESIGN_ENDPOINT")), "/"),
		CacheControl:    "public, max-age=86400",
	}
	if c.Endpoint == "" {
		return Config{}, nil
	}
	if v := strings.ToLower(strings.TrimSpace(getenv("S3_PATH_STYLE"))); v == "false" || v == "0" || v == "no" {
		c.PathStyle = false
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	if c.PresignEndpoint == "" {
		c.PresignEndpoint = c.Endpoint
	}
	var missing []string
	for name, v := range map[string]string{"S3_BUCKET": c.Bucket, "S3_ACCESS_KEY": c.AccessKey, "S3_SECRET_KEY": c.SecretKey} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Config{}, fmt.Errorf("S3_ENDPOINT is set but %s missing", strings.Join(missing, ", "))
	}
	for _, e := range []string{c.Endpoint, c.PresignEndpoint} {
		u, err := url.Parse(e)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return Config{}, fmt.Errorf("invalid S3 endpoint %q", e)
		}
	}
	return c, nil
}

// S3 is a Storage on an S3-compatible API.
type S3 struct {
	cfg    Config
	client *http.Client
	now    func() time.Time
}

// NewS3 returns a client for cfg. client nil = a 60 s timeout client.
func NewS3(cfg Config, client *http.Client) *S3 {
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.PresignEndpoint == "" {
		cfg.PresignEndpoint = cfg.Endpoint
	}
	return &S3{cfg: cfg, client: client, now: time.Now}
}

// objectURL builds the URL of key (key "" = the bucket) on base.
func (s *S3) objectURL(base, key string) (*url.URL, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, err
	}
	p := strings.TrimRight(u.Path, "/")
	if s.cfg.PathStyle {
		p += "/" + s.cfg.Bucket
	} else {
		u.Host = s.cfg.Bucket + "." + u.Host
	}
	if key != "" {
		p += "/" + key
	} else if !s.cfg.PathStyle {
		p += "/"
	}
	u.Path = p
	// The wire path must be exactly the canonical (UriEncoded) path.
	u.RawPath = uriEncode(p, false)
	return u, nil
}

// S3Error is a non-2xx answer from the store.
type S3Error struct {
	Status int
	Code   string
	Op     string
}

func (e *S3Error) Error() string {
	return fmt.Sprintf("objstore: %s: HTTP %d %s", e.Op, e.Status, e.Code)
}

func (s *S3) do(ctx context.Context, op, method string, u *url.URL, body []byte, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	if len(body) == 0 {
		req.Body = http.NoBody
	}
	for k, v := range header {
		req.Header[k] = v
	}
	sum := sha256.Sum256(body)
	signRequest(req, hex.EncodeToString(sum[:]), s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, s.now())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objstore: %s: %w", op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	defer resp.Body.Close()
	var e struct {
		Code string `xml:"Code"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = xml.Unmarshal(raw, &e)
	return nil, &S3Error{Status: resp.StatusCode, Code: e.Code, Op: op}
}

func isNotFound(err error) bool {
	var e *S3Error
	return errors.As(err, &e) && e.Status == http.StatusNotFound
}

// EnsureBucket creates the bucket when it does not exist. It never sets a
// bucket policy: the bucket stays private.
func (s *S3) EnsureBucket(ctx context.Context) error {
	u, err := s.objectURL(s.cfg.Endpoint, "")
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, "head bucket", http.MethodHead, u, nil, nil)
	if err == nil {
		resp.Body.Close()
		return nil
	}
	if !isNotFound(err) {
		return err
	}
	var body []byte
	if s.cfg.Region != "us-east-1" {
		body = []byte(`<CreateBucketConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><LocationConstraint>` + s.cfg.Region + `</LocationConstraint></CreateBucketConfiguration>`)
	}
	resp, err = s.do(ctx, "create bucket", http.MethodPut, u, body, nil)
	if err != nil {
		var e *S3Error
		if errors.As(err, &e) && (e.Code == "BucketAlreadyOwnedByYou" || e.Code == "BucketAlreadyExists") {
			return nil
		}
		return err
	}
	resp.Body.Close()
	return nil
}

func (s *S3) Put(ctx context.Context, key string, body []byte, contentType string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	u, err := s.objectURL(s.cfg.Endpoint, key)
	if err != nil {
		return err
	}
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if s.cfg.CacheControl != "" {
		h.Set("Cache-Control", s.cfg.CacheControl)
	}
	resp, err := s.do(ctx, "put", http.MethodPut, u, body, h)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, Info, error) {
	if err := ValidKey(key); err != nil {
		return nil, Info{}, err
	}
	u, err := s.objectURL(s.cfg.Endpoint, key)
	if err != nil {
		return nil, Info{}, err
	}
	resp, err := s.do(ctx, "get", http.MethodGet, u, nil, nil)
	if isNotFound(err) {
		return nil, Info{}, ErrNotFound
	}
	if err != nil {
		return nil, Info{}, err
	}
	info := Info{ContentType: resp.Header.Get("Content-Type"), Size: resp.ContentLength, ETag: resp.Header.Get("ETag")}
	if t, err := http.ParseTime(resp.Header.Get("Last-Modified")); err == nil {
		info.LastModified = t
	}
	return resp.Body, info, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	u, err := s.objectURL(s.cfg.Endpoint, key)
	if err != nil {
		return err
	}
	resp, err := s.do(ctx, "delete", http.MethodDelete, u, nil, nil)
	if isNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

type listResult struct {
	IsTruncated bool `xml:"IsTruncated"`
	Contents    []struct {
		Key          string    `xml:"Key"`
		Size         int64     `xml:"Size"`
		LastModified time.Time `xml:"LastModified"`
	} `xml:"Contents"`
}

func (s *S3) List(ctx context.Context, prefix, startAfter string, max int) ([]Object, bool, error) {
	if max <= 0 || max > 1000 {
		max = 1000
	}
	u, err := s.objectURL(s.cfg.Endpoint, "")
	if err != nil {
		return nil, false, err
	}
	q := url.Values{"list-type": {"2"}, "max-keys": {strconv.Itoa(max)}}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if startAfter != "" {
		q.Set("start-after", startAfter)
	}
	u.RawQuery = encodeQuery(q)
	resp, err := s.do(ctx, "list", http.MethodGet, u, nil, nil)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	var lr listResult
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&lr); err != nil {
		return nil, false, fmt.Errorf("objstore: list: %w", err)
	}
	out := make([]Object, len(lr.Contents))
	for i, c := range lr.Contents {
		out[i] = Object{Key: c.Key, Size: c.Size, LastModified: c.LastModified}
	}
	return out, lr.IsTruncated, nil
}

// PresignGet signs a GET URL on PresignEndpoint (Signature V4 query auth,
// at most 7 days).
func (s *S3) PresignGet(key string, expires time.Duration, now time.Time) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	if expires <= 0 || expires > 7*24*time.Hour {
		return "", errors.New("objstore: presign expiry must be within 7 days")
	}
	u, err := s.objectURL(s.cfg.PresignEndpoint, key)
	if err != nil {
		return "", err
	}
	return presignURL(u, s.cfg.AccessKey, s.cfg.SecretKey, s.cfg.Region, expires, now), nil
}

// ---- Signature Version 4 ----

const (
	sigAlgorithm    = "AWS4-HMAC-SHA256"
	amzDateFormat   = "20060102T150405Z"
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// uriEncode is the SigV4 UriEncode: everything but unreserved characters
// is percent-encoded; "/" is kept when encodeSlash is false (object paths).
func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encodeQuery is the canonical query string: keys sorted, both sides
// UriEncoded. Used for the request itself too, so the wire query matches.
func encodeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func signingKey(secret, date, region string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalRequest returns the canonical request and the signed header list.
func canonicalRequest(method string, u *url.URL, header http.Header, host, payloadHash string) (string, string) {
	names := []string{"host"}
	values := map[string]string{"host": host}
	for k, vs := range header {
		lk := strings.ToLower(k)
		if lk == "host" || lk == "authorization" || lk == "user-agent" || lk == "content-length" {
			continue
		}
		trimmed := make([]string, len(vs))
		for i, v := range vs {
			trimmed[i] = strings.Join(strings.Fields(v), " ")
		}
		names = append(names, lk)
		values[lk] = strings.Join(trimmed, ",")
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + values[n] + "\n")
	}
	signed := strings.Join(names, ";")
	path := u.Path
	if path == "" {
		path = "/"
	}
	cr := strings.Join([]string{method, uriEncode(path, false), encodeQuery(u.Query()), ch.String(), signed, payloadHash}, "\n")
	return cr, signed
}

func stringToSign(t time.Time, region, canonical string) (string, string) {
	scope := t.Format("20060102") + "/" + region + "/s3/aws4_request"
	return sigAlgorithm + "\n" + t.Format(amzDateFormat) + "\n" + scope + "\n" + sha256Hex(canonical), scope
}

// signRequest adds x-amz-date, x-amz-content-sha256 and Authorization.
func signRequest(req *http.Request, payloadHash, accessKey, secretKey, region string, now time.Time) {
	t := now.UTC()
	req.Header.Set("X-Amz-Date", t.Format(amzDateFormat))
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	cr, signed := canonicalRequest(req.Method, req.URL, req.Header, host, payloadHash)
	sts, scope := stringToSign(t, region, cr)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secretKey, t.Format("20060102"), region), sts))
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", sigAlgorithm, accessKey, scope, signed, sig))
}

// presignURL returns u with SigV4 query authentication (host signed only).
func presignURL(u *url.URL, accessKey, secretKey, region string, expires time.Duration, now time.Time) string {
	t := now.UTC()
	scope := t.Format("20060102") + "/" + region + "/s3/aws4_request"
	q := u.Query()
	q.Set("X-Amz-Algorithm", sigAlgorithm)
	q.Set("X-Amz-Credential", accessKey+"/"+scope)
	q.Set("X-Amz-Date", t.Format(amzDateFormat))
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(expires/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	v := *u
	v.RawQuery = encodeQuery(q)
	cr, _ := canonicalRequest(http.MethodGet, &v, nil, u.Host, unsignedPayload)
	sts, _ := stringToSign(t, region, cr)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secretKey, t.Format("20060102"), region), sts))
	v.RawQuery += "&X-Amz-Signature=" + sig
	return (&url.URL{Scheme: v.Scheme, Host: v.Host, Path: v.Path, RawPath: uriEncode(v.Path, false), RawQuery: v.RawQuery}).String()
}

// FromEnv builds the configured Storage (ConfigFromEnv), or nil when
// S3_ENDPOINT is empty. ensureBucket creates the private bucket if missing.
func FromEnv(ctx context.Context, getenv func(string) string, ensureBucket bool) (Storage, error) {
	cfg, err := ConfigFromEnv(getenv)
	if err != nil || !cfg.Enabled() {
		return nil, err
	}
	s := NewS3(cfg, nil)
	if ensureBucket {
		if err := s.EnsureBucket(ctx); err != nil {
			return nil, err
		}
	}
	return s, nil
}
