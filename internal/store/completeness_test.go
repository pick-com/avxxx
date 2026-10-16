package store

import (
	"avxxx/internal/completeness"
	"strings"
	"testing"
)

func TestCompletenessQueryCoversEveryField(t *testing.T) {
	for _, entity := range []string{completeness.EntityRelease, completeness.EntityPerformer} {
		sql, err := completenessQuery(entity)
		if err != nil {
			t.Fatalf("%s: %v", entity, err)
		}
		if strings.Contains(sql, "raw_data") {
			t.Errorf("%s facts query reads raw_data", entity)
		}
		if len(completenessSQL[entity].have) != len(completeness.Fields(entity)) {
			t.Errorf("%s: %d SQL expressions for %d fields", entity, len(completenessSQL[entity].have), len(completeness.Fields(entity)))
		}
	}
	if _, err := completenessQuery("video"); err == nil {
		t.Error("unknown entity accepted")
	}
}

// The report's required performer fields include AdminActors' 信息完整度.
func TestCompletenessRequiredMatchesAdminActors(t *testing.T) {
	required := map[string]bool{}
	for _, f := range completeness.PerformerFields {
		if f.Required {
			required[f.Name] = true
		}
	}
	for _, f := range CompletenessFields {
		if !required[f] {
			t.Errorf("CompletenessFields %q is not a required report field", f)
		}
	}
}
