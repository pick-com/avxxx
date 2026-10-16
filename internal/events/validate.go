package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"
)

// Event is a validated event, shaped like a row of the events table.
// Empty strings and nil pointers are stored as NULL.
type Event struct {
	EventID   string
	Name      string
	SchemaVer int
	TSClient  time.Time
	TSServer  time.Time
	UserID    *int64
	DeviceID  string
	SessionID string
	PlayID    string
	Platform  string
	AppVer    string
	UILang    string
	Theme     string
	Page      string
	RefPage   string
	Slot      string
	Consent   []string
	Exp       []string
	// ReleaseID and SourceID are lifted out of props (release_id / source_id)
	// into their own columns for per-video and per-source queries.
	ReleaseID *int64
	SourceID  *int64
	Props     map[string]any
}

// Rejected is an event that failed validation and goes to events_quarantine.
type Rejected struct {
	EventID  string // best effort; "" when absent or not a string
	Name     string // best effort
	Reason   string
	Raw      json.RawMessage // the event with user_id/device_id removed
	TSServer time.Time
}

// ValidationError explains why an event was rejected.
type ValidationError struct{ Reason string }

func (e *ValidationError) Error() string { return e.Reason }

func reject(format string, args ...any) error {
	return &ValidationError{Reason: fmt.Sprintf(format, args...)}
}

// HasAnalyticsConsent reports whether the event may be stored with
// identifiers. A GPC signal (Sec-GPC: 1) counts as no consent (PRD 14.5).
func (e *Event) HasAnalyticsConsent(gpc bool) bool {
	return !gpc && slices.Contains(e.Consent, ConsentAnalytics)
}

// ApplyConsent enforces PRD 14.5: without analytics consent only anonymous
// events are kept, so user_id and device_id are stripped.
func (e *Event) ApplyConsent(gpc bool) {
	if !e.HasAnalyticsConsent(gpc) {
		e.UserID = nil
		e.DeviceID = ""
	}
}

func validEventID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range []byte(s) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':'
		if !ok {
			return false
		}
	}
	return true
}

// Validate parses one raw event and checks it against the registry.
// ts_server is set to now; user_id is never taken from the client.
func (r Registry) Validate(raw json.RawMessage, now time.Time) (Event, error) {
	var top map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil || top == nil {
		return Event{}, reject("event is not a JSON object")
	}
	if dec.More() {
		return Event{}, reject("trailing data after event object")
	}
	// An unknown name is the most useful reason, so check it before the rest.
	if name, ok := top["event"].(string); ok && r[name] == nil {
		return Event{}, reject("unknown event %q", truncate(name, 64))
	}
	if reason := checkFields(commonFields, top); reason != "" {
		return Event{}, reject("%s", reason)
	}
	e := Event{
		EventID:   top["event_id"].(string),
		Name:      top["event"].(string),
		TSServer:  now.UTC(),
		DeviceID:  str(top, "device_id"),
		SessionID: str(top, "session_id"),
		PlayID:    str(top, "play_id"),
		Platform:  str(top, "platform"),
		AppVer:    str(top, "app_ver"),
		UILang:    str(top, "ui_lang"),
		Theme:     str(top, "theme"),
		Page:      str(top, "page"),
		RefPage:   str(top, "ref_page"),
		Slot:      str(top, "slot"),
		Consent:   strs(top, "consent"),
		Exp:       strs(top, "exp"),
	}
	if !validEventID(e.EventID) {
		return Event{}, reject("field \"event_id\" must be 1-64 characters of [A-Za-z0-9-_.:]")
	}
	ver, _ := strconv.ParseInt(top["schema_ver"].(json.Number).String(), 10, 64)
	e.SchemaVer = int(ver)
	e.TSClient, _ = parseTimestamp(top["ts_client"])

	schema, ok := r.Lookup(e.Name, e.SchemaVer)
	if !ok {
		return Event{}, reject("unknown schema_ver %d for event %q", e.SchemaVer, e.Name)
	}
	if schema.PlayScoped && e.PlayID == "" {
		return Event{}, reject("event %q requires play_id", e.Name)
	}

	props, _ := top["props"].(map[string]any)
	if props == nil {
		props = map[string]any{}
	}
	if reason := checkFields(schema.Props, props); reason != "" {
		return Event{}, reject("props: %s", reason)
	}
	e.ReleaseID = liftID(props, "release_id")
	e.SourceID = liftID(props, "source_id")
	e.Props = props
	return e, nil
}

// checkFields validates m against fields. Unknown keys are allowed so that
// older servers accept additive client changes; types of known keys are not.
func checkFields(fields []Field, m map[string]any) string {
	for _, f := range fields {
		v, present := m[f.Name]
		if !present || v == nil {
			if f.Required {
				return fmt.Sprintf("missing required field %q", f.Name)
			}
			continue
		}
		if reason := f.check(v); reason != "" {
			return reason
		}
	}
	return ""
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func strs(m map[string]any, k string) []string {
	arr, _ := m[k].([]any)
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		out = append(out, v.(string)) // checked by checkFields
	}
	return out
}

// liftID moves an integer prop into a column and removes it from props.
func liftID(props map[string]any, k string) *int64 {
	n, ok := props[k].(json.Number)
	if !ok {
		return nil
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return nil
	}
	delete(props, k)
	return &i
}

// Quarantine builds the Rejected record for raw. Identifiers (user_id,
// device_id) are removed from the stored payload regardless of consent: the
// quarantine exists to debug schemas, not to track anyone.
func Quarantine(raw json.RawMessage, reason string, now time.Time) Rejected {
	q := Rejected{Reason: reason, TSServer: now.UTC(), Raw: raw}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		if !json.Valid(raw) {
			b, _ := json.Marshal(string(raw))
			q.Raw = b
		}
		return q
	}
	_ = json.Unmarshal(m["event_id"], &q.EventID)
	_ = json.Unmarshal(m["event"], &q.Name)
	q.EventID = truncate(q.EventID, 64)
	q.Name = truncate(q.Name, 64)
	delete(m, "user_id")
	delete(m, "device_id")
	q.Raw, _ = json.Marshal(m)
	return q
}

// BatchResult splits a batch into events to store and events to quarantine.
type BatchResult struct {
	Valid       []Event
	Rejected    []Rejected
	InBatchDups int // repeated event_ids within this batch (kept once)
}

// ProcessBatch validates every raw event, applies consent, attaches userID
// (nil for anonymous requests) and drops repeated event_ids within the batch.
func (r Registry) ProcessBatch(raws []json.RawMessage, now time.Time, userID *int64, gpc bool) BatchResult {
	var res BatchResult
	seen := make(map[string]bool, len(raws))
	for _, raw := range raws {
		e, err := r.Validate(raw, now)
		if err != nil {
			res.Rejected = append(res.Rejected, Quarantine(raw, err.Error(), now))
			continue
		}
		if seen[e.EventID] {
			res.InBatchDups++
			continue
		}
		seen[e.EventID] = true
		e.UserID = userID
		e.ApplyConsent(gpc)
		res.Valid = append(res.Valid, e)
	}
	return res
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
