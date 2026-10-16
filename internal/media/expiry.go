package media

import (
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Signed stream URLs often carry their expiry. StreamURLExpiry reads it from
// the unambiguous forms below and returns ok=false for anything else:
//
//   - query parameters expires / expire / expiry / exp / e / validto holding
//     unix seconds (exactly 10 digits), e.g. ?e=1792152000&h=…
//   - AWS SigV4 presigned URLs: X-Amz-Date (20261016T120000Z) + X-Amz-Expires
//     (seconds)
//   - Akamai-style tokens (hdnts= / __token__= / token=) in a query value or a
//     path segment, with exp=<unix seconds> among ~-separated fields
//   - a path segment of the form exp=<unix seconds> or expires=<unix seconds>
//
// It never guesses: a value that is not plain unix seconds, falls outside
// [now-24h, now+30d] (a different meaning, or milliseconds), or disagrees
// with another candidate in the same URL gives ok=false.
func StreamURLExpiry(raw string, now time.Time) (time.Time, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return time.Time{}, false
	}
	var found []time.Time
	add := func(t time.Time, ok bool) {
		if ok {
			found = append(found, t)
		}
	}
	q := u.Query()
	for name, values := range q {
		lower := strings.ToLower(name)
		for _, v := range values {
			switch {
			case expiryParamNames[lower]:
				add(unixSeconds(v))
			case tokenParamNames[lower]:
				for _, t := range tokenFieldExpiries(v) {
					add(t, true)
				}
			}
		}
	}
	if d, e := firstFold(q, "X-Amz-Date"), firstFold(q, "X-Amz-Expires"); d != "" && e != "" {
		start, err1 := time.Parse("20060102T150405Z", d)
		secs, err2 := strconv.Atoi(e)
		if err1 != nil || err2 != nil || secs <= 0 || secs > 7*24*3600 {
			return time.Time{}, false // a SigV4 URL we cannot read is ambiguous
		}
		add(start.Add(time.Duration(secs)*time.Second), true)
	}
	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		seg, _ = url.PathUnescape(seg)
		if seg == "" || !strings.Contains(seg, "=") {
			continue
		}
		for _, t := range tokenFieldExpiries(seg) {
			add(t, true)
		}
	}
	if len(found) == 0 {
		return time.Time{}, false
	}
	at := found[0]
	for _, t := range found[1:] {
		if !t.Equal(at) {
			return time.Time{}, false // two different expiries: ambiguous
		}
	}
	if at.Before(now.Add(-24*time.Hour)) || at.After(now.Add(30*24*time.Hour)) {
		return time.Time{}, false
	}
	return at.UTC(), true
}

var expiryParamNames = map[string]bool{"expires": true, "expire": true, "expiry": true, "exp": true, "e": true, "validto": true}
var tokenParamNames = map[string]bool{"hdnts": true, "__token__": true, "token": true}

// tokenFieldExpiries reads exp= / expires= fields of a token such as
// "exp=1792152000~acl=/*~hmac=…" or a path segment "hdnts=exp=…~…".
func tokenFieldExpiries(s string) []time.Time {
	var out []time.Time
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '~' || r == ',' || r == ';' || r == '&' }) {
		for _, p := range []string{"hdnts=", "__token__=", "token="} {
			part = strings.TrimPrefix(part, p)
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if n := strings.ToLower(name); n == "exp" || n == "expires" {
			if t, ok := unixSeconds(value); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// unixSeconds accepts exactly 10 decimal digits (2001-09-09 … 2286-11-20).
func unixSeconds(v string) (time.Time, bool) {
	if len(v) != 10 {
		return time.Time{}, false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return time.Time{}, false
		}
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0).UTC(), true
}

func firstFold(q url.Values, name string) string {
	for k, v := range q {
		if strings.EqualFold(k, name) && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// fillStreamExpiry sets ExpiresAt from the URL on streams whose adapter left
// it empty.
func fillStreamExpiry(streams []Stream, now time.Time) {
	for i := range streams {
		if streams[i].ExpiresAt != nil {
			continue
		}
		if t, ok := StreamURLExpiry(streams[i].URL, now); ok {
			streams[i].ExpiresAt = &t
		}
	}
}
