package api

import (
	"net/http/httptest"
	"testing"
)

func TestRequestRegionOrder(t *testing.T) {
	defer setGeoIPHeader("")
	setGeoIPHeader("X-Geo")
	cases := []struct {
		name, query string
		headers     map[string]string
		want        string
		wantErr     bool
	}{
		{"none", "", nil, "", false},
		{"query wins", "?region=de", map[string]string{"CF-IPCountry": "JP", "X-Geo": "KR"}, "DE", false},
		{"bad query", "?region=deu", nil, "", true},
		{"cloudflare", "", map[string]string{"CF-IPCountry": "jp", "X-Geo": "KR"}, "JP", false},
		{"cloudflare unknown falls through", "", map[string]string{"CF-IPCountry": "XX", "X-Geo": "kr"}, "KR", false},
		{"tor", "", map[string]string{"CF-IPCountry": "T1"}, "", false},
		{"geoip header", "", map[string]string{"X-Geo": "TH"}, "TH", false},
		{"garbage header", "", map[string]string{"X-Geo": "Thailand"}, "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("GET", "/api/home/slots"+tc.query, nil)
		for k, v := range tc.headers {
			r.Header.Set(k, v)
		}
		got, err := requestRegion(r)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: %q, %v; want %q (err %v)", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
	rec := httptest.NewRecorder()
	varyRegion(rec)
	if v := rec.Header().Values("Vary"); len(v) != 2 || v[0] != "CF-IPCountry" || v[1] != "X-Geo" {
		t.Errorf("Vary = %v", v)
	}
}
