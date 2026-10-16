package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"avxxx/internal/identity"
)

// ThePornDB API adapter (source_kind third_party_api, FINAL_DATA_DESIGN 2.4:
// batch lookup and missing-field fallback, never the sole source of truth).
//
// Reference: the official OpenAPI document behind
// https://api.theporndb.net/docs, served at
// https://api.theporndb.net/specs?openapi.json (info.version v3.24.757 when
// this was written). Only fields described by its SceneResource,
// PerformerResource, SiteResource, DirectorResource and Meta schemas are read;
// testdata/theporndb/README.md lists what is mapped, what is deliberately
// left unmapped and why.
//
// Authentication is the documented "sanctum" scheme: an
// "Authorization: Bearer <token>" header. The token (THEPORNDB_API_TOKEN) is
// added by WrapTransport for the API host only, so it never appears in the
// URLs the Fetcher, the crawl, checkpoints or logs handle; Redact scrubs any
// echo of it.
const (
	ThePornDBAdapterName = "theporndb"
	// ThePornDBAPIHost is the only host the token is ever sent to.
	ThePornDBAPIHost = "api.theporndb.net"
	// ThePornDBDocsURL is the API documentation (Swagger UI) and
	// ThePornDBSpecURL the OpenAPI document it renders.
	ThePornDBDocsURL = "https://api.theporndb.net/docs"
	ThePornDBSpecURL = "https://api.theporndb.net/specs?openapi.json"
	// ThePornDBJAVListURL is the seeded discovery start URL: GET /jav, newest
	// release date first (orderBy=recently_released, SearchOrderEnum), the
	// documented per_page maximum of 100.
	ThePornDBJAVListURL = "https://api.theporndb.net/jav?orderBy=recently_released&per_page=100"
	// ThePornDBCreditHTML is the credit shown where ThePornDB data is used.
	// The API documentation states no attribution requirement; this is our
	// own credit, pending the terms review.
	ThePornDBCreditHTML = `Metadata cross-checked with <a href="https://theporndb.net/">ThePornDB</a>`

	// ThePornDBLookupPerPage is the page size of a code lookup: a code
	// should match one scene, a few more show ambiguity.
	ThePornDBLookupPerPage = 10

	tpdbIncrementalOverlap = 72 * time.Hour
	tpdbReleaseKeyPrefix   = "tpdb-jav-"
	tpdbPerformerKeyPrefix = "tpdb-performer-"
)

// ThePornDBTokenFromEnv reads THEPORNDB_API_TOKEN.
func ThePornDBTokenFromEnv() string { return strings.TrimSpace(os.Getenv("THEPORNDB_API_TOKEN")) }

// ThePornDBAdapter maps ThePornDB JSON responses to records. It never
// performs I/O itself; the Fetcher fetches through WrapTransport.
type ThePornDBAdapter struct {
	token   string
	apiHost string
}

// NewThePornDBAdapter builds the adapter from the environment. Without a
// token it is still registered but reports not configured.
func NewThePornDBAdapter() *ThePornDBAdapter {
	return NewThePornDBAdapterWith(ThePornDBTokenFromEnv(), ThePornDBAPIHost)
}

// NewThePornDBAdapterWith builds the adapter with an explicit token; apiHost
// (host[:port]) is the only host that receives it (tests use httptest).
func NewThePornDBAdapterWith(token, apiHost string) *ThePornDBAdapter {
	if apiHost == "" {
		apiHost = ThePornDBAPIHost
	}
	return &ThePornDBAdapter{token: strings.TrimSpace(token), apiHost: apiHost}
}

func (*ThePornDBAdapter) Name() string        { return ThePornDBAdapterName }
func (*ThePornDBAdapter) InlineRecords() bool { return true }

// SourceKind is the FINAL_DATA_DESIGN 2.4 tier of every record this adapter
// produces; the seeded source row carries the same value.
func (*ThePornDBAdapter) SourceKind() string { return "third_party_api" }

