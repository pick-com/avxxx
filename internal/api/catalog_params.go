package api

import (
	"avxxx/internal/store"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// page is the list envelope: items plus next_cursor (null on the last page).
func page(items any, next string) map[string]any {
	var nc any
	if next != "" {
		nc = next
	}
	return map[string]any{"items": items, "next_cursor": nc}
}

// intParam parses an optional non-negative integer query parameter.
// Missing or empty yields 0.
func intParam(q url.Values, name string, max int) (int, error) {
	raw := strings.TrimSpace(q.Get(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > max {
		return 0, badRequest("invalid "+name, err)
	}
	return n, nil
}

// limitParam parses limit: missing means the store default, values above the
// page maximum are clamped by the store, malformed or negative values are 400.
func limitParam(q url.Values) (int, error) {
	raw := strings.TrimSpace(q.Get("limit"))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, badRequest("invalid limit", err)
	}
	return n, nil
}

// cursorParam decodes an optional opaque cursor and checks it belongs to sort.
func cursorParam(q url.Values, sort string) (*store.Cursor, error) {
	raw := strings.TrimSpace(q.Get("cursor"))
	if raw == "" {
		return nil, nil
	}
	c, err := store.DecodeCursor(raw)
	if err != nil {
		return nil, badRequest("invalid cursor", err)
	}
	if c.Sort != sort {
		return nil, badRequest("cursor does not match sort", nil)
	}
	return &c, nil
}

func parseReleaseFilter(q url.Values) (store.ReleaseFilter, error) {
	f := store.ReleaseFilter{
		Q:         q.Get("q"),
		Tag:       strings.TrimSpace(q.Get("tag")),
		Maker:     strings.TrimSpace(q.Get("maker")),
		Performer: strings.TrimSpace(q.Get("performer")),
		Sort:      strings.TrimSpace(q.Get("sort")),
	}
	if f.Sort == "" {
		f.Sort = "newest"
	}
	if !store.ValidReleaseSort(f.Sort) {
		return f, badRequest("invalid sort", nil)
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		return f, err
	}
	if f.Year, err = intParam(q, "year", 9999); err != nil {
		return f, err
	}
	if f.Year != 0 && f.Year < 1900 {
		return f, badRequest("invalid year", nil)
	}
	if f.MinRuntime, err = intParam(q, "min_runtime", 10000); err != nil {
		return f, err
	}
	if f.MaxRuntime, err = intParam(q, "max_runtime", 10000); err != nil {
		return f, err
	}
	if f.MaxRuntime > 0 && f.MinRuntime > f.MaxRuntime {
		return f, badRequest("min_runtime exceeds max_runtime", nil)
	}
	switch f.Role = strings.TrimSpace(q.Get("role")); f.Role {
	case "", store.CreditLead, store.CreditGuest:
	default:
		return f, badRequest("role must be lead or guest", nil)
	}
	if f.Role != "" && f.Performer == "" {
		return f, badRequest("role needs performer", nil)
	}
	if f.MinHeight, err = intParam(q, "min_height", 4320); err != nil {
		return f, err
	}
	if f.Subtitle = strings.TrimSpace(q.Get("subtitle")); f.Subtitle != "" && f.Subtitle != "any" && !mediaLang.MatchString(f.Subtitle) {
		return f, badRequest("invalid subtitle", nil)
	}
	if f.AudioLang, err = mediaLangParam(q, "audio_lang"); err != nil {
		return f, err
	}
	f.Cursor, err = cursorParam(q, f.Sort)
	return f, err
}

// mediaLang is a playback-source language code as stored in
// playback_sources.audio_langs / subtitle_langs (ja, zh-CN, en ...).
var mediaLang = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})?$`)

func mediaLangParam(q url.Values, name string) (string, error) {
	v := strings.TrimSpace(q.Get(name))
	if v != "" && !mediaLang.MatchString(v) {
		return "", badRequest("invalid "+name, nil)
	}
	return v, nil
}

func parsePerformerFilter(q url.Values) (store.PerformerFilter, error) {
	f := store.PerformerFilter{Q: q.Get("q"), Sort: strings.TrimSpace(q.Get("sort"))}
	if f.Sort == "" {
		f.Sort = "name"
	}
	if !store.ValidPerformerSort(f.Sort) {
		return f, badRequest("invalid sort", nil)
	}
	var err error
	if f.Limit, err = limitParam(q); err != nil {
		return f, err
	}
	// era: the first year of a decade (2010 = 2010s 活跃年代).
	if f.Era, err = intParam(q, "era", 2100); err != nil {
		return f, err
	}
	if f.Era != 0 && (f.Era < 1950 || f.Era%10 != 0) {
		return f, badRequest("era must be a decade such as 2010", nil)
	}
	f.Tag = strings.TrimSpace(q.Get("tag"))
	if f.AudioLang, err = mediaLangParam(q, "audio_lang"); err != nil {
		return f, err
	}
	f.Cursor, err = cursorParam(q, f.Sort)
	return f, err
}
