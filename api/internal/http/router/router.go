// Package router wires HTTP routes for the dictum API server.
package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"dictum/api/internal/ingest"
	"dictum/api/internal/jobs"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/packages"
	"dictum/api/internal/store"
)

// CaseStore is the subset of *store.Store the router needs.
type CaseStore interface {
	CreateCase(ctx context.Context, name string) (store.Case, error)
	CaseChunkText(ctx context.Context, caseID uuid.UUID) (string, error)
	ingest.DocumentStore

	CreatePackage(ctx context.Context, in store.PackageInput) (store.Package, error)
	GetPackage(ctx context.Context, id uuid.UUID) (store.Package, error)
	ListPackages(ctx context.Context, filter store.PackageFilter) ([]store.PackageSummary, error)
	MarkPackageSubmitted(ctx context.Context, id uuid.UUID) error
	MarkPackageCompleted(ctx context.Context, id uuid.UUID) error
	MarkPackageCancelled(ctx context.Context, id uuid.UUID) error
	InsertPackageResult(ctx context.Context, in store.PackageResultInput) (store.PackageResult, error)
}

// RetrievalClient is the subset of *mlclient.Client the router needs for
// UC2/UC3 (retrieval and classification are ML-worker-owned per plan.md's
// architecture split; the router just builds a case summary and proxies).
type RetrievalClient interface {
	Similar(ctx context.Context, caseSummary string, k int, filters mlclient.SimilarFilters) ([]mlclient.SimilarResult, error)
	ClassifyKNN(ctx context.Context, caseSummary string, k int) (mlclient.ClassifyKNNResult, error)
	BuildPackage(ctx context.Context, useCase string, packageContext map[string]any) (mlclient.PackageBundle, error)
}

// Deps are the dependencies routes are built against; New wires them into
// a *http.ServeMux.
type Deps struct {
	Store CaseStore
	Jobs  *jobs.Queue
	ML    RetrievalClient
}

func New(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/cases", handleCreateCase(deps))
	mux.HandleFunc("GET /api/cases/{id}/events", handleCaseEvents(deps))
	mux.HandleFunc("GET /api/cases/{id}/similar-rulings", handleSimilarRulings(deps))
	mux.HandleFunc("GET /api/cases/{id}/classification", handleClassification(deps))

	mux.HandleFunc("POST /api/cases/{id}/packages", handleCreatePackage(deps))
	mux.HandleFunc("GET /api/packages", handleListPackages(deps))
	mux.HandleFunc("GET /api/packages/{id}", handleGetPackage(deps))
	mux.HandleFunc("GET /api/packages/{id}/archive", handleDownloadPackageArchive(deps))
	mux.HandleFunc("POST /api/packages/{id}/submit", handleSubmitPackage(deps))
	mux.HandleFunc("POST /api/packages/{id}/results", handleAttachPackageResult(deps))
	mux.HandleFunc("POST /api/packages/{id}/resubmit", handleResubmitPackage(deps))
	mux.HandleFunc("POST /api/packages/{id}/cancel", handleCancelPackage(deps))

	return mux
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

type createCaseRequest struct {
	Name       string `json:"name"`
	FolderPath string `json:"folder_path"`
}

func handleCreateCase(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createCaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Name == "" || req.FolderPath == "" {
			http.Error(w, "name and folder_path are required", http.StatusBadRequest)
			return
		}

		c, err := deps.Store.CreateCase(r.Context(), req.Name)
		if err != nil {
			http.Error(w, "failed to create case", http.StatusInternalServerError)
			return
		}

		go ingestFolder(deps, c.ID, req.FolderPath)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(c)
	}
}

func ingestFolder(deps Deps, caseID uuid.UUID, folderPath string) {
	err := ingest.WalkFolder(context.Background(), deps.Store, caseID, folderPath, func(doc store.Document, path string) {
		deps.Jobs.Submit(doc, path)
	})
	if err != nil {
		log.Printf("folder ingest failed for case %s (%s): %v", caseID, folderPath, err)
	}
}

