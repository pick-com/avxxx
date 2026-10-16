// Package objstoretest provides a fake S3 endpoint for tests.
package objstoretest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	"avxxx/internal/objstore"
)

// FakeS3 is an httptest S3 endpoint: path-style buckets backed by an
// objstore.Memory, with every request's Signature V4 header (or presigned
// query) and payload hash verified. It speaks only what objstore.S3 uses.
type FakeS3 struct {
	*httptest.Server
	Store     *objstore.Memory
	AccessKey string
	SecretKey string
	Region    string

	mu      sync.Mutex
	buckets map[string]bool
}

// New starts a fake endpoint; Close it when done.
func New(accessKey, secretKey, region string) *FakeS3 {
	f := &FakeS3{Store: objstore.NewMemory(), AccessKey: accessKey, SecretKey: secretKey, Region: region, buckets: map[string]bool{}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// Config returns a client Config for bucket on this endpoint.
func (f *FakeS3) Config(bucket string) objstore.Config {
	return objstore.Config{Endpoint: f.URL, Bucket: bucket, AccessKey: f.AccessKey, SecretKey: f.SecretKey, Region: f.Region, PathStyle: true, PresignEndpoint: f.URL}
}

func fail(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code></Error>`, code)
}

func (f *FakeS3) serve(w http.ResponseWriter, r *http.Request) {
	if !objstore.VerifySignature(r, f.AccessKey, f.SecretKey, f.Region, time.Now()) {
		fail(w, http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if h := r.Header.Get("X-Amz-Content-Sha256"); h != "" && h != "UNSIGNED-PAYLOAD" {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != h {
			fail(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch")
			return
		}
	}
	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	ctx := context.Background()
	f.mu.Lock()
	exists := f.buckets[bucket]
	f.mu.Unlock()
	if key == "" {
		switch r.Method {
		case http.MethodHead:
			if !exists {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodPut:
			f.mu.Lock()
			f.buckets[bucket] = true
			f.mu.Unlock()
		case http.MethodGet:
			if !exists {
				fail(w, http.StatusNotFound, "NoSuchBucket")
				return
			}
			f.list(ctx, w, r, bucket)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if !exists {
		fail(w, http.StatusNotFound, "NoSuchBucket")
		return
	}
	full := bucket + "/" + key
	switch r.Method {
	case http.MethodPut:
		f.Store.Put(ctx, full, body, r.Header.Get("Content-Type"))
	case http.MethodGet:
		rc, info, err := f.Store.Get(ctx, full)
		if err != nil {
			fail(w, http.StatusNotFound, "NoSuchKey")
			return
		}
		defer rc.Close()
		w.Header().Set("Content-Type", info.ContentType)
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
		w.Header().Set("Last-Modified", info.LastModified.UTC().Format(http.TimeFormat))
		io.Copy(w, rc)
	case http.MethodDelete:
		f.Store.Delete(ctx, full)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (f *FakeS3) list(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	max, _ := strconv.Atoi(q.Get("max-keys"))
	after := ""
	if s := q.Get("start-after"); s != "" {
		after = bucket + "/" + s
	}
	objs, more, _ := f.Store.List(ctx, bucket+"/"+q.Get("prefix"), after, max)
	type content struct {
		Key          string `xml:"Key"`
		Size         int64  `xml:"Size"`
		LastModified string `xml:"LastModified"`
	}
	res := struct {
		XMLName     xml.Name  `xml:"ListBucketResult"`
		IsTruncated bool      `xml:"IsTruncated"`
		Contents    []content `xml:"Contents"`
	}{IsTruncated: more}
	for _, o := range objs {
		res.Contents = append(res.Contents, content{Key: strings.TrimPrefix(o.Key, bucket+"/"), Size: o.Size, LastModified: o.LastModified.UTC().Format(time.RFC3339)})
	}
	var buf bytes.Buffer
	xml.NewEncoder(&buf).Encode(res)
	w.Header().Set("Content-Type", "application/xml")
	w.Write(buf.Bytes())
}
