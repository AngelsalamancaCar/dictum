package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"dictum/api/internal/jobs"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

var errFakeBuild = errors.New("fake build failure")

type fakeCaseStore struct {
	mu        sync.Mutex
	documents map[string]bool // sha256 -> exists
	chunkText string

	packages       map[uuid.UUID]store.Package
	packageResults map[uuid.UUID][]store.PackageResult
}

func newFakeCaseStore() *fakeCaseStore {
	return &fakeCaseStore{
		documents:      map[string]bool{},
		packages:       map[uuid.UUID]store.Package{},
		packageResults: map[uuid.UUID][]store.PackageResult{},
	}
}

func (f *fakeCaseStore) CreatePackage(ctx context.Context, in store.PackageInput) (store.Package, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := store.Package{
		ID:            uuid.New(),
		CaseID:        in.CaseID,
		UseCase:       in.UseCase,
		PromptVersion: in.PromptVersion,
		Status:        in.Status,
		Bundle:        in.Bundle,
		RetryOf:       in.RetryOf,
		CreatedAt:     time.Now(),
	}
	f.packages[p.ID] = p
	return p, nil
}

func (f *fakeCaseStore) GetPackage(ctx context.Context, id uuid.UUID) (store.Package, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.packages[id]
	if !ok {
		return store.Package{}, pgx.ErrNoRows
	}
	return p, nil
}

func (f *fakeCaseStore) ListPackages(ctx context.Context, filter store.PackageFilter) ([]store.PackageSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.PackageSummary{}
	for _, p := range f.packages {
		if filter.CaseID != nil && p.CaseID != *filter.CaseID {
			continue
		}
		if filter.UseCase != "" && p.UseCase != filter.UseCase {
			continue
		}
		if filter.Status != "" && p.Status != filter.Status {
			continue
		}
		out = append(out, store.PackageSummary{
			ID: p.ID, CaseID: p.CaseID, UseCase: p.UseCase, PromptVersion: p.PromptVersion,
			Status: p.Status, CreatedBy: p.CreatedBy, SubmittedAt: p.SubmittedAt,
			CompletedAt: p.CompletedAt, Error: p.Error, RetryOf: p.RetryOf, CreatedAt: p.CreatedAt,
		})
	}
	return out, nil
}

func (f *fakeCaseStore) setPackageStatus(id uuid.UUID, status string, mutate func(*store.Package)) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.packages[id]
	if !ok {
		return pgx.ErrNoRows
	}
	p.Status = status
	if mutate != nil {
		mutate(&p)
	}
	f.packages[id] = p
	return nil
}

func (f *fakeCaseStore) MarkPackageSubmitted(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	return f.setPackageStatus(id, "submitted", func(p *store.Package) { p.SubmittedAt = &now })
}

func (f *fakeCaseStore) MarkPackageCompleted(ctx context.Context, id uuid.UUID) error {
	now := time.Now()
	return f.setPackageStatus(id, "completed", func(p *store.Package) { p.CompletedAt = &now })
}

func (f *fakeCaseStore) MarkPackageCancelled(ctx context.Context, id uuid.UUID) error {
	return f.setPackageStatus(id, "cancelled", nil)
}

func (f *fakeCaseStore) InsertPackageResult(ctx context.Context, in store.PackageResultInput) (store.PackageResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := store.PackageResult{
		ID:               uuid.New(),
		PackageID:        in.PackageID,
		RawResponse:      in.RawResponse,
		ValidatedPayload: in.ValidatedPayload,
		ValidationStatus: in.ValidationStatus,
		ReceivedAt:       time.Now(),
	}
	f.packageResults[in.PackageID] = append(f.packageResults[in.PackageID], r)
	return r, nil
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
	buildResult    mlclient.PackageBundle
	buildErr       error

	lastSummary string
	lastUseCase string
	lastContext map[string]any
}

func (f *fakeRetrieval) Similar(ctx context.Context, caseSummary string, k int, filters mlclient.SimilarFilters) ([]mlclient.SimilarResult, error) {
	f.lastSummary = caseSummary
	return f.similarResults, f.similarErr
}

