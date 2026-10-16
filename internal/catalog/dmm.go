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

// DMM/FANZA Affiliate Web API v3 adapter (source_kind official_catalog).
//
// Reference: https://affiliate.dmm.com/api/v3/itemlist.html and
// https://affiliate.dmm.com/api/v3/actresssearch.html. Only fields described
// by those pages are read; see testdata/dmm/README.md for how the fixtures
// were derived.
//
// Credentials (DMM_API_ID, DMM_AFFILIATE_ID) travel in the query string, so
// they are added by the adapter's transport at request time and never appear
// in the URLs the Fetcher, the crawl or the checkpoints handle. The response
// echoes them back in request.parameters and embeds the affiliate ID in
// affiliateURL / sampleMovieURL; the adapter ignores the echo and redacts the
// rest before a record leaves it.
const (
	DMMAdapterName = "dmm"
	// DMMAPIHost is the only host credentials are ever sent to.
	DMMAPIHost = "api.dmm.com"
	// DMMItemListURL is the seeded discovery start URL: FANZA digital
	// videos, newest release date first, the documented maximum of 100 hits.
	DMMItemListURL = "https://api.dmm.com/affiliate/v3/ItemList?floor=videoa&hits=100&output=json&service=digital&site=FANZA&sort=date"
	// DMMActressSearchURL is the seeded performer discovery start URL:
	// newest actress IDs first (sort=-id), because incremental runs read the
	// head and stop at the previous run's head records (crawl watermark);
	// with ascending IDs new actresses would only ever be found by a
	// baseline pass. Pending live verification with credentials.
	DMMActressSearchURL = "https://api.dmm.com/affiliate/v3/ActressSearch?hits=100&output=json&sort=-id"
	// DMMCreditHTML is the credit DMM requires on sites built with the API,
	// to be shown verbatim (no added attributes or markup).
	DMMCreditHTML = `Powered by <a href="https://affiliate.dmm.com/api/">FANZA Webサービス</a>`
	// DMMAffiliatePlaceholder replaces the affiliate ID in stored affiliate
	// and preview URLs; the UI substitutes the configured ID at render time.
	DMMAffiliatePlaceholder = "{affiliate_id}"

	dmmMaxOffset  = 50000 // ItemList offset maximum (documented)
	dmmDateLayout = "2006-01-02T15:04:05"
	// dmmIncrementalOverlap re-reads a few days before the last completed
	// pass so late-published items with recent release dates are not missed.
	dmmIncrementalOverlap = 72 * time.Hour
)

var jst = time.FixedZone("JST", 9*60*60)

// DMMCredentials are the API ID and affiliate ID issued by DMM affiliate.
type DMMCredentials struct {
	APIID       string
	AffiliateID string
}

// DMMCredentialsFromEnv reads DMM_API_ID and DMM_AFFILIATE_ID.
func DMMCredentialsFromEnv() DMMCredentials {
	return DMMCredentials{
		APIID:       strings.TrimSpace(os.Getenv("DMM_API_ID")),
		AffiliateID: strings.TrimSpace(os.Getenv("DMM_AFFILIATE_ID")),
	}
}

// DMMAdapter maps ItemList and ActressSearch JSON responses to records. It
// never performs I/O itself; the Fetcher fetches through WrapTransport.
type DMMAdapter struct {
	creds   DMMCredentials
	apiHost string
}

// NewDMMAdapter builds the adapter from the environment. Without credentials
// it is still registered but reports not configured.
func NewDMMAdapter() *DMMAdapter { return NewDMMAdapterWith(DMMCredentialsFromEnv(), DMMAPIHost) }

// NewDMMAdapterWith builds the adapter with explicit credentials; apiHost
// (host[:port]) is the only host that receives them (tests use httptest).
func NewDMMAdapterWith(c DMMCredentials, apiHost string) *DMMAdapter {
	if apiHost == "" {
		apiHost = DMMAPIHost
	}
	return &DMMAdapter{creds: c, apiHost: apiHost}
}

func (*DMMAdapter) Name() string        { return DMMAdapterName }
func (*DMMAdapter) InlineRecords() bool { return true }

// SourceKind is the FINAL_DATA_DESIGN 2.4 tier of every record this adapter
// produces; the seeded source row carries the same value.
func (*DMMAdapter) SourceKind() string { return "official_catalog" }

func (a *DMMAdapter) Configured() error {
	var missing []string
	if a.creds.APIID == "" {
		missing = append(missing, "DMM_API_ID")
	}
	if a.creds.AffiliateID == "" {
		missing = append(missing, "DMM_AFFILIATE_ID")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: dmm adapter needs %s", ErrNotConfigured, strings.Join(missing, " and "))
	}
	return nil
}

