package api

import (
	"avxxx/internal/recs"
	"encoding/json"
	"strings"
	"testing"
)

// Tonight's mood / company are validated, echoed and act as soft
// preferences; cold-start interests boost matching releases.
func TestRecsTonightMoodAndColdStart(t *testing.T) {
	for path, want := range map[string]int{
		"/api/recs?slot=tonight&mood=sad":                    400,
		"/api/recs?slot=tonight&company=boss":                400,
		"/api/recs?slot=tonight&mood=light&company=friends":  200,
		"/api/recs?slot=tonight&mood=any&company=family":     200,
		"/api/recs?slot=home&mood=touching&company=self":     200,
		"/api/recs?slot=tonight&mood=brainy&duration=series": 200,
	} {
		if rec := do(recsMux(newStubRecs()), "GET", path, "", ""); rec.Code != want {
			t.Errorf("%s: %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}

	pick := func(st *stubRecs, q string) int64 {
		t.Helper()
		rec := do(recsMux(st), "GET", "/api/recs?slot=tonight"+q, "good", "")
		var body struct {
			Item *struct {
				ID int64 `json:"id"`
			} `json:"item"`
			Mood    string `json:"mood"`
			Company string `json:"company"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Item == nil {
			t.Fatalf("%s: %s", q, rec.Body)
		}
		if q == "" && (body.Mood != "any" || body.Company != "self") {
			t.Errorf("defaults echoed as %q / %q", body.Mood, body.Company)
		}
		return body.Item.ID
	}
	st := newStubRecs()
	st.recall.Candidates = []recs.Candidate{
		{ReleaseID: 3, Channel: recs.ChanTrending, Raw: 10, Label: "week", BoardRank: 1},
		{ReleaseID: 4, Channel: recs.ChanTrending, Raw: 9, Label: "week", BoardRank: 2},
	}
	x := st.infos[4]
	x.Tags = []string{"drama"}
	st.infos[4] = x
	if id := pick(st, ""); id != 3 {
		t.Errorf("without preferences: %d", id)
	}
	if id := pick(st, "&mood=touching"); id != 4 {
		t.Errorf("touching should prefer the drama: %d", id)
	}
	// A cold-start user who chose drama gets the same lift without a mood.
	st.recall.ColdStart, st.recall.Interests = true, []string{"drama"}
	if id := pick(st, ""); id != 4 {
		t.Errorf("cold-start interests: %d", id)
	}
	// Interests are ignored once the user has history.
	st.recall.ColdStart = false
	if id := pick(st, ""); id != 3 {
		t.Errorf("warm user: %d", id)
	}
	if rec := do(recsMux(st), "GET", "/api/recs?slot=home", "good", ""); !strings.Contains(rec.Body.String(), `"slot":"home"`) {
		t.Errorf("home: %s", rec.Body)
	}
}
