package store

import (
	"avxxx/internal/catalog"
	"avxxx/internal/identity"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/text/unicode/norm"
)

// Performer resolution for SaveCatalogRecord (FINAL_DATA_DESIGN 3.1). A
// performer is found, in order, by
//
//  1. a source-issued id (performer_external_ids, e.g. the DMM actress id),
//  2. its Latin-script name slug (the long-standing slug upsert, unchanged),
//  3. a unique name / alias match through identity.NormalizeName (names with
//     no Latin slug, i.e. Japanese-only names),
//
// and otherwise created with the first free slug of
// catalog.PerformerSlugCandidates, finally "performer-<id>". Slugs of
// existing performers are never rewritten: a better (romaji) slug found later
// for a performer published under a fallback slug is recorded in
// slug_redirects instead, plus a romaji alias.

// externalDMMActress is the performer_external_ids namespace of DMM ids.
const externalDMMActress = "dmm_actress"

// performerRef is one credited performer of a release record, or the
// performer of a performer record.
type performerRef struct {
	Name string
	// Readings: romaji / English forms and kana readings, best first.
	Readings []string
	DMMID    string
	// Primary is the record's is_primary flag when it has one.
	Primary *bool
}

func (r performerRef) slugInput() catalog.PerformerSlugInput {
	return catalog.PerformerSlugInput{Name: r.Name, Readings: r.Readings, SourceScoped: catalog.DMMActressSlug(r.DMMID)}
}

// readingKeys are the record / ref fields holding alternate forms of a name.
var readingKeys = []string{"name_romaji", "name_en", "romaji", "ruby", "name_kana"}

func recordText(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%.0f", x))
	case int, int64:
		return fmt.Sprint(x)
	}
	return ""
}

func refFromMap(get func(string) any) performerRef {
	r := performerRef{Name: recordText(get("name")), DMMID: recordText(get("dmm_actress_id"))}
	for _, k := range readingKeys {
		if v := recordText(get(k)); v != "" {
			r.Readings = append(r.Readings, v)
		}
	}
	switch p := get("is_primary").(type) {
	case bool:
		r.Primary = &p
	case string:
		if b := p == "true" || p == "1"; b || p == "false" || p == "0" {
			r.Primary = &b
		}
	}
	return r
}

// performerRefs returns the credited performers of a release record:
// performer_refs (name + source ids + readings) when the adapter gives them,
// else the plain "performers" names.
func performerRefs(fields map[string]any) []performerRef {
	var out []performerRef
	switch refs := fields["performer_refs"].(type) {
	case []map[string]string:
		for _, m := range refs {
			out = append(out, refFromMap(func(k string) any {
				if v, ok := m[k]; ok {
					return v
				}
				return nil
			}))
		}
	case []map[string]any:
		for _, m := range refs {
			out = append(out, refFromMap(func(k string) any { return m[k] }))
		}
	case []any:
		for _, e := range refs {
			if m, ok := e.(map[string]any); ok {
				out = append(out, refFromMap(func(k string) any { return m[k] }))
			}
		}
	}
	if len(out) > 0 {
		return out
	}
	switch names := fields["performers"].(type) {
	case []string:
		for _, n := range names {
			out = append(out, performerRef{Name: strings.TrimSpace(n)})
		}
	case []any:
		for _, n := range names {
			out = append(out, performerRef{Name: recordText(n)})
		}
	}
	return out
}

// recordPerformerRef is the ref of a performer record.
func recordPerformerRef(fields map[string]any, name string) performerRef {
	r := refFromMap(func(k string) any { return fields[k] })
	r.Name = name
	r.Primary = nil
	return r
}

// resolvePerformer finds or creates the performer of ref. id is 0 when the
// performer is identified by its Latin name slug only (step 2): the caller
// upserts by that slug, exactly as before.
func resolvePerformer(ctx context.Context, tx pgx.Tx, sourceID int64, ref performerRef) (id int64, slug string, err error) {
	if id, err = performerByExternalID(ctx, tx, ref.DMMID); err != nil || id != 0 {
		return finishResolve(ctx, tx, id, err)
	}
	if s := catalog.NameSlug(ref.Name); s != "" {
		return 0, s, nil
	}
	if id, err = performerByName(ctx, tx, ref); err != nil || id != 0 {
		return finishResolve(ctx, tx, id, err)
	}
	return createPerformer(ctx, tx, ref)
}

