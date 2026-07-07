package router

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"dictum/api/internal/jobs"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

type fakeCaseStore struct {
	mu        sync.Mutex
	documents map[string]bool // sha256 -> exists
}

func newFakeCaseStore() *fakeCaseStore {
	return &fakeCaseStore{documents: map[string]bool{}}
}

func (f *fakeCaseStore) CreateCase(ctx context.Context, name string) (store.Case, error) {
	return store.Case{ID: uuid.New(), Name: name, Status: "intake"}, nil
}

func (f *fakeCaseStore) DocumentExists(ctx context.Context, caseID uuid.UUID, sha256 string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.documents[sha256], nil
}

func (f *fakeCaseStore) CreateDocument(ctx context.Context, caseID uuid.UUID, filename, sha256 string) (store.Document, error) {
	f.mu.Lock()
	f.documents[sha256] = true
	f.mu.Unlock()
	return store.Document{ID: uuid.New(), CaseID: caseID, Filename: filename, SHA256: sha256}, nil
}

func (f *fakeCaseStore) UpdateDocumentParseStatus(ctx context.Context, documentID uuid.UUID, status string, objectRef *string) error {
	return nil
}

func (f *fakeCaseStore) InsertChunks(ctx context.Context, documentID uuid.UUID, chunks []store.ChunkInput) error {
	return nil
}

// fakeML gates Parse on a channel so tests can subscribe to job events
// before the pipeline races ahead of them.
type fakeML struct {
	proceed <-chan struct{}
}

func (f fakeML) Parse(ctx context.Context, filePath string) (mlclient.ParseResult, error) {
	if f.proceed != nil {
		<-f.proceed
	}
	return mlclient.ParseResult{Text: "texto", Chunks: []mlclient.Chunk{{Text: "un chunk"}}}, nil
}

func (fakeML) Embed(ctx context.Context, texts []string, kind string) ([][]float32, error) {
	return [][]float32{{0.1, 0.2}}, nil
}

func TestHandleCreateCase_ValidationErrors(t *testing.T) {
	cs := newFakeCaseStore()
	deps := Deps{Store: cs, Jobs: jobs.NewQueue(fakeML{}, cs, 2)}
	mux := New(deps)

	req := httptest.NewRequest(http.MethodPost, "/api/cases", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing fields, got %d", rec.Code)
	}
}

func TestHandleCreateCase_TriggersIngestAndEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc.txt"), []byte("contenido del caso"), 0o644); err != nil {
		t.Fatal(err)
	}

	proceed := make(chan struct{})
	cs := newFakeCaseStore()
	deps := Deps{Store: cs, Jobs: jobs.NewQueue(fakeML{proceed: proceed}, cs, 2)}
	mux := New(deps)

	body, _ := json.Marshal(createCaseRequest{Name: "Caso 1", FolderPath: dir})
	req := httptest.NewRequest(http.MethodPost, "/api/cases", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var c store.Case
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	// Subscribe while the pipeline is still blocked in Parse, then release
	// it, so we're guaranteed not to miss the "done" event.
	events, cancel := deps.Jobs.Subscribe(c.ID)
	defer cancel()
	close(proceed)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Status == "done" {
				return
			}
			if ev.Status == "failed" {
				t.Fatalf("pipeline failed: %s", ev.Error)
			}
		case <-deadline:
			t.Fatal("timed out waiting for ingest pipeline to complete")
		}
	}
}

func TestHandleCaseEvents_StreamsSSEFormat(t *testing.T) {
	cs := newFakeCaseStore()
	q := jobs.NewQueue(fakeML{}, cs, 2)
	deps := Deps{Store: cs, Jobs: q}
	srv := httptest.NewServer(New(deps))
	defer srv.Close()

	caseID := uuid.New()

	httpReq, err := http.NewRequest(http.MethodGet, srv.URL+"/api/cases/"+caseID.String()+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	// Give the server a moment to register the subscription before we
	// publish, then fire an event through the real queue.
	time.Sleep(50 * time.Millisecond)
	q.Submit(store.Document{ID: uuid.New(), CaseID: caseID, Filename: "x.txt"}, "/irrelevant")

	buf := make([]byte, 4096)
	n, err := resp.Body.Read(buf)
	if err != nil && n == 0 {
		t.Fatalf("reading SSE stream: %v", err)
	}
	got := string(buf[:n])
	if !strings.HasPrefix(got, "data: ") || !strings.Contains(got, `"status":"parsing"`) {
		t.Fatalf("unexpected SSE payload: %q", got)
	}
}
