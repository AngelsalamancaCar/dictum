# Dictum — Implementation Plan

Web application (Go + Python) that assists judges/legal staff working Mexican labor-court cases (*procedimiento especial laboral*): ingests case folders, classifies the case type (litis), retrieves similar rulings, drafts proposed rulings, and grades revert risk against rulings overturned on appeal.

All documents are in Spanish. All ML runs locally except LLM inference, which is executed by an external harness agent in a private service via **prepared packages** (see §5).

---

## 1. Core decisions (locked)

| Decision | Choice |
|---|---|
| Document language | Spanish (Mexican labor law) |
| Parsing | LiteParse (run-llama/liteparse) — local, Apache 2.0, Python bindings; PDF direct, Office via LibreOffice, images via ImageMagick |
| Embeddings | Local sentence-transformers, multilingual model. **Locked**: `intfloat/multilingual-e5-large` (1024-d) — see `ml/spikes/embedding_benchmark_report.md`. Inputs must use E5's `"query: "` / `"passage: "` prefix convention. Labelbox-supplied MPNet vectors are discarded — English-only model, wrong space |
| LLM access | **No LLM SDK calls in the app.** App builds prepared packages (versioned prompts + context + output schema), stores them in the database, and hands them to a harness agent running in a private service. Results return async |
| Vector store | Postgres + pgvector (single datastore for v1; Qdrant only if scale demands) |
| Corpus load | One-time import for training + vector DB prep. CLI tool, not a web flow. Labelbox NDJSON is one adapter; canonical format is dictum-native NDJSON |
| Corpus archive | `corpus_archive/` — 115 texts fetched 2026-07-07 from Labelbox before signed URLs expired. ~111 usable sentencias; `prompt_*.txt` experiment files excluded at import |

## 2. Architecture

```
 Browser (SPA, embedded in Go binary)
      │ HTTPS/JSON + SSE (job progress)
 ┌────▼─────────────────────────────────┐
 │ GO API SERVER  (dictum-api)          │  auth, case CRUD, folder intake,
 │  - REST API + static frontend        │  job orchestration, package
 │  - job queue (goroutines + Postgres) │  lifecycle management, audit log
 └────┬─────────────────────────────────┘
      │ internal HTTP + shared object storage
 ┌────▼─────────────────────────────────┐
 │ PYTHON ML WORKER  (dictum-ml)        │  FastAPI:
 │  - LiteParse parsing                 │  /parse /embed /classify-knn
 │  - sentence-transformers embeddings  │  /similar /risk-score
 │  - retrieval (pgvector + FTS)        │  /package-build
 │  - prepared-package builder          │
 └────┬─────────────────────────────────┘
      │
 ┌────▼──────────────────────┐      ┌──────────────────────────┐
 │ POSTGRES (+ pgvector)     │      │ HARNESS AGENT (external, │
 │ metadata, chunks, vectors,│ ◄──► │ private service) — runs  │
 │ prepared packages, results│      │ prepared LLM packages    │
 └───────────────────────────┘      └──────────────────────────┘
```

**Split rationale.** Go owns everything user-facing and stateful: fast single binary, strong concurrency for orchestration, embeds the frontend. Python owns everything ML: LiteParse bindings, sentence-transformers, retrieval, package assembly. Contract between them is a small internal API plus shared storage; either side is replaceable.

**Degradation property.** Parsing, embedding, kNN retrieval, and the numeric risk score are all local and synchronous — they work with the harness offline. LLM-dependent steps (classification rationale, "why similar" notes, draft generation, risk explanations) queue as packages and complete when the harness returns results.

## 3. Data model (Postgres)

| Table | Purpose / key columns |
|---|---|
| `cases` | id, name, status, detected_case_type, typology_confidence, created_at |
| `documents` | case_id, filename, sha256, parse_status; LiteParse output (text + bounding boxes) in object storage |
| `chunks` | document_id, text, section_label, `embedding vector(N)` — HNSW index |
| `rulings` | reference corpus: external_id, full text, case_type, **outcome enum: upheld / reverted / pending**, revert_reason, court, date, tags JSONB, embedding |
| `typologies` | case-type catalog: name, description, discriminating features, exemplar ruling ids |
| `packages` | prepared LLM packages — see §5 |
| `package_results` | package_id, raw response, validated payload JSONB, received_at, validation_status |
| `drafts` | case_id, package_id (provenance), generated text, cited ruling ids, prompt version, edit history |
| `risk_reports` | case_id / draft_id, risk grade, neighbor ruling ids + similarities, explanation package_id |
| `audit_log` | actor, action, entity, timestamp — every generation and override recorded |

