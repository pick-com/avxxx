package media

import "testing"

func TestExactCodeMatch(t *testing.T) {
	items := []SearchResult{
		{ID: "video/113750/snos-397-reducing-mosaic"},
		{ID: "video/113694/snos-397"},
		{ID: "video/1/abc-1234"},
	}
	cases := []struct {
		code, want string
		ok         bool
	}{
		{"snos-397", "video/113694/snos-397", true},
		{"SNOS-397", "video/113694/snos-397", true},
		{"abc-123", "", false}, // abc-1234 must not match abc-123
		{"doa-078", "", false},
	}
	for _, c := range cases {
		got, ok, err := ExactCodeMatch(items, c.code)
		if err != nil || ok != c.ok || got.ID != c.want {
			t.Errorf("%s: got %q %v %v, want %q %v", c.code, got.ID, ok, err, c.want, c.ok)
		}
	}
	dup := []SearchResult{{ID: "video/1/xyz-1"}, {ID: "video/2/xyz-1"}}
	if _, ok, _ := ExactCodeMatch(dup, "xyz-1"); ok {
		t.Error("two different exact matches must be treated as ambiguous")
	}
}
