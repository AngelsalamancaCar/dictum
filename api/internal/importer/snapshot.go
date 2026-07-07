package importer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// WriteSnapshot writes rulings as canonical-format NDJSON to a timestamped
// file under outDir. This snapshot — not the embeddings, which are cheaply
// re-derivable any time from the locked model — is the reproducible
// artifact: plan.md's goal is a corpus "reproducible from archive + tags,
// never dependent on Labelbox again," so what needs versioning is the
// canonical text+tags, not source-specific export formats.
func WriteSnapshot(rulings []Ruling, outDir string) (string, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", err
	}

	name := fmt.Sprintf("snapshot_%s.ndjson", time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(outDir, name)

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range rulings {
		r.Normalize()
		if err := enc.Encode(r); err != nil {
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	return path, nil
}