// performerRecordSlug is the slug a performer record upserts under. A record
// seen before keeps updating the performer it resolved to (its slug never
// changes, whatever the name slugs to now); otherwise resolvePerformer decides.
func performerRecordSlug(ctx context.Context, tx pgx.Tx, sourceID int64, rec catalog.Record, ref performerRef) (string, error) {
	var prev int64
	err := tx.QueryRow(ctx, `SELECT performer_id FROM source_records WHERE source_id=$1 AND entity_type='performer' AND source_record_key=$2 AND performer_id IS NOT NULL`,
		sourceID, rec.SourceKey).Scan(&prev)
	var slug string
	switch {
	case err == nil:
		if prev, err = survivingID(ctx, tx, "performers", prev); err != nil {
			return "", err
		}
		_, slug, err = finishResolve(ctx, tx, prev, nil)
	case errors.Is(err, pgx.ErrNoRows):
		_, slug, err = resolvePerformer(ctx, tx, sourceID, ref)
	}
	if err != nil {
		return "", err
	}
	return catalog.RequireSlug(slug)
}

func finishResolve(ctx context.Context, tx pgx.Tx, id int64, err error) (int64, string, error) {
	if err != nil {
		return 0, "", err
	}
	var slug string
	err = tx.QueryRow(ctx, `SELECT slug FROM performers WHERE id=$1`, id).Scan(&slug)
	return id, slug, err
}

// performerByExternalID: the DMM actress id's performer (following merges),
// or the performer already published under the id's source-scoped slug.
func performerByExternalID(ctx context.Context, tx pgx.Tx, dmmID string) (int64, error) {
	scoped := catalog.DMMActressSlug(dmmID)
	if scoped == "" {
		return 0, nil
	}
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM (SELECT performer_id AS id,0 AS pri FROM performer_external_ids WHERE namespace=$1 AND external_id=$2
		UNION ALL SELECT id,1 FROM performers WHERE slug=$3) x ORDER BY pri LIMIT 1`, externalDMMActress, dmmID, scoped).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return survivingID(ctx, tx, "performers", id)
}

// performerByName returns the one live performer whose name, Japanese name or
// alias folds (identity.NormalizeName) to ref.Name's key; 0 when none or
// several match, or when the match carries a different DMM actress id.
func performerByName(ctx context.Context, tx pgx.Tx, ref performerRef) (int64, error) {
	key := identity.NormalizeName(ref.Name)
	if key == "" {
		return 0, nil
	}
	nfkc := norm.NFKC.String(ref.Name)
	forms := []string{ref.Name, nfkc, strings.Join(strings.Fields(ref.Name), ""), strings.Join(strings.Fields(nfkc), " ")}
	rows, err := tx.Query(ctx, `SELECT p.id,p.canonical_name,COALESCE(p.canonical_name_ja,''),
		COALESCE((SELECT array_agg(a.name) FROM performer_aliases a WHERE a.performer_id=p.id),'{}'),
		EXISTS(SELECT 1 FROM performer_external_ids e WHERE e.performer_id=p.id AND e.namespace=$3 AND e.external_id<>$4)
		FROM performers p WHERE p.merged_into_id IS NULL AND (p.canonical_name=ANY($1) OR p.canonical_name_ja=ANY($1)
		  OR p.id IN (SELECT performer_id FROM performer_aliases WHERE normalized_name=ANY($2)))
		ORDER BY p.id LIMIT 20`, forms, []string{identity.AliasForm(ref.Name), key}, externalDMMActress, ref.DMMID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var found []int64
	for rows.Next() {
		var id int64
		var name, nameJA string
		var aliases []string
		var otherID bool
		if err = rows.Scan(&id, &name, &nameJA, &aliases, &otherID); err != nil {
			return 0, err
		}
		if otherID && ref.DMMID != "" {
			continue // same name, different DMM actress: not this person
		}
		for _, n := range append([]string{name, nameJA}, aliases...) {
			if n != "" && identity.NormalizeName(n) == key {
				found = append(found, id)
				break
			}
		}
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	if len(found) != 1 {
		return 0, nil // ambiguous names are left for identity review
	}
	return found[0], nil
}

// performerSlugTaken: a live or merged performer, or a redirect, owns slug.
func performerSlugTaken(ctx context.Context, tx pgx.Tx, slug string) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM performers WHERE slug=$1)
		OR EXISTS(SELECT 1 FROM slug_redirects WHERE entity_type='performer' AND old_slug=$1)`, slug).Scan(&taken)
	return taken, err
}

