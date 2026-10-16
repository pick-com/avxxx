package store

// 版权凭证 (artboard AdminVideo 发布, migration 044): private proof that a
// release may be listed — a document reference (contract number, private
// storage key) and/or a URL, with notes. Only the admin routes read
// release_rights_proofs; no public query touches it. Adding and removing
// write review_events rows (修改记录) that name the proof, not its content.

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RightsProofKinds are the allowed release_rights_proofs.kind values.
var RightsProofKinds = []string{"license", "contract", "authorization", "invoice", "other"}

// RightsProof is one release_rights_proofs row.
type RightsProof struct {
	ID         int64      `json:"id"`
	ReleaseID  int64      `json:"release_id"`
	Kind       string     `json:"kind"`
	Reference  string     `json:"reference"`
	URL        string     `json:"url"`
	Note       string     `json:"note"`
	ValidUntil *string    `json:"valid_until"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	RemovedAt  *time.Time `json:"removed_at"`
	RemovedBy  *string    `json:"removed_by"`
}

// RightsProofInput adds a proof; the API validates the URL scheme.
type RightsProofInput struct {
	Kind       string `json:"kind"`
	Reference  string `json:"reference"`
	URL        string `json:"url"`
	Note       string `json:"note"`
	ValidUntil string `json:"valid_until"`
}

const rightsProofCols = `id,release_id,kind,reference,url,note,valid_until::text,created_by,created_at,removed_at,removed_by`

func scanRightsProof(row pgx.Row) (RightsProof, error) {
	var p RightsProof
	err := row.Scan(&p.ID, &p.ReleaseID, &p.Kind, &p.Reference, &p.URL, &p.Note, &p.ValidUntil, &p.CreatedBy, &p.CreatedAt, &p.RemovedAt, &p.RemovedBy)
	return p, err
}

func (in *RightsProofInput) normalize() error {
	in.Kind = strings.TrimSpace(in.Kind)
	if in.Kind == "" {
		in.Kind = "license"
	}
	ok := false
	for _, k := range RightsProofKinds {
		ok = ok || k == in.Kind
	}
	if !ok {
		return invalid("kind must be one of %s", strings.Join(RightsProofKinds, ", "))
	}
	in.Reference, in.URL, in.Note = strings.TrimSpace(in.Reference), strings.TrimSpace(in.URL), strings.TrimSpace(in.Note)
	if in.Reference == "" && in.URL == "" {
		return invalid("reference or url is required")
	}
	if len([]rune(in.Reference)) > 500 || len([]rune(in.URL)) > 2000 || len([]rune(in.Note)) > 2000 {
		return invalid("reference is at most 500, url and note at most 2000 characters")
	}
	in.ValidUntil = strings.TrimSpace(in.ValidUntil)
	if in.ValidUntil != "" {
		if _, err := time.Parse("2006-01-02", in.ValidUntil); err != nil {
			return invalid("valid_until must be YYYY-MM-DD")
		}
	}
	return nil
}

// ReleaseRightsProofs lists a release's proofs, newest first; removed ones
// only with withRemoved. An unknown release is pgx.ErrNoRows.
func (s *Store) ReleaseRightsProofs(ctx context.Context, releaseID int64, withRemoved bool) ([]RightsProof, error) {
	var exists bool
	if err := s.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM releases WHERE id=$1)`, releaseID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, pgx.ErrNoRows
	}
	rows, err := s.Query(ctx, `SELECT `+rightsProofCols+` FROM release_rights_proofs
		WHERE release_id=$1 AND ($2 OR removed_at IS NULL) ORDER BY created_at DESC, id DESC`, releaseID, withRemoved)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RightsProof{}
	for rows.Next() {
		p, err := scanRightsProof(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddReleaseRightsProof stores a proof and a review_events row. An unknown
// release is pgx.ErrNoRows.
func (s *Store) AddReleaseRightsProof(ctx context.Context, releaseID int64, in RightsProofInput, actor string) (RightsProof, error) {
	if err := in.normalize(); err != nil {
		return RightsProof{}, err
	}
	var p RightsProof
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var id int64
		if err := tx.QueryRow(ctx, `SELECT id FROM releases WHERE id=$1 FOR UPDATE`, releaseID).Scan(&id); err != nil {
			return err
		}
		var err error
		p, err = scanRightsProof(tx.QueryRow(ctx, `INSERT INTO release_rights_proofs(release_id,kind,reference,url,note,valid_until,created_by)
			VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::date,$7) RETURNING `+rightsProofCols,
			releaseID, in.Kind, in.Reference, in.URL, in.Note, in.ValidUntil, actor))
		if err != nil {
			return err
		}
		return writeReviewEvent(ctx, tx, "release", releaseID, "add_rights_proof", nil,
			map[string]any{"proof_id": p.ID, "kind": p.Kind}, "", actor)
	})
	return p, err
}

// RemoveReleaseRightsProof marks a proof removed (the row stays) and writes
// a review_events row. Unknown proof: pgx.ErrNoRows; already removed:
// *ConflictError.
func (s *Store) RemoveReleaseRightsProof(ctx context.Context, releaseID, proofID int64, reason, actor string) (RightsProof, error) {
	var p RightsProof
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var err error
		p, err = scanRightsProof(tx.QueryRow(ctx, `UPDATE release_rights_proofs SET removed_at=now(),removed_by=$3
			WHERE id=$1 AND release_id=$2 AND removed_at IS NULL RETURNING `+rightsProofCols, proofID, releaseID, actor))
		if errors.Is(err, pgx.ErrNoRows) {
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM release_rights_proofs WHERE id=$1 AND release_id=$2)`, proofID, releaseID).Scan(&exists); e != nil {
				return e
			}
			if exists {
				return conflict("proof %d is already removed", proofID)
			}
			return pgx.ErrNoRows
		}
		if err != nil {
			return err
		}
		return writeReviewEvent(ctx, tx, "release", releaseID, "remove_rights_proof", map[string]any{"proof_id": p.ID, "kind": p.Kind}, nil, reason, actor)
	})
	return p, err
}
