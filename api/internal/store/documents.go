package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Document struct {
	ID          uuid.UUID
	CaseID      uuid.UUID
	Filename    string
	SHA256      string
	ParseStatus string
	ObjectRef   *string
	CreatedAt   time.Time
}

// DocumentExists reports whether a document with this content hash already
// exists for the case, so folder ingest can skip re-processing duplicates.
func (s *Store) DocumentExists(ctx context.Context, caseID uuid.UUID, sha256 string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM documents WHERE case_id = $1 AND sha256 = $2)`,
		caseID, sha256,
	).Scan(&exists)
	return exists, err
}

func (s *Store) CreateDocument(ctx context.Context, caseID uuid.UUID, filename, sha256 string) (Document, error) {
	var d Document
	err := s.pool.QueryRow(ctx,
		`INSERT INTO documents (case_id, filename, sha256)
		 VALUES ($1, $2, $3)
		 RETURNING id, case_id, filename, sha256, parse_status, object_ref, created_at`,
		caseID, filename, sha256,
	).Scan(&d.ID, &d.CaseID, &d.Filename, &d.SHA256, &d.ParseStatus, &d.ObjectRef, &d.CreatedAt)
	return d, err
}

func (s *Store) UpdateDocumentParseStatus(ctx context.Context, documentID uuid.UUID, status string, objectRef *string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE documents SET parse_status = $2, object_ref = $3 WHERE id = $1`,
		documentID, status, objectRef,
	)
	return err
}