// createPerformer inserts a performer with only its name, under the first
// free slug candidate or "performer-<id>". The caller's upsert fills the rest.
func createPerformer(ctx context.Context, tx pgx.Tx, ref performerRef) (int64, string, error) {
	nameJA := ""
	if catalog.NameSlug(ref.Name) == "" {
		nameJA = ref.Name // a Japanese-only name is also the Japanese name
	}
	insert := func(id any, slug string) (int64, string, error) {
		if _, err := catalog.RequireSlug(slug); err != nil {
			return 0, "", err
		}
		var out int64
		err := tx.QueryRow(ctx, `INSERT INTO performers(id,slug,canonical_name,canonical_name_ja)
			VALUES(COALESCE($1::bigint,nextval(pg_get_serial_sequence('performers','id'))),$2,$3,NULLIF($4,'')) RETURNING id`,
			id, slug, ref.Name, nameJA).Scan(&out)
		return out, slug, err
	}
	for _, c := range catalog.PerformerSlugCandidates(ref.slugInput()) {
		taken, err := performerSlugTaken(ctx, tx, c)
		if err != nil {
			return 0, "", err
		}
		if !taken {
			return insert(nil, c)
		}
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('performers','id'))`).Scan(&id); err != nil {
		return 0, "", err
	}
	return insert(id, catalog.FallbackPerformerSlug(id))
}

// linkPerformerIdentity records what a source says about a resolved
// performer: its DMM actress id, its romaji reading as an alias, and, when
// the performer is published under a fallback slug, the better slug as a
// redirect (the published slug never changes).
func linkPerformerIdentity(ctx context.Context, tx pgx.Tx, sourceID, id int64, ref performerRef) error {
	if catalog.DMMActressSlug(ref.DMMID) != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO performer_external_ids(namespace,external_id,performer_id,source_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
			externalDMMActress, ref.DMMID, id, sourceID); err != nil {
			return err
		}
	}
	var romaji string
	for _, r := range append(append([]string(nil), ref.Readings...), ref.Name) {
		if catalog.HasKana(r) {
			if v, ok := catalog.Romaji(r); ok {
				romaji = v
				break
			}
		}
	}
	if romaji != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO performer_aliases(performer_id,name,normalized_name,language,alias_type,source_id) VALUES($1,$2,$3,'ja-Latn','romaji',$4) ON CONFLICT DO NOTHING`,
			id, romaji, identity.AliasForm(romaji), sourceID); err != nil {
			return err
		}
	}
	var slug string
	if err := tx.QueryRow(ctx, `SELECT slug FROM performers WHERE id=$1`, id).Scan(&slug); err != nil {
		return err
	}
	if !catalog.IsFallbackSlug(slug) {
		return nil
	}
	scoped := catalog.DMMActressSlug(ref.DMMID)
	for _, c := range catalog.PerformerSlugCandidates(ref.slugInput()) {
		if c == slug || c == scoped || catalog.IsFallbackSlug(c) {
			continue
		}
		taken, err := performerSlugTaken(ctx, tx, c)
		if err != nil || taken {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO slug_redirects(entity_type,old_slug,entity_id) VALUES('performer',$1,$2) ON CONFLICT DO NOTHING`, c, id)
		return err
	}
	return nil
}
