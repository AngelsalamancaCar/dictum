package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"dictum/api/internal/store"
)

type fakeStore struct {
	existingHashes map[string]bool
	created        []string
}

func (f *fakeStore) DocumentExists(ctx context.Context, caseID uuid.UUID, sha256 string) (bool, error) {
	return f.existingHashes[sha256], nil
}

func (f *fakeStore) CreateDocument(ctx context.Context, caseID uuid.UUID, filename, sha256 string) (store.Document, error) {
	f.created = append(f.created, filename)
	f.existingHashes[sha256] = true
	return store.Document{CaseID: caseID, Filename: filename, SHA256: sha256}, nil
}

func TestWalkFolder_CreatesDocumentsAndSkipsDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "contenido A")
	writeFile(t, filepath.Join(dir, "b.txt"), "contenido B")
	writeFile(t, filepath.Join(dir, "a_copy.txt"), "contenido A") // duplicate content of a.txt

	fs := &fakeStore{existingHashes: map[string]bool{}}
	caseID := uuid.New()

	var enqueued []string
	err := WalkFolder(context.Background(), fs, caseID, dir, func(doc store.Document, path string) {
		enqueued = append(enqueued, doc.Filename)
	})
	if err != nil {
		t.Fatalf("WalkFolder returned error: %v", err)
	}

	if len(fs.created) != 2 {
		t.Fatalf("expected 2 documents created (dedup by hash), got %d: %v", len(fs.created), fs.created)
	}
	if len(enqueued) != 2 {
		t.Fatalf("expected 2 documents enqueued, got %d: %v", len(enqueued), enqueued)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
