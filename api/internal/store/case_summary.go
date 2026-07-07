package store

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// CaseChunkText concatenates all chunk text for a case's documents, ordered
// by document then chunk insertion order, as a stand-in case summary for
// UC2/UC3 retrieval calls. A real summarization step (LLM package) may
// replace this later; for now the full text is what /similar and
// /classify-knn embed as the query.
func (s *Store) CaseChunkText(ctx context.Context, caseID uuid.UUID) (string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.text
		FROM chunks c
		JOIN documents d ON d.id = c.document_id
		WHERE d.case_id = $1
		ORDER BY d.created_at, c.created_at
	`, caseID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var texts []string
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			return "", err
		}
		texts = append(texts, text)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return strings.Join(texts, "\n\n"), nil
}
