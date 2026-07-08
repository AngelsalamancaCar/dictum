// Package mlclient calls the dictum-ml FastAPI worker's HTTP endpoints
// (/parse, /embed) from the Go API server.
package mlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, http: &http.Client{}}
}

type Chunk struct {
	Text         string  `json:"text"`
	SectionLabel *string `json:"section_label"`
}

type ParseResult struct {
	Text   string          `json:"text"`
	Pages  json.RawMessage `json:"pages"`
	Chunks []Chunk         `json:"chunks"`
}

func (c *Client) Parse(ctx context.Context, filePath string) (ParseResult, error) {
	var out ParseResult

	f, err := os.Open(filePath)
	if err != nil {
		return out, err
	}
	defer f.Close()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return out, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return out, err
	}
	if err := writer.Close(); err != nil {
		return out, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/parse", body)
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return out, fmt.Errorf("ml /parse: status %d: %s", resp.StatusCode, b)
	}
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

// postJSON marshals req, POSTs it to path, and decodes the JSON response
// into out. Shared by every /chunk, /embed, /similar, /classify-knn style
// endpoint below; /parse is multipart and handled separately.
func (c *Client) postJSON(ctx context.Context, path string, req any, out any) error {
	reqBody, err := json.Marshal(req)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("ml %s: status %d: %s", path, resp.StatusCode, b)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type chunkRequest struct {
	Text string `json:"text"`
}

type chunkResponse struct {
	Chunks []Chunk `json:"chunks"`
}

// Chunk splits raw text using the same section-aware chunker /parse uses
// internally (ml/rag/chunking.py), for callers (like the ruling importer)
// that have text but no file to run through /parse.
func (c *Client) Chunk(ctx context.Context, text string) ([]Chunk, error) {
	var out chunkResponse
	if err := c.postJSON(ctx, "/chunk", chunkRequest{Text: text}, &out); err != nil {
		return nil, err
	}
	return out.Chunks, nil
}

type embedRequest struct {
	Texts []string `json:"texts"`
	Kind  string   `json:"kind"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Dimension  int         `json:"dimension"`
}

// Embed embeds texts. kind must be "passage" (corpus-side) or "query"
// (retrieval-side) — see ml/rag/embedding_service.py for why this matters.
func (c *Client) Embed(ctx context.Context, texts []string, kind string) ([][]float32, error) {
	var out embedResponse
	if err := c.postJSON(ctx, "/embed", embedRequest{Texts: texts, Kind: kind}, &out); err != nil {
		return nil, err
	}
	return out.Embeddings, nil
}

// SimilarFilters narrows /similar results; zero values mean "no filter".
type SimilarFilters struct {
	CaseType string
	Court    string
	DateFrom string
	DateTo   string
}

type similarRequest struct {
	CaseSummary string `json:"case_summary"`
	K           int    `json:"k"`
	CaseType    string `json:"case_type,omitempty"`
	Court       string `json:"court,omitempty"`
	DateFrom    string `json:"date_from,omitempty"`
	DateTo      string `json:"date_to,omitempty"`
}

type SimilarResult struct {
	RulingID   string  `json:"ruling_id"`
	ExternalID string  `json:"external_id"`
	CaseType   *string `json:"case_type"`
	Outcome    string  `json:"outcome"`
	Court      *string `json:"court"`
	Date       *string `json:"date"`
	FusedScore float64 `json:"fused_score"`
}

type similarResponse struct {
	Results []SimilarResult `json:"results"`
}

// Similar runs UC3 hybrid retrieval (pgvector kNN + Postgres FTS, fused by
// RRF) for a case summary.
func (c *Client) Similar(ctx context.Context, caseSummary string, k int, filters SimilarFilters) ([]SimilarResult, error) {
	req := similarRequest{
		CaseSummary: caseSummary,
		K:           k,
		CaseType:    filters.CaseType,
		Court:       filters.Court,
		DateFrom:    filters.DateFrom,
		DateTo:      filters.DateTo,
	}
	var out similarResponse
	if err := c.postJSON(ctx, "/similar", req, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

type classifyKNNRequest struct {
	CaseSummary string `json:"case_summary"`
	K           int    `json:"k"`
}

type ClassifyEvidence struct {
	RulingID   string  `json:"ruling_id"`
	ExternalID string  `json:"external_id"`
	Similarity float64 `json:"similarity"`
}

type ClassifyKNNResult struct {
	CaseType   *string            `json:"case_type"`
	Confidence float64            `json:"confidence"`
	Evidence   []ClassifyEvidence `json:"evidence"`
}

// ClassifyKNN runs UC2's local kNN classification signal (similarity-weighted
// vote of nearest tagged rulings' case_type).
func (c *Client) ClassifyKNN(ctx context.Context, caseSummary string, k int) (ClassifyKNNResult, error) {
	var out ClassifyKNNResult
	err := c.postJSON(ctx, "/classify-knn", classifyKNNRequest{CaseSummary: caseSummary, K: k}, &out)
	return out, err
}

type riskScoreRequest struct {
	Text string `json:"text"`
	K    int    `json:"k"`
}

type RiskNeighbor struct {
	RulingID     string  `json:"ruling_id"`
	ExternalID   string  `json:"external_id"`
	Outcome      string  `json:"outcome"`
	RevertReason *string `json:"revert_reason"`
	Similarity   float64 `json:"similarity"`
}

type RiskScoreResult struct {
	Risk       *float64       `json:"risk"`
	Bucket     *string        `json:"bucket"`
	SampleSize int            `json:"sample_size"`
	Caveat     *string        `json:"caveat"`
	Neighbors  []RiskNeighbor `json:"neighbors"`
}

// RiskScore runs UC5's local score signal (similarity-weighted reverted
// ratio over the text's nearest outcome-tagged rulings, see
// ml/risk/score.py) — no LLM call. text is typically a case summary or
// draft; drafts (UC4) don't exist yet, so callers today pass the same
// case-chunk-text stand-in /similar and /classify-knn use.
func (c *Client) RiskScore(ctx context.Context, text string, k int) (RiskScoreResult, error) {
	var out RiskScoreResult
	err := c.postJSON(ctx, "/risk-score", riskScoreRequest{Text: text, K: k}, &out)
	return out, err
}

type revertedNeighborsRequest struct {
	Text string `json:"text"`
	K    int    `json:"k"`
}

type RevertedNeighbor struct {
	RulingID     string  `json:"ruling_id"`
	ExternalID   string  `json:"external_id"`
	RevertReason *string `json:"revert_reason"`
	Similarity   float64 `json:"similarity"`
}

type revertedNeighborsResponse struct {
	Neighbors []RevertedNeighbor `json:"neighbors"`
}

// RevertedNeighbors fetches the nearest rulings tagged `reverted` to text
// (see ml/risk/score.py's nearest_reverted) — backs the UC5 risk_explain
// package's {{reverted_neighbors}} context, narrower than RiskScore's mixed
// upheld/reverted neighbor set.
func (c *Client) RevertedNeighbors(ctx context.Context, text string, k int) ([]RevertedNeighbor, error) {
	var out revertedNeighborsResponse
	if err := c.postJSON(ctx, "/reverted-neighbors", revertedNeighborsRequest{Text: text, K: k}, &out); err != nil {
		return nil, err
	}
	return out.Neighbors, nil
}

type buildPackageRequest struct {
	UseCase string         `json:"use_case"`
	Context map[string]any `json:"context"`
}

// PackageBundle is a fully-assembled prepared package (plan.md §5): the
// rendered prompt, the raw context it was rendered from, and the output
// schema the harness response must satisfy. The API server persists this
// verbatim into the packages.bundle jsonb column.
type PackageBundle struct {
	PackageID     string          `json:"package_id"`
	UseCase       string          `json:"use_case"`
	PromptVersion int             `json:"prompt_version"`
	CreatedAt     string          `json:"created_at"`
	Prompt        string          `json:"prompt"`
	Context       json.RawMessage `json:"context"`
	OutputSchema  json.RawMessage `json:"output_schema"`
}

// BuildPackage assembles a prepared package for useCase from context (whose
// keys must match the use case's prompt template placeholders — see
// ml/prompts/*.md).
func (c *Client) BuildPackage(ctx context.Context, useCase string, packageContext map[string]any) (PackageBundle, error) {
	var out PackageBundle
	err := c.postJSON(ctx, "/package-build", buildPackageRequest{UseCase: useCase, Context: packageContext}, &out)
	return out, err
}