// Redact strips credential query parameters from URLs and replaces any
// remaining occurrence of the API ID or affiliate ID (raw or URL-encoded).
func (a *DMMAdapter) Redact(s string) string {
	if s == "" {
		return s
	}
	if strings.Contains(s, "api_id=") || strings.Contains(s, "affiliate_id=") {
		s = stripCredentialParams(s)
	}
	replace := func(s, secret, with string) string {
		if secret == "" {
			return s
		}
		s = strings.ReplaceAll(s, secret, with)
		if esc := url.QueryEscape(secret); esc != secret {
			s = strings.ReplaceAll(s, esc, with)
		}
		if esc := url.PathEscape(secret); esc != secret {
			s = strings.ReplaceAll(s, esc, with)
		}
		return s
	}
	s = replace(s, a.creds.APIID, "[redacted]")
	s = replace(s, a.creds.AffiliateID, DMMAffiliatePlaceholder)
	return s
}

var credentialParam = regexp.MustCompile(`([?&])(api_id|affiliate_id)=[^&#"\s]*&?`)

// stripCredentialParams removes api_id/affiliate_id query parameters wherever
// a URL appears in s (including inside error text).
func stripCredentialParams(s string) string {
	for {
		out := credentialParam.ReplaceAllString(s, "$1")
		if out == s {
			break
		}
		s = out
	}
	s = strings.ReplaceAll(s, "?&", "?")
	return s
}

// WrapTransport adds api_id and affiliate_id to requests for the API host
// only, so a redirect elsewhere never carries them.
func (a *DMMAdapter) WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &dmmTransport{base: base, adapter: a}
}

type dmmTransport struct {
	base    http.RoundTripper
	adapter *DMMAdapter
}

func (t *dmmTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.EqualFold(req.URL.Host, t.adapter.apiHost) {
		return t.base.RoundTrip(req)
	}
	signed := req.Clone(req.Context())
	q := signed.URL.Query()
	q.Set("api_id", t.adapter.creds.APIID)
	q.Set("affiliate_id", t.adapter.creds.AffiliateID)
	signed.URL.RawQuery = q.Encode()
	resp, err := t.base.RoundTrip(signed)
	if resp != nil {
		// Callers (Document.FinalURL) must only ever see the unsigned URL.
		resp.Request = req
	}
	if err != nil {
		return resp, SanitizeError(err, t.adapter)
	}
	return resp, nil
}

// ---- Response shapes (documented fields only).

// dmmString accepts a JSON string, number or null (the API returns IDs as
// numbers in ItemList and as strings in ActressSearch).
type dmmString string

func (s *dmmString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if bytes.Equal(b, []byte("null")) {
		*s = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = dmmString(v)
		return nil
	}
	*s = dmmString(b)
	return nil
}

func (s dmmString) String() string { return strings.TrimSpace(string(s)) }

func (s dmmString) Int() int {
	n, _ := strconv.Atoi(s.String())
	return n
}

type dmmEnvelope struct {
	Result *struct {
		Status        dmmString    `json:"status"`
		ResultCount   dmmString    `json:"result_count"`
		TotalCount    dmmString    `json:"total_count"`
		FirstPosition dmmString    `json:"first_position"`
		Items         []dmmItem    `json:"items"`
		Actress       []dmmActress `json:"actress"`
	} `json:"result"`
}

type dmmRef struct {
	ID   dmmString `json:"id"`
	Name dmmString `json:"name"`
	Ruby dmmString `json:"ruby"`
}

