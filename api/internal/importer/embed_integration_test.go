//go:build integration

// Run against a live ml worker: cd ml && .venv/Scripts/uvicorn app:app --port 8000
// then: go test -tags integration ./internal/importer/...
package importer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dictum/api/internal/mlclient"
)

func TestEmbedRuling_LiveServer_LongRealSentencia(t *testing.T) {
	baseURL := os.Getenv("ML_WORKER_URL")
	if baseURL == "" {
		baseURL = "http://127.0.0.1:8000"
	}
	ml := mlclient.New(baseURL)

	// One of the larger archived sentencias, so chunking actually produces
	// more than one chunk and mean-pooling is exercised for real.
	path := filepath.Join("..", "..", "..", "corpus_archive", "texts", "SENTENCIA 126-202.txt")
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	if len(text) < 10000 {
		t.Fatalf("fixture unexpectedly short (%d bytes); pick a longer sentencia to exercise multi-chunk pooling", len(text))
	}

	vec, err := EmbedRuling(context.Background(), ml, string(text))
	if err != nil {
		t.Fatalf("EmbedRuling: %v", err)
	}
	if len(vec) != 1024 {
		t.Fatalf("expected 1024-dim pooled vector, got %d", len(vec))
	}

	var nonZero int
	for _, x := range vec {
		if x != 0 {
			nonZero++
		}
	}
	if nonZero == 0 {
		t.Fatal("pooled vector is all zeros")
	}
}
