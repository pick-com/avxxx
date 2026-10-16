package events

import "sort"

// Consent categories (PRD 14.1 `consent`: 必要 / 分析 / 个性化广告).
const (
	ConsentNecessary       = "necessary"
	ConsentAnalytics       = "analytics"
	ConsentPersonalizedAds = "personalized_ads"
)

// commonFields are the PRD 14.1 fields every event carries at the top level.
// user_id and ts_server are server-assigned and therefore not listed.
var commonFields = []Field{
	{Name: "event_id", Type: String, Required: true, MaxLen: 64},
	{Name: "event", Type: String, Required: true, MaxLen: 64},
	{Name: "schema_ver", Type: Int, Required: true, Min: f64(1)},
	req("ts_client", Timestamp),
	opt("device_id", String),
	req("session_id", String),
	opt("play_id", String),
	req("platform", String),
	req("app_ver", String),
	opt("ui_lang", String),
	opt("theme", String),
	opt("page", String),
	opt("ref_page", String),
	opt("slot", String),
	{Name: "consent", Type: StringArray, Required: true, Enum: []string{ConsentNecessary, ConsentAnalytics, ConsentPersonalizedAds}},
	opt("exp", StringArray),
	opt("props", Object),
}

// Shared enums.
var (
	manualAuto = []string{"manual", "auto"}
	quartiles  = []int64{25, 50, 75, 100}
	adFields   = []Field{req("placement", String), req("ad_id", String)}
	// bucketsField is the optional PRD 14.3 replay table in the r1
	// run-length format (buckets.go); a malformed table quarantines the event.
	bucketsField = Field{Name: "buckets", Type: String, MaxLen: BucketsMaxLen, Format: checkBuckets}
)