func handleSimilarRulings(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		summary, err := deps.Store.CaseChunkText(r.Context(), caseID)
		if err != nil {
			http.Error(w, "failed to load case text", http.StatusInternalServerError)
			return
		}
		if summary == "" {
			http.Error(w, "case has no parsed documents yet", http.StatusConflict)
			return
		}

		filters := mlclient.SimilarFilters{
			CaseType: r.URL.Query().Get("case_type"),
			Court:    r.URL.Query().Get("court"),
			DateFrom: r.URL.Query().Get("date_from"),
			DateTo:   r.URL.Query().Get("date_to"),
		}
		results, err := deps.ML.Similar(r.Context(), summary, 10, filters)
		if err != nil {
			http.Error(w, "similarity search failed", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(results)
	}
}

func handleClassification(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		summary, err := deps.Store.CaseChunkText(r.Context(), caseID)
		if err != nil {
			http.Error(w, "failed to load case text", http.StatusInternalServerError)
			return
		}
		if summary == "" {
			http.Error(w, "case has no parsed documents yet", http.StatusConflict)
			return
		}

		result, err := deps.ML.ClassifyKNN(r.Context(), summary, 10)
		if err != nil {
			http.Error(w, "classification failed", http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	}
}

func handleCaseEvents(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		events, cancel := deps.Jobs.Subscribe(caseID)
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				payload, err := json.Marshal(ev)
				if err != nil {
					continue
				}
				w.Write([]byte("data: "))
				w.Write(payload)
				w.Write([]byte("\n\n"))
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}

var validUseCases = map[string]bool{
	string(packages.UseCaseClassify):       true,
	string(packages.UseCaseDraft):          true,
	string(packages.UseCaseRiskExplain):    true,
	string(packages.UseCaseSimilarExplain): true,
}

var resubmittableStatus = map[string]bool{
	string(packages.StatusFailed):    true,
	string(packages.StatusCompleted): true,
	string(packages.StatusCancelled): true,
}

var cancellableStatus = map[string]bool{
	string(packages.StatusDraft):     true,
	string(packages.StatusReady):     true,
	string(packages.StatusSubmitted): true,
}

type createPackageRequest struct {
	UseCase string         `json:"use_case"`
	Context map[string]any `json:"context"`
}

// handleCreatePackage assembles a prepared package via the ML worker
// (/package-build renders the use case's prompt template against Context)
// and persists it as a package row in status "ready" (plan.md §5).
func handleCreatePackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		var req createPackageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !validUseCases[req.UseCase] {
			http.Error(w, "invalid use_case", http.StatusBadRequest)
			return
		}

		bundle, err := deps.ML.BuildPackage(r.Context(), req.UseCase, req.Context)
		if err != nil {
			http.Error(w, "package build failed", http.StatusBadGateway)
			return
		}

		pkg, err := saveNewPackage(r.Context(), deps, caseID, req.UseCase, bundle, nil)
		if err != nil {
			http.Error(w, "failed to save package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pkg)
	}
}

func saveNewPackage(ctx context.Context, deps Deps, caseID uuid.UUID, useCase string, bundle mlclient.PackageBundle, retryOf *uuid.UUID) (store.Package, error) {
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return store.Package{}, err
	}
	return deps.Store.CreatePackage(ctx, store.PackageInput{
		CaseID:        caseID,
		UseCase:       useCase,
		PromptVersion: bundle.PromptVersion,
		Status:        string(packages.StatusReady),
		Bundle:        bundleJSON,
		RetryOf:       retryOf,
	})
}

func handleListPackages(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := store.PackageFilter{
			UseCase: r.URL.Query().Get("use_case"),
			Status:  r.URL.Query().Get("status"),
		}
		if idStr := r.URL.Query().Get("case_id"); idStr != "" {
			caseID, err := uuid.Parse(idStr)
			if err != nil {
				http.Error(w, "invalid case_id", http.StatusBadRequest)
				return
			}
			filter.CaseID = &caseID
		}

		pkgs, err := deps.Store.ListPackages(r.Context(), filter)
		if err != nil {
			http.Error(w, "failed to list packages", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pkgs)
	}
}

// loadPackage fetches a package by its {id} path value, writing a 400/404
// response and returning ok=false if that's not possible.
func loadPackage(deps Deps, w http.ResponseWriter, r *http.Request) (store.Package, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "invalid package id", http.StatusBadRequest)
		return store.Package{}, false
	}

	pkg, err := deps.Store.GetPackage(r.Context(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "package not found", http.StatusNotFound)
		} else {
			http.Error(w, "failed to load package", http.StatusInternalServerError)
		}
		return store.Package{}, false
	}
	return pkg, true
}

func handleGetPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pkg, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pkg)
	}
}

func handleDownloadPackageArchive(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pkg, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}

		var bundle mlclient.PackageBundle
		if err := json.Unmarshal(pkg.Bundle, &bundle); err != nil {
			http.Error(w, "corrupt package bundle", http.StatusInternalServerError)
			return
		}

		archive, err := buildPackageArchive(bundle)
		if err != nil {
			http.Error(w, "failed to build archive", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, bundle.PackageID))
		w.Write(archive)
	}
}

