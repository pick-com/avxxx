package store

// The catalog search skips its trigram (fuzzy) branches once the plain
// ILIKE branch alone fills the page or the count cap, and ranks releases on
// the candidates' own columns (search.go: fuzzyGuard, releaseCandidates).
// This test checks both against the previous queries, kept below as
// reference SQL, on broad, narrow, typo and product-code queries.

import (
	"avxxx/internal/search"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func refPerformerCandidates(q string, trgm bool) (string, string) {
	active := "p.status='active' AND p.merged_into_id IS NULL"
	parts := []string{
		`SELECT p.id,0 how FROM performers p WHERE ` + active + ` AND (p.canonical_name ILIKE $1 OR p.canonical_name_ja ILIKE $1)`,
		`SELECT p.id,1 FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE ` + active + ` AND a.normalized_name ILIKE $1`,
		`SELECT p.id,2 FROM translations t JOIN performers p ON p.id=` + translatedID + ` WHERE t.entity_type='performer' AND t.field='name'
		  AND t.reviewed_value IS NOT NULL AND t.reviewed_value ILIKE $1 AND ` + active,
	}
	sim := "0::real"
	if fuzzyOK(trgm, q) {
		parts = append(parts,
			`SELECT p.id,3 FROM performers p WHERE `+active+` AND ($2 <% p.canonical_name OR $2 <% p.canonical_name_ja)`,
			`SELECT p.id,3 FROM performer_aliases a JOIN performers p ON p.id=a.performer_id WHERE `+active+` AND $2 <% a.normalized_name`)
		sim = "GREATEST(word_similarity($2,p.canonical_name),word_similarity($2,COALESCE(p.canonical_name_ja,'')))"
	}
	return strings.Join(parts, " UNION ALL "), sim
}

func refReleaseCandidates(q string, trgm bool) (string, string) {
	pub := "r.status='published' AND r.merged_into_id IS NULL"
	parts := []string{
		`SELECT r.id,0 how FROM releases r WHERE ` + pub + ` AND (r.canonical_title ILIKE $1 OR r.title_ja ILIKE $1)`,
		`SELECT r.id,1 FROM translations t JOIN releases r ON r.id=` + translatedID + ` WHERE t.entity_type='release' AND t.field='title'
		  AND t.reviewed_value IS NOT NULL AND t.reviewed_value ILIKE $1 AND ` + pub,
	}
	sim := "0::real"
	if fuzzyOK(trgm, q) {
		parts = append(parts,
			`SELECT r.id,2 FROM releases r WHERE `+pub+` AND ($2 <% r.canonical_title OR $2 <% r.title_ja)`,
			`SELECT r.id,2 FROM translations t JOIN releases r ON r.id=`+translatedID+` WHERE t.entity_type='release' AND t.field='title'
			  AND t.reviewed_value IS NOT NULL AND $2 <% t.reviewed_value AND `+pub)
		sim = "GREATEST(word_similarity($2,r.canonical_title),word_similarity($2,COALESCE(r.title_ja,'')))"
	}
	return strings.Join(parts, " UNION ALL "), sim
}

// refRanked returns "id/how" in rank order from the reference SQL.
func refRanked(t *testing.T, s *Store, sql, q string, limit int) []string {
	t.Helper()
	rows, err := s.Query(context.Background(), sql, containsPattern(q), q, prefixPattern(q), limit)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var how int
		if err := rows.Scan(&id, &how); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%d/%d", id, how))
	}
	return out
}

func ranked(items []map[string]any, labels []string) []string {
	var out []string
	for _, m := range items {
		how := len(labels) - 1
		for i, l := range labels {
			if l == m["match"] {
				how = i
			}
		}
		out = append(out, fmt.Sprintf("%d/%d", m["id"].(int64), how))
	}
	return out
}