func (a *ThePornDBAdapter) Configured() error {
	if a.token == "" {
		return fmt.Errorf("%w: theporndb adapter needs THEPORNDB_API_TOKEN", ErrNotConfigured)
	}
	return nil
}

// Redact replaces any occurrence of the token (raw or URL-escaped).
func (a *ThePornDBAdapter) Redact(s string) string {
	if s == "" || a.token == "" {
		return s
	}
	s = strings.ReplaceAll(s, a.token, "[redacted]")
	if esc := url.QueryEscape(a.token); esc != a.token {
		s = strings.ReplaceAll(s, esc, "[redacted]")
	}
	if esc := url.PathEscape(a.token); esc != a.token {
		s = strings.ReplaceAll(s, esc, "[redacted]")
	}
	return s
}

// WrapTransport adds the bearer token (and asks for JSON) on requests to the
// API host only, so a redirect elsewhere never carries it.
func (a *ThePornDBAdapter) WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &tpdbTransport{base: base, adapter: a}
}

type tpdbTransport struct {
	base    http.RoundTripper
	adapter *ThePornDBAdapter
}

func (t *tpdbTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Host, t.adapter.apiHost) {
		return t.base.RoundTrip(req)
	}
	signed := req.Clone(req.Context())
	signed.Header.Set("Authorization", "Bearer "+t.adapter.token)
	signed.Header.Set("Accept", "application/json")
	resp, err := t.base.RoundTrip(signed)
	if resp != nil {
		resp.Request = req // callers never see the signed request
	}
	if err != nil {
		return resp, SanitizeError(err, t.adapter)
	}
	return resp, nil
}

// ---- Response shapes (documented fields only).

// tpdbString accepts a JSON string, number, boolean or null: the schemas
// declare some ids as numbers and some examples as strings.
type tpdbString string

func (s *tpdbString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*s = ""
	case len(b) > 0 && b[0] == '"':
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = tpdbString(v)
	case len(b) > 0 && (b[0] == '{' || b[0] == '['):
		*s = "" // not a scalar: ignore rather than fail the page
	default:
		*s = tpdbString(b)
	}
	return nil
}

func (s tpdbString) String() string { return strings.TrimSpace(string(s)) }

type tpdbMeta struct {
	Total       tpdbString `json:"total"`
	PerPage     tpdbString `json:"per_page"`
	CurrentPage tpdbString `json:"current_page"`
	Last        tpdbString `json:"last"`
}

type tpdbImages struct {
	Full   tpdbString `json:"full"`
	Large  tpdbString `json:"large"`
	Medium tpdbString `json:"medium"`
	Small  tpdbString `json:"small"`
}

type tpdbSite struct {
	UUID      tpdbString `json:"uuid"`
	ID        tpdbString `json:"id"`
	ParentID  tpdbString `json:"parent_id"`
	NetworkID tpdbString `json:"network_id"`
	Name      tpdbString `json:"name"`
	ShortName tpdbString `json:"short_name"`
	URL       tpdbString `json:"url"`
}

type tpdbDirector struct {
	ID   tpdbString `json:"id"`
	UUID tpdbString `json:"uuid"`
	Name tpdbString `json:"name"`
	Slug tpdbString `json:"slug"`
}

type tpdbPerformerExtras struct {
	Gender          tpdbString `json:"gender"`
	Birthday        tpdbString `json:"birthday"`
	BirthdayType    tpdbString `json:"birthday_type"`
	Deathday        tpdbString `json:"deathday"`
	Birthplace      tpdbString `json:"birthplace"`
	BirthplaceCode  tpdbString `json:"birthplace_code"`
	Nationality     tpdbString `json:"nationality"`
	HairColour      tpdbString `json:"hair_colour"`
	EyeColour       tpdbString `json:"eye_colour"`
	Weight          tpdbString `json:"weight"`
	Height          tpdbString `json:"height"`
	Measurements    tpdbString `json:"measurements"`
	Cupsize         tpdbString `json:"cupsize"`
	Waist           tpdbString `json:"waist"`
	Hips            tpdbString `json:"hips"`
	CareerStartYear tpdbString `json:"career_start_year"`
	CareerEndYear   tpdbString `json:"career_end_year"`
}

