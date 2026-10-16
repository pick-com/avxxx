package api

// Versioned legal documents (LegalPage; PRD 15; tech plan 3.13). Public
// routes serve published versions with a language fallback; admin routes
// draft, edit, publish and diff (review_events with the X-Admin-User actor).

import (
	"avxxx/internal/store"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (srv *Server) legalRoutes() []route {
	return []route{
		{"GET /api/legal/terms/current-version", srv.currentTermsVersion},
		{"GET /api/legal/{kind}", srv.legalDocument},
		{"GET /api/legal/{kind}/versions", srv.legalVersions},
		{"GET /api/admin/legal", srv.admin(srv.adminLegalList)},
		{"POST /api/admin/legal", srv.admin(srv.adminLegalCreate)},
		{"GET /api/admin/legal/{id}", srv.admin(srv.adminLegalGet)},
		{"PUT /api/admin/legal/{id}", srv.admin(srv.adminLegalUpdate)},
		{"POST /api/admin/legal/{id}/publish", srv.admin(srv.adminLegalPublish)},
		{"GET /api/admin/legal/{id}/diff", srv.admin(srv.adminLegalDiff)},
	}
}

// currentTermsVersion: GET /api/legal/terms/current-version, the terms
// version in force: {"kind", "version" (string, e.g. "2"), "version_number",
// "effective_at"}. The account flow stores "version" in terms_acceptances
// (web lib/account.ts currentTermsVersion); in Go call
// store.CurrentLegalVersion(ctx, "terms").
func (srv *Server) currentTermsVersion(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	v, err := srv.compliance.CurrentLegalVersion(r.Context(), "terms")
	if err != nil {
		reply(w, nil, err)
		return
	}
	// terms_acceptances.version is text (migration 037; web lib/account.ts
	// currentTermsVersion reads a string), so the version goes out as one.
	reply(w, map[string]any{"kind": v.Kind, "version": strconv.Itoa(v.Version), "version_number": v.Version, "effective_at": v.EffectiveAt}, nil)
}

// legalDocument: GET /api/legal/{kind}?lang=&version=, the current (or the
// named published) version in the best available language. The response
// says which language was served and whether it is a fallback.
func (srv *Server) legalDocument(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	version := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("version")); raw != "" {
		if version, err = strconv.Atoi(raw); err != nil || version <= 0 {
			reply(w, nil, badRequest("invalid version", err))
			return
		}
	}
	v, err := srv.compliance.LegalDocument(r.Context(), r.PathValue("kind"), lang, version)
	reply(w, v, err)
}

// legalVersions: GET /api/legal/{kind}/versions?lang=, the published
// version history, newest first.
func (srv *Server) legalVersions(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	lang, err := requestLang(w, r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.LegalVersions(r.Context(), r.PathValue("kind"), lang)
	reply(w, map[string]any{"items": v}, err)
}

// adminLegalList: GET /api/admin/legal?kind=&lang=, every row (drafts too).
func (srv *Server) adminLegalList(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	q := r.URL.Query()
	kind, lang := strings.TrimSpace(q.Get("kind")), strings.TrimSpace(q.Get("lang"))
	if kind != "" && !store.ValidLegalKind(kind) {
		reply(w, nil, badRequest("kind must be one of "+strings.Join(store.LegalKinds, ", "), nil))
		return
	}
	v, err := srv.compliance.AdminLegalDocuments(r.Context(), kind, lang)
	reply(w, map[string]any{"items": v, "kinds": store.LegalKinds}, err)
}

func (srv *Server) adminLegalGet(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.LegalDocumentByID(r.Context(), id)
	reply(w, v, err)
}

// adminLegalCreate: POST /api/admin/legal {"kind", "lang", "version"
// (0 = next), "title", "summary": [...], "body" (Markdown), "effective_at"?,
// "change_note"?}. 201 with the draft.
func (srv *Server) adminLegalCreate(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	actor, err := adminActor(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var in store.LegalDraftInput
	if err := decodeLegal(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.CreateLegalDraft(r.Context(), in, actor)
	if err != nil {
		reply(w, nil, err)
		return
	}
	replyStatus(w, http.StatusCreated, v)
}

// adminLegalUpdate: PUT /api/admin/legal/{id} {"title", "summary", "body",
// "effective_at"?, "change_note"?}; published rows are 409.
func (srv *Server) adminLegalUpdate(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in store.LegalDraftInput
	if err := decodeLegal(w, r, &in); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.UpdateLegalDraft(r.Context(), id, in, actor)
	reply(w, v, err)
}

// adminLegalPublish: POST /api/admin/legal/{id}/publish {"effective_at"?,
// "note"?}.
func (srv *Server) adminLegalPublish(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, actor, ok := srv.editorCall(w, r)
	if !ok {
		return
	}
	var in struct {
		EffectiveAt *time.Time `json:"effective_at"`
		Note        string     `json:"note"`
	}
	if err := decodeStrict(w, r, &in, true); err != nil {
		reply(w, nil, err)
		return
	}
	v, err := srv.compliance.PublishLegalDocument(r.Context(), id, in.EffectiveAt, in.Note, actor)
	reply(w, v, err)
}

// adminLegalDiff: GET /api/admin/legal/{id}/diff?base={id}; without base,
// against the previous published version in the same language.
func (srv *Server) adminLegalDiff(w http.ResponseWriter, r *http.Request) {
	if !srv.withCompliance(w) {
		return
	}
	id, err := pathID(r)
	if err != nil {
		reply(w, nil, err)
		return
	}
	var base int64
	if raw := strings.TrimSpace(r.URL.Query().Get("base")); raw != "" {
		if base, err = strconv.ParseInt(raw, 10, 64); err != nil || base <= 0 {
			reply(w, nil, badRequest("invalid base", err))
			return
		}
	}
	v, err := srv.compliance.LegalDocumentDiff(r.Context(), id, base)
	reply(w, v, err)
}

// decodeLegal reads a draft body strictly (documents can be long: 256 KiB,
// above decodeStrict's 64 KiB).
func decodeLegal(w http.ResponseWriter, r *http.Request, in *store.LegalDraftInput) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(in); err != nil {
		return badRequest("invalid JSON body", err)
	}
	return nil
}
