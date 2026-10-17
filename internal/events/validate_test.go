package events

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// validProps has one valid props object per registered event (v1).
var validProps = map[string]map[string]any{
	"play_request":    {"release_id": 1, "source_id": 2, "entry": "home_row", "reason": "because you watched X"},
	"first_frame":     {"source_id": 2, "quality": "1080p", "elapsed_ms": 850, "network": "wifi"},
	"heartbeat":       {"position_ms": 10000, "rate": 1.5, "quality": "1080p", "fullscreen": false, "pip": false, "foreground": true},
	"rebuffer":        {"source_id": 2, "duration_ms": 1200, "position_ms": 30000},
	"seek":            {"from_ms": 1000, "to_ms": 65000, "method": "peak"},
	"pause":           {"position_ms": 5000},
	"resume":          {"position_ms": 5000, "paused_ms": 3000},
	"quality_change":  {"from": "720p", "to": "1080p", "mode": "auto"},
	"rate_change":     {"from": 1, "to": 1.25, "mode": "manual"},
	"source_switch":   {"from_source_id": 2, "to_source_id": 3, "mode": "auto", "reason": "first_frame_timeout"},
	"play_end":        {"reason": "completed", "watched_seconds": 3600, "buckets": "r1:0*40,1*150,2*10"},
	"peak_click":      {"curve": "playback", "position_ms": 120000, "top3": true, "watched_30s": true},
	"autoplay_next":   {"from_release_id": 1, "to_release_id": 4, "cancelled": false, "countdown_left_s": 3},
	"progress":        {"release_id": 1, "milestone": 50},
	"actor_card_open": {"performer_id": 9, "source": "hover"},
	"follow_actor":    {"performer_id": 9, "source": "actor_page"},
	"search":          {"query": "abc", "result_count": 0, "is_retry": true, "clicked_rank": 2},
	"subtitle_change": {"lang": "en", "from": "zh"},
	"audio_change":    {"lang": "ja"},
	"theme_change":    {"theme": "dark"},
	"danmaku_toggle":  {"enabled": true},
	"danmaku_send":    {"release_id": 1, "position_ms": 1000, "length": 12, "color": "#ffffff"},
	"danmaku_report":  {"danmaku_id": 77, "reason": "spam"},
	"danmaku_setting": {"setting": "opacity"},
	"ad_request":      {"placement": "preroll", "load_ms": 120},
	"ad_impression":   {"placement": "preroll", "ad_id": "ad1", "viewable": true},
	"ad_quartile":     {"placement": "preroll", "ad_id": "ad1", "quartile": 75},
	"ad_skip":         {"placement": "preroll", "ad_id": "ad1"},
	"ad_click":        {"placement": "preroll", "ad_id": "ad1"},
	"ad_error":        {"placement": "preroll", "error_code": "VAST_303"},
	"reward_offer":    {"reward": "4k"},
	"reward_granted":  {"reward": "skip_midroll", "completed": true},
	"reminder_set":    {"performer_id": 9, "channel": "push", "timing": "on_release"},
	"tonight_open":    {"filters": map[string]any{"mood": "light"}},
	"tonight_shuffle": {"attempt": 2},
	"tonight_accept":  {"release_id": 1, "attempt": 3, "reason": "short"},
	"rating_submit":   {"release_id": 1, "score": 8, "watched": true},
	"review_post":     {"release_id": 1, "spoiler": false, "watched": true},
	"review_like":     {"review_id": 5},
	"release_vote":    {"release_id": 1, "vote": "dislike"},
	"comment_post":    {"release_id": 1, "reply": true, "spoiler": false},
	"comment_like":    {"comment_id": 7},
	"share":           {"release_id": 1, "method": "copy", "with_time": true},
	"onboarding_step": {"step": "pick_actors", "skipped": false},
	"consent_change":  {"categories": []any{"necessary"}, "gpc": true},
	"age_gate":        {"passed": true},

	"auth_prompt_shown":     {"action": "favorite", "method": "sheet"},
	"auth_prompt_closed":    {"action": "danmaku", "method": "skip"},
	"auth_prompt_converted": {"action": "later", "method": "google"},
}

