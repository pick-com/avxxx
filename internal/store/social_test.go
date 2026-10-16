package store

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestApplySettingsPatch(t *testing.T) {
	def := DefaultSettings()
	ok := []struct {
		name  string
		body  string
		check func(UserSettings) bool
	}{
		{"empty object keeps defaults", `{}`, func(u UserSettings) bool { return u.Theme == "system" && u.Autoplay }},
		{"enums", `{"theme":"dark","ui_lang":"ja","content_lang":"en","quality_pref":"720p"}`, func(u UserSettings) bool {
			return u.Theme == "dark" && u.UILang == "ja" && u.ContentLang == "en" && u.QualityPref == "720p"
		}},
		{"bools", `{"data_saver":true,"autoplay":false,"remember_rate":false,"history_paused":true}`, func(u UserSettings) bool {
			return u.DataSaver && !u.Autoplay && !u.RememberRate && u.HistoryPaused
		}},
		{"later auto remove default on, can be turned off", `{"later_auto_remove":false}`, func(u UserSettings) bool {
			return def.LaterAutoRemove && !u.LaterAutoRemove
		}},
		{"subtitle langs dedupe", `{"subtitle_langs":["en","ja","en"]}`, func(u UserSettings) bool {
			return slices.Equal(u.SubtitleLangs, []string{"en", "ja"})
		}},
		{"timezone", `{"timezone":"Asia/Tokyo"}`, func(u UserSettings) bool { return u.Location().String() == "Asia/Tokyo" }},
		{"quiet hours off", `{"quiet_hours":null}`, func(u UserSettings) bool { return u.QuietHours == nil }},
		{"quiet hours set", `{"quiet_hours":{"start":"23:30","end":"07:00"}}`, func(u UserSettings) bool {
			return u.QuietHours != nil && u.QuietHours.Start == "23:30" && u.QuietHours.End == "07:00"
		}},
		{"notify prefs merge", `{"notify_prefs":{"new_work":{"email":true}}}`, func(u UserSettings) bool {
			return u.NotifyPrefs.Enabled("new_work", "email") && u.NotifyPrefs.Enabled("new_work", "push") && u.NotifyPrefs.Enabled("reservation", "in_app")
		}},
	}
	for _, tc := range ok {
		got, err := ApplySettingsPatch(def, []byte(tc.body))
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if !tc.check(got) {
			t.Errorf("%s: got %+v", tc.name, got)
		}
	}
	if def.NotifyPrefs["new_work"]["email"] {
		t.Error("ApplySettingsPatch mutated its input")
	}

	bad := []string{
		`[]`, `null`, `not json`,
		`{"unknown":1}`,
		`{"theme":"blue"}`, `{"theme":1}`,
		`{"ui_lang":"xx"}`, `{"quality_pref":"8k"}`,
		`{"autoplay":"yes"}`, `{"autoplay":null}`,
		`{"subtitle_langs":"en"}`, `{"subtitle_langs":["klingon"]}`,
		`{"timezone":"Mars/Base"}`, `{"timezone":"Local"}`, `{"timezone":""}`,
		`{"quiet_hours":{"start":"25:00","end":"08:00"}}`,
		`{"quiet_hours":{"start":"22:00"}}`,
		`{"quiet_hours":{"start":"08:00","end":"08:00"}}`,
		`{"quiet_hours":{"start":"22:00","end":"08:00","tz":"x"}}`,
		`{"notify_prefs":{"spam":{"in_app":true}}}`,
		`{"notify_prefs":{"new_work":{"sms":true}}}`,
		`{"notify_prefs":{"new_work":{"push":"on"}}}`,
	}
	for _, body := range bad {
		if _, err := ApplySettingsPatch(def, []byte(body)); !errors.Is(err, ErrInvalidSettings) {
			t.Errorf("%s: err = %v, want ErrInvalidSettings", body, err)
		}
	}
}

func TestReorderPositions(t *testing.T) {
	cur := []string{"a", "b", "c", "d"}
	got, err := ReorderPositions(cur, []string{"c", "a"})
	if err != nil || !slices.Equal(got, []string{"c", "a", "b", "d"}) {
		t.Fatalf("partial reorder = %v, %v", got, err)
	}
	got, err = ReorderPositions(cur, []string{"d", "c", "b", "a"})
	if err != nil || !slices.Equal(got, []string{"d", "c", "b", "a"}) {
		t.Fatalf("full reorder = %v, %v", got, err)
	}
	if _, err = ReorderPositions(cur, []string{"a", "a"}); !errors.Is(err, ErrInvalidOrder) {
		t.Errorf("duplicate: err = %v", err)
	}
	if _, err = ReorderPositions(cur, []string{"z"}); !errors.Is(err, ErrInvalidOrder) {
		t.Errorf("unknown: err = %v", err)
	}
}

func TestShouldNotifyFollow(t *testing.T) {
	cases := []struct {
		mode   string
		lead   bool
		expect bool
	}{
		{NotifyAll, false, true}, {NotifyAll, true, true},
		{NotifyLeadOnly, true, true}, {NotifyLeadOnly, false, false},
		{NotifyOff, true, false}, {"bogus", true, false},
	}
	for _, c := range cases {
		if got := ShouldNotifyFollow(c.mode, c.lead); got != c.expect {
			t.Errorf("ShouldNotifyFollow(%q,%v) = %v", c.mode, c.lead, got)
		}
	}
}

