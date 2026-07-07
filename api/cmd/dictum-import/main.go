// Command dictum-import runs the one-time UC6 corpus load: validate -> dry-run
// report -> embed locally -> upsert by external_id -> emit a versioned
// snapshot. See plan.md §4 UC6 and §7 (this lives under api/cmd rather than
// a separate cli/ module because Go's internal-package visibility rules
// would otherwise block it from importer/store/mlclient).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"dictum/api/internal/importer"
	"dictum/api/internal/importer/adapters"
	"dictum/api/internal/mlclient"
	"dictum/api/internal/store"
)

func main() {
	adapter := flag.String("adapter", "", "source adapter: labelbox|foldercsv")

	manifest := flag.String("manifest", "", "labelbox: path to manifest.json")
	corpusDir := flag.String("corpus-dir", "", "labelbox: base dir to resolve manifest file paths")
	excludePrefixes := flag.String("exclude-prefix", "prompt_", "labelbox: comma-separated external_id prefixes to skip")

	folder := flag.String("folder", "", "foldercsv: directory of .txt files")
	csvPath := flag.String("csv", "", "foldercsv: path to tags CSV")

	dryRun := flag.Bool("dry-run", false, "validate and report only; no DB writes, no embedding")
	snapshotDir := flag.String("snapshot-dir", "corpus_archive/snapshots", "directory for the versioned canonical-format snapshot")
	dsn := flag.String("db", os.Getenv("DATABASE_URL"), "Postgres DSN (default: $DATABASE_URL)")
	mlURL := flag.String("ml-url", envOr("ML_WORKER_URL", "http://localhost:8000"), "dictum-ml base URL")

	flag.Parse()

	rulings, err := loadRulings(*adapter, *manifest, *corpusDir, *excludePrefixes, *folder, *csvPath)
	if err != nil {
		log.Fatalf("loading rulings: %v", err)
	}

	report := importer.DryRun(rulings)
	printReport(report)

	if len(report.ValidationIssues) > 0 {
		fmt.Fprintln(os.Stderr, "\nvalidation issues found; fix these before importing:")
		for _, issue := range report.ValidationIssues {
			fmt.Fprintln(os.Stderr, " ", issue.String())
		}
		os.Exit(1)
	}

	if *dryRun {
		fmt.Println("\ndry-run: no changes made")
		return
	}

	ctx := context.Background()

	db, err := store.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("connecting to database: %v", err)
	}
	defer db.Close()

	ml := mlclient.New(*mlURL)

	fmt.Println("\nembedding and upserting...")
	if err := importer.Import(ctx, ml, db, rulings); err != nil {
		log.Fatalf("import failed: %v", err)
	}

	snapshotPath, err := importer.WriteSnapshot(rulings, *snapshotDir)
	if err != nil {
		log.Fatalf("writing snapshot: %v", err)
	}
	fmt.Printf("done. %d rulings imported. snapshot: %s\n", len(rulings), snapshotPath)
}

func loadRulings(adapter, manifest, corpusDir, excludePrefixes, folder, csvPath string) ([]importer.Ruling, error) {
	switch adapter {
	case "labelbox":
		if manifest == "" || corpusDir == "" {
			return nil, fmt.Errorf("labelbox adapter requires -manifest and -corpus-dir")
		}
		var prefixes []string
		for p := range strings.SplitSeq(excludePrefixes, ",") {
			if p = strings.TrimSpace(p); p != "" {
				prefixes = append(prefixes, p)
			}
		}
		return adapters.LoadLabelboxManifest(manifest, corpusDir, prefixes)

	case "foldercsv":
		if folder == "" || csvPath == "" {
			return nil, fmt.Errorf("foldercsv adapter requires -folder and -csv")
		}
		return adapters.LoadFolderCSV(folder, csvPath)

	default:
		return nil, fmt.Errorf("unknown -adapter %q (want labelbox|foldercsv)", adapter)
	}
}

func printReport(r importer.DryRunReport) {
	fmt.Printf("total rulings: %d\n", r.Total)
	fmt.Printf("untagged (no case_type): %d\n", r.Untagged)
	fmt.Println("by outcome:")
	for outcome, n := range r.ByOutcome {
		fmt.Printf("  %-10s %d\n", outcome, n)
	}
	if len(r.ByCaseType) > 0 {
		fmt.Println("by case_type:")
		for ct, n := range r.ByCaseType {
			fmt.Printf("  %-30s %d\n", ct, n)
		}
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
