package adapters

import (
	"path/filepath"
	"testing"

	"dictum/api/internal/importer"
)

func TestLoadFolderCSV_JoinsTagsAndIncludesUntagged(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "hechos de a")
	writeFile(t, filepath.Join(dir, "b.txt"), "hechos de b")
	writeFile(t, filepath.Join(dir, "notes.md"), "should be ignored, not .txt")

	csvPath := filepath.Join(dir, "tags.csv")
	writeFile(t, csvPath, "filename,case_type,outcome,revert_reason,court,date\n"+
		"a.txt,despido injustificado,upheld,,Junta Local,2024-01-15\n")

	rulings, err := LoadFolderCSV(dir, csvPath)
	if err != nil {
		t.Fatalf("LoadFolderCSV: %v", err)
	}
	if len(rulings) != 2 {
		t.Fatalf("expected 2 rulings (a.txt, b.txt; notes.md excluded), got %d", len(rulings))
	}

	byID := map[string]importer.Ruling{}
	for _, r := range rulings {
		byID[r.ExternalID] = r
	}

	a, ok := byID["a.txt"]
	if !ok {
		t.Fatal("missing ruling for a.txt")
	}
	if a.CaseType != "despido injustificado" || a.Outcome != "upheld" || a.Court != "Junta Local" {
		t.Fatalf("a.txt tags not applied correctly: %+v", a)
	}

	b, ok := byID["b.txt"]
	if !ok {
		t.Fatal("missing ruling for b.txt")
	}
	if b.CaseType != "" || b.Outcome != "" {
		t.Fatalf("b.txt should be untagged (no CSV row), got %+v", b)
	}
}

func TestLoadFolderCSV_MissingFilenameColumn(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "tags.csv")
	writeFile(t, csvPath, "case_type,outcome\nx,y\n")

	if _, err := LoadFolderCSV(dir, csvPath); err == nil {
		t.Fatal("expected error for CSV missing filename column")
	}
}

func TestLoadFolderCSV_MissingCSVFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadFolderCSV(dir, filepath.Join(dir, "does_not_exist.csv")); err == nil {
		t.Fatal("expected error for missing CSV file")
	}
}
