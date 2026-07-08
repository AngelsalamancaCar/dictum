// Package router wires HTTP routes for the dictum API server.
package router

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"dictum/api/internal/http/auth"
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

	ListTypologies(ctx context.Context) ([]store.Typology, error)
	GetRulingsByIDs(ctx context.Context, ids []uuid.UUID) ([]store.Ruling, error)

	CreateDraft(ctx context.Context, in store.DraftInput) (store.Draft, error)
	ListDraftsByCase(ctx context.Context, caseID uuid.UUID) ([]store.Draft, error)

	CreatePackage(ctx context.Context, in store.PackageInput) (store.Package, error)
	GetPackage(ctx context.Context, id uuid.UUID) (store.Package, error)
	ListPackages(ctx context.Context, filter store.PackageFilter) ([]store.PackageSummary, error)
	MarkPackageSubmitted(ctx context.Context, id uuid.UUID) error
	MarkPackageCompleted(ctx context.Context, id uuid.UUID) error
	MarkPackageCancelled(ctx context.Context, id uuid.UUID) error
	InsertPackageResult(ctx context.Context, in store.PackageResultInput) (store.PackageResult, error)

	InsertAudit(ctx context.Context, in store.AuditInput) (store.AuditEntry, error)
	ListAuditLog(ctx context.Context, filter store.AuditFilter) ([]store.AuditEntry, error)
}

// RetrievalClient is the subset of *mlclient.Client the router needs for
// UC2/UC3 (retrieval and classification are ML-worker-owned per plan.md's
// architecture split; the router just builds a case summary and proxies).
type RetrievalClient interface {
	Similar(ctx context.Context, caseSummary string, k int, filters mlclient.SimilarFilters) ([]mlclient.SimilarResult, error)
	ClassifyKNN(ctx context.Context, caseSummary string, k int) (mlclient.ClassifyKNNResult, error)
	RiskScore(ctx context.Context, text string, k int) (mlclient.RiskScoreResult, error)
	RevertedNeighbors(ctx context.Context, text string, k int) ([]mlclient.RevertedNeighbor, error)
	BuildPackage(ctx context.Context, useCase string, packageContext map[string]any) (mlclient.PackageBundle, error)
}

// Deps are the dependencies routes are built against; New wires them into
// a *http.ServeMux.
type Deps struct {
	Store CaseStore
	Jobs  *jobs.Queue
	ML    RetrievalClient

	// StaticFS serves the built web/ SPA (see internal/http/webui) for any
	// path not matched by an /api/... route, with index.html as the
	// client-side-routing fallback. Nil disables static serving — the
	// default in tests, which only exercise API routes.
	StaticFS fs.FS

	// AuthTokens is auth.ParseTokens' token→actor map. When non-empty,
	// every /api/... route requires a matching bearer token (or
	// ?access_token= for header-less clients — SSE, download links);
	// /healthz and the static SPA stay open so probes work and the login
	// surface can load. Empty disables auth entirely (dev / tests).
	AuthTokens map[string]string
}

func New(deps Deps) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/cases", handleCreateCase(deps))
	mux.HandleFunc("GET /api/cases/{id}/events", handleCaseEvents(deps))
	mux.HandleFunc("GET /api/cases/{id}/similar-rulings", handleSimilarRulings(deps))
	mux.HandleFunc("GET /api/cases/{id}/classification", handleClassification(deps))
	mux.HandleFunc("GET /api/cases/{id}/risk-score", handleRiskScore(deps))

	mux.HandleFunc("POST /api/cases/{id}/packages", handleCreatePackage(deps))
	mux.HandleFunc("POST /api/cases/{id}/packages/classify", handleCreateClassifyPackage(deps))
	mux.HandleFunc("POST /api/cases/{id}/packages/risk-explain", handleCreateRiskExplainPackage(deps))
	mux.HandleFunc("POST /api/cases/{id}/packages/draft", handleCreateDraftPackage(deps))
	mux.HandleFunc("GET /api/cases/{id}/drafts", handleListDrafts(deps))
	mux.HandleFunc("GET /api/packages", handleListPackages(deps))
	mux.HandleFunc("GET /api/packages/{id}", handleGetPackage(deps))
	mux.HandleFunc("GET /api/packages/{id}/archive", handleDownloadPackageArchive(deps))
	mux.HandleFunc("POST /api/packages/{id}/submit", handleSubmitPackage(deps))
	mux.HandleFunc("POST /api/packages/{id}/results", handleAttachPackageResult(deps))
	mux.HandleFunc("POST /api/packages/{id}/resubmit", handleResubmitPackage(deps))
	mux.HandleFunc("POST /api/packages/{id}/cancel", handleCancelPackage(deps))
	mux.HandleFunc("GET /api/audit", handleListAudit(deps))

	if deps.StaticFS != nil {
		mux.Handle("/", spaHandler(deps.StaticFS))
	}

	if len(deps.AuthTokens) == 0 {
		return mux
	}
	protected := auth.Middleware(deps.AuthTokens, mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			protected.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func healthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// spaHandler serves the embedded SPA build. Requests for a path that
// exists in fsys (e.g. /assets/index-abc123.js) are served as-is; anything
// else falls back to index.html's raw content so client-side routes (e.g.
// /packages/42) resolve on a hard refresh instead of 404ing. The fallback
// deliberately doesn't just rewrite r.URL.Path and hand off to
// http.FileServer — net/http's FileServer special-cases any request whose
// path ends in "index.html" and 301-redirects it to "/", which would send
// a client-side route straight back to the SPA's root instead of loading
// it at the requested path.
func spaHandler(fsys fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(fsys))
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if info, err := fs.Stat(fsys, path); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}
		data, err := fs.ReadFile(fsys, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	}
}

