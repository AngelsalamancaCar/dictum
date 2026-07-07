package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeManifest(t *testing.T, dir string, entries []manifestEntry) string {
	t.Helper()
	m := manifest{Source: "test", Rows: entries}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadLabelboxManifest_ExcludesPromptFilesAndReadsText(t *testing.T) {
	dir := t.TempDir()
	textsDir := filepath.Join(dir, "texts")
	if err := os.Mkdir(textsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(textsDir, "sentencia_1.txt"), "hechos del caso 1")
	writeFile(t, filepath.Join(textsDir, "prompt_1.txt"), "not a real ruling")

	manifestPath := writeManifest(t, dir, []manifestEntry{
		{ExternalID: "sentencia_1.txt", File: "texts/sentencia_1.txt", Dataset: "prueba"},
		{ExternalID: "prompt_1.txt", File: "texts/prompt_1.txt", Dataset: "litis_samples"},
	})

	rulings, err := LoadLabelboxManifest(manifestPath, dir, []string{"prompt_"})
	if err != nil {
		t.Fatalf("LoadLabelboxManifest: %v", err)
	}

	if len(rulings) != 1 {
		t.Fatalf("expected 1 ruling after excluding prompt_*, got %d", len(rulings))
	}
	if rulings[0].ExternalID != "sentencia_1.txt" {
		t.Fatalf("unexpected external_id: %s", rulings[0].ExternalID)
	}
	if rulings[0].Text != "hechos del caso 1" {
		t.Fatalf("unexpected text: %q", rulings[0].Text)
	}
	if rulings[0].Tags["dataset"] != "prueba" {
		t.Fatalf("expected dataset tag, got %v", rulings[0].Tags)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
