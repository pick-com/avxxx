package objstore

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// AWS's published Signature V4 examples for S3 ("Examples: Signature
// Calculations in AWS Signature Version 4" and "Authenticating Requests:
// Using Query Parameters"), with the documented example credentials.
const (
	awsExampleAccess = "AKIAIOSFODNN7EXAMPLE"
	awsExampleSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

var awsExampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func TestSignRequestAWSExampleGetObject(t *testing.T) {
	req, _ := http.NewRequest(http.MethodGet, "https://examplebucket.s3.amazonaws.com/test.txt", nil)
	req.Header.Set("Range", "bytes=0-9")
	signRequest(req, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", awsExampleAccess, awsExampleSecret, "us-east-1", awsExampleTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n%s\nwant\n%s", got, want)
	}
}

func TestPresignAWSExample(t *testing.T) {
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	got := presignURL(u, awsExampleAccess, awsExampleSecret, "us-east-1", 86400*time.Second, awsExampleTime)
	if !strings.HasSuffix(got, "X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404") {
		t.Errorf("presigned URL = %s", got)
	}
	if !strings.Contains(got, "X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request") {
		t.Errorf("credential not canonical-encoded: %s", got)
	}
}

func TestURIEncode(t *testing.T) {
	if got := uriEncode("a b/c~d*e", false); got != "a%20b/c~d%2Ae" {
		t.Errorf("uriEncode path = %q", got)
	}
	if got := uriEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("uriEncode query = %q", got)
	}
}