func (f *fakeRetrieval) ClassifyKNN(ctx context.Context, caseSummary string, k int) (mlclient.ClassifyKNNResult, error) {
	f.lastSummary = caseSummary
	return f.classifyResult, f.classifyErr
}

func (f *fakeRetrieval) BuildPackage(ctx context.Context, useCase string, packageContext map[string]any) (mlclient.PackageBundle, error) {
	f.lastUseCase = useCase
	f.lastContext = packageContext
	return f.buildResult, f.buildErr
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

// classifySchemaJSON is a minimal output_schema fixture (a trimmed version
// of ml/prompts/schemas/classify.output_schema.json) for exercising
// package-result validation without needing the real prompt bundle.
var classifySchemaJSON = []byte(`{
	"type": "object",
	"required": ["case_type"],
	"properties": {"case_type": {"type": "string"}},
	"additionalProperties": false
}`)

func classifyBundle(t *testing.T, context map[string]any) mlclient.PackageBundle {
	t.Helper()
	contextJSON, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return mlclient.PackageBundle{
		PackageID:     "pkg-x",
		UseCase:       "classify",
		PromptVersion: 1,
		CreatedAt:     "2026-01-01T00:00:00Z",
		Prompt:        "rendered prompt",
		Context:       contextJSON,
		OutputSchema:  classifySchemaJSON,
	}
}

func TestHandleCreatePackage_Success(t *testing.T) {
	cs := newFakeCaseStore()
	context := map[string]any{"case_summary": "x", "typology_catalog": []any{}}
	ml := &fakeRetrieval{buildResult: classifyBundle(t, context)}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	caseID := uuid.New()
	body, _ := json.Marshal(createPackageRequest{UseCase: "classify", Context: context})
	req := httptest.NewRequest(http.MethodPost, "/api/cases/"+caseID.String()+"/packages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var pkg store.Package
	if err := json.Unmarshal(rec.Body.Bytes(), &pkg); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if pkg.Status != "ready" {
		t.Fatalf("expected status ready, got %q", pkg.Status)
	}
	if pkg.CaseID != caseID || pkg.UseCase != "classify" || pkg.PromptVersion != 1 {
		t.Fatalf("unexpected package: %+v", pkg)
	}
	if ml.lastUseCase != "classify" {
		t.Fatalf("expected ML.BuildPackage called with classify, got %q", ml.lastUseCase)
	}
}

func TestHandleCreatePackage_InvalidUseCase(t *testing.T) {
	cs := newFakeCaseStore()
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	body, _ := json.Marshal(createPackageRequest{UseCase: "bogus", Context: map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/api/cases/"+uuid.New().String()+"/packages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid use_case, got %d", rec.Code)
	}
}

func TestHandleCreatePackage_BuildFailureIsBadGateway(t *testing.T) {
	cs := newFakeCaseStore()
	ml := &fakeRetrieval{buildErr: errFakeBuild}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	body, _ := json.Marshal(createPackageRequest{UseCase: "classify", Context: map[string]any{}})
	req := httptest.NewRequest(http.MethodPost, "/api/cases/"+uuid.New().String()+"/packages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", rec.Code, rec.Body.String())
	}
}

func createReadyPackage(t *testing.T, cs *fakeCaseStore, caseID uuid.UUID, context map[string]any) store.Package {
	t.Helper()
	ml := &fakeRetrieval{buildResult: classifyBundle(t, context)}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	body, _ := json.Marshal(createPackageRequest{UseCase: "classify", Context: context})
	req := httptest.NewRequest(http.MethodPost, "/api/cases/"+caseID.String()+"/packages", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 creating fixture package, got %d: %s", rec.Code, rec.Body.String())
	}

	var pkg store.Package
	if err := json.Unmarshal(rec.Body.Bytes(), &pkg); err != nil {
		t.Fatal(err)
	}
	return pkg
}

func TestHandleListPackages_FiltersByCaseAndStatus(t *testing.T) {
	cs := newFakeCaseStore()
	caseA, caseB := uuid.New(), uuid.New()
	pkgA := createReadyPackage(t, cs, caseA, map[string]any{"case_summary": "a", "typology_catalog": []any{}})
	createReadyPackage(t, cs, caseB, map[string]any{"case_summary": "b", "typology_catalog": []any{}})

	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/packages?case_id="+caseA.String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var pkgs []store.PackageSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &pkgs); err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].ID != pkgA.ID {
		t.Fatalf("expected only caseA's package, got %+v", pkgs)
	}
}

func TestHandleGetPackage_NotFound(t *testing.T) {
	cs := newFakeCaseStore()
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/packages/"+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleDownloadPackageArchive_ReturnsZip(t *testing.T) {
	cs := newFakeCaseStore()
	pkg := createReadyPackage(t, cs, uuid.New(), map[string]any{"case_summary": "resumen", "typology_catalog": []any{}})
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	req := httptest.NewRequest(http.MethodGet, "/api/packages/"+pkg.ID.String()+"/archive", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("expected application/zip, got %q", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("response body is not a valid zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"manifest.json", "prompts/classify.md", "output_schema.json", "context/case_summary.json"} {
		if !names[want] {
			t.Fatalf("expected archive to contain %q, got %v", want, names)
		}
	}
}

func TestHandlePackageLifecycle_SubmitAttachValidResultCompletes(t *testing.T) {
	cs := newFakeCaseStore()
	pkg := createReadyPackage(t, cs, uuid.New(), map[string]any{"case_summary": "x", "typology_catalog": []any{}})
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	submitReq := httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/submit", nil)
	submitRec := httptest.NewRecorder()
	mux.ServeHTTP(submitRec, submitReq)
	if submitRec.Code != http.StatusOK {
		t.Fatalf("expected 200 submitting, got %d: %s", submitRec.Code, submitRec.Body.String())
	}

	// Resubmitting a "submitted" (not ready) package must be rejected.
	reSubmitRec := httptest.NewRecorder()
	mux.ServeHTTP(reSubmitRec, httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/submit", nil))
	if reSubmitRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 double-submitting, got %d", reSubmitRec.Code)
	}

	resultBody, _ := json.Marshal(attachPackageResultRequest{RawResponse: json.RawMessage(`{"case_type":"despido injustificado"}`)})
	resultReq := httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/results", bytes.NewReader(resultBody))
	resultRec := httptest.NewRecorder()
	mux.ServeHTTP(resultRec, resultReq)
	if resultRec.Code != http.StatusOK {
		t.Fatalf("expected 200 attaching valid result, got %d: %s", resultRec.Code, resultRec.Body.String())
	}
	var attached attachPackageResultResponse
	if err := json.Unmarshal(resultRec.Body.Bytes(), &attached); err != nil {
		t.Fatal(err)
	}
	if len(attached.ValidationErrors) != 0 {
		t.Fatalf("expected no validation errors, got %v", attached.ValidationErrors)
	}
	if attached.Result.ValidationStatus != "valid" {
		t.Fatalf("expected valid validation_status, got %q", attached.Result.ValidationStatus)
	}

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/packages/"+pkg.ID.String(), nil))
	var completed store.Package
	if err := json.Unmarshal(getRec.Body.Bytes(), &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Status != "completed" {
		t.Fatalf("expected package completed after valid result, got %q", completed.Status)
	}
}

func TestHandleAttachPackageResult_InvalidKeepsSubmittedAndReportsErrors(t *testing.T) {
	cs := newFakeCaseStore()
	pkg := createReadyPackage(t, cs, uuid.New(), map[string]any{"case_summary": "x", "typology_catalog": []any{}})
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/submit", nil))

	// Missing the required "case_type" field per classifySchemaJSON.
	resultBody, _ := json.Marshal(attachPackageResultRequest{RawResponse: json.RawMessage(`{}`)})
	resultReq := httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/results", bytes.NewReader(resultBody))
	resultRec := httptest.NewRecorder()
	mux.ServeHTTP(resultRec, resultReq)
	if resultRec.Code != http.StatusOK {
		t.Fatalf("expected 200 (result recorded even though invalid), got %d: %s", resultRec.Code, resultRec.Body.String())
	}
	var attached attachPackageResultResponse
	if err := json.Unmarshal(resultRec.Body.Bytes(), &attached); err != nil {
		t.Fatal(err)
	}
	if len(attached.ValidationErrors) == 0 {
		t.Fatal("expected validation errors for missing case_type")
	}

	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, httptest.NewRequest(http.MethodGet, "/api/packages/"+pkg.ID.String(), nil))
	var still store.Package
	if err := json.Unmarshal(getRec.Body.Bytes(), &still); err != nil {
		t.Fatal(err)
	}
	if still.Status != "submitted" {
		t.Fatalf("expected package to remain submitted after invalid result, got %q", still.Status)
	}
}

func TestHandleAttachPackageResult_WrongStatusIsConflict(t *testing.T) {
	cs := newFakeCaseStore()
	pkg := createReadyPackage(t, cs, uuid.New(), map[string]any{"case_summary": "x", "typology_catalog": []any{}})
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	// pkg is still "ready", never submitted.
	resultBody, _ := json.Marshal(attachPackageResultRequest{RawResponse: json.RawMessage(`{"case_type":"x"}`)})
	req := httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/results", bytes.NewReader(resultBody))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 attaching a result to a non-submitted package, got %d", rec.Code)
	}
}

func TestHandleResubmitPackage_CreatesLinkedPackage(t *testing.T) {
	cs := newFakeCaseStore()
	context := map[string]any{"case_summary": "x", "typology_catalog": []any{}}
	pkg := createReadyPackage(t, cs, uuid.New(), context)
	ml := &fakeRetrieval{buildResult: classifyBundle(t, context)}
	deps := Deps{Store: cs, ML: ml}
	mux := New(deps)

	// Not resubmittable while still "ready".
	notYetRec := httptest.NewRecorder()
	mux.ServeHTTP(notYetRec, httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/resubmit", nil))
	if notYetRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 resubmitting a ready package, got %d", notYetRec.Code)
	}

	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/submit", nil))
	resultBody, _ := json.Marshal(attachPackageResultRequest{RawResponse: json.RawMessage(`{"case_type":"x"}`)})
	mux.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/results", bytes.NewReader(resultBody)))

	resubmitRec := httptest.NewRecorder()
	mux.ServeHTTP(resubmitRec, httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/resubmit", nil))
	if resubmitRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 resubmitting a completed package, got %d: %s", resubmitRec.Code, resubmitRec.Body.String())
	}
	var retry store.Package
	if err := json.Unmarshal(resubmitRec.Body.Bytes(), &retry); err != nil {
		t.Fatal(err)
	}
	if retry.RetryOf == nil || *retry.RetryOf != pkg.ID {
		t.Fatalf("expected retry_of to point at original package %s, got %+v", pkg.ID, retry.RetryOf)
	}
	if retry.Status != "ready" {
		t.Fatalf("expected new package status ready, got %q", retry.Status)
	}
}

func TestHandleCancelPackage(t *testing.T) {
	cs := newFakeCaseStore()
	pkg := createReadyPackage(t, cs, uuid.New(), map[string]any{"case_summary": "x", "typology_catalog": []any{}})
	deps := Deps{Store: cs, ML: &fakeRetrieval{}}
	mux := New(deps)

	cancelRec := httptest.NewRecorder()
	mux.ServeHTTP(cancelRec, httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/cancel", nil))
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("expected 200 cancelling a ready package, got %d: %s", cancelRec.Code, cancelRec.Body.String())
	}
	var cancelled store.Package
	if err := json.Unmarshal(cancelRec.Body.Bytes(), &cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != "cancelled" {
		t.Fatalf("expected status cancelled, got %q", cancelled.Status)
	}

	// Cancelling an already-cancelled package is not a legal transition.
	againRec := httptest.NewRecorder()
	mux.ServeHTTP(againRec, httptest.NewRequest(http.MethodPost, "/api/packages/"+pkg.ID.String()+"/cancel", nil))
	if againRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 cancelling an already-cancelled package, got %d", againRec.Code)
	}
}