// recordAudit writes an audit_log row for a mutating action, attributing it
// to the authenticated actor (auth.AnonymousActor when auth is disabled).
// Best-effort by design: the mutation already happened, so an audit-write
// failure is logged rather than turned into a client-facing error that
// would misreport the mutation as failed.
func recordAudit(ctx context.Context, deps Deps, action, entity string, entityID uuid.UUID, metadata map[string]any) {
	var raw json.RawMessage
	if metadata != nil {
		var err error
		raw, err = json.Marshal(metadata)
		if err != nil {
			log.Printf("audit: marshaling metadata for %s %s %s: %v", action, entity, entityID, err)
			raw = nil
		}
	}
	id := entityID
	_, err := deps.Store.InsertAudit(ctx, store.AuditInput{
		Actor:    auth.Actor(ctx),
		Action:   action,
		Entity:   entity,
		EntityID: &id,
		Metadata: raw,
	})
	if err != nil {
		log.Printf("audit: recording %s %s %s: %v", action, entity, entityID, err)
	}
}

func handleListAudit(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		filter := store.AuditFilter{
			Actor:  r.URL.Query().Get("actor"),
			Entity: r.URL.Query().Get("entity"),
		}
		if idStr := r.URL.Query().Get("entity_id"); idStr != "" {
			entityID, err := uuid.Parse(idStr)
			if err != nil {
				http.Error(w, "invalid entity_id", http.StatusBadRequest)
				return
			}
			filter.EntityID = &entityID
		}

		entries, err := deps.Store.ListAuditLog(r.Context(), filter)
		if err != nil {
			http.Error(w, "failed to list audit log", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}
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
		recordAudit(r.Context(), deps, "create", "case", c.ID, map[string]any{
			"name": req.Name, "folder_path": req.FolderPath,
		})

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

// handleRiskScore serves UC5's local score signal. No drafts table exists
// yet (UC4 is separate, still-open plan.md scope), so — same as
// handleSimilarRulings/handleClassification — this scores the case's
// stored chunk text as a stand-in until a real draft can be passed instead.
func handleRiskScore(deps Deps) http.HandlerFunc {
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

		result, err := deps.ML.RiskScore(r.Context(), summary, 10)
		if err != nil {
			http.Error(w, "risk scoring failed", http.StatusBadGateway)
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

// typologyCatalogEntry is the shape rendered into the classify prompt's
// {{typology_catalog}} placeholder — deliberately its own DTO rather than
// marshaling store.Typology directly, since store structs carry no json
// tags (see CLAUDE.md) and would leak PascalCase field names into the
// prompt text.
type typologyCatalogEntry struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description,omitempty"`
	DiscriminatingFeatures []string `json:"discriminating_features,omitempty"`
}

func typologyCatalog(typologies []store.Typology) ([]typologyCatalogEntry, error) {
	catalog := make([]typologyCatalogEntry, 0, len(typologies))
	for _, t := range typologies {
		var features []string
		if len(t.DiscriminatingFeatures) > 0 {
			if err := json.Unmarshal(t.DiscriminatingFeatures, &features); err != nil {
				return nil, err
			}
		}
		description := ""
		if t.Description != nil {
			description = *t.Description
		}
		catalog = append(catalog, typologyCatalogEntry{
			Name:                   t.Name,
			Description:            description,
			DiscriminatingFeatures: features,
		})
	}
	return catalog, nil
}

// handleCreateClassifyPackage assembles UC2's LLM-signal package: the
// case's stored chunk text as case_summary (same stand-in
// handleSimilarRulings/handleClassification/handleRiskScore use) and the
// full typology catalog as typology_catalog, then builds and persists a
// classify package exactly like handleCreatePackage does for
// caller-supplied context.
func handleCreateClassifyPackage(deps Deps) http.HandlerFunc {
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

		typologies, err := deps.Store.ListTypologies(r.Context())
		if err != nil {
			http.Error(w, "failed to load typology catalog", http.StatusInternalServerError)
			return
		}
		catalog, err := typologyCatalog(typologies)
		if err != nil {
			http.Error(w, "corrupt typology catalog", http.StatusInternalServerError)
			return
		}

		packageContext := map[string]any{
			"case_summary":     summary,
			"typology_catalog": catalog,
		}

		bundle, err := deps.ML.BuildPackage(r.Context(), string(packages.UseCaseClassify), packageContext)
		if err != nil {
			http.Error(w, "package build failed", http.StatusBadGateway)
			return
		}

		pkg, err := saveNewPackage(r.Context(), deps, caseID, string(packages.UseCaseClassify), bundle, nil)
		if err != nil {
			http.Error(w, "failed to save package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pkg)
	}
}

// revertedNeighborEntry is the shape rendered into the risk_explain
// prompt's {{reverted_neighbors}} placeholder — its own DTO for the same
// reason typologyCatalogEntry is: mlclient.RevertedNeighbor already carries
// json tags, but keeping context-rendering DTOs separate from the wire
// types they're built from avoids coupling the prompt's shape to the ML
// worker's response shape.
type revertedNeighborEntry struct {
	RulingID     string  `json:"ruling_id"`
	ExternalID   string  `json:"external_id"`
	RevertReason *string `json:"revert_reason,omitempty"`
	Similarity   float64 `json:"similarity"`
}

// handleCreateRiskExplainPackage assembles UC5's explanation package: the
// case's stored chunk text as draft_text (same case-text stand-in used
// elsewhere, since there's no drafts table yet — see plan.md §4 UC4) and
// its nearest reverted-outcome neighbors (with revert_reason) as
// reverted_neighbors, then builds and persists a risk_explain package.
func handleCreateRiskExplainPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		draftText, err := deps.Store.CaseChunkText(r.Context(), caseID)
		if err != nil {
			http.Error(w, "failed to load case text", http.StatusInternalServerError)
			return
		}
		if draftText == "" {
			http.Error(w, "case has no parsed documents yet", http.StatusConflict)
			return
		}

		neighbors, err := deps.ML.RevertedNeighbors(r.Context(), draftText, 5)
		if err != nil {
			http.Error(w, "reverted-neighbor lookup failed", http.StatusBadGateway)
			return
		}

		entries := make([]revertedNeighborEntry, 0, len(neighbors))
		for _, n := range neighbors {
			entries = append(entries, revertedNeighborEntry{
				RulingID:     n.RulingID,
				ExternalID:   n.ExternalID,
				RevertReason: n.RevertReason,
				Similarity:   n.Similarity,
			})
		}

		packageContext := map[string]any{
			"draft_text":         draftText,
			"reverted_neighbors": entries,
		}

		bundle, err := deps.ML.BuildPackage(r.Context(), string(packages.UseCaseRiskExplain), packageContext)
		if err != nil {
			http.Error(w, "package build failed", http.StatusBadGateway)
			return
		}

		pkg, err := saveNewPackage(r.Context(), deps, caseID, string(packages.UseCaseRiskExplain), bundle, nil)
		if err != nil {
			http.Error(w, "failed to save package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pkg)
	}
}

// exemplarRulingEntry is the shape rendered into the draft prompt's
// {{exemplar_rulings}} placeholder. Excerpt is truncated — the prompt
// explicitly instructs the harness not to cite outside the provided
// material, so the material needs real ruling text, not just metadata.
type exemplarRulingEntry struct {
	RulingID   string `json:"ruling_id"`
	ExternalID string `json:"external_id"`
	Outcome    string `json:"outcome"`
	Excerpt    string `json:"excerpt"`
}

// exemplarExcerptLen caps how much of a cited ruling's full_text is
// included per exemplar — enough for grounding, not the whole document.
const exemplarExcerptLen = 1500

// truncateExcerpt cuts text to exemplarExcerptLen bytes, backing off to the
// nearest rune boundary — a plain byte-index slice can split a multi-byte
// UTF-8 rune (Spanish accented characters are 2 bytes) and produce invalid
// UTF-8 that encoding/json would silently replace with U+FFFD.
func truncateExcerpt(text string) string {
	if len(text) <= exemplarExcerptLen {
		return text
	}
	cut := exemplarExcerptLen
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// typologyStructureEntry is the shape rendered into the draft prompt's
// {{typology_structure}} placeholder — the single typology matching the
// confirmed case_type, reusing the same fields typologyCatalogEntry does.
type typologyStructureEntry struct {
	Name                   string   `json:"name"`
	Description            string   `json:"description,omitempty"`
	DiscriminatingFeatures []string `json:"discriminating_features,omitempty"`
}

func findTypology(typologies []store.Typology, caseType string) (store.Typology, bool) {
	for _, t := range typologies {
		if t.Name == caseType {
			return t, true
		}
	}
	return store.Typology{}, false
}

type createDraftPackageRequest struct {
	CaseType string `json:"case_type"`
}

// handleCreateDraftPackage assembles UC4's draft package: case_type is the
// judge/staff-confirmed typology (UC2 confirmation isn't a built flow yet —
// see plan.md §4 UC2 — so it's supplied directly by the caller), case_facts
// is the case-chunk-text stand-in used elsewhere, typology_structure is the
// matching catalog entry's description/discriminating_features, and
// exemplar_rulings is UC3's top similar rulings for that case_type
// (upheld ones sorted first — plan.md §4 UC4: "preferably upheld") with a
// text excerpt attached so the harness has real material to cite.
func handleCreateDraftPackage(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		var req createDraftPackageRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.CaseType == "" {
			http.Error(w, "case_type is required", http.StatusBadRequest)
			return
		}

		caseFacts, err := deps.Store.CaseChunkText(r.Context(), caseID)
		if err != nil {
			http.Error(w, "failed to load case text", http.StatusInternalServerError)
			return
		}
		if caseFacts == "" {
			http.Error(w, "case has no parsed documents yet", http.StatusConflict)
			return
		}

		typologies, err := deps.Store.ListTypologies(r.Context())
		if err != nil {
			http.Error(w, "failed to load typology catalog", http.StatusInternalServerError)
			return
		}
		typology, ok := findTypology(typologies, req.CaseType)
		if !ok {
			http.Error(w, "unknown case_type: not found in typology catalog", http.StatusBadRequest)
			return
		}
		var features []string
		if len(typology.DiscriminatingFeatures) > 0 {
			if err := json.Unmarshal(typology.DiscriminatingFeatures, &features); err != nil {
				http.Error(w, "corrupt typology catalog", http.StatusInternalServerError)
				return
			}
		}
		description := ""
		if typology.Description != nil {
			description = *typology.Description
		}
		structure := typologyStructureEntry{Name: typology.Name, Description: description, DiscriminatingFeatures: features}

		similar, err := deps.ML.Similar(r.Context(), caseFacts, 5, mlclient.SimilarFilters{CaseType: req.CaseType})
		if err != nil {
			http.Error(w, "similarity search failed", http.StatusBadGateway)
			return
		}
		sort.SliceStable(similar, func(i, j int) bool {
			return similar[i].Outcome == "upheld" && similar[j].Outcome != "upheld"
		})

		rulingIDs := make([]uuid.UUID, 0, len(similar))
		for _, s := range similar {
			id, err := uuid.Parse(s.RulingID)
			if err != nil {
				continue
			}
			rulingIDs = append(rulingIDs, id)
		}
		rulings, err := deps.Store.GetRulingsByIDs(r.Context(), rulingIDs)
		if err != nil {
			http.Error(w, "failed to load exemplar rulings", http.StatusInternalServerError)
			return
		}
		textByID := make(map[uuid.UUID]string, len(rulings))
		for _, rl := range rulings {
			textByID[rl.ID] = rl.FullText
		}

		exemplars := make([]exemplarRulingEntry, 0, len(similar))
		for _, s := range similar {
			id, err := uuid.Parse(s.RulingID)
			if err != nil {
				continue
			}
			exemplars = append(exemplars, exemplarRulingEntry{
				RulingID:   s.RulingID,
				ExternalID: s.ExternalID,
				Outcome:    s.Outcome,
				Excerpt:    truncateExcerpt(textByID[id]),
			})
		}

		packageContext := map[string]any{
			"case_type":          req.CaseType,
			"case_facts":         caseFacts,
			"typology_structure": structure,
			"exemplar_rulings":   exemplars,
		}

		bundle, err := deps.ML.BuildPackage(r.Context(), string(packages.UseCaseDraft), packageContext)
		if err != nil {
			http.Error(w, "package build failed", http.StatusBadGateway)
			return
		}

		pkg, err := saveNewPackage(r.Context(), deps, caseID, string(packages.UseCaseDraft), bundle, nil)
		if err != nil {
			http.Error(w, "failed to save package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(pkg)
	}
}

func handleListDrafts(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caseID, err := uuid.Parse(r.PathValue("id"))
		if err != nil {
			http.Error(w, "invalid case id", http.StatusBadRequest)
			return
		}

		drafts, err := deps.Store.ListDraftsByCase(r.Context(), caseID)
		if err != nil {
			http.Error(w, "failed to list drafts", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(drafts)
	}
}

// draftOutputPayload mirrors ml/prompts/schemas/draft.output_schema.json's
// shape, for parsing a validated draft-package result into a drafts row.
type draftOutputPayload struct {
	Sections []struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	} `json:"sections"`
	CitedRulingIDs []string `json:"cited_ruling_ids"`
}

// writeDraftFromResult parses a validated draft-package result payload and
// inserts the corresponding drafts row. Sections are joined into a single
// generated_text blob since drafts.generated_text is one text column, not
// structured sections. Cited ruling ids that don't parse as UUIDs are
// skipped rather than failing the whole write — the raw response is
// already retained in package_results for audit regardless.
func writeDraftFromResult(ctx context.Context, deps Deps, pkg store.Package, promptVersion int, validated json.RawMessage) (store.Draft, error) {
	var payload draftOutputPayload
	if err := json.Unmarshal(validated, &payload); err != nil {
		return store.Draft{}, err
	}

	var text strings.Builder
	for _, s := range payload.Sections {
		text.WriteString("## ")
		text.WriteString(s.Label)
		text.WriteString("\n\n")
		text.WriteString(s.Text)
		text.WriteString("\n\n")
	}

	citedIDs := make([]uuid.UUID, 0, len(payload.CitedRulingIDs))
	for _, s := range payload.CitedRulingIDs {
		id, err := uuid.Parse(s)
		if err != nil {
			continue
		}
		citedIDs = append(citedIDs, id)
	}

	return deps.Store.CreateDraft(ctx, store.DraftInput{
		CaseID:         pkg.CaseID,
		PackageID:      &pkg.ID,
		GeneratedText:  strings.TrimSpace(text.String()),
		CitedRulingIDs: citedIDs,
		PromptVersion:  promptVersion,
	})
}

func saveNewPackage(ctx context.Context, deps Deps, caseID uuid.UUID, useCase string, bundle mlclient.PackageBundle, retryOf *uuid.UUID) (store.Package, error) {
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return store.Package{}, err
	}
	pkg, err := deps.Store.CreatePackage(ctx, store.PackageInput{
		CaseID:        caseID,
		UseCase:       useCase,
		PromptVersion: bundle.PromptVersion,
		Status:        string(packages.StatusReady),
		Bundle:        bundleJSON,
		CreatedBy:     auth.Actor(ctx),
		RetryOf:       retryOf,
	})
	if err != nil {
		return store.Package{}, err
	}
	metadata := map[string]any{"use_case": useCase, "case_id": caseID.String()}
	if retryOf != nil {
		metadata["retry_of"] = retryOf.String()
	}
	recordAudit(ctx, deps, "create", "package", pkg.ID, metadata)
	return pkg, nil
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
		recordAudit(r.Context(), deps, "submit", "package", pkg.ID, nil)
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
		recordAudit(r.Context(), deps, "result_attach", "package", pkg.ID, map[string]any{
			"result_id": result.ID.String(), "validation_status": resultInput.ValidationStatus,
		})

		if resultInput.ValidationStatus == "valid" {
			if pkg.UseCase == string(packages.UseCaseDraft) {
				draft, err := writeDraftFromResult(r.Context(), deps, pkg, bundle.PromptVersion, req.RawResponse)
				if err != nil {
					http.Error(w, "failed to save draft: "+err.Error(), http.StatusInternalServerError)
					return
				}
				recordAudit(r.Context(), deps, "create", "draft", draft.ID, map[string]any{
					"case_id": pkg.CaseID.String(), "package_id": pkg.ID.String(),
				})
			}
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
		recordAudit(r.Context(), deps, "cancel", "package", pkg.ID, nil)
		pkg, err := deps.Store.GetPackage(r.Context(), pkg.ID)
		if err != nil {
			http.Error(w, "failed to reload package", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(pkg)
	}
}
