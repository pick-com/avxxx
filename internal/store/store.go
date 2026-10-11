package store

import (
	"avxxx/internal/model"
	"avxxx/migrations"
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
)

type Store struct{ *pgxpool.Pool }

func Open(ctx context.Context) (*Store, error) {
	p, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return nil, e
	}
	s := &Store{p}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	tx, e := p.Begin(ctx)
	if e != nil {
		p.Close()
		return nil, e
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(842010)")
	if e == nil {
		_, e = tx.Exec(ctx, migrations.Schema)
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	if e != nil {
		p.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Sources(ctx context.Context) ([]model.Source, error) {
	r, e := s.Query(ctx, "SELECT id,name,url,enabled FROM sources ORDER BY id")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Source{}
	for r.Next() {
		var x model.Source
		if e = r.Scan(&x.ID, &x.Name, &x.URL, &x.Enabled); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}
func (s *Store) AddSource(ctx context.Context, x model.Source) error {
	_, e := s.Exec(ctx, "INSERT INTO sources(name,url) VALUES($1,$2) ON CONFLICT(url) DO UPDATE SET name=excluded.name", x.Name, x.URL)
	return e
}
func (s *Store) Enqueue(ctx context.Context, id int64) error {
	_, e := s.Exec(ctx, "INSERT INTO jobs(source_id) SELECT id FROM sources WHERE id=$1 AND enabled ON CONFLICT DO NOTHING", id)
	return e
}
func (s *Store) Items(ctx context.Context, q string) ([]model.Item, error) {
	r, e := s.Query(ctx, "SELECT id,source_id,title,url,summary,updated_at FROM items WHERE title ILIKE '%'||$1||'%' ORDER BY updated_at DESC LIMIT 100", q)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Item{}
	for r.Next() {
		var x model.Item
		if e = r.Scan(&x.ID, &x.SourceID, &x.Title, &x.URL, &x.Summary, &x.UpdatedAt); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}
func (s *Store) Jobs(ctx context.Context) ([]model.Job, error) {
	r, e := s.Query(ctx, "SELECT id,source_id,status,error,created_at FROM jobs ORDER BY id DESC LIMIT 100")
	if e != nil {
		return nil, e
	}
	defer r.Close()
	out := []model.Job{}
	for r.Next() {
		var x model.Job
		if e = r.Scan(&x.ID, &x.SourceID, &x.Status, &x.Error, &x.CreatedAt); e != nil {
			return nil, e
		}
		out = append(out, x)
	}
	return out, r.Err()
}
func (s *Store) Claim(ctx context.Context) (int64, model.Source, error) {
	var id int64
	var x model.Source
	e := s.QueryRow(ctx, `WITH picked AS (SELECT id FROM jobs WHERE status='pending' ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1), claimed AS (UPDATE jobs SET status='running',started_at=now() WHERE id IN (SELECT id FROM picked) RETURNING id,source_id) SELECT c.id,s.id,s.name,s.url,s.enabled FROM claimed c JOIN sources s ON s.id=c.source_id`).Scan(&id, &x.ID, &x.Name, &x.URL, &x.Enabled)
	return id, x, e
}
func (s *Store) Finish(ctx context.Context, id int64, items []model.Item, cause error) error {
	if cause != nil {
		_, e := s.Exec(ctx, "UPDATE jobs SET status='failed',error=$2,finished_at=now() WHERE id=$1", id, cause.Error())
		return e
	}
	tx, e := s.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	for _, x := range items {
		_, e = tx.Exec(ctx, "INSERT INTO items(source_id,title,url,summary) VALUES($1,$2,$3,$4) ON CONFLICT(url) DO UPDATE SET title=excluded.title,summary=excluded.summary,updated_at=now()", x.SourceID, x.Title, x.URL, x.Summary)
		if e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, "UPDATE jobs SET status='succeeded',finished_at=now() WHERE id=$1", id)
	if e != nil {
		return e
	}
	return tx.Commit(ctx)
}
