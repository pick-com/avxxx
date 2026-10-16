package api

// Content-language support for the public catalog routes (PRD 8, tech plan
// 3.10). A request picks its language with ?lang= (BCP 47, e.g. zh-TW) or,
// without it, Accept-Language. When a language is resolved, responses keep
// every existing field and add:
//
//   - "lang": the resolved language (list envelopes and detail objects);
//   - "display_<field>" / "display_<field>_lang" for each translatable field:
//     releases display_title (+ display_synopsis on detail), performers
//     display_name (+ display_biography on detail), release-detail tags
//     display_name, search items display_name.
//
// display_<field> follows lang -> zh-TW -> zh-CN -> en -> original (ja goes
// straight to the original) using only reviewed (or stale, i.e. last
// reviewed) translations; display_<field>_lang is the language served or
// "original". Without a language nothing is added. Translations are batch
// loaded once per response.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/model"
	"avxxx/internal/store"
	"context"
	"net/http"
	"strconv"
	"strings"
)

// contentLang resolves the request's content language: ?lang= (400 when
// unsupported, empty means unset), else the best Accept-Language match,
// else "" (no localization).
func contentLang(r *http.Request) (string, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("lang")); v != "" {
		l := i18n.Normalize(v)
		if l == "" {
			return "", badRequest("unsupported lang", nil)
		}
		return l, nil
	}
	return i18n.FromAcceptLanguage(r.Header.Get("Accept-Language")), nil
}

// localizer resolves translatable fields for one response.
type localizer struct {
	lang  string
	chain []string
	set   i18n.Set
}

// requestLang resolves the content language before any store call and
// sets the Vary / Content-Language headers. An unsupported ?lang= is a 400.
func requestLang(w http.ResponseWriter, r *http.Request) (string, error) {
	w.Header().Add("Vary", "Accept-Language")
	lang, err := contentLang(r)
	if lang != "" {
		w.Header().Set("Content-Language", lang)
	}
	return lang, err
}

// localizer batch-loads the translations of refs for lang in one query. It
// returns nil (no localization) when lang is "".
func (srv *Server) localizer(ctx context.Context, lang string, refs func() []i18n.Ref) (*localizer, error) {
	if lang == "" {
		return nil, nil
	}
	l := &localizer{lang: lang, chain: i18n.FallbackChain(lang), set: i18n.Set{}}
	if srv.i18n == nil {
		return l, nil
	}
	if rs := refs(); len(rs) > 0 {
		set, err := srv.i18n.PublicTranslations(ctx, l.chain, rs)
		if err != nil {
			return nil, err
		}
		l.set = set
	}
	return l, nil
}

func (l *localizer) get(entityType string, id, field, original string) (string, string) {
	return l.set.Resolve(i18n.Ref{EntityType: entityType, EntityID: id, Field: field}, l.chain, original)
}

func idText(id int64) string { return strconv.FormatInt(id, 10) }

func ref(entityType, id, field string) i18n.Ref {
	return i18n.Ref{EntityType: entityType, EntityID: id, Field: field}
}

// withLang adds "lang" to a list envelope when l is set.
func withLang(m map[string]any, l *localizer) map[string]any {
	if l != nil {
		m["lang"] = l.lang
	}
	return m
}

// ---- releases ----

type localRelease struct {
	model.Release
	DisplayTitle     string `json:"display_title,omitempty"`
	DisplayTitleLang string `json:"display_title_lang,omitempty"`
}

func releaseRefs(rs []model.Release) func() []i18n.Ref {
	return func() []i18n.Ref {
		out := make([]i18n.Ref, 0, len(rs))
		for _, x := range rs {
			out = append(out, ref(i18n.EntityRelease, idText(x.ID), "title"))
		}
		return out
	}
}

// localizeReleases returns rs unchanged when l is nil.
func localizeReleases(l *localizer, rs []model.Release) any {
	if l == nil {
		return rs
	}
	out := make([]localRelease, len(rs))
	for i, x := range rs {
		out[i].Release = x
		out[i].DisplayTitle, out[i].DisplayTitleLang = l.get(i18n.EntityRelease, idText(x.ID), "title", x.CanonicalTitle)
	}
	return out
}

type localTag struct {
	store.TagRef
	DisplayName     string `json:"display_name,omitempty"`
	DisplayNameLang string `json:"display_name_lang,omitempty"`
}

