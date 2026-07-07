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
	chunkText string
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

func (f *fakeCaseStore) CaseChunkText(ctx context.Context, caseID uuid.UUID) (string, error) {
	return f.chunkText, nil
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

type fakeRetrieval struct {
	similarResults []mlclient.SimilarResult
	similarErr     error
	classifyResult mlclient.ClassifyKNNResult
	classifyErr    error

	lastSummary string
}

func (f *fakeRetrieval) Similar(ctx context.Context, caseSummary string, k int, filters mlclient.SimilarFilters) ([]mlclient.SimilarResult, error) {
	f.lastSummary = caseSummary
	return f.similarResults, f.similarErr
}

func (f *fakeRetrieval) ClassifyKNN(ctx context.Context, caseSummary string, k int) (mlclient.ClassifyKNNResult, error) {
	f.lastSummary = caseSummary
	return f.classifyResult, f.classifyErr
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

func TestHandleSimilarRulings(t *testing.T) {
	cs := newFakeCaseStore()
	cs.chunkText = "hechos del caso: despido sin justificacion"
	ml := &fakeRetrieval{similarResults: []mlclient.SimilarResult{
		{RulingID: "r1", ExternalID: "sentencia_1.txt", Outcome: "upheld", FusedScore: 0.03},
	}}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	caseID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/cases/"+caseID.String()+"/similar-rulings?case_type=despido+injustificado", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ml.lastSummary != cs.chunkText {
		t.Fatalf("expected case summary %q passed to ML client, got %q", cs.chunkText, ml.lastSummary)
	}

	var results []mlclient.SimilarResult
	if err := json.Unmarshal(rec.Body.Bytes(), &results); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(results) != 1 || results[0].ExternalID != "sentencia_1.txt" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestHandleSimilarRulings_NoDocumentsYet(t *testing.T) {
	cs := newFakeCaseStore() // chunkText left empty: no parsed documents
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/cases/"+uuid.New().String()+"/similar-rulings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for case with no parsed text, got %d", rec.Code)
	}
}

func TestHandleClassification(t *testing.T) {
	cs := newFakeCaseStore()
	cs.chunkText = "hechos del caso: pago de utilidades no cubierto"
	caseType := "pago de utilidades"
	ml := &fakeRetrieval{classifyResult: mlclient.ClassifyKNNResult{
		CaseType:   &caseType,
		Confidence: 0.8,
		Evidence: []mlclient.ClassifyEvidence{
			{RulingID: "r2", ExternalID: "sentencia_2.txt", Similarity: 0.9},
		},
	}}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	caseID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/cases/"+caseID.String()+"/classification", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result mlclient.ClassifyKNNResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.CaseType == nil || *result.CaseType != caseType {
		t.Fatalf("unexpected case_type: %+v", result.CaseType)
	}
	if result.Confidence != 0.8 {
		t.Fatalf("unexpected confidence: %v", result.Confidence)
	}
}
