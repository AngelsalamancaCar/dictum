package adapters

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"dictum/api/internal/importer"
)

// LoadFolderCSV reads a directory of .txt files plus a CSV of tags and
// produces canonical Rulings. This is the recommended path for the one-time
// corpus load once a grading pass has produced tags (plan.md §4 UC6).
//
// CSV header (order-independent, extra columns ignored):
//
//	filename,case_type,outcome,revert_reason,court,date
//
// Every .txt file in folderPath is included even if the CSV has no matching
// row for it — untagged rows still import (outcome defaults to "pending"),
// so partial grading progress isn't a blocker; DryRun's Untagged count
// surfaces how much tagging work remains.
func LoadFolderCSV(folderPath, csvPath string) ([]importer.Ruling, error) {
	tags, err := readTagsCSV(csvPath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(folderPath)
	if err != nil {
		return nil, err
	}

	var rulings []importer.Ruling
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".txt" {
			continue
		}
		text, err := os.ReadFile(filepath.Join(folderPath, e.Name()))
		if err != nil {
			return nil, err
		}

		r := importer.Ruling{ExternalID: e.Name(), Text: string(text)}
		if t, ok := tags[e.Name()]; ok {
			r.CaseType = t.CaseType
			r.Outcome = t.Outcome
			r.RevertReason = t.RevertReason
			r.Court = t.Court
			r.Date = t.Date
		}
		rulings = append(rulings, r)
	}
	return rulings, nil
}

type csvTags struct {
	CaseType     string
	Outcome      string
	RevertReason string
	Court        string
	Date         string
}

func readTagsCSV(path string) (map[string]csvTags, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("reading CSV header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, h := range header {
		col[h] = i
	}
	for _, required := range []string{"filename"} {
		if _, ok := col[required]; !ok {
			return nil, fmt.Errorf("CSV missing required column %q", required)
		}
	}

	get := func(row []string, name string) string {
		if i, ok := col[name]; ok && i < len(row) {
			return row[i]
		}
		return ""
	}

	tags := make(map[string]csvTags)
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		filename := get(row, "filename")
		tags[filename] = csvTags{
			CaseType:     get(row, "case_type"),
			Outcome:      get(row, "outcome"),
			RevertReason: get(row, "revert_reason"),
			Court:        get(row, "court"),
			Date:         get(row, "date"),
		}
	}
	return tags, nil
}
