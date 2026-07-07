// Package ingest walks a user-supplied case folder, dedupes files by
// content hash, and registers them as documents for parsing (UC1).
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	"dictum/api/internal/store"
)

// DocumentStore is the subset of *store.Store the walker needs, so callers
// can fake it in tests.
type DocumentStore interface {
	DocumentExists(ctx context.Context, caseID uuid.UUID, sha256 string) (bool, error)
	CreateDocument(ctx context.Context, caseID uuid.UUID, filename, sha256 string) (store.Document, error)
}

// WalkFolder walks root, registering each regular file as a document on
// caseID. Files whose content hash already exists for this case are
// skipped. onNewDocument is called (with the absolute file path) for every
// document actually created, so the caller can enqueue parse work.
func WalkFolder(
	ctx context.Context,
	s DocumentStore,
	caseID uuid.UUID,
	root string,
	onNewDocument func(doc store.Document, path string),
) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		hash, err := hashFile(path)
		if err != nil {
			return err
		}

		exists, err := s.DocumentExists(ctx, caseID, hash)
		if err != nil {
			return err
		}
		if exists {
			return nil
		}

		doc, err := s.CreateDocument(ctx, caseID, filepath.Base(path), hash)
		if err != nil {
			return err
		}
		onNewDocument(doc, path)
		return nil
	})
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
