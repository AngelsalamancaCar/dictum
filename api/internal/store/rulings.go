package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

type RulingInput struct {
	ExternalID   string
	Text         string
	CaseType     string
	Outcome      string
	RevertReason string
	Court        string
	Date         string // "2006-01-02", empty means null
	Tags         map[string]string
	Embedding    []float32
}

// UpsertRuling inserts or updates a ruling by external_id, matching UC6's
// "upsert by external_id" re-import semantics (plan.md §4).
func (s *Store) UpsertRuling(ctx context.Context, r RulingInput) error {
	var date *time.Time
	if r.Date != "" {
		t, err := time.Parse("2006-01-02", r.Date)
		if err != nil {
			return fmt.Errorf("ruling %s: invalid date %q: %w", r.ExternalID, r.Date, err)
		}
		date = &t
	}

	tags := r.Tags
	if tags == nil {
		tags = map[string]string{}
	}
	tagsJSON, err := json.Marshal(tags)
	if err != nil {
		return err
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO rulings (external_id, full_text, case_type, outcome, revert_reason, court, date, tags, embedding)
		VALUES ($1, $2, NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9)
		ON CONFLICT (external_id) DO UPDATE SET
			full_text     = EXCLUDED.full_text,
			case_type     = EXCLUDED.case_type,
			outcome       = EXCLUDED.outcome,
			revert_reason = EXCLUDED.revert_reason,
			court         = EXCLUDED.court,
			date          = EXCLUDED.date,
			tags          = EXCLUDED.tags,
			embedding     = EXCLUDED.embedding
	`,
		r.ExternalID, r.Text, r.CaseType, r.Outcome, r.RevertReason, r.Court, date, tagsJSON, pgvector.NewVector(r.Embedding),
	)
	return err
}

// Ruling is a row from the rulings table, fetched by id — used to pull the
// full text of UC3-retrieved exemplar rulings for UC4 draft context (the
// exemplar_rulings placeholder needs real citable text, not just metadata).
type Ruling struct {
	ID         uuid.UUID
	ExternalID string
	CaseType   *string
	Outcome    string
	Court      *string
	FullText   string
}

// GetRulingsByIDs fetches full rows for a set of ruling ids, in no
// particular order (callers that need a specific order re-sort by id).
func (s *Store) GetRulingsByIDs(ctx context.Context, ids []uuid.UUID) ([]Ruling, error) {
	if len(ids) == 0 {
		return []Ruling{}, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, external_id, case_type, outcome, court, full_text
		FROM rulings
		WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Ruling{}
	for rows.Next() {
		var r Ruling
		if err := rows.Scan(&r.ID, &r.ExternalID, &r.CaseType, &r.Outcome, &r.Court, &r.FullText); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
