package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

type fakeML struct {
	parseResult mlclient.ParseResult
	parseErr    error
	embeddings  [][]float32
	embedErr    error
}

func (f *fakeML) Parse(ctx context.Context, filePath string) (mlclient.ParseResult, error) {
	return f.parseResult, f.parseErr
}

func (f *fakeML) Embed(ctx context.Context, texts []string, kind string) ([][]float32, error) {
	return f.embeddings, f.embedErr
}

type fakeStore struct {
	statusUpdates []string
	insertedCount int
}

func (f *fakeStore) UpdateDocumentParseStatus(ctx context.Context, documentID uuid.UUID, status string, objectRef *string) error {
	f.statusUpdates = append(f.statusUpdates, status)
	return nil
}

func (f *fakeStore) InsertChunks(ctx context.Context, documentID uuid.UUID, chunks []store.ChunkInput) error {
	f.insertedCount = len(chunks)
	return nil
}

func TestQueue_ProcessSuccess(t *testing.T) {
	ml := &fakeML{
		parseResult: mlclient.ParseResult{
			Text:   "texto",
			Pages:  []byte(`[{"page_num":1}]`),
			Chunks: []mlclient.Chunk{{Text: "chunk uno"}, {Text: "chunk dos"}},
		},
		embeddings: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
	}
	fs := &fakeStore{}
	q := NewQueue(ml, fs, 2)

	caseID := uuid.New()
	doc := store.Document{ID: uuid.New(), CaseID: caseID, Filename: "a.pdf"}

	events, cancel := q.Subscribe(caseID)
	defer cancel()

	q.process(context.Background(), doc, "/fake/path/a.pdf")

	var got []Event
	timeout := time.After(time.Second)
	for len(got) < 3 { // parsing, embedding, done
		select {
		case ev := <-events:
			got = append(got, ev)
		case <-timeout:
			t.Fatalf("timed out waiting for events, got %d: %+v", len(got), got)
		}
	}

	if got[len(got)-1].Status != "done" {
		t.Fatalf("expected final event status 'done', got %q", got[len(got)-1].Status)
	}
	if fs.insertedCount != 2 {
		t.Fatalf("expected 2 chunks inserted, got %d", fs.insertedCount)
	}
	if fs.statusUpdates[len(fs.statusUpdates)-1] != "done" {
		t.Fatalf("expected final store status 'done', got %v", fs.statusUpdates)
	}
}

func TestQueue_ProcessParseFailure(t *testing.T) {
	ml := &fakeML{parseErr: context.DeadlineExceeded}
	fs := &fakeStore{}
	q := NewQueue(ml, fs, 2)

	caseID := uuid.New()
	doc := store.Document{ID: uuid.New(), CaseID: caseID, Filename: "bad.pdf"}

	events, cancel := q.Subscribe(caseID)
	defer cancel()

	q.process(context.Background(), doc, "/fake/path/bad.pdf")

	timeout := time.After(time.Second)
	var last Event
	for range 2 { // parsing, failed
		select {
		case ev := <-events:
			last = ev
		case <-timeout:
			t.Fatalf("timed out waiting for failure event")
		}
	}
	if last.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", last.Status)
	}
	if fs.statusUpdates[len(fs.statusUpdates)-1] != "failed" {
		t.Fatalf("expected store status 'failed', got %v", fs.statusUpdates)
	}
}
