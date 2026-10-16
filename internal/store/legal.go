package store

// Versioned legal documents (LegalPage; PRD 15; tech plan 3.13;
// migration 041). Public reads return published versions only; admins
// draft, edit and publish. Every admin write is a review_events row
// (entity_type 'legal_document').

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// LegalDocument is one legal_documents row (admin view).
type LegalDocument struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Lang        string     `json:"lang"`
	Version     int        `json:"version"`
	Title       string     `json:"title"`
	Summary     []string   `json:"summary"`
	Body        string     `json:"body"`
	Status      string     `json:"status"`
	EffectiveAt *time.Time `json:"effective_at"`
	PublishedAt *time.Time `json:"published_at"`
	PublishedBy *string    `json:"published_by"`
	ChangeNote  string     `json:"change_note"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// PublicLegalDocument is a published version as LegalPage renders it.
// Lang is the language served; Fallback is set when it is not the one asked
// for. CurrentVersion is the version in force now.
type PublicLegalDocument struct {
	Kind           string    `json:"kind"`
	Lang           string    `json:"lang"`
	RequestedLang  string    `json:"requested_lang"`
	Fallback       bool      `json:"fallback"`
	Version        int       `json:"version"`
	CurrentVersion int       `json:"current_version"`
	IsCurrent      bool      `json:"is_current"`
	Title          string    `json:"title"`
	Summary        []string  `json:"summary"`
	Body           string    `json:"body"`
	EffectiveAt    time.Time `json:"effective_at"`
	PublishedAt    time.Time `json:"published_at"`
}

// LegalVersionInfo is one entry of a document's public version history.
type LegalVersionInfo struct {
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	Lang        string    `json:"lang"`
	Langs       []string  `json:"langs"`
	EffectiveAt time.Time `json:"effective_at"`
	PublishedAt time.Time `json:"published_at"`
	IsCurrent   bool      `json:"is_current"`
}

// LegalVersion identifies the version of a kind in force.
type LegalVersion struct {
	Kind        string    `json:"kind"`
	Version     int       `json:"version"`
	EffectiveAt time.Time `json:"effective_at"`
}

const legalColumns = `id,kind,lang,version,title,summary,body,status,effective_at,published_at,published_by,change_note,created_by,created_at,updated_at`

func scanLegal(r pgx.Row) (LegalDocument, error) {
	var d LegalDocument
	err := r.Scan(&d.ID, &d.Kind, &d.Lang, &d.Version, &d.Title, &d.Summary, &d.Body, &d.Status, &d.EffectiveAt, &d.PublishedAt,
		&d.PublishedBy, &d.ChangeNote, &d.CreatedBy, &d.CreatedAt, &d.UpdatedAt)
	if d.Summary == nil {
		d.Summary = []string{}
	}
	return d, err
}

// currentVersionSQL is the version of kind $1 in force now (0 when none).
const currentVersionSQL = `COALESCE((SELECT max(version) FROM legal_documents WHERE kind=$1 AND status='published' AND effective_at<=now()),0)`

// CurrentLegalVersion returns the version of kind in force now: the
// highest published version whose effective_at has passed. This is the
// stable hook for terms_acceptances (account work): record
// CurrentLegalVersion(ctx, "terms").Version when a user accepts the terms.
// pgx.ErrNoRows when nothing of that kind is in force.
func (s *Store) CurrentLegalVersion(ctx context.Context, kind string) (LegalVersion, error) {
	if !ValidLegalKind(kind) {
		return LegalVersion{}, pgx.ErrNoRows
	}
	v := LegalVersion{Kind: kind}
	err := s.QueryRow(ctx, `SELECT version,min(effective_at) FROM legal_documents
		WHERE kind=$1 AND status='published' AND version=`+currentVersionSQL+` GROUP BY version`, kind).Scan(&v.Version, &v.EffectiveAt)
	return v, err
}

// LegalDocument returns a published version of kind (0 = the current one)
// in the first language of LegalLangChain(lang) that has it. Unknown kind,
// version or nothing published: pgx.ErrNoRows.
func (s *Store) LegalDocument(ctx context.Context, kind, lang string, version int) (PublicLegalDocument, error) {
	out := PublicLegalDocument{Kind: kind, RequestedLang: lang}
	if !ValidLegalKind(kind) {
		return out, pgx.ErrNoRows
	}
	if lang == "" {
		out.RequestedLang = LegalDefaultLang
	}
	chain := LegalLangChain(out.RequestedLang)
	if err := s.QueryRow(ctx, `SELECT `+currentVersionSQL, kind).Scan(&out.CurrentVersion); err != nil {
		return out, err
	}
	if version == 0 {
		version = out.CurrentVersion
	}
	err := s.QueryRow(ctx, `SELECT lang,version,title,summary,body,
		(SELECT min(effective_at) FROM legal_documents x WHERE x.kind=d.kind AND x.version=d.version AND x.status='published'),published_at
		FROM legal_documents d WHERE kind=$1 AND version=$2 AND status='published' AND lang=ANY($3)
		ORDER BY array_position($3,lang) LIMIT 1`, kind, version, chain).
		Scan(&out.Lang, &out.Version, &out.Title, &out.Summary, &out.Body, &out.EffectiveAt, &out.PublishedAt)
	if err != nil {
		return out, err
	}
	if out.Summary == nil {
		out.Summary = []string{}
	}
	out.Fallback = out.Lang != out.RequestedLang
	out.IsCurrent = out.Version == out.CurrentVersion
	return out, nil
}

// LegalVersions is the public version history of kind, newest first, each
// titled in the best language of LegalLangChain(lang). Versions published
// with a future effective date are listed (is_current false).
func (s *Store) LegalVersions(ctx context.Context, kind, lang string) ([]LegalVersionInfo, error) {
	if !ValidLegalKind(kind) {
		return nil, pgx.ErrNoRows
	}
	if lang == "" {
		lang = LegalDefaultLang
	}
	chain := LegalLangChain(lang)
	var current int
	if err := s.QueryRow(ctx, `SELECT `+currentVersionSQL, kind).Scan(&current); err != nil {
		return nil, err
	}
	list, err := collect[LegalVersionInfo](s.Query(ctx, `SELECT version,
		(array_agg(title ORDER BY COALESCE(array_position($2,lang),1000),lang))[1],
		(array_agg(lang ORDER BY COALESCE(array_position($2,lang),1000),lang))[1],
		array_agg(lang ORDER BY lang),min(effective_at),min(published_at)
		FROM legal_documents WHERE kind=$1 AND status='published' GROUP BY version ORDER BY version DESC`, kind, chain))(
		func(r pgx.Rows) (LegalVersionInfo, error) {
			var v LegalVersionInfo
			return v, r.Scan(&v.Version, &v.Title, &v.Lang, &v.Langs, &v.EffectiveAt, &v.PublishedAt)
		})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, pgx.ErrNoRows
	}
	for i := range list {
		list[i].IsCurrent = list[i].Version == current
	}
	return list, nil
}

// AdminLegalDocuments lists every row (drafts included), optionally for one
// kind and/or language, newest version first.
func (s *Store) AdminLegalDocuments(ctx context.Context, kind, lang string) ([]LegalDocument, error) {
	return collect[LegalDocument](s.Query(ctx, `SELECT `+legalColumns+` FROM legal_documents
		WHERE ($1='' OR kind=$1) AND ($2='' OR lang=$2) ORDER BY kind, version DESC, lang`, kind, lang))(
		func(r pgx.Rows) (LegalDocument, error) { return scanLegal(r) })
}

// LegalDocumentByID returns one row (admin).
func (s *Store) LegalDocumentByID(ctx context.Context, id int64) (LegalDocument, error) {
	return scanLegal(s.QueryRow(ctx, `SELECT `+legalColumns+` FROM legal_documents WHERE id=$1`, id))
}

// legalEventState is what review_events keeps of a document: the text is
// in the row itself, so only a fingerprint and its size are logged.
func legalEventState(d LegalDocument) map[string]any {
	sum := sha256.Sum256([]byte(d.Body))
	return map[string]any{"kind": d.Kind, "lang": d.Lang, "version": d.Version, "status": d.Status, "title": d.Title,
		"summary": d.Summary, "body_sha256": hex.EncodeToString(sum[:]), "body_bytes": len(d.Body), "effective_at": d.EffectiveAt}
}

// CreateLegalDraft adds a draft: version 0 means the next version of the
// kind; an existing version adds a translation of it. Duplicate (kind,
// lang, version) is a conflict.
func (s *Store) CreateLegalDraft(ctx context.Context, in LegalDraftInput, actor string) (LegalDocument, error) {
	if err := in.Normalize(true); err != nil {
		return LegalDocument{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return LegalDocument{}, err
	}
	defer tx.Rollback(ctx)
	// One writer per kind at a time, so "next version" is unique.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(842041, hashtext($1))`, in.Kind); err != nil {
		return LegalDocument{}, err
	}
	var maxVersion int
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM legal_documents WHERE kind=$1`, in.Kind).Scan(&maxVersion); err != nil {
		return LegalDocument{}, err
	}
	if in.Version == 0 {
		in.Version = maxVersion + 1
	} else if in.Version > maxVersion+1 {
		return LegalDocument{}, invalid("version %d skips ahead: the next version of %s is %d", in.Version, in.Kind, maxVersion+1)
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM legal_documents WHERE kind=$1 AND lang=$2 AND version=$3)`, in.Kind, in.Lang, in.Version).Scan(&exists); err != nil {
		return LegalDocument{}, err
	}
	if exists {
		return LegalDocument{}, conflict("%s version %d already exists in %s", in.Kind, in.Version, in.Lang)
	}
	d, err := scanLegal(tx.QueryRow(ctx, `INSERT INTO legal_documents(kind,lang,version,title,summary,body,effective_at,change_note,created_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+legalColumns,
		in.Kind, in.Lang, in.Version, in.Title, in.Summary, in.Body, in.EffectiveAt, in.ChangeNote, actor))
	if err != nil {
		return d, err
	}
	if err = writeReviewEvent(ctx, tx, "legal_document", d.ID, "legal_draft", nil, legalEventState(d), in.ChangeNote, actor); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

// lockLegal locks a row for an edit; published rows are frozen (409).
func lockLegal(ctx context.Context, tx pgx.Tx, id int64) (LegalDocument, error) {
	d, err := scanLegal(tx.QueryRow(ctx, `SELECT `+legalColumns+` FROM legal_documents WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return d, err
	}
	if d.Status == "published" {
		return d, conflict("published documents are frozen; create a new version instead")
	}
	return d, nil
}