type tpdbPerformer struct {
	ID             tpdbString          `json:"id"`
	NumericID      tpdbString          `json:"_id"`
	Slug           tpdbString          `json:"slug"`
	Name           tpdbString          `json:"name"`
	FullName       tpdbString          `json:"full_name"`
	Disambiguation tpdbString          `json:"disambiguation"`
	Extras         tpdbPerformerExtras `json:"extras"`
	Aliases        []tpdbString        `json:"aliases"`
	Image          tpdbString          `json:"image"`
	Thumbnail      tpdbString          `json:"thumbnail"`
	Face           tpdbString          `json:"face"`
}

type tpdbScene struct {
	ID          tpdbString      `json:"id"`
	NumericID   tpdbString      `json:"_id"`
	Title       tpdbString      `json:"title"`
	Type        tpdbString      `json:"type"`
	Slug        tpdbString      `json:"slug"`
	ExternalID  tpdbString      `json:"external_id"`
	SiteID      tpdbString      `json:"site_id"`
	Date        tpdbString      `json:"date"`
	URL         tpdbString      `json:"url"`
	Image       tpdbString      `json:"image"`
	BackImage   tpdbString      `json:"back_image"`
	PosterImage tpdbString      `json:"poster_image"`
	Poster      tpdbString      `json:"poster"`
	Duration    tpdbString      `json:"duration"`
	SKU         tpdbString      `json:"sku"`
	Background  tpdbImages      `json:"background"`
	Posters     tpdbImages      `json:"posters"`
	LastUpdated tpdbString      `json:"last_updated"`
	Performers  []tpdbPerformer `json:"performers"`
	Site        *tpdbSite       `json:"site"`
	Directors   []tpdbDirector  `json:"directors"`
}

// tpdbEnvelope is every documented 2xx body: {"data": <resource or array>,
// "meta": Meta} (meta on list endpoints only).
type tpdbEnvelope struct {
	Data json.RawMessage `json:"data"`
	Meta *tpdbMeta       `json:"meta"`
}

func decodeTPDB(body []byte) (tpdbEnvelope, bool, error) {
	var env tpdbEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return env, false, fmt.Errorf("theporndb: decode response: %w", err)
	}
	d := bytes.TrimSpace(env.Data)
	if len(d) == 0 || bytes.Equal(d, []byte("null")) {
		return env, false, errors.New("theporndb: response has no data")
	}
	return env, d[0] == '[', nil
}

// tpdbResource classifies a request path: "jav" for scene endpoints
// (/jav, /jav/{id}, /performers/{id}/jav, /sites/{id}/jav), "performer" for
// /performers and /performers/{id}.
func tpdbResource(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	switch {
	case len(parts) >= 1 && parts[0] == "jav" && (len(parts) == 1 || (len(parts) == 2 && parts[1] != "similar" && parts[1] != "hash")):
		return "jav"
	case len(parts) == 3 && (parts[0] == "performers" || parts[0] == "sites") && parts[2] == "jav":
		return "jav"
	case len(parts) >= 1 && parts[0] == "performers" && len(parts) <= 2 && !(len(parts) == 2 && parts[1] == "similar"):
		return "performer"
	}
	return ""
}

// ---- Adapter interface.

