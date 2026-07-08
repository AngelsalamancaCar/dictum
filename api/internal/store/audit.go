package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// AuditEntry is a row from audit_log (plan.md §3: "every generation and
// override recorded").
type AuditEntry struct {
	ID        uuid.UUID
	Actor     string
	Action    string
	Entity    string
	EntityID  *uuid.UUID
	Metadata  json.RawMessage
	CreatedAt time.Time
}

type AuditInput struct {
	Actor    string
	Action   string
	Entity   string
	EntityID *uuid.UUID
	Metadata json.RawMessage // nil stores as {}
}

func (s *Store) InsertAudit(ctx context.Context, in AuditInput) (AuditEntry, error) {
	metadata := in.Metadata
	if metadata == nil {
		metadata = json.RawMessage(`{}`)
	}
	var e AuditEntry
	err := s.pool.QueryRow(ctx, `
		INSERT INTO audit_log (actor, action, entity, entity_id, metadata)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, actor, action, entity, entity_id, metadata, created_at
	`, in.Actor, in.Action, in.Entity, in.EntityID, []byte(metadata),
	).Scan(&e.ID, &e.Actor, &e.Action, &e.Entity, &e.EntityID, &e.Metadata, &e.CreatedAt)
	return e, err
}

// AuditFilter narrows ListAuditLog; zero values mean "no filter", matching
// the PackageFilter convention.
type AuditFilter struct {
	Actor    string
	Entity   string
	EntityID *uuid.UUID
	Limit    int
}

func (s *Store) ListAuditLog(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, actor, action, entity, entity_id, metadata, created_at
		FROM audit_log
		WHERE ($1 = '' OR actor = $1)
		  AND ($2 = '' OR entity = $2)
		  AND ($3::uuid IS NULL OR entity_id = $3)
		ORDER BY created_at DESC
		LIMIT $4
	`, filter.Actor, filter.Entity, filter.EntityID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.Actor, &e.Action, &e.Entity, &e.EntityID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
