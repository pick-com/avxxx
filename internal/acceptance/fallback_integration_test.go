package acceptance

import (
	"avxxx/internal/catalog"
	"fmt"
	"strings"
	"testing"
)

const tpdbToken = "ACCEPTANCE-TPDB-TOKEN-91c2"

func tpdbScene(id, title, externalID, date, site string, performers ...string) map[string]any {
	ps := []map[string]any{}
	for i, p := range performers {
		ps = append(ps, map[string]any{"id": fmt.Sprintf("bbbbbbbb-0000-4000-8000-%s%02d", id[len(id)-10:], i), "slug": catalog.Slug(p), "name": p, "extras": map[string]any{"birthday": "1980-01-01"}})
	}
	return map[string]any{"id": id, "title": title, "type": "JAV", "external_id": externalID, "date": date, "duration": 9000,
		"site": map[string]any{"id": 77, "name": site}, "performers": ps, "tags": []map[string]any{{"id": 1, "name": "TPDB Only Tag"}}}
}

// TestFallbackSourceNeverCreatesOrOverridesIntegration crawls a ThePornDB-
// style third_party_api source (FINAL_DATA_DESIGN 2.4: batch lookup and
// missing-field fallback, never the sole source of truth) over a catalog the
// other sources built. Its records may attach to existing releases and
// performers but must never create an entity, never change a value a higher
// tier provides and never open a conflict on their own — on the first crawl
// and on re-crawls.
func TestFallbackSourceNeverCreatesOrOverridesIntegration(t *testing.T) {
	e := newEnv(t)
	c := e.newCatalog(t)
	e.crawlAll(c, catalog.PhaseBaseline)
	e.crawlAll(c, catalog.PhaseIncremental)

	tp := newSite(t)
	host := strings.TrimPrefix(tp.srv.URL, "http://")
	if err := e.registry.Register(catalog.NewThePornDBAdapterWith(tpdbToken, host)); err != nil {
		t.Fatal(err)
	}
	const list = "/jav?orderBy=recently_released&per_page=100"
	tp.set(list, dmmJSON(map[string]any{
		"data": []map[string]any{
			// Known release (DVD code JUQ-743): different title, date, maker,
			// an extra performer the catalog does not know.
			tpdbScene("aaaaaaaa-0000-4000-8000-000000000001", "JUQ-743 TPDB Alternate Title", "juq00743", "2024-05-25", "Madonna TPDB", "Yurine Tsukino", "Brand New Person"),
			// Unknown code: nothing to attach to.
			tpdbScene("aaaaaaaa-0000-4000-8000-000000000002", "ZZZ-999 Unknown Release", "zzz00999", "2024-09-09", "Nobody Studio", "Another New Person"),
		},
		"meta": map[string]any{"total": 2, "per_page": 100, "current_page": 1, "last": 1},
	}))
	src := e.addSource("theporndb-fixture", catalog.ThePornDBAdapterName, tp.url(list), "third_party_api")

	before := e.snap()
	conflicts := e.int64(`SELECT count(*) FROM field_conflicts WHERE status='pending'`)
	for i, ph := range []catalog.Phase{catalog.PhaseBaseline, catalog.PhaseIncremental, catalog.PhaseBaseline} {
		e.mustSucceed(e.run(src, ph), "theporndb "+string(ph))
		after := e.snap()
		for _, table := range []string{"performers", "releases", "release_identifiers", "credits", "release_tags", "performer_external_ids", "performer_aliases", "slug_redirects", "series"} {
			if d := diffRows(before, after, []string{table}); d != "" {
				t.Errorf("pass %d: the fallback source changed %s:%s", i+1, table, d)
			}
		}
		if n := e.int64(`SELECT count(*) FROM field_conflicts WHERE status='pending'`); n != conflicts {
			t.Errorf("pass %d: the fallback source opened %d conflicts on its own", i+1, n-conflicts)
		}
	}
	// It is recorded as evidence: attached to juq-743, unresolved for the unknown code.
	if got := e.str(`SELECT r.slug FROM source_records sr JOIN releases r ON r.id=sr.release_id WHERE sr.source_id=$1 AND sr.source_record_key LIKE '%aaaaaaaa-0000-4000-8000-000000000001'`, src); got != "juq-743" {
		t.Errorf("TPDB record of JUQ-743 attached to %s, want juq-743", got)
	}
	if n := e.int64(`SELECT count(*) FROM source_records WHERE source_id=$1 AND source_record_key LIKE '%aaaaaaaa-0000-4000-8000-000000000002' AND release_id IS NULL AND performer_id IS NULL`, src); n != 1 {
		t.Errorf("TPDB record of the unknown code: %d unattached records, want 1", n)
	}
	if n := e.int64(`SELECT count(*) FROM performers WHERE canonical_name IN ('Brand New Person','Another New Person')`); n != 0 {
		t.Errorf("the fallback source created %d performers", n)
	}
	// The token never reaches the database.
	if n := e.int64(`SELECT count(*) FROM source_records WHERE raw_data::text LIKE '%`+tpdbToken+`%'`) +
		e.int64(`SELECT count(*) FROM jobs WHERE COALESCE(error,'')||COALESCE(result_counts::text,'') LIKE '%`+tpdbToken+`%'`); n != 0 {
		t.Errorf("ThePornDB token stored in %d rows", n)
	}
}