func (a *ThePornDBAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
	_ = ctx
	env, list, err := decodeTPDB(doc.Body)
	if err != nil {
		return nil, err
	}
	var out []Record
	switch res := tpdbResource(doc.URL); res {
	case "jav":
		var scenes []tpdbScene
		if list {
			err = json.Unmarshal(env.Data, &scenes)
		} else {
			var one tpdbScene
			err = json.Unmarshal(env.Data, &one)
			scenes = []tpdbScene{one}
		}
		if err != nil {
			return nil, fmt.Errorf("theporndb: decode scenes: %w", err)
		}
		for _, s := range scenes {
			if rec, ok := a.releaseRecord(s); ok {
				out = append(out, RedactRecord(a, rec))
			}
		}
	case "performer":
		var people []tpdbPerformer
		if list {
			err = json.Unmarshal(env.Data, &people)
		} else {
			var one tpdbPerformer
			err = json.Unmarshal(env.Data, &one)
			people = []tpdbPerformer{one}
		}
		if err != nil {
			return nil, fmt.Errorf("theporndb: decode performers: %w", err)
		}
		for _, p := range people {
			if rec, ok := a.performerRecord(p); ok {
				out = append(out, RedactRecord(a, rec))
			}
		}
	default:
		return nil, fmt.Errorf("theporndb: unsupported endpoint %q", a.Redact(doc.URL))
	}
	return out, nil
}

