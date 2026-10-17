package notify

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"net/url"
	"strings"
	texttemplate "text/template"
	"unicode/utf8"
)

// Template kinds. Outbox rows written by NotifyFanout all have payload kind
// "new_work"; a digest whose items are all reservation notices uses the
// reservation template (and reservation notify prefs).
const (
	KindNewWork     = "new_work"
	KindReservation = "reservation"
)

// DefaultLang is used when no template exists for the user's ui_lang.
const DefaultLang = "zh-CN"

// Web paths linked from notifications (web/app/routes.ts). The settings
// page does not exist yet; it is where the unsubscribe link must land.
const (
	SettingsPath  = "/settings/notifications"
	FollowingPath = "/following"
	ReleasePath   = "/releases/"
)

// maxListed caps the releases listed in one mail / push.
const (
	maxMailItems = 10
	maxPushItems = 3
)

//go:embed templates/*.txt templates/*.html
var templateFS embed.FS

// Payload is notification_outbox.payload as NotifyFanout writes it.
type Payload struct {
	Kind  string        `json:"kind"`
	Items []PayloadItem `json:"items"`
	Count int           `json:"count"`
}

// PayloadItem is one release in a merged payload.
type PayloadItem struct {
	ReleaseID  int64  `json:"release_id"`
	Slug       string `json:"slug"`
	Title      string `json:"title"`
	Reason     string `json:"reason"` // follow | reservation
	Performers []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"performers"`
}

// ParsePayload decodes an outbox payload.
func ParsePayload(raw []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("bad payload: %w", err)
	}
	if len(p.Items) == 0 {
		return p, fmt.Errorf("bad payload: no items")
	}
	if p.Count < len(p.Items) {
		p.Count = len(p.Items)
	}
	return p, nil
}

// KindReminder is the day-before reservation reminder template; it uses the
// reservation notify prefs (store.RemindReservations).
const KindReminder = "reservation_reminder"

// TemplateKind is the template (and notify-prefs kind) for a payload.
func (p Payload) TemplateKind() string {
	reminders := 0
	for _, it := range p.Items {
		switch it.Reason {
		case "reservation":
		case KindReminder:
			reminders++
		default:
			return KindNewWork
		}
	}
	if reminders == len(p.Items) {
		return KindReminder
	}
	return KindReservation
}

// PrefKinds are the notify-prefs kinds the payload's items belong to.
func (p Payload) PrefKinds() []string {
	seen := map[string]bool{}
	var out []string
	for _, it := range p.Items {
		k := KindNewWork
		if it.Reason == "reservation" || it.Reason == KindReminder {
			k = KindReservation
		}
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// Content is a rendered notification for every channel.
type Content struct {
	Lang        string
	Subject     string
	Text        string
	HTML        string
	PushTitle   string
	PushBody    string
	URL         string // where a click lands
	SettingsURL string
}

type tmplItem struct {
	Title, URL, Performers string
	Reservation            bool
}

type tmplData struct {
	Name, Subject              string
	Count, More                int
	Items                      []tmplItem
	ListURL, SettingsURL, Base string
}

type langTemplates struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

// Renderer renders embedded templates with absolute links under BaseURL.
type Renderer struct {
	BaseURL string                   // e.g. https://javpick.com; "" yields site-relative links
	tmpl    map[string]langTemplates // "<kind>.<lang>"
}

// NewRenderer parses every embedded template.
func NewRenderer(baseURL string) (*Renderer, error) {
	r := &Renderer{BaseURL: strings.TrimRight(baseURL, "/"), tmpl: map[string]langTemplates{}}
	names, err := fs.Glob(templateFS, "templates/*.txt")
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		key := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".txt")
		t, err := texttemplate.ParseFS(templateFS, n)
		if err != nil {
			return nil, err
		}
		kind, _, _ := strings.Cut(key, ".")
		parts := []string{"subject", "push_title", "push_body", "text"}
		if mailOnlyKinds[kind] {
			parts = []string{"subject", "text"}
		}
		for _, part := range parts {
			if t.Lookup(part) == nil {
				return nil, fmt.Errorf("template %s: missing %q", n, part)
			}
		}
		var h *htmltemplate.Template
		if !mailOnlyKinds[kind] { // mail-only kinds are plain text
			if h, err = htmltemplate.ParseFS(templateFS, "templates/"+key+".html"); err != nil {
				return nil, err
			}
		}
		r.tmpl[key] = langTemplates{text: t, html: h}
	}
	if _, ok := r.tmpl[KindNewWork+"."+DefaultLang]; !ok {
		return nil, fmt.Errorf("missing default %s templates", DefaultLang)
	}
	if _, ok := r.tmpl[KindReservation+"."+DefaultLang]; !ok {
		return nil, fmt.Errorf("missing default %s templates", DefaultLang)
	}
	return r, nil
}

// Lang picks the template language for a ui_lang: exact match, then the
// base language ("en-US" -> "en"), then DefaultLang.
func (r *Renderer) Lang(kind, uiLang string) string {
	if _, ok := r.tmpl[kind+"."+uiLang]; ok && uiLang != "" {
		return uiLang
	}
	if base, _, _ := strings.Cut(uiLang, "-"); base != "" {
		if _, ok := r.tmpl[kind+"."+base]; ok {
			return base
		}
	}
	return DefaultLang
}

func (r *Renderer) link(path string) string { return r.BaseURL + path }

// Render renders p for a user.
func (r *Renderer) Render(p Payload, uiLang, displayName string) (Content, error) {
	kind := p.TemplateKind()
	lang := r.Lang(kind, uiLang)
	t := r.tmpl[kind+"."+lang]
	data := tmplData{Name: displayName, Count: p.Count, ListURL: r.link(FollowingPath),
		SettingsURL: r.link(SettingsPath), Base: r.BaseURL}
	for i, it := range p.Items {
		if i == maxMailItems {
			break
		}
		var names []string
		for _, pf := range it.Performers {
			names = append(names, pf.Name)
		}
		sep := ", "
		if strings.HasPrefix(lang, "zh") {
			sep = "、"
		}
		data.Items = append(data.Items, tmplItem{Title: it.Title, URL: r.link(ReleasePath + url.PathEscape(it.Slug)),
			Performers: strings.Join(names, sep), Reservation: it.Reason == "reservation"})
	}
	data.More = p.Count - len(data.Items)
	c := Content{Lang: lang, SettingsURL: data.SettingsURL, URL: data.ListURL}
	if p.Count == 1 {
		c.URL = data.Items[0].URL
	}
	var err error
	if c.Subject, err = execText(t.text, "subject", data); err != nil {
		return c, err
	}
	c.Subject = strings.Join(strings.Fields(c.Subject), " ") // header-safe: no line breaks
	data.Subject = c.Subject
	if c.Text, err = execText(t.text, "text", data); err != nil {
		return c, err
	}
	var hb bytes.Buffer
	if err := t.html.Execute(&hb, data); err != nil {
		return c, err
	}
	c.HTML = hb.String()

	pushData := data
	if len(pushData.Items) > maxPushItems {
		pushData.Items = pushData.Items[:maxPushItems]
	}
	pushData.More = p.Count - len(pushData.Items)
	if c.PushTitle, err = execText(t.text, "push_title", pushData); err != nil {
		return c, err
	}
	if c.PushBody, err = execText(t.text, "push_body", pushData); err != nil {
		return c, err
	}
	c.PushBody = clip(c.PushBody, 300)
	return c, nil
}

func execText(t *texttemplate.Template, name string, data any) (string, error) {
	var b bytes.Buffer
	if err := t.ExecuteTemplate(&b, name, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// clip shortens s to at most n runes, ending with "…".
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// PushMessage is the JSON the service worker receives in the push event.
type PushMessage struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// PushJSON is the encrypted push payload for c (well under the 4 KiB limit:
// the body is clipped to 300 runes and titles are short).
func PushJSON(c Content, kind string, count int) ([]byte, error) {
	return json.Marshal(PushMessage{Title: c.PushTitle, Body: c.PushBody, URL: c.URL, Tag: kind, Count: count})
}