func TestSearchFuzzyGuardMatchesReferenceIntegration(t *testing.T) {
	openIsolatedTestDB(t, "searchguard")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	trgm := s.SearchTrgm(ctx)
	t.Logf("pg_trgm: %v", trgm)

	letters := ""
	for _, c := range fmt.Sprintf("%06d", time.Now().UnixNano()%1_000_000) {
		letters += string(rune('a' + (c - '0')))
	}
	w := "glimmer" + letters   // broad: 40 titles contain it
	typo := "glimer" + letters // fuzzy only
	p := "kaedeh" + letters
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for i := 0; i < 40; i++ {
		title := fmt.Sprintf("Big %s night %d", w, i)
		switch i % 5 {
		case 0:
			title = fmt.Sprintf("%s story %d", w, i) // prefix
		case 1:
			title = fmt.Sprintf("%s%d", w, i)
		}
		if i == 7 {
			title = w // exact
		}
		date := any(fmt.Sprintf("2024-01-%02d", 1+i%9)) // ties on date
		if i%6 == 0 {
			date = nil
		}
		exec(`INSERT INTO releases(slug,canonical_title,title_ja,status,release_date) VALUES($1,$2,$3,$4,$5)`,
			fmt.Sprintf("sg-%s-%d", letters, i), title, map[bool]any{true: "夜 " + w, false: nil}[i%4 == 0],
			map[bool]string{true: "unpublished", false: "published"}[i == 13], date)
	}
	for i := 0; i < 6; i++ { // typo titles: only fuzzy recall finds them for w
		exec(`INSERT INTO releases(slug,canonical_title,status,release_date) VALUES($1,$2,'published','2025-01-01')`,
			fmt.Sprintf("sg-typo-%s-%d", letters, i), fmt.Sprintf("The %s %d", typo, i))
	}
	exec(`INSERT INTO translations(entity_type,entity_id,field,lang,value,status,source_hash,reviewed_value)
		SELECT 'release',id::text,'title','en',$1,'reviewed',i18n_hash(canonical_title),$1 FROM releases WHERE slug=$2`,
		"Translated "+w, "sg-typo-"+letters+"-0")
	exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value)
		SELECT id,'dvd_code',$1,$2 FROM releases WHERE slug=$3`, "ZQ"+strings.ToUpper(letters)+"-101", "zq"+letters+"-101", "sg-"+letters+"-3")
	for i := 0; i < 15; i++ {
		exec(`INSERT INTO performers(slug,canonical_name,canonical_name_ja) VALUES($1,$2,$3)`,
			fmt.Sprintf("sgp-%s-%d", letters, i), fmt.Sprintf("%s %d", p, i), "かえで"+letters)
	}
	exec(`INSERT INTO performers(slug,canonical_name) VALUES($1,$2)`, "sgp-typo-"+letters, "kaedh"+letters)
	exec(`INSERT INTO performer_aliases(performer_id,name,normalized_name)
		SELECT id,$1,$1 FROM performers WHERE slug=$2`, "alias "+p, "sgp-typo-"+letters)

	refRel := func(q string, limit int) []string {
		cand, sim := refReleaseCandidates(q, trgm)
		return refRanked(t, s, `WITH cand AS (`+cand+`), best AS (SELECT id,min(how) how FROM cand GROUP BY id)
	SELECT r.id,b.how FROM best b JOIN releases r ON r.id=b.id
	ORDER BY b.how,(lower(r.canonical_title)=$2 OR lower(COALESCE(r.title_ja,''))=$2) DESC,
	  (r.canonical_title ILIKE $3 OR r.title_ja ILIKE $3) DESC,`+sim+` DESC,r.release_date DESC NULLS LAST,r.id DESC LIMIT $4`, q, limit)
	}
	refPerf := func(q string, limit int) []string {
		cand, sim := refPerformerCandidates(q, trgm)
		return refRanked(t, s, `WITH cand AS (`+cand+`), best AS (SELECT id,min(how) how FROM cand GROUP BY id)
	SELECT p.id,b.how FROM best b JOIN performers p ON p.id=b.id
	ORDER BY b.how,(lower(p.canonical_name)=$2 OR lower(COALESCE(p.canonical_name_ja,''))=$2) DESC,
	  (p.canonical_name ILIKE $3 OR p.canonical_name_ja ILIKE $3) DESC,`+sim+` DESC,p.canonical_name,p.id LIMIT $4`, q, limit)
	}
	refCounts := func(q string, cap int) map[string]int {
		perf, _ := refPerformerCandidates(q, trgm)
		rel, _ := refReleaseCandidates(q, trgm)
		args := []any{containsPattern(q), q, cap}
		if keys := search.CodeKeys(q); keys != nil {
			rel += ` UNION ALL SELECT r.id,0 FROM release_identifiers ri JOIN releases r ON r.id=ri.release_id
			  WHERE search_code_keys(ri.normalized_value) && $4::text[] AND r.status='published' AND r.merged_into_id IS NULL`
			args = append(args, keys)
		}
		var np, nr int
		if err := s.QueryRow(ctx, `WITH pc AS (`+perf+`), rc AS (`+rel+`)
		SELECT (SELECT count(*) FROM (SELECT DISTINCT id FROM pc WHERE $2::text IS NOT NULL LIMIT $3) a)::int,
		       (SELECT count(*) FROM (SELECT DISTINCT id FROM rc LIMIT $3) b)::int`, args...).Scan(&np, &nr); err != nil {
			t.Fatal(err)
		}
		return map[string]int{SearchTypePerformer: np, SearchTypeRelease: nr}
	}

	sawFuzzy := false
	for _, q := range []string{w, typo, w + " story", w + "1", "big " + w, p, "kaedh" + letters, "かえで" + letters, "zq" + letters + "-101", "nomatch" + letters} {
		for _, limit := range []int{1, 5, 20, 100} {
			got, err := s.matchReleases(ctx, q, limit, trgm)
			if err != nil {
				t.Fatal(err)
			}
			if g, want := ranked(got, releaseMatch), refRel(q, limit); !reflect.DeepEqual(g, want) {
				t.Errorf("releases %q limit %d:\n got  %v\n want %v", q, limit, g, want)
			}
			for _, m := range got {
				sawFuzzy = sawFuzzy || m["match"] == "fuzzy"
			}
			gp, err := s.matchPerformers(ctx, q, limit, trgm)
			if err != nil {
				t.Fatal(err)
			}
			if g, want := ranked(gp, performerMatch), refPerf(q, limit); !reflect.DeepEqual(g, want) {
				t.Errorf("performers %q limit %d:\n got  %v\n want %v", q, limit, g, want)
			}
		}
		// Small caps make the guard fire on these few rows, as 1000 does
		// on a production catalog.
		for _, limit := range []int{3, 12, SearchCountCap} {
			got, err := s.searchCountsCap(ctx, q, limit)
			if err != nil {
				t.Fatal(err)
			}
			if want := refCounts(q, limit); !reflect.DeepEqual(got, want) {
				t.Errorf("counts %q cap %d = %v, reference %v", q, limit, got, want)
			}
		}
	}
	if trgm && !sawFuzzy {
		t.Error("no query exercised the fuzzy branches")
	}
}
