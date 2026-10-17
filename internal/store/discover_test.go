package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// LeadCreditSQL must name the same lead roles as IsLeadCredit.
func TestLeadCreditSQLMatchesLeadRoles(t *testing.T) {
	sql := LeadCreditSQL("rp")
	for role := range leadRoles {
		if !strings.Contains(sql, "'"+role+"'") {
			t.Errorf("LeadCreditSQL misses role %q", role)
		}
	}
	if n := strings.Count(leadRoleNames, "'") / 2; n != len(leadRoles) {
		t.Errorf("leadRoleNames has %d roles, leadRoles %d", n, len(leadRoles))
	}
	if !strings.Contains(sql, "rp.is_primary") || !strings.Contains(sql, "lc.release_id=rp.release_id") {
		t.Errorf("sql = %s", sql)
	}
}

func TestBuildReleaseQueryRoleAndFacets(t *testing.T) {
	sql, args, _, err := buildReleaseQuery(ReleaseFilter{Performer: "ai", Role: CreditGuest, MinHeight: 1080, Subtitle: "any", AudioLang: "ja"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"WHERE p.slug=$1 AND NOT (rp.is_primary",
		"pbs.status<>'disabled' AND pbs.max_height>=$2",
		"cardinality(pbs.subtitle_langs)>0",
		"pbs.audio_langs @> ARRAY[$3::text]",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if !reflect.DeepEqual(args, []any{"ai", 1080, "ja", 21}) {
		t.Errorf("args = %#v", args)
	}
	if sql, _, _, _ = buildReleaseQuery(ReleaseFilter{Subtitle: "zh-CN"}); !strings.Contains(sql, "pbs.subtitle_langs @> ARRAY[$1::text]") {
		t.Errorf("subtitle lang sql:\n%s", sql)
	}
	if sql, _, _, _ = buildReleaseQuery(ReleaseFilter{Performer: "ai", Role: CreditLead}); !strings.Contains(sql, "WHERE p.slug=$1 AND (rp.is_primary") {
		t.Errorf("lead sql:\n%s", sql)
	}
	for _, f := range []ReleaseFilter{{Role: CreditLead}, {Performer: "ai", Role: "cameo"}} {
		if _, _, _, err := buildReleaseQuery(f); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%+v err = %v", f, err)
		}
	}
}

func TestBuildPerformerQueryStats(t *testing.T) {
	sql, args, _, err := buildPerformerQuery(PerformerFilter{Sort: "heat", Era: 2010, Tag: "drama", AudioLang: "ja", Cursor: &Cursor{Sort: "heat", Key: "1500", ID: 4}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"LEFT JOIN performer_stats ps ON ps.performer_id=p.id",
		"ps.first_work_date<$1 AND ps.latest_work_date>=$2",
		"ps.top_tags @> ARRAY[$3::text]",
		"ps.audio_langs @> ARRAY[$4::text]",
		"ps.performer_id IS NOT NULL",
		"(ps.heat_30d < $5 OR (ps.heat_30d = $5 AND p.id < $6))",
		"ORDER BY ps.heat_30d DESC,p.id DESC",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if args[0] != time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) || args[1] != time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC) || args[4] != int64(1500) {
		t.Errorf("args = %#v", args)
	}
	for sort, order := range map[string]string{
		"followers": "ORDER BY ps.followers DESC,p.id DESC",
		"rising":    "ORDER BY COALESCE(ps.first_work_date,DATE '0001-01-01') DESC,p.id DESC",
	} {
		sql, _, _, err := buildPerformerQuery(PerformerFilter{Sort: sort})
		if err != nil || !strings.Contains(sql, order) || !strings.Contains(sql, "ps.performer_id IS NOT NULL") {
			t.Errorf("%s: %v\n%s", sort, err, sql)
		}
	}
	// name / works keep listing performers without a stats row (works: in
	// a branch of its own, sorted on the live count).
	if sql, _, _, _ = buildPerformerQuery(PerformerFilter{Sort: "name"}); strings.Contains(sql, "ps.performer_id IS NOT NULL") {
		t.Errorf("name requires stats:\n%s", sql)
	}
	if sql, _, _, _ = buildPerformerQuery(PerformerFilter{Sort: "works"}); !strings.Contains(sql, "UNION ALL") || !strings.Contains(sql, "ps.performer_id IS NULL") {
		t.Errorf("works drops performers without stats:\n%s", sql)
	}
}

func TestBuildUpcomingQuery(t *testing.T) {
	now := time.Date(2026, 10, 16, 23, 0, 0, 0, time.FixedZone("x", 9*3600))
	c := &Cursor{Sort: UpcomingSort, Key: "2026-10-20", ID: 8}
	sql, args, limit, err := buildUpcomingQuery(UpcomingFilter{Performer: "ai", FollowedBy: 3, Cursor: c, Limit: 5, Now: now})
	if err != nil || limit != 5 {
		t.Fatal(err, limit)
	}
	for _, want := range []string{
		"r.status='upcoming' AND r.merged_into_id IS NULL",
		"COALESCE(r.release_date, DATE '0001-01-01') > $1::date",
		"p.slug=$2", "fo.user_id=$3",
		"(u.ukey > $4 OR (u.ukey = $4 AND u.id > $5))",
		"ORDER BY u.ukey ASC,u.id ASC LIMIT $6",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q:\n%s", want, sql)
		}
	}
	if args[0] != time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC) || args[5] != 6 {
		t.Errorf("args = %#v", args)
	}
	if _, _, _, err = buildUpcomingQuery(UpcomingFilter{Cursor: &Cursor{Sort: "newest", Key: "x", ID: 1}}); !errors.Is(err, ErrInvalidQuery) {
		t.Errorf("foreign cursor err = %v", err)
	}
}
