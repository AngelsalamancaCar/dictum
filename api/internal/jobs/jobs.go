// Package jobs orchestrates async work (parse, embed, package submission)
// via goroutines backed by durable state in Postgres.
package jobs

import (
	"context"
	"log"
	"sync"

	"github.com/google/uuid"

	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

type Event struct {
	CaseID     uuid.UUID `json:"case_id"`
	DocumentID uuid.UUID `json:"document_id"`
	Filename   string    `json:"filename"`
	Status     string    `json:"status"` // parsing | embedding | done | failed
	Error      string    `json:"error,omitempty"`
}

// DocumentStore is the subset of *store.Store the queue needs.
type DocumentStore interface {
	UpdateDocumentParseStatus(ctx context.Context, documentID uuid.UUID, status string, objectRef *string) error
	InsertChunks(ctx context.Context, documentID uuid.UUID, chunks []store.ChunkInput) error
}

// MLClient is the subset of *mlclient.Client the queue needs.
type MLClient interface {
	Parse(ctx context.Context, filePath string) (mlclient.ParseResult, error)
	Embed(ctx context.Context, texts []string, kind string) ([][]float32, error)
}

type Queue struct {
	ml    MLClient
	store DocumentStore
	sem   chan struct{}

	mu          sync.Mutex
	subscribers map[uuid.UUID][]chan Event
}

func NewQueue(ml MLClient, s DocumentStore, concurrency int) *Queue {
	if concurrency <= 0 {
		concurrency = 4
	}
	return &Queue{
		ml:          ml,
		store:       s,
		sem:         make(chan struct{}, concurrency),
		subscribers: make(map[uuid.UUID][]chan Event),
	}
}

// Submit schedules parse+embed for doc and returns immediately; progress is
// published via Subscribe(caseID).
func (q *Queue) Submit(doc store.Document, filePath string) {
	go func() {
		q.sem <- struct{}{}
		defer func() { <-q.sem }()
		q.process(context.Background(), doc, filePath)
	}()
}

func (q *Queue) process(ctx context.Context, doc store.Document, filePath string) {
	q.publish(doc, Event{Status: "parsing"})

	parsed, err := q.ml.Parse(ctx, filePath)
	if err != nil {
		q.fail(ctx, doc, err)
		return
	}

	q.publish(doc, Event{Status: "embedding"})

	texts := make([]string, len(parsed.Chunks))
	for i, c := range parsed.Chunks {
		texts[i] = c.Text
	}

	var chunkInputs []store.ChunkInput
	if len(texts) > 0 {
		vectors, err := q.ml.Embed(ctx, texts, "passage")
		if err != nil {
			q.fail(ctx, doc, err)
			return
		}
		chunkInputs = make([]store.ChunkInput, len(parsed.Chunks))
		for i, c := range parsed.Chunks {
			chunkInputs[i] = store.ChunkInput{
				Text:         c.Text,
				SectionLabel: c.SectionLabel,
				Embedding:    vectors[i],
			}
		}
		if err := q.store.InsertChunks(ctx, doc.ID, chunkInputs); err != nil {
			q.fail(ctx, doc, err)
			return
		}
	}

	// object_ref holds the parsed pages JSON directly for now; plan.md's
	// object storage integration isn't built yet, and Postgres text storage
	// is a fine stand-in for now given the corpus/case sizes at hand.
	objectRef := string(parsed.Pages)
	if err := q.store.UpdateDocumentParseStatus(ctx, doc.ID, "done", &objectRef); err != nil {
		q.fail(ctx, doc, err)
		return
	}

	q.publish(doc, Event{Status: "done"})
}

func (q *Queue) fail(ctx context.Context, doc store.Document, err error) {
	log.Printf("document %s (%s) failed: %v", doc.ID, doc.Filename, err)
	_ = q.store.UpdateDocumentParseStatus(ctx, doc.ID, "failed", nil)
	q.publish(doc, Event{Status: "failed", Error: err.Error()})
}

func (q *Queue) publish(doc store.Document, ev Event) {
	ev.CaseID = doc.CaseID
	ev.DocumentID = doc.ID
	ev.Filename = doc.Filename

	q.mu.Lock()
	defer q.mu.Unlock()
	for _, ch := range q.subscribers[doc.CaseID] {
		select {
		case ch <- ev:
		default: // slow subscriber; drop rather than block ingestion
		}
	}
}

// Subscribe returns a channel of events for caseID and an unsubscribe func.
func (q *Queue) Subscribe(caseID uuid.UUID) (<-chan Event, func()) {
	ch := make(chan Event, 32)

	q.mu.Lock()
	q.subscribers[caseID] = append(q.subscribers[caseID], ch)
	q.mu.Unlock()

	cancel := func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		subs := q.subscribers[caseID]
		for i, c := range subs {
			if c == ch {
				q.subscribers[caseID] = append(subs[:i], subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
	return ch, cancel
}
