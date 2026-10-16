package objstore_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"avxxx/internal/objstore"
	"avxxx/internal/objstore/objstoretest"
)

func TestS3RoundTripAgainstFake(t *testing.T) {
	f := objstoretest.New("AKIDTEST", "secret/with+chars", "us-east-1")
	defer f.Close()
	c := objstore.NewS3(f.Config("media"), nil)
	ctx := context.Background()
	if err := c.EnsureBucket(ctx); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	if err := c.EnsureBucket(ctx); err != nil { // idempotent
		t.Fatalf("ensure bucket again: %v", err)
	}
	keys := []string{"release/1/cover/aa.jpg", "release/1/cover/aa.w320.jpg", "performer/9/avatar/bb.png"}
	for _, k := range keys {
		if err := c.Put(ctx, k, []byte("body:"+k), "image/jpeg"); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	rc, info, err := c.Get(ctx, keys[0])
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "body:"+keys[0] || info.ContentType != "image/jpeg" {
		t.Errorf("get = %q %+v", b, info)
	}
	if _, _, err := c.Get(ctx, "release/2/cover/none.jpg"); !errors.Is(err, objstore.ErrNotFound) {
		t.Errorf("missing get err = %v, want ErrNotFound", err)
	}
	objs, more, err := c.List(ctx, "release/", "", 1)
	if err != nil || len(objs) != 1 || !more || objs[0].Key != "release/1/cover/aa.jpg" {
		t.Fatalf("list page 1 = %+v more=%v err=%v", objs, more, err)
	}
	objs, more, err = c.List(ctx, "release/", objs[0].Key, 10)
	if err != nil || len(objs) != 1 || more || objs[0].Key != "release/1/cover/aa.w320.jpg" {
		t.Fatalf("list page 2 = %+v more=%v err=%v", objs, more, err)
	}
	if err := c.Delete(ctx, keys[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := c.Delete(ctx, keys[0]); err != nil { // missing is fine
		t.Fatalf("delete missing: %v", err)
	}
	if got := f.Store.Keys(); len(got) != 2 {
		t.Errorf("keys after delete = %v", got)
	}
	// A presigned URL reads without credentials until it expires.
	u, err := c.PresignGet(keys[1], time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "body:"+keys[1] {
		t.Errorf("presigned get = %d %q", resp.StatusCode, body)
	}
	old, _ := c.PresignGet(keys[1], time.Minute, time.Now().Add(-time.Hour))
	if resp, err := http.Get(old); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired presigned get = %v %v", resp.StatusCode, err)
	}
	// Wrong credentials are refused by the (verifying) fake.
	bad := f.Config("media")
	bad.SecretKey = "wrong"
	if err := objstore.NewS3(bad, nil).Put(ctx, "x/y.jpg", []byte("x"), "image/jpeg"); err == nil {
		t.Error("put with a wrong secret succeeded")
	}
}

func TestConfigFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := objstore.ConfigFromEnv(env(nil))
	if err != nil || c.Enabled() {
		t.Fatalf("empty env = %+v %v, want disabled", c, err)
	}
	if _, err := objstore.ConfigFromEnv(env(map[string]string{"S3_ENDPOINT": "http://minio:9000"})); err == nil ||
		!strings.Contains(err.Error(), "S3_ACCESS_KEY, S3_BUCKET, S3_SECRET_KEY") {
		t.Errorf("partial config err = %v", err)
	}
	c, err = objstore.ConfigFromEnv(env(map[string]string{"S3_ENDPOINT": "http://minio:9000/", "S3_BUCKET": "b", "S3_ACCESS_KEY": "a", "S3_SECRET_KEY": "s", "S3_PATH_STYLE": "false"}))
	if err != nil || c.Endpoint != "http://minio:9000" || c.Region != "us-east-1" || c.PathStyle || c.PresignEndpoint != c.Endpoint {
		t.Errorf("config = %+v %v", c, err)
	}
	if _, err := objstore.ConfigFromEnv(env(map[string]string{"S3_ENDPOINT": "minio:9000", "S3_BUCKET": "b", "S3_ACCESS_KEY": "a", "S3_SECRET_KEY": "s"})); err == nil {
		t.Error("endpoint without scheme accepted")
	}
}

func TestValidKey(t *testing.T) {
	for _, k := range []string{"", "/a", "a//b", "a/../b", "a?b", "a\\b", "a\nb"} {
		if objstore.ValidKey(k) == nil {
			t.Errorf("ValidKey(%q) = nil", k)
		}
	}
	if err := objstore.ValidKey("release/1/cover/" + strings.Repeat("a", 64) + ".w320.jpg"); err != nil {
		t.Error(err)
	}
}

func TestPresignVirtualHost(t *testing.T) {
	c := objstore.NewS3(objstore.Config{Endpoint: "https://s3.amazonaws.com", Bucket: "examplebucket", AccessKey: "a", SecretKey: "s", Region: "us-east-1"}, nil)
	raw, err := c.PresignGet("test.txt", time.Hour, time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	if u.Host != "examplebucket.s3.amazonaws.com" || u.Path != "/test.txt" || u.Query().Get("X-Amz-Expires") != "3600" {
		t.Errorf("presigned = %s", raw)
	}
}