One `rulings` table serves both RAGs: the "reverted rulings RAG" is a filtered view (`outcome = 'reverted'`). Simpler, and risk scoring compares reverted vs upheld neighbors drawn from the same distribution.

## 4. Use cases

### UC1 — Folder ingestion + parsing
User points the app at a folder (upload or server-side path). Go walks it, dedupes by sha256, creates `documents`, enqueues parse jobs. Python worker runs LiteParse per file; scanned-image PDFs may need a Tesseract OCR fallback (verify in spike). Output is chunked section-aware (~512 tokens, overlap), embedded, stored. SSE progress to the UI.

### UC2 — Case typology (litis) classification
Known litis categories include: despido injustificado, rescisión de contrato, pago de utilidades, pago de horas extra. Two signals:
- **Local**: kNN vote — case chunk-centroid vs typology exemplars. Instant.
- **LLM**: prepared package (`classify` prompt + case summary + typology catalog) → harness → type, confidence, evidence quotes.

UI shows both; user confirms or overrides. Confirmations accumulate as training signal. Typology catalog is seeded from the imported corpus's `case_type` tags.

### UC3 — Similar-ruling retrieval (RAG)
Query = case summary embedding + metadata filters (case_type, court, date range). Hybrid retrieval: pgvector kNN + Postgres full-text search → reciprocal rank fusion. Results show similarity, outcome badge (upheld/reverted), metadata. Optional LLM package per result set writes grounded "why similar" notes.

### UC4 — Draft ruling generation
Input: parsed case docs (UC1), confirmed type (UC2), top-N similar rulings — preferably upheld — from UC3. Package builder assembles the `draft` prompt, case-fact extract, typology structure template, and exemplar rulings with citations into a prepared package. Harness returns the draft; stored in `drafts` with full provenance (package id, prompt version, cited rulings). Versioned; user edits or regenerates sections (each regeneration = new package). Framed strictly as an assistive draft — the human judge owns the output; audit trail is built in from day one.

### UC5 — Revert-risk grading
Two layers:
- **Score (local, instant)**: embed the draft, retrieve top-k neighbors from `rulings`, compute similarity-weighted reverted ratio `risk = Σ(simᵢ·revertedᵢ) / Σ(simᵢ)`, bucket into low/medium/high. Display a sample-size caveat until the corpus grows (111 rulings is thin; calibrate buckets once volume allows).
- **Explanation (package)**: nearest reverted rulings + their `revert_reason` tags → harness explains which characteristics of the draft resemble reverted patterns.

Report = grade + neighbor list + reasons, stored in `risk_reports`.

### UC6 — Corpus import (one-time, CLI)
`dictum-import` CLI, not a web flow.
- **Canonical format** — dictum-native NDJSON, one ruling per line:
  ```json
  {"external_id": "...", "text": "... | file: path", "case_type": "...",
   "outcome": "upheld|reverted|pending", "revert_reason": "...",
   "court": "...", "date": "...", "tags": {}}
  ```
- **Adapters** → canonical: `labelbox` (existing export format), `folder+csv` (directory of .txt + spreadsheet of tags — recommended for the one-time load). If a real annotation UI is ever needed, self-hosted **Label Studio** (open source, fits the private-service posture) over Labelbox/Argilla subscriptions.
- Pipeline: validate → dry-run report → embed locally → upsert by external_id → emit **versioned corpus snapshot** (vector DB reproducible from archive + tags, never dependent on Labelbox again).

**Open gap:** archived texts carry no tags — the Labelbox export had zero annotations. `case_type`, `outcome`, and `revert_reason` must come from wherever grading happened, or from a grading pass over the 111 sentencias. This is the critical path for UC2/UC3/UC5: without outcome labels there is no revert-risk signal.

## 5. Prepared packages — format, storage, lifecycle

### Bundle format
```
package/
  manifest.json      # package id, use case, prompt version, model hints, created_at
  prompts/           # predeveloped specialized prompts, versioned in this repo
    classify.md | draft.md | risk_explain.md | similar_explain.md
  context/           # payload assembled per job
    case_summary.json, retrieved_rulings.json, typology_catalog.json, ...
  output_schema.json # JSON Schema the harness response must satisfy
```
Prompt files live in the repo (`ml/prompts/`), version-stamped; every package records which prompt version it embeds.

