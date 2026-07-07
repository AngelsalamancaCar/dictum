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
	reqBody, err := json.Marshal(embedRequest{Texts: texts, Kind: kind})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embed", bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ml /embed: status %d: %s", resp.StatusCode, b)
	}

	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Embeddings, nil
}