// v1 is the initial version of every event's props (schema_ver 1).
// Bump the version (and add a new Schema) whenever a field changes (PRD 14.1).
var v1 = []Schema{
	// --- 14 / 14.2 playback sequence: play_request → first_frame → heartbeat… → play_end
	{Event: "play_request", PlayScoped: true, Props: []Field{
		id("release_id"), id("source_id"),
		enum("entry", "home_row", "search", "actor", "recommend", "history", "favorites", "playlist", "autoplay", "deeplink", "other"),
		opt("reason", String), // 推荐理由
	}},
	{Event: "first_frame", PlayScoped: true, Props: []Field{
		id("source_id"), req("quality", String), nonNeg("elapsed_ms"), req("network", String),
	}},
	{Event: "heartbeat", PlayScoped: true, Props: []Field{
		nonNeg("position_ms"),
		{Name: "rate", Type: Number, Required: true, Min: f64(0)},
		req("quality", String), req("fullscreen", Bool), req("pip", Bool), req("foreground", Bool),
		bucketsField, // 14.3 replay-table delta, long videos every 5 min
	}},
	{Event: "rebuffer", PlayScoped: true, Props: []Field{
		id("source_id"), nonNeg("duration_ms"), nonNeg("position_ms"),
	}},
	{Event: "seek", PlayScoped: true, Props: []Field{
		nonNeg("from_ms"), nonNeg("to_ms"),
		enum("method", "drag", "double_tap", "chapter", "peak", "keyboard"),
	}},
	{Event: "pause", PlayScoped: true, Props: []Field{nonNeg("position_ms")}},
	{Event: "resume", PlayScoped: true, Props: []Field{nonNeg("position_ms"), nonNeg("paused_ms")}},
	{Event: "quality_change", PlayScoped: true, Props: []Field{
		req("from", String), req("to", String), enum("mode", manualAuto...),
	}},
	{Event: "rate_change", PlayScoped: true, Props: []Field{
		{Name: "from", Type: Number, Required: true, Min: f64(0)},
		{Name: "to", Type: Number, Required: true, Min: f64(0)},
		enum("mode", manualAuto...),
	}},
	{Event: "source_switch", PlayScoped: true, Props: []Field{
		id("from_source_id"), id("to_source_id"), enum("mode", manualAuto...), req("reason", String),
	}},
	{Event: "play_end", PlayScoped: true, Props: []Field{
		enum("reason", "completed", "closed", "autoplay_next", "error"),
		nonNeg("watched_seconds"),
		bucketsField, // 14.3 200-bucket replay table (remainder since the last heartbeat)
		opt("error_code", String),
	}},
	{Event: "peak_click", PlayScoped: true, Props: []Field{
		enum("curve", "danmaku", "playback"), nonNeg("position_ms"), req("top3", Bool), req("watched_30s", Bool),
	}},
	{Event: "progress", PlayScoped: true, Props: []Field{
		id("release_id"),
		{Name: "milestone", Type: Int, Required: true, Ints: quartiles}, // every 25% + 100 = finished
	}},
	{Event: "autoplay_next", Props: []Field{
		id("from_release_id"), id("to_release_id"), req("cancelled", Bool), nonNeg("countdown_left_s"),
	}},

	// --- 14 other events
	{Event: "actor_card_open", Props: []Field{id("performer_id"), enum("source", "hover", "half_sheet", "search")}},
	{Event: "follow_actor", Props: []Field{id("performer_id"), req("source", String)}},
	{Event: "search", Props: []Field{
		{Name: "query", Type: String, Required: true, MaxLen: 200},
		nonNeg("result_count"), req("is_retry", Bool),
		{Name: "clicked_rank", Type: Int, Min: f64(1)},
		// rescued: the response came from the server's modifier-stripped
		// retry (GET /api/catalog/search "rescued": true; PRD 4.5).
		opt("rescued", Bool),
	}},
	{Event: "subtitle_change", Props: []Field{req("lang", String), opt("from", String)}},
	{Event: "audio_change", Props: []Field{req("lang", String), opt("from", String)}},
	{Event: "theme_change", Props: []Field{enum("theme", "system", "dark", "light")}},

	// --- 14.4 module events
	// 弹幕
	{Event: "danmaku_toggle", Props: []Field{req("enabled", Bool)}},
	{Event: "danmaku_send", Props: []Field{id("release_id"), nonNeg("position_ms"), nonNeg("length"), req("color", String)}},
	{Event: "danmaku_report", Props: []Field{id("danmaku_id"), req("reason", String)}},
	{Event: "danmaku_setting", Props: []Field{req("setting", String)}},
	// 广告
	{Event: "ad_request", Props: []Field{req("placement", String), {Name: "load_ms", Type: Int, Min: f64(0)}}},
	{Event: "ad_impression", Props: append(adFields[:2:2], req("viewable", Bool))},
	{Event: "ad_quartile", Props: append(adFields[:2:2], Field{Name: "quartile", Type: Int, Required: true, Ints: quartiles})},
	{Event: "ad_skip", Props: adFields},
	{Event: "ad_click", Props: adFields},
	{Event: "ad_error", Props: []Field{req("placement", String), opt("ad_id", String), req("error_code", String)}},
	// 激励广告
	{Event: "reward_offer", Props: []Field{enum("reward", "4k", "skip_midroll")}},
	{Event: "reward_granted", Props: []Field{enum("reward", "4k", "skip_midroll"), req("completed", Bool)}},
	// 演员
	{Event: "reminder_set", Props: []Field{id("performer_id"), enum("channel", "in_app", "push", "email"), req("timing", String)}},
	// 今晚看什么
	{Event: "tonight_open", Props: []Field{opt("filters", Object)}},
	{Event: "tonight_shuffle", Props: []Field{{Name: "attempt", Type: Int, Required: true, Min: f64(1)}, opt("filters", Object)}},
	{Event: "tonight_accept", Props: []Field{id("release_id"), {Name: "attempt", Type: Int, Required: true, Min: f64(1)}, opt("reason", String)}},
	// 评分与短评
	{Event: "rating_submit", Props: []Field{
		id("release_id"), {Name: "score", Type: Int, Required: true, Min: f64(1), Max: f64(10)}, req("watched", Bool),
	}},
	{Event: "review_post", Props: []Field{id("release_id"), req("spoiler", Bool), req("watched", Bool)}},
	{Event: "review_like", Props: []Field{id("review_id")}},
	// 互动 (PRD 5.4): 赞 / 踩, 评论, 分享
	{Event: "release_vote", Props: []Field{id("release_id"), enum("vote", "like", "dislike", "none")}},
	{Event: "comment_post", Props: []Field{id("release_id"), req("reply", Bool), req("spoiler", Bool)}},
	{Event: "comment_like", Props: []Field{id("comment_id")}},
	{Event: "share", Props: []Field{id("release_id"), enum("method", "native", "copy"), req("with_time", Bool)}},
	// 引导与合规
	{Event: "onboarding_step", Props: []Field{req("step", String), req("skipped", Bool)}},
	{Event: "consent_change", Props: []Field{
		{Name: "categories", Type: StringArray, Required: true, Enum: []string{ConsentNecessary, ConsentAnalytics, ConsentPersonalizedAds}},
		opt("gpc", Bool),
	}},
	// Site-entry 18+ gate; there is no teen mode (DESIGN_ALIGNMENT #8).
	{Event: "age_gate", Props: []Field{req("passed", Bool)}},
}

// Registry maps event name → schema_ver → Schema.
type Registry map[string]map[int]Schema

// Default is the registry of every PRD 14 event at schema_ver 1.
var Default = func() Registry {
	r := Registry{}
	for _, s := range v1 {
		s.Version = 1
		r.add(s)
	}
	return r
}()

func (r Registry) add(s Schema) {
	if r[s.Event] == nil {
		r[s.Event] = map[int]Schema{}
	}
	if _, dup := r[s.Event][s.Version]; dup {
		panic("events: duplicate schema " + s.Event)
	}
	r[s.Event][s.Version] = s
}

// Lookup returns the schema for event at version.
func (r Registry) Lookup(event string, version int) (Schema, bool) {
	s, ok := r[event][version]
	return s, ok
}

// Names returns every registered event name, sorted.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for n := range r {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
