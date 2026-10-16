package events

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestDecodeBucketsValid(t *testing.T) {
	tab, err := DecodeBuckets("r1:0*40,1*150,2*10")
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range tab {
		want := uint8(0)
		switch {
		case i >= 190:
			want = 2
		case i >= 40:
			want = 1
		}
		if v != want {
			t.Fatalf("bucket %d = %d, want %d", i, v, want)
		}
	}
	// Single tokens, explicit *1, max values.
	single := "r1:" + strings.TrimSuffix(strings.Repeat("255,", 199), ",") + ",7*1"
	tab, err = DecodeBuckets(single)
	if err != nil {
		t.Fatal(err)
	}
	if tab[0] != 255 || tab[198] != 255 || tab[199] != 7 {
		t.Fatalf("got %v", tab)
	}
	if _, err := DecodeBuckets("r1:0*200"); err != nil {
		t.Fatalf("all-zero table: %v", err)
	}
}

func TestDecodeBucketsInvalid(t *testing.T) {
	cases := map[string]string{
		"no prefix":         "0*200",
		"wrong version":     "r2:0*200",
		"empty body":        "r1:",
		"short":             "r1:0*199",
		"long":              "r1:0*200,1",
		"overflow run":      "r1:1*201",
		"count > 255":       "r1:256*200",
		"negative":          "r1:-1*200",
		"leading zero":      "r1:01*200",
		"run leading zero":  "r1:1*0200",
		"zero run":          "r1:1*0,0*200",
		"empty token":       "r1:0*100,,0*100",
		"trailing comma":    "r1:0*200,",
		"empty count":       "r1:*200",
		"empty run":         "r1:1*",
		"double star":       "r1:1*2*100",
		"space":             "r1: 0*200",
		"float":             "r1:1.5*200",
		"hex":               "r1:0x1*200",
		"plus":              "r1:+1*200",
		"base64 (old)":      "eJzLSM3JyQcABiwCFQ==",
		"unicode digits":    "r1:٣*200",
		"too long overall":  "r1:" + strings.Repeat("0,", 600),
		"four digit count":  "r1:1000*200",
		"four digit run":    "r1:1*1000",
		"many runs overrun": "r1:" + strings.Repeat("0*199,", 3),
	}
	for name, in := range cases {
		if _, err := DecodeBuckets(in); err == nil {
			t.Errorf("%s: %q accepted", name, in)
		}
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range 2000 {
		var tab ReplayTable
		// Mostly runs, sometimes noise, like real tables.
		v := uint8(r.IntN(4))
		for i := range tab {
			if r.IntN(10) == 0 {
				v = uint8(r.IntN(256))
			}
			tab[i] = v
		}
		s := EncodeBuckets(tab)
		if len(s) > BucketsMaxLen {
			t.Fatalf("encoded length %d", len(s))
		}
		got, err := DecodeBuckets(s)
		if err != nil {
			t.Fatalf("%q: %v", s, err)
		}
		if got != tab {
			t.Fatalf("round trip mismatch for %q", s)
		}
	}
	var worst ReplayTable
	for i := range worst {
		worst[i] = uint8(255 - i%2)
	}
	if s := EncodeBuckets(worst); len(s) > BucketsMaxLen {
		t.Fatalf("worst case %d bytes", len(s))
	}
}

// TestDecodeBucketsMutations feeds random byte mutations of valid tables:
// the decoder must never panic, and anything it accepts must re-encode to a
// table that decodes to the same values.
func TestDecodeBucketsMutations(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	alphabet := []byte("0123456789*,r1:-+ x")
	base := []string{"r1:0*40,1*150,2*10", "r1:0*200", "r1:3,2,1*198", EncodeBuckets(ReplayTable{1, 2, 3})}
	for range 20000 {
		b := []byte(base[r.IntN(len(base))])
		for range 1 + r.IntN(4) {
			switch op := r.IntN(3); {
			case op == 0 && len(b) > 0: // replace
				b[r.IntN(len(b))] = alphabet[r.IntN(len(alphabet))]
			case op == 1: // insert
				i := r.IntN(len(b) + 1)
				b = append(b[:i], append([]byte{alphabet[r.IntN(len(alphabet))]}, b[i:]...)...)
			case len(b) > 0: // delete
				i := r.IntN(len(b))
				b = append(b[:i], b[i+1:]...)
			}
		}
		tab, err := DecodeBuckets(string(b))
		if err != nil {
			continue
		}
		again, err := DecodeBuckets(EncodeBuckets(tab))
		if err != nil || again != tab {
			t.Fatalf("%q decoded but does not round-trip: %v", b, err)
		}
	}
}

func FuzzDecodeBuckets(f *testing.F) {
	for _, s := range []string{"r1:0*40,1*150,2*10", "r1:0*200", "r1:", "r1:256*200", "r1:1*0"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		tab, err := DecodeBuckets(s)
		if err != nil {
			return
		}
		if !strings.HasPrefix(s, BucketsVersion) || len(s) > BucketsMaxLen {
			t.Fatalf("accepted %q", s)
		}
		if again, err := DecodeBuckets(EncodeBuckets(tab)); err != nil || again != tab {
			t.Fatalf("round trip %q: %v", s, err)
		}
	})
}

func TestRegistryBucketsQuarantine(t *testing.T) {
	ev := func(name string, props map[string]any) json.RawMessage {
		m := map[string]any{"event_id": "e1", "event": name, "schema_ver": 1, "ts_client": 1760000000000,
			"session_id": "s", "play_id": "p", "platform": "web", "app_ver": "1", "consent": []string{"necessary"}, "props": props}
		b, _ := json.Marshal(m)
		return b
	}
	hb := func(buckets any) map[string]any {
		p := map[string]any{"position_ms": 1, "rate": 1, "quality": "720p", "fullscreen": false, "pip": false, "foreground": true}
		if buckets != nil {
			p["buckets"] = buckets
		}
		return p
	}
	end := func(buckets any) map[string]any {
		p := map[string]any{"reason": "closed", "watched_seconds": 10}
		if buckets != nil {
			p["buckets"] = buckets
		}
		return p
	}
	for _, mk := range []func(any) map[string]any{hb, end} {
		name := "heartbeat"
		if _, ok := mk(nil)["reason"]; ok {
			name = "play_end"
		}
		if _, err := Default.Validate(ev(name, mk(nil)), testNow); err != nil {
			t.Errorf("%s without buckets rejected: %v", name, err)
		}
		if _, err := Default.Validate(ev(name, mk("r1:0*40,1*150,2*10")), testNow); err != nil {
			t.Errorf("%s with valid buckets rejected: %v", name, err)
		}
		for _, bad := range []any{"r1:0*199", "eJzLSM3JyQcABiwCFQ==", 5, "r1:" + strings.Repeat("1,", 600)} {
			_, err := Default.Validate(ev(name, mk(bad)), testNow)
			if err == nil {
				t.Errorf("%s with buckets %v accepted", name, bad)
				continue
			}
			if !strings.Contains(err.Error(), `"buckets"`) {
				t.Errorf("%s: reason %q does not name the field", name, err)
			}
		}
	}
	res := Default.ProcessBatch([]json.RawMessage{ev("play_end", end("r1:9*201"))}, testNow, nil, false)
	if len(res.Valid) != 0 || len(res.Rejected) != 1 || !strings.Contains(res.Rejected[0].Reason, "replay table") {
		t.Fatalf("batch: %+v", res)
	}
}