type dmmItem struct {
	ServiceCode  dmmString `json:"service_code"`
	FloorCode    dmmString `json:"floor_code"`
	ContentID    dmmString `json:"content_id"`
	ProductID    dmmString `json:"product_id"`
	Title        dmmString `json:"title"`
	Volume       dmmString `json:"volume"`
	URL          dmmString `json:"URL"`
	AffiliateURL dmmString `json:"affiliateURL"`
	ImageURL     struct {
		List  dmmString `json:"list"`
		Small dmmString `json:"small"`
		Large dmmString `json:"large"`
	} `json:"imageURL"`
	SampleImageURL struct {
		SampleS struct {
			Image []dmmString `json:"image"`
		} `json:"sample_s"`
		SampleL struct {
			Image []dmmString `json:"image"`
		} `json:"sample_l"`
	} `json:"sampleImageURL"`
	SampleMovieURL struct {
		Size476 dmmString `json:"size_476_306"`
		Size560 dmmString `json:"size_560_360"`
		Size644 dmmString `json:"size_644_414"`
		Size720 dmmString `json:"size_720_480"`
	} `json:"sampleMovieURL"`
	Prices struct {
		Price      dmmString `json:"price"`
		ListPrice  dmmString `json:"list_price"`
		Deliveries struct {
			Delivery []struct {
				Type  dmmString `json:"type"`
				Price dmmString `json:"price"`
			} `json:"delivery"`
		} `json:"deliveries"`
	} `json:"prices"`
	Date     dmmString `json:"date"`
	ItemInfo struct {
		Genre    []dmmRef `json:"genre"`
		Series   []dmmRef `json:"series"`
		Maker    []dmmRef `json:"maker"`
		Actress  []dmmRef `json:"actress"`
		Director []dmmRef `json:"director"`
		Label    []dmmRef `json:"label"`
	} `json:"iteminfo"`
	JANCode      dmmString `json:"jancode"`
	MakerProduct dmmString `json:"maker_product"`
	Stock        dmmString `json:"stock"`
}

type dmmActress struct {
	ID          dmmString `json:"id"`
	Name        dmmString `json:"name"`
	Ruby        dmmString `json:"ruby"`
	Bust        dmmString `json:"bust"`
	Cup         dmmString `json:"cup"`
	Waist       dmmString `json:"waist"`
	Hip         dmmString `json:"hip"`
	Height      dmmString `json:"height"`
	Birthday    dmmString `json:"birthday"`
	BloodType   dmmString `json:"blood_type"`
	Hobby       dmmString `json:"hobby"`
	Prefectures dmmString `json:"prefectures"`
	ImageURL    struct {
		Small dmmString `json:"small"`
		Large dmmString `json:"large"`
	} `json:"imageURL"`
}

// DMMAPIError is a response whose result.status is not 200.
type DMMAPIError struct{ Status string }

func (e *DMMAPIError) Error() string { return "dmm: api returned status " + e.Status }

func decodeDMM(body []byte) (*dmmEnvelope, error) {
	var env dmmEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("dmm: decode response: %w", err)
	}
	if env.Result == nil {
		return nil, errors.New("dmm: response has no result")
	}
	if st := env.Result.Status.String(); st != "" && st != "200" {
		return nil, &DMMAPIError{Status: st}
	}
	return &env, nil
}

func dmmOperation(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	p := strings.TrimSuffix(u.Path, "/")
	return p[strings.LastIndex(p, "/")+1:]
}

// ---- Adapter interface.

func (a *DMMAdapter) Discover(ctx context.Context, doc Document) ([]Record, error) {
	_ = ctx
	env, err := decodeDMM(doc.Body)
	if err != nil {
		return nil, err
	}
	var out []Record
	switch op := dmmOperation(doc.URL); op {
	case "ItemList":
		for _, it := range env.Result.Items {
			if rec, ok := a.releaseRecord(it); ok {
				out = append(out, RedactRecord(a, rec))
			}
		}
	case "ActressSearch":
		for _, ac := range env.Result.Actress {
			if rec, ok := a.performerRecord(ac); ok {
				out = append(out, RedactRecord(a, rec))
			}
		}
	default:
		return nil, fmt.Errorf("dmm: unsupported operation %q", op)
	}
	return out, nil
}

// Parse returns the first record of entity in doc (e.g. an ItemList cid=
// lookup). Crawls do not call it: discovery records are already complete.
func (a *DMMAdapter) Parse(ctx context.Context, doc Document, entity EntityType) (Record, error) {
	recs, err := a.Discover(ctx, doc)
	if err != nil {
		return Record{}, err
	}
	for _, r := range recs {
		if r.EntityType == entity {
			return r, nil
		}
	}
	return Record{}, fmt.Errorf("dmm: no %s in response", entity)
}

