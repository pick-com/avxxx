package audit

import (
	"context"
	"testing"
)

func TestCheckPerformerName(t *testing.T) {
	cases := []struct {
		name string
		p    PerformerNameFacts
		want []string // field:reason
	}{
		{"real name", PerformerNameFacts{Name: "Sena Hashimoto", NameJA: "橋本セナ", PageTitles: []string{"Sena Hashimoto - JAV Database"}}, nil},
		{"bare page title equal to name", PerformerNameFacts{Name: "山田花子", PageTitles: []string{"山田花子"}}, nil},
		{"site title saved as person", PerformerNameFacts{Name: "JAV Japanese Porn Actress & Idol Database"}, []string{"canonical_name:boilerplate_words"}},
		{"url", PerformerNameFacts{Name: "www.example-idols.com"}, []string{"canonical_name:url_like"}},
		{"long tagline", PerformerNameFacts{Name: "The Best Free Collection Of Videos Updated Every Single Day"}, []string{"canonical_name:too_long"}},
		{"too many words", PerformerNameFacts{Name: "Aa Bb Cc Dd Ee Ff"}, []string{"canonical_name:too_many_words"}},
		{"site name segment", PerformerNameFacts{Name: "Idol Hub", PageTitles: []string{"Someone | Idol Hub"}}, []string{"canonical_name:matches_site_name"}},
		{"shared site title", PerformerNameFacts{Name: "Idol Hub", SiteTitles: []string{"IDOL HUB"}}, []string{"canonical_name:matches_site_title"}},
		{"japanese boilerplate", PerformerNameFacts{Name: "Hanako", NameJA: "XCITY 公式サイト"}, []string{"canonical_name_ja:boilerplate_words"}},
		{"both", PerformerNameFacts{Name: "Official Site", NameJA: "女優一覧"}, []string{"canonical_name:boilerplate_words", "canonical_name_ja:boilerplate_words"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := CheckPerformerName(c.p)
			var got []string
			for _, f := range fs {
				got = append(got, f.Field+":"+f.Reason)
			}
			if len(got) != len(c.want) {
				t.Fatalf("findings = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("findings = %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestRulePerformerNameOpensIssuesAndNeverActs(t *testing.T) {
	f := newFake()
	f.names = []PerformerNameFacts{
		{ID: 1, Name: "Sena Hashimoto"},
		{ID: 2, Name: "JAV Japanese Porn Actress & Idol Database", Releases: 3},
		{ID: 3, Name: "www.junk.example.com"},
	}
	r := &Runner{Store: f, Now: clock(), BatchSize: 2}
	start := r.now()
	out, err := r.rulePerformerName(context.Background(), start)
	if err != nil {
		t.Fatal(err)
	}
	if out.checked != 3 || out.issues != 2 {
		t.Fatalf("outcome %+v", out)
	}
	open := f.open(RulePerformerName)
	if len(open) != 2 || open[0].EntityID != 2 || open[0].Severity != SeverityHigh || open[1].Severity != SeverityMedium {
		t.Fatalf("issues %+v", open)
	}
	if open[0].Detail["auto_action"] != "none" {
		t.Errorf("detail %+v", open[0].Detail)
	}
	if len(f.events) != 0 {
		t.Fatalf("rule changed data: %+v", f.events)
	}

	// The admin hid performer 2 (no longer listed) and dismissed 3: the next
	// run resolves 2 and does not reopen 3.
	open[1].Status = StatusDismissed
	f.names = f.names[:1]
	f.names = append(f.names, PerformerNameFacts{ID: 3, Name: "www.junk.example.com"})
	out, err = r.rulePerformerName(context.Background(), r.now())
	if err != nil {
		t.Fatal(err)
	}
	if out.issues != 0 || out.fixed != 1 || len(f.open(RulePerformerName)) != 0 {
		t.Fatalf("second run %+v, open %d", out, len(f.open(RulePerformerName)))
	}
}

func TestPerformerNameIsAnAuditRule(t *testing.T) {
	if !KnownRule(RulePerformerName) {
		t.Fatal("R-PERFORMER-NAME not in AllRules")
	}
	if AllRules[0] != RuleAge {
		t.Fatal("R-AGE must run first")
	}
}
