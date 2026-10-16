package catalog

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RobotsToken is the product token the catalog crawler identifies as. It is
// the name robots.txt groups must use to address this crawler specifically.
const RobotsToken = "avxxx-catalog"

// crawlerVersion is the version in the User-Agent string.
const crawlerVersion = "0.2"

// UserAgent builds the honest crawler User-Agent: the product token, version
// and a contact URL or email operators can use to reach us. It returns "" when
// contact is empty, which callers must treat as a configuration error.
func UserAgent(contact string) string {
	contact = strings.TrimSpace(contact)
	if contact == "" {
		return ""
	}
	if strings.Contains(contact, "@") && !strings.Contains(contact, "://") && !strings.HasPrefix(contact, "mailto:") {
		contact = "mailto:" + contact
	}
	return fmt.Sprintf("%s/%s (+%s)", RobotsToken, crawlerVersion, contact)
}

// UserAgentFromEnv reads CATALOG_USER_AGENT (used verbatim; it must contain a
// contact URL or email) or else builds one from CATALOG_CONTACT.
func UserAgentFromEnv(getenv func(string) string) (string, error) {
	if ua := strings.TrimSpace(getenv("CATALOG_USER_AGENT")); ua != "" {
		if !strings.Contains(ua, "://") && !strings.Contains(ua, "@") {
			return "", errors.New("CATALOG_USER_AGENT must include a contact URL or email")
		}
		return ua, nil
	}
	ua := UserAgent(getenv("CATALOG_CONTACT"))
	if ua == "" {
		return "", errors.New("CATALOG_CONTACT (a contact URL or email for the crawler User-Agent) is not set")
	}
	return ua, nil
}

// RobotsRules is one parsed robots.txt, reduced to the groups that apply to a
// single product token (RFC 9309 2.2.1).
type RobotsRules struct {
	rules      []robotsRule
	CrawlDelay time.Duration
}

type robotsRule struct {
	allow   bool
	pattern string
}

type robotsGroup struct {
	agents     []string
	rules      []robotsRule
	crawlDelay time.Duration
	hasDelay   bool
}

// maxRobotsBytes is the RFC 9309 2.5 parsing limit.
const maxRobotsBytes = 500 << 10

// ParseRobots parses body and keeps the groups addressed to token (case
// insensitive); when none names it, the "*" groups apply; when there are
// none either, everything is allowed.
func ParseRobots(body []byte, token string) *RobotsRules {
	if len(body) > maxRobotsBytes {
		body = body[:maxRobotsBytes]
	}
	var groups []*robotsGroup
	var cur *robotsGroup
	inRules := false
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 64<<10), maxRobotsBytes)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "user-agent":
			if cur == nil || inRules {
				cur = &robotsGroup{}
				groups = append(groups, cur)
				inRules = false
			}
			cur.agents = append(cur.agents, strings.ToLower(value))
		case "allow", "disallow":
			if cur == nil {
				continue // rules before any user-agent line are ignored
			}
			inRules = true
			if value == "" {
				continue // an empty Disallow allows everything: no rule
			}
			cur.rules = append(cur.rules, robotsRule{allow: key == "allow", pattern: value})
		case "crawl-delay":
			if cur == nil {
				continue
			}
			inRules = true
			if secs, err := strconv.ParseFloat(value, 64); err == nil && secs >= 0 {
				d := time.Duration(secs * float64(time.Second))
				if d > time.Hour {
					d = time.Hour
				}
				cur.crawlDelay, cur.hasDelay = d, true
			}
		}
	}
	token = strings.ToLower(token)
	pick := func(match func(string) bool) *RobotsRules {
		var out *RobotsRules
		for _, g := range groups {
			for _, a := range g.agents {
				if match(a) {
					if out == nil {
						out = &RobotsRules{}
					}
					out.rules = append(out.rules, g.rules...)
					if g.hasDelay && g.crawlDelay > out.CrawlDelay {
						out.CrawlDelay = g.crawlDelay
					}
					break
				}
			}
		}
		return out
	}
	if r := pick(func(a string) bool { return a == token }); r != nil {
		return r
	}
	if r := pick(func(a string) bool { return a == "*" }); r != nil {
		return r
	}
	return &RobotsRules{}
}

// Allowed reports whether path (path plus optional ?query) may be fetched:
// the longest matching rule wins and Allow wins ties (RFC 9309 2.2.2).
func (r *RobotsRules) Allowed(path string) bool {
	if r == nil {
		return true
	}
	if path == "" {
		path = "/"
	}
	if path == "/robots.txt" {
		return true
	}
	best, allow := -1, true
	for _, rule := range r.rules {
		if !robotsMatch(rule.pattern, path) {
			continue
		}
		n := len(rule.pattern)
		if n > best || (n == best && rule.allow) {
			best, allow = n, rule.allow
		}
	}
	return allow
}

// robotsMatch matches a robots.txt path pattern with * wildcards and an
// optional trailing $ end anchor against path.
func robotsMatch(pattern, path string) bool {
	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = strings.TrimSuffix(pattern, "$")
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	pos := len(parts[0])
	for i := 1; i < len(parts); i++ {
		part := parts[i]
		if i == len(parts)-1 && anchored {
			return strings.HasSuffix(path[pos:], part)
		}
		j := strings.Index(path[pos:], part)
		if j < 0 {
			return false
		}
		pos += j + len(part)
	}
	if anchored {
		return pos == len(path)
	}
	return true
}

