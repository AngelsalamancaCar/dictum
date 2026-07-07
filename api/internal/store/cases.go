package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Case struct {
	ID        uuid.UUID
	Name      string
	Status    string
	CreatedAt time.Time
}

func (s *Store) CreateCase(ctx context.Context, name string) (Case, error) {
	var c Case
	err := s.pool.QueryRow(ctx,
		`INSERT INTO cases (name) VALUES ($1) RETURNING id, name, status, created_at`,
		name,
	).Scan(&c.ID, &c.Name, &c.Status, &c.CreatedAt)
	return c, err
}
