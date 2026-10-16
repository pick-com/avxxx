package api

// AdminI18n (PRD 8, tech plan 3.10 / 3.13): the translation workbench,
// glossary and UI strings, plus the public UI-string bundle. Admin routes
// sit behind ADMIN_TOKEN; the optional X-Admin-User header is recorded as
// updated_by.

import (
	"avxxx/internal/i18n"
	"avxxx/internal/store"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// i18nStore is the subset of *store.Store used by the i18n handlers, as an
// interface so they can be tested without a database.
type i18nStore interface {
	translationReader
	TranslationTasks(ctx context.Context, f store.TaskFilter) ([]store.TranslationTask, string, error)
	TranslationStats(ctx context.Context) (map[string]any, error)
	UpsertTranslation(ctx context.Context, ref i18n.Ref, lang, value, by string) (store.TranslationTask, error)
	TransitionTranslation(ctx context.Context, ref i18n.Ref, lang, action, by string) (store.TranslationTask, error)
	MarkStaleTranslations(ctx context.Context) (int64, error)
	GlossaryTerms(ctx context.Context, lang, q string, limit int) ([]store.GlossaryEntry, error)
	UpsertGlossaryTerm(ctx context.Context, g store.GlossaryEntry, by string) (store.GlossaryEntry, error)
	UpdateGlossaryTerm(ctx context.Context, id int64, g store.GlossaryEntry, by string) (store.GlossaryEntry, error)
	DeleteGlossaryTerm(ctx context.Context, id int64) error
	UIStrings(ctx context.Context, lang, status, prefix string) ([]store.UIString, error)
	UpsertUIString(ctx context.Context, u store.UIString, by string) (store.UIString, error)
	PublicUIStrings(ctx context.Context, lang string) (map[string]string, error)
}

const translationPath = "/api/admin/i18n/translations/{entity_type}/{entity_id}/{field}/{lang}"

func (srv *Server) adminI18nRoutes() []route {
	return []route{
		{"GET /api/i18n/ui/{lang}", srv.publicUIStrings},
		{"GET /api/admin/i18n/translations", srv.admin(srv.translationTasks)},
		{"GET /api/admin/i18n/stats", srv.admin(srv.translationStats)},
		{"POST /api/admin/i18n/translations/mark-stale", srv.admin(srv.markStale)},
		{"PUT " + translationPath, srv.admin(srv.upsertTranslation)},
		{"POST " + translationPath + "/submit", srv.admin(srv.transition(i18n.ActionSubmit))},
		{"POST " + translationPath + "/review", srv.admin(srv.transition(i18n.ActionReview))},
		{"GET /api/admin/i18n/glossary", srv.admin(srv.glossary)},
		{"POST /api/admin/i18n/glossary", srv.admin(srv.upsertGlossary)},
		{"PUT /api/admin/i18n/glossary/{id}", srv.admin(srv.updateGlossary)},
		{"DELETE /api/admin/i18n/glossary/{id}", srv.admin(srv.deleteGlossary)},
		{"GET /api/admin/i18n/ui", srv.admin(srv.uiStrings)},
		{"PUT /api/admin/i18n/ui/{lang}/{key}", srv.admin(srv.upsertUIString)},
	}
}

const maxTranslationLen = 20000

// adminUser is the optional X-Admin-User header, recorded as updated_by.
func adminUser(r *http.Request) string {
	u := strings.TrimSpace(r.Header.Get("X-Admin-User"))
	if len(u) > 100 {
		u = u[:100]
	}
	return u
}

// langParam validates an optional language filter (exact supported tag).
func langParam(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v != "" && !i18n.Supported(v) {
		return "", badRequest("unsupported lang", nil)
	}
	return v, nil
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return badRequest("invalid JSON body", err)
	}
	return nil
}

// mapI18nError turns workflow errors into 409s.
func mapI18nError(err error) error {
	if errors.Is(err, i18n.ErrTransition) {
		return &httpError{status: http.StatusConflict, msg: i18n.ErrTransition.Error(), err: err}
	}
	return err
}

