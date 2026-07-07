// Package adapters converts source-specific export formats into the
// dictum-native canonical Ruling format (importer.Ruling).
package adapters

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"dictum/api/internal/importer"
)

// manifestEntry mirrors one row of corpus_archive/manifest.json, which was
// produced when the Labelbox export's texts were fetched to local disk
// (the raw Labelbox NDJSON export only carries a signed, expiring URL per
// row and no annotations — the manifest is the actual join between a
// Labelbox external_id and its local text file).
type manifestEntry struct {
	ExternalID string `json:"external_id"`
	LabelboxID string `json:"labelbox_id"`
	File       string `json:"file"`
	Bytes      int    `json:"bytes"`
	SHA256     string `json:"sha256"`
	Dataset    string `json:"dataset"`
}

type manifest struct {
	Source    string          `json:"source"`
	FetchedAt string          `json:"fetched_at"`
	Rows      []manifestEntry `json:"rows"`
}

// LoadLabelboxManifest reads corpus_archive/manifest.json plus its texts/
// files and produces canonical Rulings. Entries whose filename starts with
// any of excludePrefixes are skipped (the archive's manifest.json notes a
// handful of prompt_*.txt experiment files mixed into the export that
// aren't real sentencias).
//
// No case_type/outcome/revert_reason tags are set — the source export has
// no annotations (verified against the raw NDJSON: zero rows have non-empty
// projects/annotations/labels fields). Callers must apply tags via a
// separate grading pass before these rulings are useful for UC2/UC3/UC5.
//
// A single ruling can appear as multiple manifest rows when Labelbox
// catalogs the same file under more than one dataset (observed in the real
// archive: identical sha256/bytes, different dataset name) — those are
// deduped here by external_id, keeping the first row's content and merging
// dataset names for provenance rather than emitting duplicate rulings.
func LoadLabelboxManifest(manifestPath string, corpusDir string, excludePrefixes []string) ([]importer.Ruling, error) {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}

	var rulings []importer.Ruling
	index := map[string]int{} // external_id -> index into rulings

	for _, row := range m.Rows {
		if hasAnyPrefix(row.ExternalID, excludePrefixes) {
			continue
		}

		if i, ok := index[row.ExternalID]; ok {
			existing := rulings[i].Tags["dataset"]
			if !strings.Contains(existing, row.Dataset) {
				rulings[i].Tags["dataset"] = existing + "," + row.Dataset
			}
			continue
		}

		text, err := os.ReadFile(filepath.Join(corpusDir, row.File))
		if err != nil {
			return nil, err
		}

		index[row.ExternalID] = len(rulings)
		rulings = append(rulings, importer.Ruling{
			ExternalID: row.ExternalID,
			Text:       string(text),
			Tags: map[string]string{
				"labelbox_id": row.LabelboxID,
				"dataset":     row.Dataset,
			},
		})
	}
	return rulings, nil
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
