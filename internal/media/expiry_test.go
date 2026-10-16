package media

import (
	"testing"
	"time"
)

func TestStreamURLExpiry(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	in1h := now.Add(time.Hour)
	u := func(t time.Time) string { return itoa(t.Unix()) }
	for _, tc := range []struct {
		name string
		url  string
		want time.Time // zero: not parsed
	}{
		{"expires", "https://cdn.example/v.mp4?expires=" + u(in1h) + "&sig=ab", in1h},
		{"e + s", "https://cdn.example/v.mp4?s=" + u(now) + "&e=" + u(in1h) + "&h=x", in1h},
		{"exp upper-case", "https://cdn.example/v.m3u8?EXP=" + u(in1h), in1h},
		{"validto", "https://cdn.example/v.mp4?validfrom=" + u(now) + "&validto=" + u(in1h), in1h},
		{"cloudfront", "https://d1.cloudfront.net/v.mp4?Expires=" + u(in1h) + "&Signature=x&Key-Pair-Id=K", in1h},
		{"sigv4", "https://b.s3.amazonaws.com/v.mp4?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=20261016T120000Z&X-Amz-Expires=3600&X-Amz-Signature=x", in1h},
		{"akamai query", "https://cdn.example/v.m3u8?hdnts=exp=" + u(in1h) + "~acl=/*~hmac=ab", in1h},
		{"akamai path", "https://cdn.example/hdnts=st=" + u(now) + "~exp=" + u(in1h) + "~hmac=ab/v.m3u8", in1h},
		{"path exp", "https://cdn.example/exp=" + u(in1h) + "/v.mp4", in1h},
		{"agreeing duplicates", "https://cdn.example/exp=" + u(in1h) + "/v.mp4?expires=" + u(in1h), in1h},

		{"no token", "https://cdn.example/v.mp4", time.Time{}},
		{"unrelated number", "https://cdn.example/v.mp4?t=" + u(in1h), time.Time{}},
		{"milliseconds", "https://cdn.example/v.mp4?expires=" + u(in1h) + "000", time.Time{}},
		{"not a timestamp", "https://cdn.example/v.mp4?e=abc", time.Time{}},
		{"duration not time", "https://cdn.example/v.mp4?expires=3600", time.Time{}},
		{"far past", "https://cdn.example/v.mp4?e=1234567890", time.Time{}},
		{"far future", "https://cdn.example/v.mp4?e=" + u(now.Add(60*24*time.Hour)), time.Time{}},
		{"conflicting", "https://cdn.example/v.mp4?expires=" + u(in1h) + "&e=" + u(now.Add(2*time.Hour)), time.Time{}},
		{"bad sigv4", "https://b.s3.amazonaws.com/v.mp4?X-Amz-Date=oops&X-Amz-Expires=3600", time.Time{}},
		{"not a url", "::", time.Time{}},
	} {
		got, ok := StreamURLExpiry(tc.url, now)
		if ok != !tc.want.IsZero() || (ok && !got.Equal(tc.want)) {
			t.Errorf("%s: StreamURLExpiry(%q) = %s, %v; want %s", tc.name, tc.url, got, ok, tc.want)
		}
	}
}

func TestFillStreamExpiryKeepsAdapterValue(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	set := now.Add(5 * time.Minute)
	streams := []Stream{
		{URL: "https://cdn.example/a.mp4?expires=" + itoa(now.Add(time.Hour).Unix()), ExpiresAt: &set},
		{URL: "https://cdn.example/b.mp4?expires=" + itoa(now.Add(time.Hour).Unix())},
		{URL: "https://cdn.example/c.mp4"},
	}
	fillStreamExpiry(streams, now)
	if !streams[0].ExpiresAt.Equal(set) || streams[1].ExpiresAt == nil || !streams[1].ExpiresAt.Equal(now.Add(time.Hour)) || streams[2].ExpiresAt != nil {
		t.Fatalf("fill = %v %v %v", streams[0].ExpiresAt, streams[1].ExpiresAt, streams[2].ExpiresAt)
	}
}

func itoa(n int64) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