### Database-managed storage
Packages are first-class records the app manages — not loose files.

`packages` table: id, case_id, use_case (classify/draft/risk_explain/similar_explain), prompt_version, **status** (`draft → ready → submitted → completed / failed / cancelled`), bundle (JSONB for manifest/context/schema + bytea or object-store ref for the packed archive), created_by, submitted_at, completed_at, error, retry_of (package id lineage on resubmission).

Management UI (admin section of the SPA):
- List/filter packages by case, use case, status, prompt version.
- Inspect full bundle contents (prompts rendered, context payload, expected schema).
- Download packed archive for manual hand-off to the harness; mark submitted.
- Attach/ingest results (paste/upload or automatic if the harness has a return endpoint); validation against `output_schema.json` runs on ingest, failures flagged.
- Resubmit with a newer prompt version (creates a linked new package, preserves lineage).
- Retention policy configurable; completed bundles kept for provenance/audit.

### Harness interface (decision still open)
Submission and return paths depend on the harness intake spec: filesystem drop, queue, or HTTP endpoint. The `packages` table and status machine are designed so any of the three plugs in; worst case is fully manual download/upload through the management UI.

## 6. Frontend & branding

Modern, credible, calm — a professional tool for judicial staff, not a consumer app.

- **Stack**: SPA (React + TypeScript + Vite), embedded in the Go binary via `embed.FS`. Tailwind with a design-token layer (colors, spacing, type scale defined once).
- **Look**: clean neutral surfaces, one restrained accent color, generous whitespace, strong typographic hierarchy (a modern grotesque for UI, readable serif optional for ruling text). Light and dark themes from day one via tokens.
- **Language**: UI copy in Spanish.
- **Key screens**: case dashboard (pipeline status per case), document viewer (parsed text with layout), classification review (both signals side by side), similar-rulings explorer (similarity + outcome badges), draft editor (versioned, citations inline), risk report (grade with neighbor evidence), package manager (admin).
- **Signature elements**: outcome badges (upheld/reverted), risk gauge with explicit sample-size caveat, provenance chips on every generated artifact (prompt version, package id, cited rulings).
- Load the `frontend-design` and `dataviz` skills when building UI and risk visualizations.

## 7. Repository layout

```
dictum/
  api/            # Go: cmd/dictum-api, internal/{http,jobs,store,packages,importer}
  ml/             # Python: FastAPI app; parsing/, rag/, classify/, risk/, packager/
    prompts/      # versioned specialized prompt files
  web/            # React SPA (embedded via embed.FS)
  api/cmd/dictum-import/  # the "cli/" tool — lives under api/cmd, not a separate cli/ module:
                          # Go internal-package visibility would block a sibling module from
                          # importing api/internal/{importer,store,mlclient}
  migrations/
  corpus_archive/ # rescued Labelbox texts + manifest (do not modify)
  docker-compose.yml   # postgres+pgvector, api, ml
  plan.md
```

## 8. Phases

