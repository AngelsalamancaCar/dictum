package store

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
)

type ChunkInput struct {
	Text         string
	SectionLabel *string
	Embedding    []float32
}

func (s *Store) InsertChunks(ctx context.Context, documentID uuid.UUID, chunks []ChunkInput) error {
	batch := &pgx.Batch{}
	for _, c := range chunks {
		batch.Queue(
			`INSERT INTO chunks (document_id, text, section_label, embedding) VALUES ($1, $2, $3, $4)`,
			documentID, c.Text, c.SectionLabel, pgvector.NewVector(c.Embedding),
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	for range chunks {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}
