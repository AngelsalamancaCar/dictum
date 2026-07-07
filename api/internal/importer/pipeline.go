package importer

import (
	"context"
	"fmt"

	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

// Embedder is the subset of *mlclient.Client the pipeline needs.
type Embedder interface {
	Chunk(ctx context.Context, text string) ([]mlclient.Chunk, error)
	Embed(ctx context.Context, texts []string, kind string) ([][]float32, error)
}

// RulingSink is the subset of *store.Store the pipeline needs.
type RulingSink interface {
	UpsertRuling(ctx context.Context, r store.RulingInput) error
}

// EmbedRuling chunks text, embeds each chunk as a passage, and mean-pools
// the results into a single vector. rulings.embedding is one column per
// ruling, but sentencias regularly exceed a single embedding call's
// effective context — mean-pooling chunk embeddings is the standard
// technique for representing a long document in one vector without
// truncating everything after the first ~512 words.
func EmbedRuling(ctx context.Context, ml Embedder, text string) ([]float32, error) {
	chunks, err := ml.Chunk(ctx, text)
	if err != nil {
		return nil, err
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("text produced no chunks")
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	vectors, err := ml.Embed(ctx, texts, "passage")
	if err != nil {
		return nil, err
	}
	return meanPool(vectors), nil
}

func meanPool(vectors [][]float32) []float32 {
	dim := len(vectors[0])
	sum := make([]float32, dim)
	for _, v := range vectors {
		for i, x := range v {
			sum[i] += x
		}
	}
	n := float32(len(vectors))
	for i := range sum {
		sum[i] /= n
	}
	return sum
}

// Import embeds and upserts every ruling. It does not validate — call
// Validate first and refuse to proceed on issues, matching UC6's
// validate -> dry-run -> embed -> upsert pipeline order.
func Import(ctx context.Context, ml Embedder, sink RulingSink, rulings []Ruling) error {
	for _, r := range rulings {
		r.Normalize()

		vec, err := EmbedRuling(ctx, ml, r.Text)
		if err != nil {
			return fmt.Errorf("ruling %s: embedding: %w", r.ExternalID, err)
		}

		err = sink.UpsertRuling(ctx, store.RulingInput{
			ExternalID:   r.ExternalID,
			Text:         r.Text,
			CaseType:     r.CaseType,
			Outcome:      r.Outcome,
			RevertReason: r.RevertReason,
			Court:        r.Court,
			Date:         r.Date,
			Tags:         r.Tags,
			Embedding:    vec,
		})
		if err != nil {
			return fmt.Errorf("ruling %s: upsert: %w", r.ExternalID, err)
		}
	}
	return nil
}