type localReleaseDetail struct {
	store.ReleaseDetail
	Lang                string     `json:"lang"`
	DisplayTitle        string     `json:"display_title,omitempty"`
	DisplayTitleLang    string     `json:"display_title_lang,omitempty"`
	DisplaySynopsis     string     `json:"display_synopsis,omitempty"`
	DisplaySynopsisLang string     `json:"display_synopsis_lang,omitempty"`
	Tags                []localTag `json:"tags"` // shadows ReleaseDetail.Tags
}

func releaseDetailRefs(d store.ReleaseDetail) func() []i18n.Ref {
	return func() []i18n.Ref {
		id := idText(d.ID)
		out := []i18n.Ref{ref(i18n.EntityRelease, id, "title"), ref(i18n.EntityRelease, id, "synopsis")}
		for _, t := range d.Tags {
			out = append(out, ref(i18n.EntityTag, t.Slug, "name"))
		}
		return out
	}
}

func localizeReleaseDetail(l *localizer, d store.ReleaseDetail) any {
	if l == nil {
		return d
	}
	id := idText(d.ID)
	out := localReleaseDetail{ReleaseDetail: d, Lang: l.lang, Tags: make([]localTag, len(d.Tags))}
	out.DisplayTitle, out.DisplayTitleLang = l.get(i18n.EntityRelease, id, "title", d.CanonicalTitle)
	out.DisplaySynopsis, out.DisplaySynopsisLang = l.get(i18n.EntityRelease, id, "synopsis", d.Synopsis)
	for i, t := range d.Tags {
		out.Tags[i].TagRef = t
		out.Tags[i].DisplayName, out.Tags[i].DisplayNameLang = l.get(i18n.EntityTag, t.Slug, "name", t.Name)
	}
	return out
}

// ---- performers ----

type localPerformer struct {
	store.PerformerListItem
	DisplayName     string `json:"display_name,omitempty"`
	DisplayNameLang string `json:"display_name_lang,omitempty"`
}

func performerRefs(ps []store.PerformerListItem) func() []i18n.Ref {
	return func() []i18n.Ref {
		out := make([]i18n.Ref, 0, len(ps))
		for _, p := range ps {
			out = append(out, ref(i18n.EntityPerformer, idText(p.ID), "name"))
		}
		return out
	}
}

func localizePerformers(l *localizer, ps []store.PerformerListItem) any {
	if l == nil {
		return ps
	}
	out := make([]localPerformer, len(ps))
	for i, p := range ps {
		out[i].PerformerListItem = p
		out[i].DisplayName, out[i].DisplayNameLang = l.get(i18n.EntityPerformer, idText(p.ID), "name", p.CanonicalName)
	}
	return out
}

// mapID returns the "id" of a JSON-decoded or store-built map as text.
func mapID(m map[string]any) string {
	switch v := m["id"].(type) {
	case float64:
		return idText(int64(v))
	case int64:
		return idText(v)
	}
	return ""
}

func mapString(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// setDisplay writes display_<field> and display_<field>_lang into m.
func setDisplay(m map[string]any, field, value, lang string) {
	if value == "" {
		return
	}
	m["display_"+field] = value
	m["display_"+field+"_lang"] = lang
}

func localizePerformerDetail(l *localizer, m map[string]any) {
	if l == nil || m == nil {
		return
	}
	id := mapID(m)
	m["lang"] = l.lang
	v, lang := l.get(i18n.EntityPerformer, id, "name", mapString(m, "canonical_name"))
	setDisplay(m, "name", v, lang)
	v, lang = l.get(i18n.EntityPerformer, id, "biography", mapString(m, "biography"))
	setDisplay(m, "biography", v, lang)
}

// ---- search ----

// searchField maps a search item type to its entity type and title field.
func searchField(typ string) (string, string) {
	if typ == "release" {
		return i18n.EntityRelease, "title"
	}
	return i18n.EntityPerformer, "name"
}

func searchRefs(items []map[string]any) func() []i18n.Ref {
	return func() []i18n.Ref {
		out := make([]i18n.Ref, 0, len(items))
		for _, m := range items {
			et, f := searchField(mapString(m, "type"))
			out = append(out, ref(et, mapID(m), f))
		}
		return out
	}
}

func localizeSearch(l *localizer, items []map[string]any) {
	if l == nil {
		return
	}
	for _, m := range items {
		et, f := searchField(mapString(m, "type"))
		v, lang := l.get(et, mapID(m), f, mapString(m, "name"))
		setDisplay(m, "name", v, lang)
	}
}

// translationReader is the read side used by the public catalog routes.
type translationReader interface {
	PublicTranslations(ctx context.Context, langs []string, refs []i18n.Ref) (i18n.Set, error)
}
