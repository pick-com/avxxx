// Package events defines the analytics event schema registry (PRD section 14)
// and validates raw client events against it.
//
// Wire format of one event (one element of the POST /api/events array):
//
//	{
//	  "event_id": "01J...",            // client-generated, dedupe key
//	  "event": "seek", "schema_ver": 1,
//	  "ts_client": 1760000000000,      // epoch ms, or an RFC 3339 string
//	  "device_id": "...", "session_id": "...", "play_id": "...",
//	  "platform": "web", "app_ver": "1.4.0", "ui_lang": "zh-CN", "theme": "dark",
//	  "page": "watch", "ref_page": "home", "slot": "home:r2:c3",
//	  "consent": ["necessary", "analytics"], "exp": ["home_v2:b"],
//	  "props": { "from_ms": 1000, "to_ms": 65000, "method": "drag" }
//	}
//
// The PRD 14.1 common fields sit at the top level (they become columns of the
// events table); everything event-specific goes in props. user_id and
// ts_server are always set by the server; a client-sent user_id is ignored.
package events

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Type is the JSON type a field must have.
type Type int

const (
	String      Type = iota // JSON string
	Int                     // JSON number with no fractional part
	Number                  // any JSON number
	Bool                    // JSON true/false
	StringArray             // JSON array of strings
	Object                  // JSON object (free-form)
	Timestamp               // epoch milliseconds (number) or RFC 3339 string
)

func (t Type) String() string {
	switch t {
	case String:
		return "string"
	case Int:
		return "integer"
	case Number:
		return "number"
	case Bool:
		return "boolean"
	case StringArray:
		return "string array"
	case Object:
		return "object"
	case Timestamp:
		return "timestamp"
	}
	return "unknown"
}

// Field describes one property of an event.
type Field struct {
	Name     string
	Type     Type
	Required bool
	// Enum restricts String values (and StringArray elements) to this set.
	Enum []string
	// Ints restricts Int values to this set (e.g. progress milestones).
	Ints []int64
	// Min and Max bound Int and Number values when non-nil.
	Min, Max *float64
	// MaxLen bounds String length in bytes (0 = defaultMaxLen).
	MaxLen int
	// Format, when set, checks the content of a String value after the
	// length check and returns a reason ("" when valid).
	Format func(string) string
}

const defaultMaxLen = 256

// Schema is one version of one event's props definition.
type Schema struct {
	Event   string
	Version int
	// PlayScoped events belong to a playback and must carry play_id.
	PlayScoped bool
	Props      []Field
}

// helpers for building field lists compactly.

func f64(v float64) *float64 { return &v }

func req(name string, t Type) Field { return Field{Name: name, Type: t, Required: true} }
func opt(name string, t Type) Field { return Field{Name: name, Type: t} }

// nonNeg is a required non-negative integer (ids, positions, durations, counts).
func nonNeg(name string) Field { return Field{Name: name, Type: Int, Required: true, Min: f64(0)} }

// id is a required positive integer id.
func id(name string) Field { return Field{Name: name, Type: Int, Required: true, Min: f64(1)} }

func enum(name string, values ...string) Field {
	return Field{Name: name, Type: String, Required: true, Enum: values}
}

// check validates v (decoded with json.Decoder.UseNumber) against f and
// returns a human-readable reason, or "" when valid.
func (f Field) check(v any) string {
	switch f.Type {
	case String:
		s, ok := v.(string)
		if !ok {
			return f.mistyped(v)
		}
		max := f.MaxLen
		if max == 0 {
			max = defaultMaxLen
		}
		if len(s) > max {
			return fmt.Sprintf("field %q longer than %d bytes", f.Name, max)
		}
		if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
			return fmt.Sprintf("field %q: %q not in [%s]", f.Name, s, strings.Join(f.Enum, " "))
		}
		if f.Format != nil {
			if reason := f.Format(s); reason != "" {
				return fmt.Sprintf("field %q: %s", f.Name, reason)
			}
		}
	case Int:
		n, ok := v.(json.Number)
		if !ok {
			return f.mistyped(v)
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return f.mistyped(v)
		}
		if len(f.Ints) > 0 && !slices.Contains(f.Ints, i) {
			return fmt.Sprintf("field %q: %d not in %v", f.Name, i, f.Ints)
		}
		return f.bounds(float64(i))
	case Number:
		n, ok := v.(json.Number)
		if !ok {
			return f.mistyped(v)
		}
		x, err := n.Float64()
		if err != nil {
			return f.mistyped(v)
		}
		return f.bounds(x)
	case Bool:
		if _, ok := v.(bool); !ok {
			return f.mistyped(v)
		}
	case StringArray:
		arr, ok := v.([]any)
		if !ok {
			return f.mistyped(v)
		}
		for _, el := range arr {
			s, ok := el.(string)
			if !ok {
				return fmt.Sprintf("field %q must contain only strings", f.Name)
			}
			if len(s) > defaultMaxLen {
				return fmt.Sprintf("field %q element longer than %d bytes", f.Name, defaultMaxLen)
			}
			if len(f.Enum) > 0 && !slices.Contains(f.Enum, s) {
				return fmt.Sprintf("field %q: %q not in [%s]", f.Name, s, strings.Join(f.Enum, " "))
			}
		}
	case Object:
		if _, ok := v.(map[string]any); !ok {
			return f.mistyped(v)
		}
	case Timestamp:
		if _, ok := parseTimestamp(v); !ok {
			return fmt.Sprintf("field %q must be epoch milliseconds or an RFC 3339 string", f.Name)
		}
	}
	return ""
}

func (f Field) bounds(x float64) string {
	if f.Min != nil && x < *f.Min {
		return fmt.Sprintf("field %q below minimum %v", f.Name, *f.Min)
	}
	if f.Max != nil && x > *f.Max {
		return fmt.Sprintf("field %q above maximum %v", f.Name, *f.Max)
	}
	return ""
}

func (f Field) mistyped(v any) string {
	return fmt.Sprintf("field %q must be %s, got %s", f.Name, f.Type, jsonKind(v))
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		if _, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// parseTimestamp accepts epoch milliseconds (json.Number) or an RFC 3339 string.
func parseTimestamp(v any) (time.Time, bool) {
	switch x := v.(type) {
	case json.Number:
		ms, err := strconv.ParseInt(x.String(), 10, 64)
		if err != nil || ms <= 0 {
			return time.Time{}, false
		}
		return time.UnixMilli(ms).UTC(), true
	case string:
		t, err := time.Parse(time.RFC3339Nano, x)
		if err != nil {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}