1. **Spike** — ✅ embedding benchmark (locked `multilingual-e5-large`, see `ml/spikes/embedding_benchmark_report.md`); ✅ LiteParse validated on synthetic PDF from real sentencia text, correct Spanish text + bounding-box extraction (see `ml/spikes/liteparse_spike.md`) — real scanned-PDF/OCR validation still open, see item 4 below; ✅ package bundle format scaffolded (`ml/prompts/`, `ml/packager/bundle.py`) — still needs a walkthrough against the actual harness intake spec once that's known (item 1 below).
2. **Core pipeline** — ✅ schema + migrations (`migrations/0001_init.sql`); ✅ UC1 ingest end-to-end — Go folder walker (sha256 dedupe) → job queue → ML `/parse` (LiteParse) → chunking → `/embed` (e5-large, `passage:` prefix) → Postgres, with SSE progress over `GET /api/cases/{id}/events`; verified with unit tests (fakes) at each layer plus one live integration test exercising the real Go↔Python wire contract (`api/internal/mlclient/integration_test.go`, `-tags integration`).
   ✅ UC6 importer (`api/cmd/dictum-import`) — labelbox adapter (`internal/importer/adapters/labelbox.go`, reads `corpus_archive/manifest.json` since the raw Labelbox NDJSON only carries expiring signed URLs and zero annotations) and folder+csv adapter; `Validate`/`DryRun` reporting; `/chunk` + `/embed` (mean-pooled per ruling, since `rulings.embedding` is one vector but sentencias exceed a single embed call's effective context) → `UpsertRuling` by external_id → versioned canonical-format NDJSON snapshot. Found and fixed a real data issue in dry-run against the real archive: 3 manifest rows were the same file cataloged under two Labelbox datasets — deduped by external_id. **The corpus is now actually loaded**: ran the real (non-dry-run) import against a live Postgres — all 111 rulings have `full_text` + `embedding` populated (`migrations/0001_init.sql` applied cleanly; a versioned snapshot was written to `corpus_archive/snapshots/`). All 111 are still untagged (`case_type`/`outcome`/`revert_reason` all missing) — that part still needs the grading pass (item 2 below) before UC2/UC3 have real signal to work with, but the loading pipeline itself is done and proven.
   - **Known gap**: SSE is fire-and-forget — a client that subscribes to `/events` after a document's pipeline already finished misses those events permanently (no persisted event log, no case/document status GET endpoint yet). Document `parse_status` is durable in Postgres, so a status-polling fallback is the fix; not built yet since there's no case-detail read endpoint at all.
   - **Known simplification**: `documents.object_ref` currently stores the parsed-pages JSON inline (as text) rather than a pointer into real object storage — fine at current scale, revisit if page payloads get large or object storage gets built for other reasons.
3. **Retrieval** — ✅ UC3 hybrid retrieval: `ml/rag/retrieval.py` (pgvector cosine kNN + Postgres Spanish full-text search, combined by reciprocal rank fusion) behind `/similar`; ✅ UC2 local kNN signal: `ml/classify/knn.py` (similarity-weighted vote over a wider candidate pool's tagged neighbors, since the archive is mostly untagged and a naive top-k fetch would starve the vote) behind `/classify-knn`. Both proxied through new Go routes (`GET /api/cases/{id}/similar-rulings`, `GET /api/cases/{id}/classification`) that build a case "summary" from its stored chunk text.
   - **Verified live**, not just with fakes: brought up the full docker-compose stack for the first time this project (found and fixed two real bugs doing so — a Go base image older than `go.mod` required, and `liteparse` missing from `pyproject.toml` despite being used in code) and ran the real corpus import against it. Live-tested `/similar`/`/classify-knn` against the real 111-ruling corpus; caught and fixed a genuine bug (`psycopg.errors.AmbiguousParameter` — Postgres can't infer a bare NULL parameter's type in an `IS NULL` filter clause without an explicit cast). Since the real corpus is 100% untagged, temporarily tagged one real already-imported ruling (matching a synthetic test case's exact content) to get a clean, realistic signal-bearing test rather than relying only on short synthetic text (which turned out to embed very differently from full mean-pooled documents — a real finding, not a bug: full sentencias cluster together via shared boilerplate in a way short one-off sentences don't). Confirmed correct classification (right case_type, 0.96 similarity) and correct top-ranked similar result end-to-end through the real Go→ML→Postgres path, then reverted the temporary tag and deleted all test rows/cases so the DB reflects only real, honestly-labeled state.
   - UC2's LLM signal (prepared package) is Phase 4 scope, not built here.
4. **Packages + generation** — `packages` table + status machine + management UI; package builder; UC2 LLM signal, UC4 drafts, UC5 score + explanation.
5. **Hardening** — auth, audit coverage, risk-bucket calibration, eval harness (golden set with known type/outcome; measure classification accuracy and retrieval precision each release), branding polish.

## 9. Open items

1. **Harness intake spec** — how packages are submitted and results returned (folder drop / queue / HTTP). Blocks Phase 4 automation only; manual hand-off works meanwhile.
2. **Grading tags for the archived corpus** — case_type / outcome / revert_reason per sentencia. Blocks Phase 2 corpus load.
3. ~~**Embedding model final pick**~~ — resolved: `multilingual-e5-large`, `vector(1024)` locked in `migrations/0001_init.sql`.
4. **OCR fallback need** — LiteParse's PDF-direct + bounding-box extraction validated (`ml/spikes/liteparse_spike.md`), but no raw scanned PDFs exist yet to exercise the Tesseract OCR path end-to-end. Tesseract + `tesseract-ocr-spa` added to `ml/Dockerfile` on the strength of the library docs; get one real scanned case PDF before UC1 ingestion work to close this out. LibreOffice (Office docs) and ImageMagick (images) paths are also unexercised.
