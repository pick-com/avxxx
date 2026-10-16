package store

import (
	"context"
	"testing"
)

// TestReleaseVersionsIntegration covers migration 047's release_versions and
// the AdminDedupe decisions around it: the compare summaries, 其他版本
// (link, review event, not_duplicate memory, candidate closed), the release
// page's other_versions (published only, through chains and merges) and
// unlinking.
func TestReleaseVersionsIntegration(t *testing.T) {
	openIsolatedTestDB(t, "relversions")
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	s.Close()
	if s, err = Open(ctx); err != nil { // 047 re-runs
		t.Fatalf("second Open: %v", err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	release := func(slug, status, edition string) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO releases(slug,canonical_title,status,edition_type,maker_name,release_date,runtime_minutes)
			VALUES($1,$1,$2,$3,'Maker',DATE '2024-01-02',120) RETURNING id`, slug, status, edition).Scan(&id))
		exec(`INSERT INTO release_identifiers(release_id,identifier_type,identifier_value,normalized_value) VALUES($1,'dvd_code','ABC-001',$2)`, id, slug)
		return id
	}
	orig := release("rv-orig", "published", "original")
	uhd := release("rv-4k", "published", "4k")
	draft := release("rv-draft", "draft", "uncensored")
	var src, perf int64
	must(s.QueryRow(ctx, `INSERT INTO sources(name,url) VALUES('JAV Database','https://rv.example/') RETURNING id`).Scan(&src))
	exec(`INSERT INTO source_records(source_id,entity_type,source_record_key,source_url,release_id,parser_version) VALUES($1,'release','k1','https://rv.example/abc-001',$2,'1')`, src, orig)
	exec(`INSERT INTO playback_sources(release_id,kind,provider,label,url) VALUES($1,'external_link','prov','正片','https://play.example/1')`, uhd)
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,status) VALUES('rv-p','Perf','active') RETURNING id`).Scan(&perf))
	exec(`INSERT INTO release_performers(release_id,performer_id,is_primary) VALUES($1,$2,true)`, orig, perf)
	cand := func(a, b int64) int64 {
		var id int64
		must(s.QueryRow(ctx, `INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence) VALUES('release',$1,$2,0.95,'{"rules":[]}') RETURNING id`, a, b).Scan(&id))
		return id
	}
	c1 := cand(uhd, orig)

	cmp, err := s.CandidateCompare(ctx, c1)
	must(err)
	if cmp.Left.ID != uhd || cmp.Right.ID != orig || cmp.Right.Codes[0] != "ABC-001" || len(cmp.Right.Performers) != 1 ||
		len(cmp.Right.Sources) != 1 || cmp.Right.Sources[0].Source != "JAV Database" || len(cmp.Left.Playback) != 1 || cmp.Left.EditionType != "4k" {
		t.Fatalf("compare %+v", cmp)
	}

	res, err := s.LinkCandidateVersions(ctx, c1, "editor", "4K 版")
	must(err)
	if res.Status != "versions" || res.ReviewEventID == 0 {
		t.Fatalf("link %+v", res)
	}
	var status, action string
	must(s.QueryRow(ctx, `SELECT status FROM identity_candidates WHERE id=$1`, c1).Scan(&status))
	must(s.QueryRow(ctx, `SELECT action FROM review_events WHERE id=$1`, res.ReviewEventID).Scan(&action))
	var nd int
	must(s.QueryRow(ctx, `SELECT count(*) FROM not_duplicate_pairs WHERE entity_type='release' AND a_id=$1 AND b_id=$2`, orig, uhd).Scan(&nd))
	if status != "versions" || action != "link_versions" || nd != 1 {
		t.Errorf("status %q action %q not_dup %d", status, action, nd)
	}
	if _, err := s.LinkCandidateVersions(ctx, c1, "editor", ""); err == nil {
		t.Error("deciding a closed candidate twice must fail")
	}

	// A chain orig–4k–draft: the page lists published versions only.
	c2 := cand(uhd, draft)
	_, err = s.LinkCandidateVersions(ctx, c2, "editor", "")
	must(err)
	d, err := s.ReleaseDetailBySlug(ctx, "rv-orig")
	must(err)
	if len(d.OtherVersions) != 1 || d.OtherVersions[0].ID != uhd || d.OtherVersions[0].Code != "ABC-001" || d.OtherVersions[0].EditionType != "4k" {
		t.Fatalf("other versions %+v", d.OtherVersions)
	}
	all, err := s.ReleaseVersions(ctx, orig, false)
	must(err)
	if len(all) != 2 {
		t.Errorf("admin versions %+v", all)
	}

	// A merged release stands for its target: merge 4k into a new release.
	dup := release("rv-4k-dup", "published", "4k")
	c3 := cand(dup, uhd)
	_, err = s.MergeCandidate(ctx, c3, dup, "editor", "")
	must(err)
	d, err = s.ReleaseDetailBySlug(ctx, "rv-orig")
	must(err)
	if len(d.OtherVersions) != 1 || d.OtherVersions[0].ID != dup {
		t.Errorf("after merge %+v", d.OtherVersions)
	}

	// Performer candidates cannot be linked as versions.
	var p2, pc int64
	must(s.QueryRow(ctx, `INSERT INTO performers(slug,canonical_name,status) VALUES('rv-p2','Perf','active') RETURNING id`).Scan(&p2))
	must(s.QueryRow(ctx, `INSERT INTO identity_candidates(entity_type,left_id,right_id,score,evidence) VALUES('performer',$1,$2,0.9,'{}') RETURNING id`, perf, p2).Scan(&pc))
	if _, err := s.LinkCandidateVersions(ctx, pc, "editor", ""); err == nil {
		t.Error("performer versions accepted")
	}
	pcmp, err := s.CandidateCompare(ctx, pc)
	must(err)
	if pcmp.Left.Works == nil || *pcmp.Left.Works != 1 {
		t.Errorf("performer summary %+v", pcmp.Left)
	}

	// Unlink the direct pair; a missing pair is not found.
	ev, err := s.UnlinkReleaseVersion(ctx, uhd, orig, "editor", "错误关联")
	must(err)
	must(s.QueryRow(ctx, `SELECT action FROM review_events WHERE id=$1`, ev).Scan(&action))
	if action != "unlink_versions" {
		t.Errorf("unlink action %q", action)
	}
	if _, err := s.UnlinkReleaseVersion(ctx, uhd, orig, "editor", ""); err == nil {
		t.Error("unlinking twice must fail")
	}
	if _, err := s.UnlinkReleaseVersion(ctx, orig, orig, "editor", ""); err == nil {
		t.Error("self link")
	}
	d, err = s.ReleaseDetailBySlug(ctx, "rv-orig")
	must(err)
	if len(d.OtherVersions) != 0 {
		t.Errorf("after unlink %+v", d.OtherVersions)
	}
}
