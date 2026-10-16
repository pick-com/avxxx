package objstore

import (
	"crypto/hmac"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VerifySignature checks a received request's Signature V4 header or
// presigned query against the given credentials, the way an S3 server
// would (fake servers in tests; objstoretest). now bounds presigned expiry.
func VerifySignature(r *http.Request, accessKey, secretKey, region string, now time.Time) bool {
	if want := r.URL.Query().Get("X-Amz-Signature"); want != "" {
		q := r.URL.Query()
		t, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
		if err != nil || q.Get("X-Amz-Credential") != accessKey+"/"+t.Format("20060102")+"/"+region+"/s3/aws4_request" {
			return false
		}
		exp, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || now.After(t.Add(time.Duration(exp)*time.Second)) {
			return false
		}
		u := &url.URL{Scheme: "http", Host: r.Host, Path: r.URL.Path}
		got := presignURL(u, accessKey, secretKey, region, time.Duration(exp)*time.Second, t)
		return strings.HasSuffix(got, "X-Amz-Signature="+want)
	}
	auth := r.Header.Get("Authorization")
	i := strings.Index(auth, "SignedHeaders=")
	j := strings.Index(auth, "Signature=")
	if !strings.HasPrefix(auth, sigAlgorithm+" Credential="+accessKey+"/") || i < 0 || j < 0 || j < i {
		return false
	}
	signed := strings.TrimSuffix(auth[i+len("SignedHeaders="):j], ", ")
	h := http.Header{}
	for _, n := range strings.Split(signed, ";") {
		if n != "host" {
			h[http.CanonicalHeaderKey(n)] = r.Header.Values(n)
		}
	}
	t, err := time.Parse(amzDateFormat, r.Header.Get("X-Amz-Date"))
	if err != nil {
		return false
	}
	cr, _ := canonicalRequest(r.Method, r.URL, h, r.Host, r.Header.Get("X-Amz-Content-Sha256"))
	sts, _ := stringToSign(t, region, cr)
	sig := hex.EncodeToString(hmacSHA256(signingKey(secretKey, t.Format("20060102"), region), sts))
	return hmac.Equal([]byte(sig), []byte(auth[j+len("Signature="):]))
}
