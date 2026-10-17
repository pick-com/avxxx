package api

import (
	"avxxx/internal/community"
	"fmt"
	"net/http"
	"testing"
)

// Anonymous ratings and 赞 / 踩 count once per X-Device-ID, but the caller
// picks the device id: rotating it on every request must not give one
// client address unlimited votes. Signed-in voters are not affected.
func TestAnonymousVotesAreLimitedPerAddress(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		h                http.Handler
	}{
		{"rating", "/api/releases/abc-123/rating", `{"score":5}`, newCommunityServer(&stubCommunity{poster: oldPoster}, nil, community.NewMemoryLimiter())},
		{"reaction", "/api/releases/abc-123/reaction", `{"value":1}`, newInteractionServer(&stubInteractions{}, &stubCommunity{poster: oldPoster}, false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := 0; i < limitAnonVoteIP.limit; i++ {
				rec := doH(tc.h, "PUT", tc.path, tc.body, map[string]string{"X-Device-ID": fmt.Sprintf("rotating-device-%04d", i)})
				if rec.Code != 200 {
					t.Fatalf("vote %d: %d %s", i, rec.Code, rec.Body)
				}
			}
			rec := doH(tc.h, "PUT", tc.path, tc.body, map[string]string{"X-Device-ID": "yet-another-device"})
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("anonymous vote past the per-address limit: %d %s", rec.Code, rec.Body)
			}
			// A signed-in voter from the same address still votes.
			if rec := doH(tc.h, "PUT", tc.path, tc.body, map[string]string{"Authorization": "Bearer good"}); rec.Code != 200 {
				t.Fatalf("signed-in vote: %d %s", rec.Code, rec.Body)
			}
		})
	}
}