// Parse returns the first record of entity in doc (e.g. GET /jav/{id}).
// Crawls do not call it: discovery records are already complete.
func (a *ThePornDBAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	recs, err := a.Discover(ctx, doc)
	if err != nil {
		return Record{}, err
	}
	for _, r := range recs {
		if r.EntityType == entity {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("theporndb: no %s in response", entity)
}

// NextPage follows the documented Meta: page=current_page+1 while
// current_page < last. An empty page stops the walk.
func (a *ThePornDBAdapter) NextPage(doc Document) string {
	env, list, err := decodeTPDB(doc.Body)
	if err != nil || !list || env.Meta == nil {
		return ""
	}
	if d := bytes.TrimSpace(env.Data); bytes.Equal(d, []byte("[]")) {
		return ""
	}
	cur, err1 := strconv.Atoi(env.Meta.CurrentPage.String())
	last, err2 := strconv.Atoi(env.Meta.Last.String())
	if err1 != nil || err2 != nil || cur < 1 || cur >= last {
		return ""
	}
	u, err := url.Parse(doc.URL)
	if err != nil {
		return ""
	}
	q := u.Query()
	if p := q.Get("page"); p != "" && p != strconv.Itoa(cur) {
		return "" // the API did not serve the page we asked for: stop rather than loop
	}
	q.Set("page", strconv.Itoa(cur+1))
	u.RawQuery = q.Encode()
	return u.String()
}

// IncrementalStart narrows a GET /jav pass ordered by release date to dates
// on or after the last completed pass minus an overlap (documented date +
// date_operation filters).
func (a *ThePornDBAdapter) IncrementalStart(start string, watermark time.Time) string {
	if watermark.IsZero() || tpdbResource(start) != "jav" {
		return ""
	}
	u, err := url.Parse(start)
	if err != nil || strings.Trim(u.Path, "/") != "jav" || u.Query().Get("orderBy") != "recently_released" {
		return ""
	}
	q := u.Query()
	q.Set("date", watermark.UTC().Add(-tpdbIncrementalOverlap).Format(time.DateOnly))
	q.Set("date_operation", ">=")
	q.Del("page")
	u.RawQuery = q.Encode()
	return u.String()
}

// LookupURLs returns the API URLs that search for one product code: the
// documented free-text search (q) on GET /jav. Matching happens on the
// mapped records (ExactCodeMatches), never on the search ranking.
func (a *ThePornDBAdapter) LookupURLs(code string) []string {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil
	}
	q := url.Values{}
	q.Set("q", strings.ToUpper(code))
	q.Set("per_page", strconv.Itoa(ThePornDBLookupPerPage))
	scheme := "https"
	if a.apiHost != ThePornDBAPIHost {
		scheme = "http" // httptest
	}
	return []string{scheme + "://" + a.apiHost + "/jav?" + q.Encode()}
}

// ---- Mapping.

var (
	tpdbUUID   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	tpdbHeight = regexp.MustCompile(`^(\d{2,3}(?:\.\d+)?)\s*cm$`)
)

// ThePornDBPerformerID returns id when it is a ThePornDB performer UUID
// (PerformerResource.id), else "".
func ThePornDBPerformerID(id string) string {
	id = strings.TrimSpace(id)
	if !tpdbUUID.MatchString(id) {
		return ""
	}
	return strings.ToLower(id)
}

// ThePornDBCode derives a DVD code from the documented sku or external_id
// field when one of them has the letters-digits shape; origin says which.
// Which field carries the JAV product code is not documented, so a scene with
// neither in code shape gets no code (and is never matched by code).
func ThePornDBCode(sku, externalID string) (code, origin string) {
	for _, c := range []struct{ v, origin string }{{sku, "sku"}, {externalID, "external_id"}} {
		if keys := identity.CodeKeys(c.v); len(keys) > 0 {
			if k := keys[len(keys)-1]; dvdCodeShape.MatchString(k) {
				return strings.ToUpper(k), c.origin
			}
		}
	}
	return "", ""
}

func tpdbImageMap(m map[string]string, prefix string, im tpdbImages) {
	for k, v := range map[string]tpdbString{"full": im.Full, "large": im.Large, "medium": im.Medium, "small": im.Small} {
		if s := v.String(); s != "" {
			m[prefix+"_"+k] = s
		}
	}
}

func (a *ThePornDBAdapter) releaseRecord(s tpdbScene) (Record, bool) {
	id, title := strings.ToLower(s.ID.String()), s.Title.String()
	if !tpdbUUID.MatchString(id) || title == "" {
		return Record{}, false
	}
	f := map[string]any{"title": title, "theporndb_id": id}
	set := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	set("theporndb_numeric_id", s.NumericID.String())
	set("theporndb_slug", s.Slug.String())
	set("theporndb_type", s.Type.String())
	set("sku", s.SKU.String())
	set("external_id", s.ExternalID.String())
	if code, origin := ThePornDBCode(s.SKU.String(), s.ExternalID.String()); code != "" {
		f["dvd_code"], f["dvd_code_origin"] = code, origin
	}
	if d := s.Date.String(); d != "" {
		f["release_date_raw"] = d
		if t, err := time.Parse(time.DateOnly, d); err == nil {
			f["release_date"] = t.Format(time.DateOnly)
		}
	}
	// duration is documented as a number without a unit: kept as evidence,
	// not mapped to runtime_minutes.
	set("theporndb_duration", s.Duration.String())
	if s.Site != nil {
		// The site is the studio a scene is filed under; its id is a
		// ThePornDB id, never a DMM maker id (MakerScope must not see it).
		set("maker", s.Site.Name.String())
		set("theporndb_site_id", s.Site.ID.String())
		set("theporndb_site_uuid", s.Site.UUID.String())
		set("theporndb_site_url", s.Site.URL.String())
	} else {
		set("theporndb_site_id", s.SiteID.String())
	}
	for _, d := range s.Directors {
		if n := d.Name.String(); n != "" {
			f["director"] = n
			break
		}
	}
	performers := []string{}
	refs := []map[string]string{}
	for _, p := range s.Performers {
		name := p.Name.String()
		if name == "" || strings.EqualFold(p.Extras.Gender.String(), "male") {
			continue // the catalog credits actresses, as DMM does
		}
		performers = append(performers, name)
		ref := map[string]string{"name": name}
		if pid := ThePornDBPerformerID(p.ID.String()); pid != "" {
			ref["theporndb_performer_id"] = pid
			ref["source_key"] = tpdbPerformerKeyPrefix + pid
		}
		refs = append(refs, ref)
	}
	if len(performers) > 0 {
		f["performers"] = performers
		f["performer_refs"] = refs
	}
	// Images are source evidence only; nothing is downloaded or hotlinked.
	images := map[string]string{}
	for k, v := range map[string]tpdbString{"image": s.Image, "back_image": s.BackImage, "poster_image": s.PosterImage, "poster": s.Poster} {
		if x := v.String(); x != "" {
			images[k] = x
		}
	}
	tpdbImageMap(images, "background", s.Background)
	tpdbImageMap(images, "posters", s.Posters)
	if len(images) > 0 {
		f["image_urls"] = images
	}
	set("cover_url", s.Image.String())
	set("studio_url", s.URL.String())
	set("theporndb_last_updated", s.LastUpdated.String())
	return Record{EntityType: Release, SourceKey: tpdbReleaseKeyPrefix + id, URL: "https://" + ThePornDBAPIHost + "/jav/" + id, Fields: f}, true
}

func (a *ThePornDBAdapter) performerRecord(p tpdbPerformer) (Record, bool) {
	id, name := ThePornDBPerformerID(p.ID.String()), p.Name.String()
	if id == "" || name == "" {
		return Record{}, false
	}
	f := map[string]any{"name": name, "theporndb_performer_id": id}
	set := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	set("theporndb_numeric_id", p.NumericID.String())
	set("theporndb_slug", p.Slug.String())
	set("disambiguation", p.Disambiguation.String())
	aliases := []string{}
	for _, al := range p.Aliases {
		if s := al.String(); s != "" && s != name {
			aliases = append(aliases, s)
		}
	}
	if len(aliases) > 0 {
		f["aliases"] = aliases
	}
	x := p.Extras
	set("gender", x.Gender.String())
	if b := x.Birthday.String(); b != "" {
		f["birthday_raw"] = b
		set("birthday_type", x.BirthdayType.String())
		// birthday_type (DAY_MONTH / MONTH_YEAR / YEAR) marks a partial date:
		// only a full date with no type is a birth_date.
		if t, err := time.Parse(time.DateOnly, b); err == nil && x.BirthdayType.String() == "" {
			f["birth_date"] = t.Format(time.DateOnly)
		}
	}
	set("birth_place", x.Birthplace.String())
	set("birthplace_code", x.BirthplaceCode.String())
	set("nationality", x.Nationality.String())
	if h := x.Height.String(); h != "" {
		f["height_raw"] = h
		if m := tpdbHeight.FindStringSubmatch(h); m != nil {
			f["height_cm"] = m[1]
		}
	}
	// Measurements, cup size, waist, hips and weight are documented without a
	// unit system ("34C-24-34", "24", "50kg"): evidence only.
	set("measurements_raw", x.Measurements.String())
	set("cupsize_raw", x.Cupsize.String())
	set("waist_raw", x.Waist.String())
	set("hips_raw", x.Hips.String())
	set("weight_raw", x.Weight.String())
	set("career_start_year", x.CareerStartYear.String())
	set("career_end_year", x.CareerEndYear.String())
	images := map[string]string{}
	for k, v := range map[string]tpdbString{"image": p.Image, "thumbnail": p.Thumbnail, "face": p.Face} {
		if s := v.String(); s != "" {
			images[k] = s
		}
	}
	if len(images) > 0 {
		f["image_urls"] = images
	}
	return Record{EntityType: Performer, SourceKey: tpdbPerformerKeyPrefix + id, URL: "https://" + ThePornDBAPIHost + "/performers/" + id, Fields: f}, true
}

// ExactCodeMatches returns the release records whose dvd_code is one of
// codes (compared by identity.CodeKeys), deduplicated by source key.
func ExactCodeMatches(recs []Record, codes []string) []Record {
	want := map[string]bool{}
	for _, c := range codes {
		for _, k := range identity.CodeKeys(c) {
			want[k] = true
		}
	}
	seen := map[string]bool{}
	var out []Record
	for _, r := range recs {
		code, _ := r.Fields["dvd_code"].(string)
		if r.EntityType != Release || code == "" || seen[r.SourceKey] {
			continue
		}
		for _, k := range identity.CodeKeys(code) {
			if want[k] {
				seen[r.SourceKey] = true
				out = append(out, r)
				break
			}
		}
	}
	return out
}
