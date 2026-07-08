package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Draft is a row from the drafts table — UC4's generated proposed ruling,
// with provenance back to the package that produced it (plan.md §3).
type Draft struct {
	ID             uuid.UUID
	CaseID         uuid.UUID
	PackageID      *uuid.UUID
	GeneratedText  string
	CitedRulingIDs []uuid.UUID
	PromptVersion  *int
	CreatedAt      time.Time
}

type DraftInput struct {
	CaseID         uuid.UUID
	PackageID      *uuid.UUID
	GeneratedText  string
	CitedRulingIDs []uuid.UUID
	PromptVersion  int
}

func (s *Store) CreateDraft(ctx context.Context, in DraftInput) (Draft, error) {
	var d Draft
	err := s.pool.QueryRow(ctx, `
		INSERT INTO drafts (case_id, package_id, generated_text, cited_ruling_ids, prompt_version)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, case_id, package_id, generated_text, cited_ruling_ids, prompt_version, created_at
	`, in.CaseID, in.PackageID, in.GeneratedText, in.CitedRulingIDs, in.PromptVersion,
	).Scan(&d.ID, &d.CaseID, &d.PackageID, &d.GeneratedText, &d.CitedRulingIDs, &d.PromptVersion, &d.CreatedAt)
	return d, err
}

// ListDraftsByCase returns a case's drafts newest first, for the package
// management UI's draft view.
func (s *Store) ListDraftsByCase(ctx context.Context, caseID uuid.UUID) ([]Draft, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, case_id, package_id, generated_text, cited_ruling_ids, prompt_version, created_at
		FROM drafts
		WHERE case_id = $1
		ORDER BY created_at DESC
	`, caseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Draft{}
	for rows.Next() {
		var d Draft
		if err := rows.Scan(&d.ID, &d.CaseID, &d.PackageID, &d.GeneratedText, &d.CitedRulingIDs, &d.PromptVersion, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