// buildPackageArchive reconstitutes the on-disk bundle layout from plan.md
// §5 (manifest.json, prompts/<use_case>.md, context/*.json,
// output_schema.json) as a zip, for manual hand-off to the harness.
func buildPackageArchive(bundle mlclient.PackageBundle) ([]byte, error) {
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	manifest, err := json.MarshalIndent(map[string]any{
		"package_id":     bundle.PackageID,
		"use_case":       bundle.UseCase,
		"prompt_version": bundle.PromptVersion,
		"created_at":     bundle.CreatedAt,
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeZipFile(zw, "manifest.json", manifest); err != nil {
		return nil, err
	}
	if err := writeZipFile(zw, fmt.Sprintf("prompts/%s.md", bundle.UseCase), []byte(bundle.Prompt)); err != nil {
		return nil, err
	}
	if err := writeZipFile(zw, "output_schema.json", bundle.OutputSchema); err != nil {
		return nil, err
	}

	var bundleContext map[string]json.RawMessage
	if err := json.Unmarshal(bundle.Context, &bundleContext); err != nil {
		return nil, err
	}
	for name, payload := range bundleContext {
		if err := writeZipFile(zw, fmt.Sprintf("context/%s.json", name), payload); err != nil {
			return nil, err
		}
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func writeZipFile(zw *zip.Writer, name string, content []byte) error {
	f, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	return err
}

func handleSubmitPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pkg, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}
		if pkg.Status != string(packages.StatusReady) {
			http.Error(w, fmt.Sprintf("cannot submit package in status %q", pkg.Status), http.StatusConflict)
			return
		}

		if err := deps.Store.MarkPackageSubmitted(r.Context(), pkg.ID); err != nil {
			http.Error(w, "failed to mark package submitted", http.StatusInternalServerError)
			return
		}
		pkg, err := deps.Store.GetPackage(r.Context(), pkg.ID)
		if err != nil {
			http.Error(w, "failed to reload package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pkg)
	}
}

type attachPackageResultRequest struct {
	RawResponse json.RawMessage `json:"raw_response"`
}

type attachPackageResultResponse struct {
	Result           store.PackageResult
	ValidationErrors []string
}

// handleAttachPackageResult ingests a harness response for a submitted
// package, validating it against the bundle's output_schema (plan.md §5:
// "validation against output_schema.json runs on ingest, failures
// flagged"). The package only transitions to completed on a valid result —
// an invalid one is still recorded (for visibility) but leaves the package
// submitted so a corrected result can be attached later.
func handleAttachPackageResult(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req attachPackageResultRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.RawResponse) == 0 {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		pkg, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}
		if pkg.Status != string(packages.StatusSubmitted) {
			http.Error(w, fmt.Sprintf("cannot attach a result to package in status %q", pkg.Status), http.StatusConflict)
			return
		}

		var bundle mlclient.PackageBundle
		if err := json.Unmarshal(pkg.Bundle, &bundle); err != nil {
			http.Error(w, "corrupt package bundle", http.StatusInternalServerError)
			return
		}

		validationErrs, err := packages.Validate(bundle.OutputSchema, req.RawResponse)
		if err != nil {
			http.Error(w, "malformed response or schema: "+err.Error(), http.StatusBadRequest)
			return
		}

		resultInput := store.PackageResultInput{PackageID: pkg.ID, RawResponse: req.RawResponse}
		if len(validationErrs) == 0 {
			resultInput.ValidationStatus = "valid"
			resultInput.ValidatedPayload = req.RawResponse
		} else {
			resultInput.ValidationStatus = "invalid"
		}

		result, err := deps.Store.InsertPackageResult(r.Context(), resultInput)
		if err != nil {
			http.Error(w, "failed to save result", http.StatusInternalServerError)
			return
		}

		if resultInput.ValidationStatus == "valid" {
			if err := deps.Store.MarkPackageCompleted(r.Context(), pkg.ID); err != nil {
				http.Error(w, "failed to mark package completed", http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(attachPackageResultResponse{Result: result, ValidationErrors: validationErrs})
	}
}

// handleResubmitPackage rebuilds a package from its original context
// (picking up any prompt-file changes since — build_bundle always reads
// prompt_version fresh) and links it to the original via retry_of.
func handleResubmitPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		original, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}
		if !resubmittableStatus[original.Status] {
			http.Error(w, fmt.Sprintf("cannot resubmit package in status %q", original.Status), http.StatusConflict)
			return
		}

		var origBundle mlclient.PackageBundle
		if err := json.Unmarshal(original.Bundle, &origBundle); err != nil {
			http.Error(w, "corrupt package bundle", http.StatusInternalServerError)
			return
		}
		var origContext map[string]any
		if err := json.Unmarshal(origBundle.Context, &origContext); err != nil {
			http.Error(w, "corrupt package bundle context", http.StatusInternalServerError)
			return
		}

		bundle, err := deps.ML.BuildPackage(r.Context(), original.UseCase, origContext)
		if err != nil {
			http.Error(w, "package build failed", http.StatusBadGateway)
			return
		}

		pkg, err := saveNewPackage(r.Context(), deps, original.CaseID, original.UseCase, bundle, &original.ID)
		if err != nil {
			http.Error(w, "failed to save package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pkg)
	}
}

func handleCancelPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pkg, ok := loadPackage(deps, w, r)
		if !ok {
			return
		}
		if !cancellableStatus[pkg.Status] {
			http.Error(w, fmt.Sprintf("cannot cancel package in status %q", pkg.Status), http.StatusConflict)
			return
		}

		if err := deps.Store.MarkPackageCancelled(r.Context(), pkg.ID); err != nil {
			http.Error(w, "failed to cancel package", http.StatusInternalServerError)
			return
		}
		pkg, err := deps.Store.GetPackage(r.Context(), pkg.ID)
		if err != nil {
			http.Error(w, "failed to reload package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pkg)
	}
}
