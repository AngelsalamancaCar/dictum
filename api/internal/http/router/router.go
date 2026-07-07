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
	"dictum/api/internal/store"
)

// CaseStore is the subset of *store.Store the router needs.
type CaseStore interface {
	CreateCase(ctx context.Context, name string) (store.Case, error)
	ingest.DocumentStore
}

// Deps are the dependencies routes are built against; New wires them into
// a *http.ServeMux.
type Deps struct {
	Store CaseStore
	Jobs  *jobs.Queue
}

func New(deps Deps) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("POST /api/cases", handleCreateCase(deps))
	mux.HandleFunc("GET /api/cases/{id}/events", handleCaseEvents(deps))

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