// RobotsVerdict is the robots.txt answer for one URL.
type RobotsVerdict struct {
	Allowed    bool
	CrawlDelay time.Duration
	// Unavailable is true when robots.txt could not be fetched (network
	// error, 429 or 5xx). Automatic crawls treat this as a disallow; manual
	// verify jobs may proceed.
	Unavailable bool
	Reason      string
	// Challenge is set when robots.txt itself was answered with a bot
	// challenge (ChallengeStatus is its HTTP status): the host is challenging
	// this egress, so the Fetcher reports the source blocked instead of
	// fetching anything.
	Challenge       bool
	ChallengeStatus int
}

// RobotsChecker answers robots.txt questions for the Fetcher.
type RobotsChecker interface {
	Check(ctx context.Context, rawURL string) RobotsVerdict
}

// Robots fetches robots.txt once per scheme+host and caches it: successful
// answers for TTL, failures for FailureTTL. It is safe for concurrent use and
// meant to be shared by every crawl in a process.
type Robots struct {
	Client     *http.Client
	UserAgent  string
	Token      string
	TTL        time.Duration
	FailureTTL time.Duration

	now   func() time.Time
	mu    sync.Mutex
	hosts map[string]robotsEntry
}

type robotsEntry struct {
	rules     *RobotsRules
	failure   string
	challenge int // HTTP status of a challenge served instead of robots.txt
	expires   time.Time
}

// NewRobots builds a cache with 24h success / 10m failure lifetimes.
func NewRobots(client *http.Client, userAgent string) *Robots {
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &Robots{Client: client, UserAgent: userAgent, Token: RobotsToken, TTL: 24 * time.Hour, FailureTTL: 10 * time.Minute, now: time.Now, hosts: map[string]robotsEntry{}}
}

// Check returns the verdict for rawURL, fetching robots.txt on a cache miss.
func (r *Robots) Check(ctx context.Context, rawURL string) RobotsVerdict {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return RobotsVerdict{Unavailable: true, Reason: "invalid URL"}
	}
	origin := strings.ToLower(u.Scheme + "://" + u.Host)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hosts == nil {
		r.hosts = map[string]robotsEntry{}
	}
	if r.now == nil {
		r.now = time.Now
	}
	e, ok := r.hosts[origin]
	if !ok || !r.now().Before(e.expires) {
		e = r.fetch(ctx, origin)
		r.hosts[origin] = e
	}
	if e.failure != "" {
		return RobotsVerdict{Unavailable: true, Reason: e.failure, Challenge: e.challenge != 0, ChallengeStatus: e.challenge}
	}
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	v := RobotsVerdict{Allowed: e.rules.Allowed(path), CrawlDelay: e.rules.CrawlDelay}
	if !v.Allowed {
		v.Reason = "disallowed by " + origin + "/robots.txt"
	}
	return v
}

func (r *Robots) fetch(ctx context.Context, origin string) robotsEntry {
	fail := func(reason string) robotsEntry {
		return robotsEntry{failure: reason, expires: r.now().Add(r.FailureTTL)}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/robots.txt", nil)
	if err != nil {
		return fail(err.Error())
	}
	if r.UserAgent != "" {
		req.Header.Set("User-Agent", r.UserAgent)
	}
	resp, err := r.Client.Do(req)
	if err != nil {
		return fail("robots.txt fetch failed: " + err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRobotsBytes))
	if isChallenge(resp, body) {
		// A bot challenge (e.g. Cloudflare's 403 "Just a moment...") instead
		// of robots.txt is not "no robots.txt" (RFC 9309 4xx = allow all):
		// the rules are unknown. The Fetcher reports the source blocked
		// (reason challenge) without fetching any page, for every phase.
		e := fail(fmt.Sprintf("robots.txt answered with a bot challenge (HTTP %d)", resp.StatusCode))
		e.challenge = resp.StatusCode
		return e
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if err != nil {
			return fail("robots.txt read failed: " + err.Error())
		}
		return robotsEntry{rules: ParseRobots(body, r.Token), expires: r.now().Add(r.TTL)}
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		// RFC 9309 2.3.1.4: unreachable — assume complete disallow.
		return fail(fmt.Sprintf("robots.txt unreachable (HTTP %d)", resp.StatusCode))
	case resp.StatusCode >= 400:
		// RFC 9309 2.3.1.3: unavailable (e.g. 404) — no restrictions.
		return robotsEntry{rules: &RobotsRules{}, expires: r.now().Add(r.TTL)}
	}
	return fail(fmt.Sprintf("robots.txt unexpected HTTP %d", resp.StatusCode))
}

// RobotsError is a fetch refused by robots.txt (or, for automatic crawls, by
// an unreachable robots.txt). The run stops for index pages; detail pages are
// skipped.
type RobotsError struct {
	URL    string
	Reason string
}

func (e *RobotsError) Error() string { return "robots.txt: " + e.Reason + " (" + e.URL + ")" }

// IsRobots reports whether err (or a wrapped error) is a RobotsError.
func IsRobots(err error) (*RobotsError, bool) {
	var r *RobotsError
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}
