package adapters

import (
	"path/filepath"
	"testing"
)

// TestLoadLabelboxManifest_RealCorpusArchive runs against the actual
// corpus_archive/ fixture checked into the repo root — a real regression
// check that the adapter handles the real manifest shape, not just a
// synthetic one.
func TestLoadLabelboxManifest_RealCorpusArchive(t *testing.T) {
	repoRoot := filepath.Join("..", "..", "..", "..") // api/internal/importer/adapters -> repo root
	corpusDir := filepath.Join(repoRoot, "corpus_archive")
	manifestPath := filepath.Join(corpusDir, "manifest.json")

	rulings, err := LoadLabelboxManifest(manifestPath, corpusDir, []string{"prompt_"})
	if err != nil {
		t.Fatalf("LoadLabelboxManifest: %v", err)
	}

	// 115 manifest rows - 1 prompt_* experiment file - 3 duplicate rows
	// (same file cataloged under two Labelbox datasets) = 111 unique
	// real sentencias.
	if len(rulings) != 111 {
		t.Fatalf("expected 111 real sentencias, got %d", len(rulings))
	}

	seen := map[string]bool{}
	for _, r := range rulings {
		if seen[r.ExternalID] {
			t.Fatalf("duplicate external_id in adapter output: %s", r.ExternalID)
		}
		seen[r.ExternalID] = true

		if r.Text == "" {
			t.Fatalf("ruling %s has empty text", r.ExternalID)
		}
		if r.CaseType != "" || r.Outcome != "" {
			t.Fatalf("ruling %s unexpectedly has tags (source export has none): case_type=%q outcome=%q", r.ExternalID, r.CaseType, r.Outcome)
		}
	}
}
