// Package router wires HTTP routes for the dictum API server.
package router

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/google/uuid"

	"dictum/api/internal/ingest"
	"dictum/api/internal/jobs"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

// CaseStore is the subset of *store.Store the router needs.
type CaseStore interface {
	CreateCase(ctx context.Context, name string) (store.Case, error)
	CaseChunkText(ctx context.Context, caseID uuid.UUID) (string, error)
	ingest.DocumentStore
}

// RetrievalClient is the subset of *mlclient.Client the router needs for
// UC2/UC3 (retrieval and classification are ML-worker-owned per plan.md's
// architecture split; the router just builds a case summary and proxies).
type RetrievalClient interface {
	Similar(ctx context.Context, caseSummary string, k int, filters mlclient.SimilarFilters) ([]mlclient.SimilarResult, error)
	ClassifyKNN(ctx context.Context, caseSummary string, k int) (mlclient.ClassifyKNNResult, error)
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