// UpdateLegalDraft replaces a draft's title, summary, body, planned
// effective date and change note.
func (s *Store) UpdateLegalDraft(ctx context.Context, id int64, in LegalDraftInput, actor string) (LegalDocument, error) {
	if err := in.Normalize(false); err != nil {
		return LegalDocument{}, err
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return LegalDocument{}, err
	}
	defer tx.Rollback(ctx)
	before, err := lockLegal(ctx, tx, id)
	if err != nil {
		return before, err
	}
	d, err := scanLegal(tx.QueryRow(ctx, `UPDATE legal_documents SET title=$2,summary=$3,body=$4,effective_at=$5,change_note=$6,updated_at=now()
		WHERE id=$1 RETURNING `+legalColumns, id, in.Title, in.Summary, in.Body, in.EffectiveAt, in.ChangeNote))
	if err != nil {
		return d, err
	}
	if err = writeReviewEvent(ctx, tx, "legal_document", id, "legal_edit", legalEventState(before), legalEventState(d), in.ChangeNote, actor); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

// PublishLegalDocument publishes a draft. A new version must be newer than
// every published version of its kind and takes effect at effectiveAt (nil:
// the draft's planned date, else now). A translation of an already
// published version inherits that version's effective date.
func (s *Store) PublishLegalDocument(ctx context.Context, id int64, effectiveAt *time.Time, note, actor string) (LegalDocument, error) {
	note = strings.TrimSpace(note)
	if len(note) > MaxReasonLen {
		return LegalDocument{}, invalid("note too long")
	}
	tx, err := s.Begin(ctx)
	if err != nil {
		return LegalDocument{}, err
	}
	defer tx.Rollback(ctx)
	before, err := lockLegal(ctx, tx, id)
	if err != nil {
		return before, err
	}
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(842041, hashtext($1))`, before.Kind); err != nil {
		return before, err
	}
	var sibling *time.Time
	var maxPublished int
	if err = tx.QueryRow(ctx, `SELECT min(effective_at) FILTER (WHERE version=$2),COALESCE(max(version),0)
		FROM legal_documents WHERE kind=$1 AND status='published'`, before.Kind, before.Version).Scan(&sibling, &maxPublished); err != nil {
		return before, err
	}
	effective := effectiveAt
	switch {
	case sibling != nil:
		if effectiveAt != nil && !effectiveAt.Equal(*sibling) {
			return before, invalid("version %d is already in force from %s; translations share its effective date", before.Version, sibling.Format(time.RFC3339))
		}
		effective = sibling
	case before.Version <= maxPublished:
		return before, conflict("version %d is older than the published version %d; create a new version", before.Version, maxPublished)
	case effective == nil && before.EffectiveAt != nil:
		effective = before.EffectiveAt
	case effective == nil:
		now := time.Now()
		effective = &now
	}
	d, err := scanLegal(tx.QueryRow(ctx, `UPDATE legal_documents SET status='published',effective_at=$2,published_at=now(),published_by=$3,updated_at=now()
		WHERE id=$1 RETURNING `+legalColumns, id, *effective, actor))
	if err != nil {
		return d, err
	}
	if err = writeReviewEvent(ctx, tx, "legal_document", id, "legal_publish", legalEventState(before), legalEventState(d), note, actor); err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

// LegalDiff is a line diff between two rows of the same kind.
type LegalDiff struct {
	Base   *LegalDocument `json:"base"`
	Target LegalDocument  `json:"target"`
	Lines  []DiffLine     `json:"lines"`
}

// legalText is the diffed text: title, summary lines, then the body.
func legalText(d LegalDocument) string {
	var b strings.Builder
	b.WriteString("# " + d.Title + "\n\n")
	for _, s := range d.Summary {
		b.WriteString("> " + s + "\n")
	}
	if len(d.Summary) > 0 {
		b.WriteString("\n")
	}
	b.WriteString(d.Body)
	return b.String()
}

// LegalDocumentDiff diffs row id against baseID; baseID 0 picks the newest
// published row of the same kind and language with a lower version (or,
// for a translation, the same version in zh-CN). No base: every line added.
func (s *Store) LegalDocumentDiff(ctx context.Context, id, baseID int64) (LegalDiff, error) {
	target, err := s.LegalDocumentByID(ctx, id)
	if err != nil {
		return LegalDiff{}, err
	}
	out := LegalDiff{Target: target}
	var base LegalDocument
	if baseID > 0 {
		if base, err = s.LegalDocumentByID(ctx, baseID); err != nil {
			return out, err
		}
		if base.Kind != target.Kind {
			return out, invalid("base is a %s document, target a %s document", base.Kind, target.Kind)
		}
	} else {
		base, err = scanLegal(s.QueryRow(ctx, `SELECT `+legalColumns+` FROM legal_documents
			WHERE kind=$1 AND status='published' AND id<>$4 AND ((lang=$2 AND version<$3) OR (version=$3 AND lang=$5))
			ORDER BY (lang=$2) DESC, version DESC LIMIT 1`, target.Kind, target.Lang, target.Version, target.ID, LegalDefaultLang))
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	}
	baseText := ""
	if base.ID != 0 {
		out.Base = &base
		baseText = legalText(base)
	}
	out.Lines = LineDiff(baseText, legalText(target))
	return out, nil
}
