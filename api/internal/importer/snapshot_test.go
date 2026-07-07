package importer

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteSnapshot_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	rulings := []Ruling{
		{ExternalID: "a", Text: "texto a", CaseType: "despido injustificado"},
		{ExternalID: "b", Text: "texto b"}, // no outcome, should normalize to pending on write
	}

	path, err := WriteSnapshot(rulings, filepath.Join(dir, "snapshots"))
	if err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening snapshot: %v", err)
	}
	defer f.Close()

	var got []Ruling
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Ruling
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("decoding line: %v", err)
		}
		got = append(got, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scanning snapshot: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(got))
	}
	if got[1].Outcome != OutcomePending {
		t.Fatalf("expected second ruling normalized to pending, got %q", got[1].Outcome)
	}
}
