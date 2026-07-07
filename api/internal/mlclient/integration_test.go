//go:build integration

// Run against a live ml worker: cd ml && .venv/Scripts/uvicorn app:app --port 8000
// then: go test -tags integration ./internal/mlclient/...
package mlclient

import (
	"context"
	"os"
	"testing"
)

func TestLiveServer_ParseAndEmbed(t *testing.T) {
	baseURL := os.Getenv("ML_WORKER_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8000"
	}
	c := New(baseURL)
	ctx := context.Background()

	result, err := c.Parse(ctx, "../../../corpus_archive/texts/sentencia_98_2023.txt")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if result.Text == "" {
		t.Fatal("Parse returned empty text")
	}
	if len(result.Chunks) == 0 {
		t.Fatal("Parse returned no chunks")
	}
	t.Logf("parsed %d chars into %d chunks; first chunk: %q", len(result.Text), len(result.Chunks), result.Chunks[0].Text[:min(60, len(result.Chunks[0].Text))])

	texts := make([]string, len(result.Chunks))
	for i, c := range result.Chunks {
		texts[i] = c.Text
	}
	vectors, err := c.Embed(ctx, texts, "passage")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != len(texts) {
		t.Fatalf("expected %d vectors, got %d", len(texts), len(vectors))
	}
	if len(vectors[0]) != 1024 {
		t.Fatalf("expected 1024-dim vectors, got %d", len(vectors[0]))
	}
}
