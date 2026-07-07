package importer

import (
	"context"
	"testing"

	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

type fakeEmbedder struct {
	chunks     []mlclient.Chunk
	embeddings [][]float32
}

func (f fakeEmbedder) Chunk(ctx context.Context, text string) ([]mlclient.Chunk, error) {
	return f.chunks, nil
}

func (f fakeEmbedder) Embed(ctx context.Context, texts []string, kind string) ([][]float32, error) {
	return f.embeddings, nil
}

func TestEmbedRuling_MeanPoolsChunkVectors(t *testing.T) {
	ml := fakeEmbedder{
		chunks:     []mlclient.Chunk{{Text: "a"}, {Text: "b"}},
		embeddings: [][]float32{{1, 2, 3}, {3, 4, 5}},
	}

	vec, err := EmbedRuling(context.Background(), ml, "irrelevant, chunking is faked")
	if err != nil {
		t.Fatalf("EmbedRuling: %v", err)
	}

	want := []float32{2, 3, 4}
	if len(vec) != len(want) {
		t.Fatalf("expected dim %d, got %d", len(want), len(vec))
	}
	for i := range want {
		if vec[i] != want[i] {
			t.Fatalf("mean pool mismatch at %d: want %v got %v", i, want, vec)
		}
	}
}

type fakeSink struct {
	upserted []store.RulingInput
}

func (f *fakeSink) UpsertRuling(ctx context.Context, r store.RulingInput) error {
	f.upserted = append(f.upserted, r)
	return nil
}

func TestImport_NormalizesAndUpserts(t *testing.T) {
	ml := fakeEmbedder{
		chunks:     []mlclient.Chunk{{Text: "a"}},
		embeddings: [][]float32{{1, 1}},
	}
	sink := &fakeSink{}

	rulings := []Ruling{
		{ExternalID: "r1", Text: "texto uno"}, // no outcome -> should default to pending
	}

	if err := Import(context.Background(), ml, sink, rulings); err != nil {
		t.Fatalf("Import: %v", err)
	}

	if len(sink.upserted) != 1 {
		t.Fatalf("expected 1 upsert, got %d", len(sink.upserted))
	}
	if sink.upserted[0].Outcome != OutcomePending {
		t.Fatalf("expected outcome normalized to pending, got %q", sink.upserted[0].Outcome)
	}
	if len(sink.upserted[0].Embedding) != 2 {
		t.Fatalf("expected embedding to be set, got %v", sink.upserted[0].Embedding)
	}
}