func TestLeadPerformers(t *testing.T) {
	leads := LeadPerformers([]Credit{{PerformerID: 1, IsPrimary: true}, {PerformerID: 2}, {PerformerID: 3, Role: "Lead"}})
	if !leads[1] || leads[2] || !leads[3] {
		t.Errorf("leads = %v", leads)
	}
	// No lead marked anywhere: everyone counts as lead.
	leads = LeadPerformers([]Credit{{PerformerID: 1, Role: "guest"}, {PerformerID: 2}})
	if !leads[1] || !leads[2] {
		t.Errorf("fallback leads = %v", leads)
	}
}

func TestFanoutRecipients(t *testing.T) {
	credits := []Credit{{PerformerID: 10, IsPrimary: true}, {PerformerID: 20}}
	follows := []FollowRow{
		{UserID: 1, PerformerID: 10, NotifyMode: NotifyAll},
		{UserID: 1, PerformerID: 20, NotifyMode: NotifyAll}, // same user twice -> one recipient
		{UserID: 2, PerformerID: 20, NotifyMode: NotifyLeadOnly},
		{UserID: 3, PerformerID: 10, NotifyMode: NotifyLeadOnly},
		{UserID: 4, PerformerID: 10, NotifyMode: NotifyOff},
		{UserID: 5, PerformerID: 99, NotifyMode: NotifyAll}, // not credited
	}
	got := FanoutRecipients(credits, follows, []int64{3, 6}, true)
	if len(got) != 3 {
		t.Fatalf("recipients = %v", keys(got))
	}
	if r := got[1]; r == nil || r.Reason != ReasonFollow || len(r.Via) != 2 {
		t.Errorf("user 1 = %+v", r)
	}
	if r := got[3]; r == nil || r.Reason != ReasonReservation {
		t.Errorf("user 3 = %+v (reservation wins)", r)
	}
	if r := got[6]; r == nil || r.Reason != ReasonReservation {
		t.Errorf("user 6 = %+v", r)
	}
	stale := FanoutRecipients(credits, follows, []int64{6}, false)
	if len(stale) != 1 || stale[6] == nil {
		t.Errorf("stale release recipients = %v, want only reservation holder", keys(stale))
	}
}

func keys(m map[int64]*Recipient) []int64 {
	var out []int64
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestFreshForFollowers(t *testing.T) {
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	old := now.AddDate(0, 0, -60)
	recent := now.AddDate(0, 0, -3)
	if !FreshForFollowers(nil, now) || !FreshForFollowers(&recent, now) || FreshForFollowers(&old, now) {
		t.Error("freshness window wrong")
	}
}

func TestMergedKey(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	now := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC) // 05:00 next day in Tokyo
	if got := MergedKey(7, now, time.UTC); got != "7:2026-10-15" {
		t.Errorf("utc key = %q", got)
	}
	if got := MergedKey(7, now, tokyo); got != "7:2026-10-16" {
		t.Errorf("tokyo key = %q", got)
	}
}

func TestQuietUntil(t *testing.T) {
	loc := time.UTC
	night := &QuietHours{Start: "22:00", End: "08:00"}
	day := &QuietHours{Start: "13:00", End: "15:00"}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 15, h, m, 0, 0, loc) }
	cases := []struct {
		name string
		now  time.Time
		qh   *QuietHours
		want time.Time
	}{
		{"no window", at(23, 0), nil, at(23, 0)},
		{"before window", at(21, 59), night, at(21, 59)},
		{"late evening -> next morning", at(23, 0), night, time.Date(2026, 10, 16, 8, 0, 0, 0, loc)},
		{"early morning -> same morning", at(3, 0), night, at(8, 0)},
		{"end is exclusive", at(8, 0), night, at(8, 0)},
		{"daytime window", at(14, 0), day, at(15, 0)},
		{"after daytime window", at(15, 1), day, at(15, 1)},
	}
	for _, c := range cases {
		if got := QuietUntil(c.now, loc, c.qh); !got.Equal(c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestPlanDelivery(t *testing.T) {
	now := time.Date(2026, 10, 15, 23, 0, 0, 0, time.UTC)
	u := DefaultSettings()
	d := PlanDelivery(u, 9, ReasonFollow, now)
	if !d.InApp || !slices.Equal(d.Channels, []string{"push"}) || d.MergedKey != "9:2026-10-15" {
		t.Errorf("default delivery = %+v", d)
	}
	if !d.SendAfter.After(now) {
		t.Errorf("quiet hours should delay push, send_after = %v", d.SendAfter)
	}
	u, _ = ApplySettingsPatch(u, []byte(`{"quiet_hours":null,"notify_prefs":{"reservation":{"in_app":false,"push":false,"email":true}}}`))
	d = PlanDelivery(u, 9, ReasonReservation, now)
	if d.InApp || !slices.Equal(d.Channels, []string{"email"}) || !d.SendAfter.Equal(now) {
		t.Errorf("reservation delivery = %+v", d)
	}
}
