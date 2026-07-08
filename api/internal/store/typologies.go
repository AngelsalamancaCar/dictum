package store

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// Typology is a row from the typologies table — the UC2 catalog of known
// litis categories a case can be classified against (plan.md §3/§4).
type Typology struct {
	ID                     uuid.UUID
	Name                   string
	Description            *string
	DiscriminatingFeatures json.RawMessage
	ExemplarRulingIDs      []uuid.UUID
}

// ListTypologies returns the full typology catalog, for assembling the
// classify package's {{typology_catalog}} context (plan.md §4 UC2).
func (s *Store) ListTypologies(ctx context.Context) ([]Typology, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, description, discriminating_features, exemplar_ruling_ids
		FROM typologies
		ORDER BY name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Typology{}
	for rows.Next() {
		var t Typology
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.DiscriminatingFeatures, &t.ExemplarRulingIDs); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