func baseEvent(name string, props map[string]any) map[string]any {
	return map[string]any{
		"event_id":   "evt-" + name,
		"event":      name,
		"schema_ver": 1,
		"ts_client":  1760529600000,
		"device_id":  "dev-1",
		"session_id": "sess-1",
		"play_id":    "play-1",
		"platform":   "web",
		"app_ver":    "1.0.0",
		"ui_lang":    "zh-CN",
		"theme":      "dark",
		"page":       "watch",
		"consent":    []any{"necessary", "analytics"},
		"exp":        []any{"home_v2:b"},
		"props":      props,
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// wrongValue returns a value of the wrong JSON type for f.
func wrongValue(f Field) any {
	switch f.Type {
	case String, Timestamp:
		return true
	case Int:
		return 1.5
	case Number, Bool, StringArray, Object:
		return "x"
	}
	return nil
}

func TestEveryEventHasSample(t *testing.T) {
	for _, name := range Default.Names() {
		if _, ok := validProps[name]; !ok {
			t.Errorf("no valid sample for registered event %q", name)
		}
	}
	for name := range validProps {
		if _, ok := Default.Lookup(name, 1); !ok {
			t.Errorf("sample for unregistered event %q", name)
		}
	}
	for _, want := range []string{"play_request", "first_frame", "heartbeat", "rebuffer", "seek", "pause", "resume",
		"quality_change", "rate_change", "source_switch", "play_end", "peak_click", "autoplay_next", "progress",
		"actor_card_open", "follow_actor", "search", "subtitle_change", "audio_change", "theme_change"} {
		if _, ok := Default.Lookup(want, 1); !ok {
			t.Errorf("PRD 14 event %q missing from registry", want)
		}
	}
}

// TestValidatePerEvent checks, for every event: the sample passes; dropping
// each required prop fails; a wrong type for each prop fails.
func TestValidatePerEvent(t *testing.T) {
	for _, name := range Default.Names() {
		schema, _ := Default.Lookup(name, 1)
		t.Run(name, func(t *testing.T) {
			e, err := Default.Validate(mustJSON(t, baseEvent(name, maps.Clone(validProps[name]))), testNow)
			if err != nil {
				t.Fatalf("valid sample rejected: %v", err)
			}
			if e.Name != name || e.SchemaVer != 1 || !e.TSServer.Equal(testNow) {
				t.Errorf("got name=%q ver=%d ts_server=%v", e.Name, e.SchemaVer, e.TSServer)
			}
			for _, f := range schema.Props {
				if f.Required {
					props := maps.Clone(validProps[name])
					delete(props, f.Name)
					_, err := Default.Validate(mustJSON(t, baseEvent(name, props)), testNow)
					if err == nil || !strings.Contains(err.Error(), `missing required field "`+f.Name+`"`) {
						t.Errorf("without %q: err = %v", f.Name, err)
					}
				}
				props := maps.Clone(validProps[name])
				props[f.Name] = wrongValue(f)
				_, err := Default.Validate(mustJSON(t, baseEvent(name, props)), testNow)
				if err == nil || !strings.Contains(err.Error(), `"`+f.Name+`"`) {
					t.Errorf("mistyped %q: err = %v", f.Name, err)
				}
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	seek := func(mod func(m map[string]any)) json.RawMessage {
		m := baseEvent("seek", maps.Clone(validProps["seek"]))
		mod(m)
		return mustJSON(t, m)
	}
	props := func(k string, v any) func(map[string]any) {
		return func(m map[string]any) { m["props"].(map[string]any)[k] = v }
	}
	tests := []struct {
		name   string
		raw    json.RawMessage
		reason string
	}{
		{"not an object", json.RawMessage(`[1]`), "not a JSON object"},
		{"null", json.RawMessage(`null`), "not a JSON object"},
		{"unknown event", seek(func(m map[string]any) { m["event"] = "launch_rocket" }), `unknown event "launch_rocket"`},
		{"unknown version", seek(func(m map[string]any) { m["schema_ver"] = 2 }), "unknown schema_ver 2"},
		{"schema_ver zero", seek(func(m map[string]any) { m["schema_ver"] = 0 }), `"schema_ver" below minimum`},
		{"missing event_id", seek(func(m map[string]any) { delete(m, "event_id") }), `missing required field "event_id"`},
		{"bad event_id chars", seek(func(m map[string]any) { m["event_id"] = "a b" }), `"event_id" must be`},
		{"event_id too long", seek(func(m map[string]any) { m["event_id"] = strings.Repeat("a", 65) }), `"event_id" longer than 64`},
		{"missing ts_client", seek(func(m map[string]any) { delete(m, "ts_client") }), `missing required field "ts_client"`},
		{"bad ts_client", seek(func(m map[string]any) { m["ts_client"] = "yesterday" }), `"ts_client" must be epoch`},
		{"missing session", seek(func(m map[string]any) { delete(m, "session_id") }), `missing required field "session_id"`},
		{"missing consent", seek(func(m map[string]any) { delete(m, "consent") }), `missing required field "consent"`},
		{"unknown consent", seek(func(m map[string]any) { m["consent"] = []any{"everything"} }), `"consent": "everything" not in`},
		{"consent not array", seek(func(m map[string]any) { m["consent"] = "analytics" }), `"consent" must be string array`},
		{"props not object", seek(func(m map[string]any) { m["props"] = []any{} }), `"props" must be object`},
		{"play-scoped without play_id", seek(func(m map[string]any) { delete(m, "play_id") }), `requires play_id`},
		{"enum violation", seek(props("method", "teleport")), `"method": "teleport" not in`},
		{"negative position", seek(props("from_ms", -1)), `"from_ms" below minimum`},
		{"null required", seek(props("to_ms", nil)), `missing required field "to_ms"`},
		{"long string", mustJSON(t, baseEvent("search", map[string]any{"query": strings.Repeat("q", 201), "result_count": 1, "is_retry": false})), `"query" longer than 200`},
		{"score out of range", mustJSON(t, baseEvent("rating_submit", map[string]any{"release_id": 1, "score": 11, "watched": true})), `"score" above maximum 10`},
		{"milestone not quartile", mustJSON(t, baseEvent("progress", map[string]any{"release_id": 1, "milestone": 30})), `"milestone": 30 not in`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Default.Validate(tt.raw, testNow)
			if err == nil {
				t.Fatal("accepted, want rejection")
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("reason %q, want it to contain %q", err.Error(), tt.reason)
			}
		})
	}
}

func TestValidateFields(t *testing.T) {
	m := baseEvent("play_request", maps.Clone(validProps["play_request"]))
	m["ts_client"] = "2026-10-15T11:59:58.5+08:00"
	m["user_id"] = 999 // client-sent user_id is ignored
	m["props"].(map[string]any)["future_field"] = "ok"
	e, err := Default.Validate(mustJSON(t, m), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 15, 3, 59, 58, 5e8, time.UTC); !e.TSClient.Equal(want) {
		t.Errorf("ts_client %v, want %v", e.TSClient, want)
	}
	if e.UserID != nil {
		t.Errorf("client user_id accepted: %v", *e.UserID)
	}
	if e.ReleaseID == nil || *e.ReleaseID != 1 || e.SourceID == nil || *e.SourceID != 2 {
		t.Errorf("release/source not lifted: %v %v", e.ReleaseID, e.SourceID)
	}
	if _, ok := e.Props["release_id"]; ok {
		t.Error("release_id left in props")
	}
	if e.Props["future_field"] != "ok" || e.Props["entry"] != "home_row" {
		t.Errorf("props = %v", e.Props)
	}
	if e.DeviceID != "dev-1" || e.PlayID != "play-1" || len(e.Consent) != 2 || len(e.Exp) != 1 {
		t.Errorf("common fields not copied: %+v", e)
	}
}

func TestConsent(t *testing.T) {
	uid := int64(42)
	tests := []struct {
		name     string
		consent  []any
		gpc      bool
		keepsIDs bool
	}{
		{"analytics", []any{"necessary", "analytics"}, false, true},
		{"analytics with GPC", []any{"analytics"}, true, false},
		{"necessary only", []any{"necessary"}, false, false},
		{"ads but not analytics", []any{"necessary", "personalized_ads"}, false, false},
		{"empty", []any{}, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := baseEvent("pause", map[string]any{"position_ms": 1})
			m["consent"] = tt.consent
			res := Default.ProcessBatch([]json.RawMessage{mustJSON(t, m)}, testNow, &uid, tt.gpc)
			if len(res.Valid) != 1 {
				t.Fatalf("rejected: %+v", res.Rejected)
			}
			e := res.Valid[0]
			if hasIDs := e.UserID != nil && e.DeviceID != ""; hasIDs != tt.keepsIDs {
				t.Errorf("user_id=%v device_id=%q, want identifiers kept=%v", e.UserID, e.DeviceID, tt.keepsIDs)
			}
			if e.SessionID == "" || e.PlayID == "" {
				t.Error("anonymous event lost session/play ids")
			}
		})
	}
}

func TestProcessBatch(t *testing.T) {
	a := mustJSON(t, baseEvent("pause", map[string]any{"position_ms": 1}))
	b := baseEvent("pause", map[string]any{"position_ms": 2})
	b["event_id"] = "other"
	bad := mustJSON(t, map[string]any{"event_id": "x1", "event": "nope", "user_id": 5, "device_id": "d"})
	res := Default.ProcessBatch([]json.RawMessage{a, mustJSON(t, b), a, bad, json.RawMessage(`"str"`)}, testNow, nil, false)
	if len(res.Valid) != 2 || res.InBatchDups != 1 || len(res.Rejected) != 2 {
		t.Fatalf("valid=%d dups=%d rejected=%d", len(res.Valid), res.InBatchDups, len(res.Rejected))
	}
	q := res.Rejected[0]
	if q.EventID != "x1" || q.Name != "nope" || !strings.Contains(q.Reason, "unknown event") || !q.TSServer.Equal(testNow) {
		t.Errorf("quarantine record %+v", q)
	}
	if strings.Contains(string(q.Raw), "user_id") || strings.Contains(string(q.Raw), "device_id") {
		t.Errorf("quarantine raw keeps identifiers: %s", q.Raw)
	}
	if !json.Valid(res.Rejected[1].Raw) {
		t.Errorf("non-object raw not stored as JSON: %s", res.Rejected[1].Raw)
	}
}