// NextPage pages by offset (first_position + result_count). ItemList offsets
// stop at 50000, so past that a sort=date pass continues in a new window:
// lte_date = the oldest release date on this page, offset 1. Overlapping
// items are re-saved idempotently.
func (a *DMMAdapter) NextPage(doc Document) string {
	env, err := decodeDMM(doc.Body)
	if err != nil {
		return ""
	}
	r := env.Result
	count, first, total := r.ResultCount.Int(), r.FirstPosition.Int(), r.TotalCount.Int()
	if count <= 0 {
		return ""
	}
	if first <= 0 {
		first = 1
	}
	next := first + count
	if total > 0 && next > total {
		return ""
	}
	u, err := url.Parse(doc.URL)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Del("api_id")
	q.Del("affiliate_id")
	if dmmOperation(doc.URL) == "ItemList" && next > dmmMaxOffset {
		if q.Get("sort") != "date" {
			return "" // other sorts cannot be windowed
		}
		var oldest time.Time
		for _, it := range r.Items {
			if t, ok := parseDMMDate(it.Date.String()); ok && (oldest.IsZero() || t.Before(oldest)) {
				oldest = t
			}
		}
		if oldest.IsZero() {
			return ""
		}
		lte := oldest.Format(dmmDateLayout)
		if prev := q.Get("lte_date"); prev != "" && lte >= prev {
			return "" // no progress: stop rather than loop
		}
		q.Set("lte_date", lte)
		q.Set("offset", "1")
	} else {
		q.Set("offset", strconv.Itoa(next))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// IncrementalStart narrows an ItemList pass to release dates on or after the
// last completed pass (minus an overlap), in JST as the API expects.
func (a *DMMAdapter) IncrementalStart(start string, watermark time.Time) string {
	if watermark.IsZero() || dmmOperation(start) != "ItemList" {
		return ""
	}
	u, err := url.Parse(start)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("gte_date", watermark.In(jst).Add(-dmmIncrementalOverlap).Format(dmmDateLayout))
	q.Del("lte_date")
	q.Del("offset")
	u.RawQuery = q.Encode()
	return u.String()
}

// ---- Mapping.

func parseDMMDate(s string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006/1/2 15:04", "2006/01/02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, jst); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var dvdCodeShape = regexp.MustCompile(`^[a-z]+-\d+[a-z]?$`)

// DMMDVDCode derives the maker-scoped DVD code (品番) for an item: the maker's
// own product number when the API gives one, otherwise the content ID with
// its numeric label prefix and zero padding removed ("118abp00123" ->
// "ABP-123"). origin says which; "" when no letters+digits code can be made.
func DMMDVDCode(contentID, makerProduct string) (code, origin string) {
	if keys := identity.CodeKeys(makerProduct); len(keys) > 0 && dvdCodeShape.MatchString(keys[0]) {
		return strings.ToUpper(keys[0]), "maker_product"
	}
	if keys := identity.CodeKeys(contentID); len(keys) > 0 {
		if k := keys[len(keys)-1]; dvdCodeShape.MatchString(k) {
			return strings.ToUpper(k), "content_id"
		}
	}
	return "", ""
}

func refNames(refs []dmmRef) []string {
	out := []string{}
	for _, r := range refs {
		if n := r.Name.String(); n != "" {
			out = append(out, n)
		}
	}
	return out
}

func refIDs(refs []dmmRef) []string {
	out := []string{}
	for _, r := range refs {
		if id := r.ID.String(); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func firstRef(refs []dmmRef) (name, id string) {
	for _, r := range refs {
		if n := r.Name.String(); n != "" {
			return n, r.ID.String()
		}
	}
	return "", ""
}

var digitsOnly = regexp.MustCompile(`^\d+$`)

func (a *DMMAdapter) releaseRecord(it dmmItem) (Record, bool) {
	cid := it.ContentID.String()
	title := it.Title.String()
	if cid == "" || title == "" {
		return Record{}, false
	}
	f := map[string]any{"title": title, "content_id": cid}
	set := func(k, v string) {
		if v != "" {
			f[k] = v
		}
	}
	set("product_id", it.ProductID.String())
	set("service_code", it.ServiceCode.String())
	set("floor_code", it.FloorCode.String())
	if code, origin := DMMDVDCode(cid, it.MakerProduct.String()); code != "" {
		f["dvd_code"] = code
		f["dvd_code_origin"] = origin
	}
	set("maker_product", it.MakerProduct.String())
	set("jancode", it.JANCode.String())
	if raw := it.Date.String(); raw != "" {
		f["release_datetime"] = raw
		if t, ok := parseDMMDate(raw); ok {
			f["release_date"] = t.Format("2006-01-02")
		}
	}
	// volume is the runtime in minutes for video floors (page count for books).
	if v := it.Volume.String(); digitsOnly.MatchString(v) && it.ServiceCode.String() != "ebook" {
		f["runtime_minutes"] = v
	}
	if n, id := firstRef(it.ItemInfo.Maker); n != "" {
		f["maker"], f["maker_id"] = n, id
	}
	if n, id := firstRef(it.ItemInfo.Label); n != "" {
		f["label"], f["label_id"] = n, id
	}
	if n, id := firstRef(it.ItemInfo.Series); n != "" {
		f["series"], f["series_id"] = n, id
	}
	if n, id := firstRef(it.ItemInfo.Director); n != "" {
		f["director"], f["director_id"] = n, id
	}
	if g := refNames(it.ItemInfo.Genre); len(g) > 0 {
		f["genres"] = g
		f["tags"] = append([]string(nil), g...)
		f["genre_ids"] = refIDs(it.ItemInfo.Genre)
	}
	performers := []string{}
	refs := []map[string]string{}
	for _, ac := range it.ItemInfo.Actress {
		name := ac.Name.String()
		if name == "" {
			continue
		}
		performers = append(performers, name)
		ref := map[string]string{"name": name}
		if id := ac.ID.String(); id != "" {
			ref["dmm_actress_id"] = id
			ref["source_key"] = dmmActressKey(id)
		}
		if r := ac.Ruby.String(); r != "" {
			ref["ruby"] = r
		}
		refs = append(refs, ref)
	}
	if len(performers) > 0 {
		f["performers"] = performers
		f["performer_refs"] = refs
	}
	// Images and sample media are source evidence only; nothing is downloaded.
	images := map[string]string{}
	for k, v := range map[string]dmmString{"list": it.ImageURL.List, "small": it.ImageURL.Small, "large": it.ImageURL.Large} {
		if s := v.String(); s != "" {
			images[k] = s
		}
	}
	if len(images) > 0 {
		f["image_urls"] = images
		for _, k := range []string{"large", "small", "list"} {
			if images[k] != "" {
				f["cover_url"] = images[k]
				break
			}
		}
	}
	samples := it.SampleImageURL.SampleL.Image
	if len(samples) == 0 {
		samples = it.SampleImageURL.SampleS.Image
	}
	if len(samples) > 0 {
		s := make([]string, 0, len(samples))
		for _, x := range samples {
			if v := x.String(); v != "" {
				s = append(s, v)
			}
		}
		f["sample_image_urls"] = s
	}
	for _, m := range []dmmString{it.SampleMovieURL.Size720, it.SampleMovieURL.Size644, it.SampleMovieURL.Size560, it.SampleMovieURL.Size476} {
		if v := m.String(); v != "" {
			f["official_preview_url"] = v
			break
		}
	}
	set("product_url", it.URL.String())
	set("purchase_url", it.AffiliateURL.String())
	set("price", it.Prices.Price.String())
	set("list_price", it.Prices.ListPrice.String())
	if d := it.Prices.Deliveries.Delivery; len(d) > 0 {
		types := []string{}
		for _, x := range d {
			if t := x.Type.String(); t != "" {
				types = append(types, t)
			}
		}
		if len(types) > 0 {
			f["delivery_types"] = types
		}
	}
	set("stock", it.Stock.String())
	recURL := it.URL.String()
	if recURL == "" {
		recURL = "https://" + DMMAPIHost + "/affiliate/v3/ItemList?cid=" + url.QueryEscape(cid) + "&output=json&site=FANZA"
	}
	return Record{EntityType: Release, SourceKey: cid, URL: recURL, Fields: f}, true
}

func dmmActressKey(id string) string { return "dmm-actress-" + id }

func (a *DMMAdapter) performerRecord(ac dmmActress) (Record, bool) {
	id, name := ac.ID.String(), ac.Name.String()
	if id == "" || name == "" {
		return Record{}, false
	}
	f := map[string]any{"name": name, "name_ja": name, "dmm_actress_id": id}
	set := func(k string, v dmmString) {
		if s := v.String(); s != "" {
			f[k] = s
		}
	}
	set("name_kana", ac.Ruby)
	set("birth_date", ac.Birthday)
	set("height_cm", ac.Height)
	set("bust_cm", ac.Bust)
	set("waist_cm", ac.Waist)
	set("hip_cm", ac.Hip)
	set("cup_size", ac.Cup)
	set("blood_type", ac.BloodType)
	set("hobby", ac.Hobby)
	set("birth_place", ac.Prefectures)
	images := map[string]string{}
	if s := ac.ImageURL.Small.String(); s != "" {
		images["small"] = s
	}
	if s := ac.ImageURL.Large.String(); s != "" {
		images["large"] = s
	}
	if len(images) > 0 {
		f["image_urls"] = images
	}
	// listURL is an affiliate link per floor; it adds nothing the record
	// needs, so it is not stored.
	u := "https://" + DMMAPIHost + "/affiliate/v3/ActressSearch?actress_id=" + url.QueryEscape(id) + "&output=json"
	return Record{EntityType: Performer, SourceKey: dmmActressKey(id), URL: u, Fields: f}, true
}
