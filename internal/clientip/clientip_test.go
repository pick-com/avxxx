package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestParse(t *testing.T) {
	for _, ok := range []string{"", " ", "127.0.0.1/32,::1/128", " 10.0.0.0/8 , ,172.16.0.0/12", "127.0.0.1", "::1", "::ffff:10.0.0.0/104", "10.1.2.3/8"} {
		if _, err := Parse(ok); err != nil {
			t.Errorf("Parse(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"localhost", "127.0.0.1/33", "10.0.0.0/8;", "fe80::1%eth0/64", "fe80::1%eth0", "1.2.3", "300.1.1.1/8", "*"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
	if r, _ := Parse(""); r.Trusts() {
		t.Error("empty spec trusts a proxy")
	}
}

func TestIP(t *testing.T) {
	caddy := "127.0.0.1/32,::1/128"
	cases := []struct {
		name, trusted, remote string
		xff                   []string
		want                  string
	}{
		{"default ignores header", "", "203.0.113.7:5555", []string{"198.51.100.1"}, "203.0.113.7"},
		{"default proxy peer", "", "127.0.0.1:40000", []string{"198.51.100.1"}, "127.0.0.1"},
		{"default ipv6 peer", "", "[2001:db8::5]:443", nil, "2001:db8::5"},
		{"unparsable remote kept", "", "pipe", nil, "pipe"},
		{"untrusted peer ignores header", caddy, "203.0.113.7:5555", []string{"198.51.100.1"}, "203.0.113.7"},
		{"trusted peer uses header", caddy, "127.0.0.1:40000", []string{"198.51.100.1"}, "198.51.100.1"},
		{"trusted ipv6 peer", caddy, "[::1]:40000", []string{"2001:db8::9"}, "2001:db8::9"},
		{"trusted peer without header", caddy, "127.0.0.1:40000", nil, "127.0.0.1"},
		{"spoofed left entries ignored", caddy, "127.0.0.1:40000", []string{"10.9.9.9, 1.1.1.1, 198.51.100.1"}, "198.51.100.1"},
		{"spoofed trusted address ignored", caddy, "127.0.0.1:40000", []string{"127.0.0.1, 198.51.100.1"}, "198.51.100.1"},
		{"spoofed separate header ignored", caddy, "127.0.0.1:40000", []string{"6.6.6.6", "198.51.100.1"}, "198.51.100.1"},
		{"trailing trusted hops skipped", "127.0.0.1/32,10.0.0.0/8", "127.0.0.1:1", []string{"6.6.6.6, 198.51.100.1, 10.0.0.2"}, "198.51.100.1"},
		{"all trusted gives left-most", "127.0.0.1/32,10.0.0.0/8", "127.0.0.1:1", []string{"10.0.0.3, 10.0.0.2"}, "10.0.0.3"},
		{"malformed right-most stops", caddy, "127.0.0.1:1", []string{"198.51.100.1, not-an-ip"}, "127.0.0.1"},
		{"malformed left entry harmless", caddy, "127.0.0.1:1", []string{"<script>, 198.51.100.1"}, "198.51.100.1"},
		{"empty entries skipped", caddy, "127.0.0.1:1", []string{"198.51.100.1, ,"}, "198.51.100.1"},
		{"entry with port", caddy, "127.0.0.1:1", []string{"198.51.100.1:7777"}, "198.51.100.1"},
		{"bracketed ipv6 entry", caddy, "127.0.0.1:1", []string{"[2001:db8::1]:80"}, "2001:db8::1"},
		{"mapped ipv4 peer is trusted", caddy, "[::ffff:127.0.0.1]:1", []string{"198.51.100.1"}, "198.51.100.1"},
		{"mapped entry unmapped", caddy, "127.0.0.1:1", []string{"::ffff:198.51.100.1"}, "198.51.100.1"},
		{"zoned entry", caddy, "127.0.0.1:1", []string{"fe80::1%eth0"}, "fe80::1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := Parse(c.trusted)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = c.remote
			for _, v := range c.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if got := r.IP(req); got != c.want {
				t.Errorf("IP = %q, want %q", got, c.want)
			}
		})
	}
}

func TestFromEnv(t *testing.T) {
	r, err := FromEnv(func(k string) string {
		if k == EnvVar {
			return "127.0.0.1/32"
		}
		return ""
	})
	if err != nil || !r.Trusts() {
		t.Fatalf("FromEnv = %+v, %v", r, err)
	}
	if _, err := FromEnv(func(string) string { return "nope" }); err == nil {
		t.Fatal("invalid env accepted")
	}
}