// translationTasks lists the workbench: ?lang=&status=machine|draft|reviewed|stale
// &entity_type=release|performer|tag|series&missing=true&limit=&cursor=.
// missing=true lists source fields with no translation in lang (lang required).
func (srv *Server) translationTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f store.TaskFilter
	var err error
	if f.Lang, err = langParam(q.Get("lang")); err != nil {
		reply(w, nil, err)
		return
	}
	f.Status = strings.TrimSpace(q.Get("status"))
	if f.Status != "" && !i18n.ValidStatus(f.Status) {
		reply(w, nil, badRequest("invalid status", nil))
		return
	}
	f.EntityType = strings.TrimSpace(q.Get("entity_type"))
	if _, ok := i18n.Fields[f.EntityType]; f.EntityType != "" && !ok {
		reply(w, nil, badRequest("invalid entity_type", nil))
		return
	}
	switch strings.TrimSpace(q.Get("missing")) {
	case "", "false", "0":
	case "true", "1":
		f.Missing = true
	default:
		reply(w, nil, badRequest("invalid missing", nil))
		return
	}
	if f.Missing && f.Lang == "" {
		reply(w, nil, badRequest("missing=true needs lang", nil))
		return
	}
	if f.Limit, err = limitParam(q); err != nil {
		reply(w, nil, err)
		return
	}
	if raw := strings.TrimSpace(q.Get("cursor")); raw != "" {
		if f.Cursor, err = store.DecodeTaskCursor(raw); err != nil {
			reply(w, nil, badRequest("invalid cursor", err))
			return
		}
	}
	items, next, err := srv.i18n.TranslationTasks(r.Context(), f)
	reply(w, page(items, next), err)
}

// translationStats counts translations per language and status (+ missing).
func (srv *Server) translationStats(w http.ResponseWriter, r *http.Request) {
	v, err := srv.i18n.TranslationStats(r.Context())
	reply(w, v, err)
}

// markStale sweeps all reviewed translations against their current source.
func (srv *Server) markStale(w http.ResponseWriter, r *http.Request) {
	n, err := srv.i18n.MarkStaleTranslations(r.Context())
	reply(w, map[string]int64{"marked_stale": n}, err)
}

// translationRef parses and validates the {entity_type}/{entity_id}/{field}/{lang} path.
func translationRef(r *http.Request) (i18n.Ref, string, error) {
	ref := i18n.Ref{EntityType: r.PathValue("entity_type"), EntityID: r.PathValue("entity_id"), Field: r.PathValue("field")}
	if err := ref.Validate(); err != nil {
		return ref, "", badRequest(err.Error(), nil)
	}
	lang := r.PathValue("lang")
	if !i18n.Supported(lang) {
		return ref, "", badRequest("unsupported lang", nil)
	}
	return ref, lang, nil
}

// upsertTranslation saves a draft: body {"value": "..."}. 404 when the
// entity or its source field does not exist.
func (srv *Server) upsertTranslation(w http.ResponseWriter, r *http.Request) {
	ref, lang, err := translationRef(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err = decodeBody(w, r, &body); err != nil {
		reply(w, nil, err)
		return
	}
	body.Value = strings.TrimSpace(body.Value)
	if body.Value == "" || utf8.RuneCountInString(body.Value) > maxTranslationLen {
		reply(w, nil, badRequest("value must be 1-20000 characters", nil))
		return
	}
	v, err := srv.i18n.UpsertTranslation(r.Context(), ref, lang, body.Value, adminUser(r))
	reply(w, v, err)
}

// transition submits a translation for review or marks it reviewed.
// 404 when there is no translation, 409 when the status does not allow it.
func (srv *Server) transition(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref, lang, err := translationRef(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		v, err := srv.i18n.TransitionTranslation(r.Context(), ref, lang, action, adminUser(r))
		reply(w, v, mapI18nError(err))
	}
}

// ---- glossary ----

type glossaryBody struct {
	Term        string `json:"term"`
	Lang        string `json:"lang"`
	Translation string `json:"translation"`
	Note        string `json:"note"`
}

func (b glossaryBody) entry() (store.GlossaryEntry, error) {
	g := store.GlossaryEntry{Term: strings.TrimSpace(b.Term), Lang: strings.TrimSpace(b.Lang), Translation: strings.TrimSpace(b.Translation), Note: strings.TrimSpace(b.Note)}
	switch {
	case g.Term == "" || len(g.Term) > 500:
		return g, badRequest("term is required (max 500 bytes)", nil)
	case !i18n.Supported(g.Lang):
		return g, badRequest("unsupported lang", nil)
	case g.Translation == "" || len(g.Translation) > 2000:
		return g, badRequest("translation is required (max 2000 bytes)", nil)
	case len(g.Note) > 2000:
		return g, badRequest("note too long", nil)
	}
	return g, nil
}

// glossary lists terms: ?lang=&q=&limit=.
func (srv *Server) glossary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lang, err := langParam(q.Get("lang"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	limit, err := limitParam(q)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.i18n.GlossaryTerms(r.Context(), lang, strings.TrimSpace(q.Get("q")), limit)
	reply(w, map[string]any{"items": v}, err)
}

// upsertGlossary creates or replaces the entry for (term, lang).
func (srv *Server) upsertGlossary(w http.ResponseWriter, r *http.Request) {
	var b glossaryBody
	if err := decodeBody(w, r, &b); err != nil {
		reply(w, nil, err)
		return
	}
	g, err := b.entry()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.i18n.UpsertGlossaryTerm(r.Context(), g, adminUser(r))
	reply(w, v, err)
}

func glossaryID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid id", err)
	}
	return id, nil
}

