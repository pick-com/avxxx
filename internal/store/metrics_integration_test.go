package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestMetricsSnapshotIntegration checks the SQL behind the Prometheus
// database gauges (obs.DBSource) against a real, disposable PostgreSQL.
// Skipped unless AVXXX_TEST_DATABASE_URL is set. Assertions are deltas or
// scoped to this test's rows so it can share the database.
func TestMetricsSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("AVXXX_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AVXXX_TEST_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", dsn)
	ctx := context.Background()
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	before, err := s.MetricsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := before.Holds["R-AGE"]; !ok {
		t.Fatal("R-AGE hold counter must always be present")
	}

	sfx := fmt.Sprint(time.Now().UnixNano())
	due, stuck := "mt_due_"+sfx, "mt_stuck_"+sfx
	exec(`INSERT INTO jobs(kind,status,available_at) VALUES($1,'pending',now()-interval '10 minutes')`, due)
	exec(`INSERT INTO jobs(kind,status,started_at,attempt,worker_id,lease_until) VALUES($1,'running',now(),1,'w',now()-interval '1 minute')`, stuck)
	exec(`INSERT INTO review_events(entity_type,entity_id,action,reason,actor) VALUES('release',0,'hold','R-AGE: metrics test','audit')`)

	after, err := s.MetricsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var gotDue, gotRunning bool
	for _, j := range after.Jobs {
		gotDue = gotDue || (j.Kind == due && j.State == "due" && j.N == 1)
		gotRunning = gotRunning || (j.Kind == stuck && j.State == "running" && j.N == 1)
	}
	if !gotDue || !gotRunning {
		t.Errorf("jobs = %+v", after.Jobs)
	}
	if after.ExpiredLeases[stuck] != 1 {
		t.Errorf("expired leases = %v", after.ExpiredLeases)
	}
	if age := after.OldestDueAge[due]; age < 590 || age > 3600 {
		t.Errorf("oldest due age = %v", age)
	}
	if after.Holds["R-AGE"] != before.Holds["R-AGE"]+1 {
		t.Errorf("R-AGE holds %d -> %d", before.Holds["R-AGE"], after.Holds["R-AGE"])
	}
	exec(`DELETE FROM jobs WHERE kind IN ($1,$2)`, due, stuck)

	// Pairing over a window no other test writes to.
	from := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.EnsureEventPartition(ctx, from); err != nil {
		t.Fatal(err)
	}
	ev := func(name, play string, at time.Time) {
		exec(`INSERT INTO events(event_id,event,schema_ver,ts_client,ts_server,session_id,platform,app_ver,play_id)
			VALUES($1,$2,1,$3,$3,'s','web','1',$4)`, name+play+sfx, name, at, play+sfx)
	}
	ev("play_request", "p1", from.Add(time.Minute))
	ev("play_end", "p1", from.Add(5*time.Minute))
	ev("play_request", "p2", from.Add(2*time.Minute))
	ev("play_request", "p3", from.Add(2*time.Hour)) // outside the window
	p, err := s.PlayPairing(ctx, from, from.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if p.Requests < 2 || p.Paired < 1 || p.Requests-p.Paired < 1 {
		t.Errorf("pairing = %+v, want at least 2 requests / 1 paired", p)
	}
}
