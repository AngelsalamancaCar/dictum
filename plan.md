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
4. **Packages + generation** — in progress.
   ✅ **Package plumbing** (backend, no UI yet): `ml/packager/bundle.py` now does real placeholder substitution (`render_prompt`: `{{key}}` → context value, string verbatim / structured-as-JSON, raises listing every missing placeholder rather than shipping an unrendered `{{...}}` token) and `build_bundle` returns an in-memory dict (no more loose files) for the Go side to persist; `pack_bundle` reconstitutes the plan.md §5 on-disk layout as an in-memory zip for the download/hand-off path. Wired behind `POST /package-build` in `ml/app.py`.
   Go owns the actual `packages`/`package_results` table CRUD and the status machine (`api/internal/store/packages.go`, `api/internal/packages/validate.go` for JSON-Schema validation of harness responses via `santhosh-tekuri/jsonschema/v5`) and lifecycle routes in `api/internal/http/router/router.go`: `POST /api/cases/{id}/packages` (calls ML `/package-build`, stores bundle in status `ready`), `GET /api/packages` (filter by case/use_case/status), `GET /api/packages/{id}`, `GET /api/packages/{id}/archive` (zip, built in Go from the stored bundle — no round-trip to ML needed), `POST /api/packages/{id}/submit`, `POST /api/packages/{id}/results` (validates against the bundle's stored `output_schema`; only a *valid* result transitions the package to `completed` — an invalid one is still recorded for visibility but leaves the package `submitted` so a corrected result can be attached later), `POST /api/packages/{id}/resubmit` (rebuilds from the original's stored context, links via `retry_of`, only legal from a terminal status), `POST /api/packages/{id}/cancel`. Illegal status transitions (e.g. double-submit, resubmitting a `ready` package) return 409, matching the router's existing `StatusConflict` convention. Covered by `api/internal/packages/validate_test.go` and new fake-store-backed cases in `api/internal/http/router/router_test.go`.
   **Deliberately deferred** (this was plumbing only, not UC-specific work): the create-package endpoint takes `context` as-is from the caller rather than assembling it — building the actual `{{case_summary}}`/`{{typology_catalog}}`/`{{exemplar_rulings}}`/etc. payloads per use case is UC2/UC4/UC5 scope, still open. No admin management UI (`web/` doesn't exist yet at all — see plan.md §6). `MarkPackageFailed` exists in the store but nothing calls it yet; there's no endpoint for the harness to report failure, since item 9.1 (harness intake spec) is still unresolved.
   ✅ **UC5 score** (local, no package — no LLM/harness dependency): `ml/risk/score.py` — `compute_risk`/`risk_bucket` are pure functions (`risk = Σ(simᵢ·revertedᵢ) / Σ(simᵢ)`, bucketed low/medium/high at simple tertile thresholds pending real calibration) over already-fetched neighbors, tested without a DB; `score_by_knn` fetches a wide candidate pool and scores over the first k outcome-tagged (`upheld`/`reverted`, excluding `pending`) neighbors within it — same starved-vote fix as `classify/knn.py`'s `weighted_vote`, applied to outcome instead of case_type, since the corpus is still 100% untagged (plan.md §9 item 2). Carries a `caveat` string whenever fewer than `MIN_SAMPLE_SIZE` (20) tagged neighbors are found, satisfying the "sample-size caveat" requirement from §4 UC5. Wired behind `POST /risk-score` in `ml/app.py` (was a `NotImplementedError` stub). Go proxies it the same way as UC2/UC3 — new `GET /api/cases/{id}/risk-score` route (`mlclient.RiskScore`, `router.handleRiskScore`) scores the case's stored chunk text as a stand-in, since there's no `drafts` table yet to score a real draft against. Not persisted to `risk_reports` — like `/similar-rulings`/`/classification`, it's a read-only computed proxy, no write path exists for any of the three yet.
   **Verified live**: brought up docker compose (postgres+pgvector, ml, api all healthy), fabricated a synthetic case whose single chunk's text and embedding exactly match one real archived ruling, temporarily tagged that ruling `reverted` and six others `upheld`/`reverted` (leaving the rest `pending`), and called the real `GET /api/cases/{id}/risk-score` end to end. The exact-match ruling correctly surfaced as the top neighbor (0.937 similarity, `reverted`), all 7 tagged neighbors were returned with correct outcomes, the weighted risk came back 0.429 → `medium`, and the thin-sample caveat fired (7 < `MIN_SAMPLE_SIZE`). Reverted all ruling tags to `pending` and deleted the test case afterward so the DB reflects only real, honestly-untagged state.
   ✅ **UC2 LLM signal — context assembly**: seeded the previously-empty `typologies` table (`migrations/0002_seed_typologies.sql`) with the four litis categories named in §4 UC2 (despido injustificado, rescisión de contrato, pago de utilidades, pago de horas extra) plus Spanish descriptions and discriminating features; `exemplar_ruling_ids` left empty since there's still no case_type-tagged ruling to link (§9 item 2). New `api/internal/store/typologies.go` (`ListTypologies`) and a dedicated `POST /api/cases/{id}/packages/classify` route (`router.handleCreateClassifyPackage`) assemble the classify package's context itself — `case_summary` from the same case-chunk-text stand-in as UC3/UC5, `typology_catalog` shaped by a purpose-built `typologyCatalogEntry` DTO (not the bare `store.Typology`, which carries no json tags per the existing struct convention) — then build and persist through the existing `BuildPackage`/`saveNewPackage` path unchanged. The original generic `POST /api/cases/{id}/packages` (caller-supplied context) is untouched; this is a second, additive route for the one use case whose assembly is now real. Covered by new router tests (`TestHandleCreateClassifyPackage_AssemblesContextFromStore`, `..._NoParsedText`).
   **Verified live**: applied `0002_seed_typologies.sql` by hand against the already-initialized compose volume (docker's `docker-entrypoint-initdb.d` only runs against an empty data directory, so a new migration file on an existing volume needs a manual `psql -f`, unlike a fresh `docker compose up` from scratch); confirmed all 4 typologies loaded. Fabricated a synthetic case (chunk text: a despido-injustificado fact pattern, real e5-large embedding) and called the real `POST /api/cases/{id}/packages/classify` end to end — got back a `ready` package whose rendered prompt correctly interpolated the live case text and all 4 catalog entries with intact Spanish diacritics (confirmed via raw byte inspection — an earlier apparent mojibake was traced to `python -m json.tool`'s pretty-printing in this shell, not the app; the actual HTTP response bytes were correct UTF-8 throughout). Deleted the test case afterward (`packages.case_id` cascades) so the DB reflects only real state.
   ✅ **UC5 explanation package**: `ml/rag/retrieval.py`'s `vector_search` now selects `revert_reason` (previously omitted) so it flows through to every caller's neighbor dicts; `ml/risk/score.py` adds `_filter_reverted` (pure, tested) and `nearest_reverted` (DB-touching, mirrors `score_by_knn`'s wide-candidate-pool pattern but keeps only `outcome == 'reverted'` rather than either tagged outcome, since an explanation only ever cites rulings that were actually overturned), wired behind a new `POST /reverted-neighbors` in `ml/app.py`. `/risk-score`'s neighbor payload also now carries `revert_reason`. Go: `mlclient.RevertedNeighbors`, new `POST /api/cases/{id}/packages/risk-explain` (`router.handleCreateRiskExplainPackage`) assembles `draft_text` (case-chunk-text stand-in, same as UC4's not-yet-existing real draft) and `reverted_neighbors` (a purpose-built `revertedNeighborEntry` DTO, same rationale as `typologyCatalogEntry`) into a `risk_explain` package via the existing `BuildPackage`/`saveNewPackage` path. Covered by new router tests.
   **Verified live** — and surfaced a real, pre-existing gap while doing so: fabricated a case matching a real ruling, tagged that ruling plus two others `reverted` with `revert_reason` text, and called the real endpoint end to end. The exact-match ruling correctly ranked first (0.939 similarity) with its `revert_reason` attached, confirming the assembly mechanism works — but only 2 of the 3 tagged reverted rulings made it into `reverted_neighbors`, not 3. Root cause: `draft_text`/`case_summary` is embedded with a single `embed_queries` call (no chunking or mean-pooling), so for full-length case/ruling text this silently truncates at the model's max sequence length before the vector is even computed — unlike `rulings.embedding`, which `importer.EmbedRuling` deliberately chunks and mean-pools for exactly this reason (see CLAUDE.md). This isn't specific to risk_explain: `/similar`, `/classify-knn`, and `/risk-score` all embed the same untreated `CaseChunkText` concatenation and share the identical exposure — it just happened to be observable here because the effect on the tail end of a small (3-row) reverted set is easy to see, whereas it's invisible against the top of a 111-row ranked list. Reverted all test tags and deleted the test case afterward.
   - **New open item** (not fixed here — cuts across UC2/UC3/UC5 uniformly, deserves its own pass): give case/draft query text the same chunk + mean-pool treatment `importer.EmbedRuling` already gives corpus rulings, instead of one untreated `embed_queries` call on `CaseChunkText`'s raw concatenation.
   ✅ **UC4 drafts**: `api/internal/store/rulings.go` adds `GetRulingsByIDs` (fetches full_text for a set of exemplar rulings — UC3's `/similar` only returns metadata, and the draft prompt's "don't cite outside the provided material" instruction needs real citable text); `api/internal/store/drafts.go` adds `CreateDraft`/`ListDraftsByCase`. New `POST /api/cases/{id}/packages/draft` (`router.handleCreateDraftPackage`) takes a caller-supplied `case_type` (UC2 confirmation isn't a built flow yet, so the confirmed type is passed directly rather than read off the case row) and assembles all four `draft.md` placeholders: `case_type` verbatim, `case_facts` from the usual case-chunk-text stand-in, `typology_structure` from the matching catalog entry (400 if `case_type` isn't in the catalog), and `exemplar_rulings` from UC3's `Similar` call filtered to that `case_type`, sorted upheld-first (plan.md §4 UC4: "preferably upheld") via a stable sort, each with a truncated full-text excerpt. `handleAttachPackageResult` now writes a `drafts` row (`writeDraftFromResult`) whenever a `draft`-use-case package's result validates — sections join into one `generated_text` blob (drafts.generated_text is a single column, not structured sections), and `cited_ruling_ids` strings that don't parse as UUIDs are skipped rather than failing the whole write, since the raw response is already retained in `package_results` regardless. New `GET /api/cases/{id}/drafts` lists a case's drafts. Covered by new router tests.
   **Verified live** — and found a real bug doing so: `truncateExcerpt` originally sliced exemplar text by byte index (`text[:1500]`), which can split a multi-byte UTF-8 rune (Spanish accented characters are 2 bytes) and hand `encoding/json` invalid UTF-8 to silently mangle. Fixed to back off to the nearest rune boundary (`utf8.RuneStart`), with a unit test constructing a rune deliberately straddling the cut point. Live pass: tagged 4 real rulings `despido injustificado` (2 `upheld`, 2 `reverted`), built a real draft package end to end — all 4 correctly surfaced as `exemplar_rulings`, upheld ones sorted first, excerpts valid UTF-8 (confirmed via `utf8.ValidString` in the unit test, not just visual inspection — an earlier `python -m json.tool`-adjacent display glitch this session already taught not to trust visually). Submitted the package, attached a 3-section synthetic result citing 2 of the 4 exemplars plus one deliberately malformed id, and confirmed: package transitioned to `completed`, a `drafts` row was written with the sections correctly joined into `generated_text`, `cited_ruling_ids` held only the 2 valid UUIDs (malformed one silently dropped as designed), and `GET .../drafts` returned it. Cleaned up all test state (case delete cascades to its draft; ruling tags reverted) afterward.
   ✅ **Package management UI**: `web/` now has its first real content — a Vite + React + TypeScript SPA scoped exactly to plan.md §5's "Management UI (admin section of the SPA)" spec (list/filter by case/use_case/status, inspect full bundle contents, download archive, submit, attach/ingest results with validation-error display, resubmit, cancel) — not the full product SPA from §6 (case dashboard, draft editor, risk report, etc.), which is separate, larger scope. `src/types.ts` models the wire format precisely per CLAUDE.md's gotcha: `Package`/`PackageSummary`/`PackageResult` are PascalCase (no json tags on those store structs), while the nested `Bundle` field is snake_case (`mlclient.PackageBundle` does carry json tags). `src/api.ts` is a thin typed fetch wrapper; `App.tsx`/`components/{PackageList,PackageDetail,StatusBadge}.tsx` are the three real pieces of UI. Added `?package=<id>` deep-linking (read on mount, written via `history.replaceState` on selection) — genuinely useful for sharing a package link, not just a test seam. `vite.config.ts` proxies `/api` to `localhost:8080` for CORS-free dev against the real Go API.
   **Verified live**, in a real browser, against the real running stack — not just `tsc`/`vite build`: created real packages through the actual API, then loaded the app in headless Chrome (screenshots + a `--virtual-time-budget` DOM dump to check actual `disabled` attributes rather than trust a compressed screenshot's visual contrast) and via a short puppeteer-core-driven interaction (typed into the result textarea, clicked the real button) to exercise paths a static screenshot can't reach. Confirmed: package list renders real data; action buttons are gated correctly across every status transition (`ready`→submit disabled once `submitted`, `cancel` enabled while non-terminal, `resubmit` only once terminal); a real classify package's prompt/context/output_schema render correctly; attaching a valid result flips the badge to `Completado` and correctly re-enables `resubmit`; attaching a schema-invalid result renders the exact validation error message returned by the API (`"(root): missing properties: 'confidence', 'evidence'"`) instead of silently failing. Deleted all test cases/packages afterward.
   ✅ **SPA packaging (`embed.FS`)**: new `api/internal/http/webui/` package (`webui.go`) embeds `dist/` and exposes it as an `fs.FS`; `dist/` ships with only a `.gitkeep` placeholder in git (real build output is gitignored via `webui/.gitignore`) so a clean checkout still `go build`/`go test`s without ever running `npm run build`. `router.Deps` gained an optional `StaticFS fs.FS` field (nil in every existing test — static serving is additive, not required); when set, `router.New` registers a catch-all `"/"` handler (`spaHandler`) behind all the explicit `/api/...`/`/healthz` routes, serving matched files as-is and falling back to `index.html`'s raw bytes for anything else so client-side routes survive a hard refresh. That fallback deliberately does *not* rewrite `r.URL.Path` to `/index.html` and hand off to `http.FileServer` — net/http's `FileServer` 301-redirects any request path literally ending in `index.html` to `/`, which would bounce every deep link back to the SPA root instead of loading it at the requested path; found live while testing the fallback, not from documentation. `api/Dockerfile` is now a three-stage build with context moved to the repo root (`docker-compose.yml`'s `api` service updated to `context: .` / `dockerfile: api/Dockerfile`) — a new `node:22-slim` stage runs `npm ci && npm run build` in `web/`, and its `dist/` output is `COPY --from=`'d into `internal/http/webui/dist/` before the Go build stage compiles, since `//go:embed` can only reach inside its own module tree and `web/` is a sibling directory outside `api/`'s module root.
   **Verified live** — not just `go build`: ran `npm run build` for real, copied the output into `internal/http/webui/dist/`, rebuilt the real `dictum-api` binary, and ran it standalone. Confirmed via `curl`: `GET /` returns the real built `index.html` (200), a hashed asset path parsed out of that HTML (`/assets/index-*.js`) returns 200 with `Content-Type: text/javascript`, an arbitrary client-side route (`/packages/42`) falls back to `index.html` content at 200 rather than 404ing or redirecting, and `/healthz` still resolves to its own handler rather than being shadowed by the catch-all static route. Then restored `dist/` to its git-committed placeholder-only state and confirmed `go build ./...`/`go test ./...` still pass clean.
   ✅ **Query-side embedding truncation** (§9 item 5): `ml/rag/embedding_service.py` adds `embed_query_pooled(text, embed_fn=embed_queries)` — chunks the input with the same `chunk_document` used for corpus rulings, embeds each chunk query-prefixed, and mean-pools, mirroring `importer.EmbedRuling` (Go)'s treatment of corpus-side text. `embed_fn` is injectable so `ml/rag/test_embedding_service.py` can assert the chunking/pooling logic without loading the real sentence-transformers model. `ml/app.py`'s four call sites that previously did a raw `embed_queries([text])[0]` (`/classify-knn`, `/similar`, `/risk-score`, `/reverted-neighbors`) now all call `embed_query_pooled` instead — closing the gap found live during the UC5 explanation-package verification pass, where a `nearest_reverted` result silently dropped a tagged reverted ruling because the query text got truncated before embedding.
   **Phase 4 status**: complete — all five originally-open items plus both packaging/embedding follow-ups are done.
5. **Hardening** — in progress.
   ✅ **Auth**: static named bearer tokens (`api/internal/http/auth`) — `DICTUM_API_TOKENS="actor:token,actor:token"` (passed through `docker-compose.yml`); no user directory, matching the private-service posture: the operator hands each staff member a token, and the actor name attached to it is what lands in the audit log and `packages.created_by` (which existed since Phase 4 but nothing set until now). `router.New` (now returning `http.Handler`, not `*http.ServeMux`) gates every `/api/...` route when `Deps.AuthTokens` is non-empty; `/healthz` and the static SPA stay open so probes work and the login surface can load. Token rides `Authorization: Bearer` with an `?access_token=` query fallback for header-less clients — EventSource (SSE `/events`) and plain `<a href>` archive downloads both need it. Empty/unset tokens disable auth entirely (dev default; `main.go` logs a warning and attributes everything to `anonymous`). SPA side: token field in the header (persisted to localStorage), `api.ts` attaches the header on every call and appends `access_token` to `archiveUrl`, 401s render a Spanish re-enter-token message.
   ✅ **Audit coverage**: `store/audit.go` (`InsertAudit`/`ListAuditLog` over the until-now-unused `audit_log` table) + `recordAudit` in the router on every mutating action — case create, package create (resubmissions carry `retry_of` in metadata), submit, result attach (with validation status), cancel, and draft create (when a valid draft result writes a `drafts` row). Audit writes are deliberately best-effort (logged, not turned into client-facing errors): the mutation already happened, and failing the request would misreport it. New `GET /api/audit` filters by actor/entity/entity_id.
   ✅ **Eval harness** (`ml/eval/`): pure metric functions (`metrics.py` — classification accuracy with abstention accounting, retrieval precision@k, empirical-tertile bucket suggestion, mean-risk-per-outcome separation; unit-tested without DB or model, same split as retrieval/score) + a leave-one-out runner (`run_eval.py`, `python -m eval.run_eval`) that scores UC2 classification, UC3 retrieval precision, and UC5 risk calibration against the labeled part of the corpus, each ruling's stored embedding querying the rest with itself excluded. Labels come from the DB, optionally overridden by a `--golden` NDJSON (`external_id`/`case_type`/`outcome`, same fields as the UC6 canonical format) — so the eval runs *before* the grading pass lands (§9 item 2), and that same file becomes the held-out golden set afterward. Degrades gracefully to an explicit "nothing to evaluate" report on the current untagged corpus.
   **Verified live**: full stack up with two real tokens set — confirmed 401 (no/wrong token) vs 200 (header and query-param token) vs open `/healthz`+SPA via curl; ran a classify package's whole lifecycle as two different actors (create+submit as one, result attach as the other) and read back the correct 3-entry per-actor audit trail through `GET /api/audit`, with `packages.created_by` correctly attributed; loaded the SPA in headless Chrome with a fresh profile and confirmed the token input renders and a tokenless list shows the exact Spanish unauthorized message. Ran the eval harness against the real 111-ruling corpus both ways: untagged → clean "nothing to evaluate" report (found and fixed a real Windows bug doing so — a `→` in report text crashes cp1252 consoles with `UnicodeEncodeError`; report output is ASCII-only now), and with a 12-ruling fabricated golden file → full report end-to-end (≈33% accuracy over 4 classes — exactly chance for made-up labels, which is the honest expectation; the run proves mechanics, not signal). Cleaned up all test state (case cascade, audit rows, ruling tags never touched).
   **Remaining in this phase**: risk-bucket calibration against real tags and real accuracy/precision baselines (both blocked on §9 item 2 — the harness is ready, the labels aren't); branding polish (the §6 full product SPA is unbuilt; only the package-manager admin screen exists); audit coverage for UC2 confirmations/overrides (that flow itself doesn't exist yet).

## 9. Open items

1. **Harness intake spec** — how packages are submitted and results returned (folder drop / queue / HTTP). Blocks Phase 4 automation only; manual hand-off works meanwhile.
2. **Grading tags for the archived corpus** — case_type / outcome / revert_reason per sentencia. Blocks real UC2/UC3/UC5 signal and phase-5 calibration/baselines. The eval harness (`ml/eval/run_eval.py`) is ready to consume the tags the moment they land — via DB tags or a `--golden` NDJSON before that.
3. ~~**Embedding model final pick**~~ — resolved: `multilingual-e5-large`, `vector(1024)` locked in `migrations/0001_init.sql`.
4. **OCR fallback need** — LiteParse's PDF-direct + bounding-box extraction validated (`ml/spikes/liteparse_spike.md`), but no raw scanned PDFs exist yet to exercise the Tesseract OCR path end-to-end. Tesseract + `tesseract-ocr-spa` added to `ml/Dockerfile` on the strength of the library docs; get one real scanned case PDF before UC1 ingestion work to close this out. LibreOffice (Office docs) and ImageMagick (images) paths are also unexercised.
5. ~~**Query-side embedding truncation**~~ — resolved in phase 4: `ml/rag/embedding_service.embed_query_pooled` gives `/similar`, `/classify-knn`, `/risk-score`, and `/reverted-neighbors` the same chunk + mean-pool treatment `importer.EmbedRuling` gives corpus rulings, instead of a single truncating `embed_queries` call.