// updateGlossary rewrites one entry; 409 when (term, lang) clashes.
func (srv *Server) updateGlossary(w http.ResponseWriter, r *http.Request) {
	id, err := glossaryID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var b glossaryBody
	if err = decodeBody(w, r, &b); err != nil {
		reply(w, nil, err)
		return
	}
	g, err := b.entry()
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.i18n.UpdateGlossaryTerm(r.Context(), id, g, adminUser(r))
	reply(w, v, err)
}

func (srv *Server) deleteGlossary(w http.ResponseWriter, r *http.Request) {
	id, err := glossaryID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	err = srv.i18n.DeleteGlossaryTerm(r.Context(), id)
	reply(w, map[string]bool{"ok": true}, err)
}

// ---- UI strings ----

var uiKeyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// uiStrings lists UI strings: ?lang=&status=machine|draft|reviewed&prefix=.
func (srv *Server) uiStrings(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lang, err := langParam(q.Get("lang"))
	if err != nil {
		reply(w, nil, err)
		return
	}
	status := strings.TrimSpace(q.Get("status"))
	if status != "" && !validUIStatus(status) {
		reply(w, nil, badRequest("invalid status", nil))
		return
	}
	v, err := srv.i18n.UIStrings(r.Context(), lang, status, strings.TrimSpace(q.Get("prefix")))
	reply(w, map[string]any{"items": v}, err)
}

func validUIStatus(s string) bool {
	return s == i18n.StatusMachine || s == i18n.StatusDraft || s == i18n.StatusReviewed
}

// upsertUIString sets one UI string: body {"value": "...", "status": "draft"|"reviewed"}
// (status defaults to draft).
func (srv *Server) upsertUIString(w http.ResponseWriter, r *http.Request) {
	lang, key := r.PathValue("lang"), r.PathValue("key")
	if !i18n.Supported(lang) {
		reply(w, nil, badRequest("unsupported lang", nil))
		return
	}
	if !uiKeyRE.MatchString(key) {
		reply(w, nil, badRequest("invalid key", nil))
		return
	}
	var b struct {
		Value  string `json:"value"`
		Status string `json:"status"`
	}
	if err := decodeBody(w, r, &b); err != nil {
		reply(w, nil, err)
		return
	}
	if b.Status == "" {
		b.Status = i18n.StatusDraft
	}
	if !validUIStatus(b.Status) {
		reply(w, nil, badRequest("invalid status", nil))
		return
	}
	if b.Value == "" || len(b.Value) > 4000 {
		reply(w, nil, badRequest("value must be 1-4000 bytes", nil))
		return
	}
	v, err := srv.i18n.UpsertUIString(r.Context(), store.UIString{Key: key, Lang: lang, Value: b.Value, Status: b.Status}, adminUser(r))
	reply(w, v, err)
}

// publicUIStrings serves the reviewed UI strings of {lang} as a flat
// {"key": "value"} map (no fallback across languages). Cacheable: strong
// ETag over the body, 304 on If-None-Match, max-age 5 minutes.
func (srv *Server) publicUIStrings(w http.ResponseWriter, r *http.Request) {
	lang := r.PathValue("lang")
	if !i18n.Supported(lang) {
		reply(w, nil, errNotFound)
		return
	}
	m, err := srv.i18n.PublicUIStrings(r.Context(), lang)
	if err != nil {
		reply(w, nil, err)
		return
	}
	body, err := json.Marshal(m) // map keys are sorted: stable bytes, stable ETag
	if err != nil {
		reply(w, nil, err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Content-Language", lang)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", "application/json")
	w.Write(append(body, '\n'))
}

// etagMatches implements the weak comparison of If-None-Match.
func etagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimPrefix(strings.TrimSpace(t), "W/")
		if t == "*" || t == etag {
			return true
		}
	}
	return false
}
