# Dictum

**Dictum** is a case-assistance platform for judges and legal staff working *procedimiento especial laboral* cases (Mexican labor courts). It ingests case folders, classifies case type (*litis*), retrieves similar prior rulings, drafts proposed rulings, and grades revert-risk against rulings overturned on appeal.

All case documents and UI copy are in Spanish; this document is provided in [English](README.md) and [Español](README.es.md).

> **Status**: active development. Core pipeline, retrieval, classification, package lifecycle, drafting, and a package-management admin UI are built and verified against a live stack. See [`plan.md`](plan.md) for the full design doc, phase-by-phase status, and open items.

---

## Table of contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Tech stack](#tech-stack)
- [Repository layout](#repository-layout)
- [Getting started](#getting-started)
  - [Full stack via Docker](#full-stack-via-docker)
  - [Go API (local)](#go-api-local)
  - [Python ML worker (local)](#python-ml-worker-local)
  - [Web SPA (local)](#web-spa-local)
- [Authentication](#authentication)
- [Corpus import](#corpus-import)
- [Testing](#testing)
- [Design decisions worth knowing](#design-decisions-worth-knowing)
- [Project status](#project-status)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

Dictum assists — never replaces — the human judge. Every generated artifact (classification, similar-ruling notes, draft text, risk explanation) is produced through an auditable, versioned **prepared package**: a bundle of prompt + context + JSON output schema that is handed to an external LLM harness and returned asynchronously. **No LLM SDK is ever called directly from the app** — this keeps the system's data boundary explicit and every generation traceable to a prompt version, a context payload, and an actor.

Core use cases:

| # | Use case | Signal |
|---|---|---|
| UC1 | Folder ingestion & parsing | Local (LiteParse, sha256 dedupe, section-aware chunking) |
| UC2 | Case typology (*litis*) classification | Local kNN + LLM package |
| UC3 | Similar-ruling retrieval (RAG) | Local hybrid search (pgvector + full-text) + optional LLM package |
| UC4 | Draft ruling generation | LLM package, grounded in retrieved rulings |
| UC5 | Revert-risk grading | Local score (instant) + LLM package (explanation) |
| UC6 | Corpus import (one-time) | CLI tool, not a web flow |

## Architecture

```
Browser (React SPA, embedded in the Go binary)
      │ HTTPS/JSON + SSE (job progress)
┌─────▼──────────────────────────────┐
│ GO API SERVER  (dictum-api)        │  auth, case CRUD, folder intake,
│  - REST API + static frontend      │  job orchestration, package
│  - job queue (goroutines)          │  lifecycle, audit log
└─────┬──────────────────────────────┘
      │ internal HTTP
┌─────▼──────────────────────────────┐
│ PYTHON ML WORKER  (dictum-ml)      │  FastAPI: /parse /chunk /embed
│  - LiteParse parsing               │  /similar /classify-knn
│  - sentence-transformers embeddings│  /risk-score /package-build
│  - hybrid retrieval (pgvector+FTS) │
│  - prepared-package builder        │
└─────┬──────────────────────────────┘
      │
┌─────▼─────────────────────┐      ┌───────────────────────────┐
│ POSTGRES (+ pgvector)     │      │ HARNESS AGENT (external,  │
│ metadata, chunks, vectors,│ ◄──► │ private service) — runs   │
│ prepared packages, results│      │ prepared LLM packages     │
└────────────────────────────┘      └───────────────────────────┘
```

Go owns everything user-facing and stateful (REST API, job orchestration, Postgres access, auth, audit log). Python owns everything ML (parsing, embeddings, retrieval, classification, package assembly) and talks to the **same** Postgres database independently — Go and Python do not proxy through each other for data access, only for orchestration. Either side is independently replaceable.

## Tech stack

| Layer | Technology |
|---|---|
| API server | Go 1.26, `pgx/v5`, `pgvector-go`, `jsonschema/v5` |
| ML worker | Python 3.11+, FastAPI, sentence-transformers (`multilingual-e5-large`), psycopg 3, pgvector |
| Parsing | LiteParse (PDF direct; LibreOffice for Office docs; ImageMagick/Tesseract for images and OCR) |
| Frontend | React 19 + TypeScript + Vite, embedded into the Go binary via `embed.FS` |
| Database | PostgreSQL + pgvector extension |
| Containerization | Docker Compose (postgres, api, ml) |

## Repository layout

```
dictum/
├── api/                     Go module (dictum/api)
│   ├── cmd/
│   │   ├── dictum-api/      server entrypoint
│   │   └── dictum-import/   corpus import CLI (UC6)
│   └── internal/            http/router, jobs, store, packages, importer, mlclient
├── ml/                      Python package (dictum-ml), FastAPI app
│   ├── parsing/  rag/  classify/  risk/  packager/
│   ├── prompts/             versioned LLM prompt templates
│   └── eval/                offline evaluation harness
├── web/                     React SPA (package-management admin UI)
├── migrations/              SQL schema migrations
├── corpus_archive/          reference ruling corpus (gitignored, not shipped)
├── docker-compose.yml       postgres + api + ml
├── plan.md                  authoritative design doc & status log
└── CLAUDE.md                contributor/agent guidance for this repo
```

## Getting started

### Full stack via Docker

```bash
docker compose up -d --build
```

This starts Postgres (with pgvector), the ML worker, and the Go API. The database migration applies automatically on first Postgres start.

- API: `http://localhost:8080`
- ML worker: `http://localhost:8000`
- Postgres: `localhost:5432` (user/password/db: `dictum`)

Useful follow-ups:

```bash
docker compose logs ml --tail=50
docker compose restart ml         # picks up ml/ source changes (bind-mounted), no reinstall
docker compose up -d --build ml   # rebuild instead, if pyproject.toml dependencies changed
```

### Go API (local)

```bash
cd api
go build ./...
go vet ./...
go test ./...
```

### Python ML worker (local)

```bash
cd ml
.venv/Scripts/python -m pip install -e ".[dev]"
.venv/Scripts/python -m uvicorn app:app --host 127.0.0.1 --port 8000
```

### Web SPA (local)

```bash
cd web
npm install
npm run dev        # dev server, proxies /api to localhost:8080
npm run build       # production build → dist/, embedded into the Go binary
```

## Authentication

Set `DICTUM_API_TOKENS="actor:token,actor:token"` before starting the API to require a bearer token on every `/api/...` route. The actor name attached to a token is what lands in `audit_log` and `packages.created_by`. Unset, auth is disabled and every action is attributed to `anonymous` (dev default). Header-less clients (SSE, download links) may pass `?access_token=` instead.

## Corpus import

```bash
cd api
go run ./cmd/dictum-import -adapter=labelbox -manifest="../corpus_archive/manifest.json" -corpus-dir="../corpus_archive" -dry-run
go run ./cmd/dictum-import -adapter=foldercsv -folder=<dir of .txt> -csv=<tags.csv> -db=<dsn> -ml-url=<url>
```

Always dry-run first — it validates and reports tag coverage without writing anything.

## Testing

```bash
# Go
cd api && go test ./...

# Python
cd ml && .venv/Scripts/python -m pytest

# Offline evaluation (UC2/UC3/UC5, against a live DB)
cd ml && .venv/Scripts/python -m eval.run_eval [--golden labels.ndjson]
```

## Design decisions worth knowing

- **LLM isolation**: the app never calls an LLM SDK directly. Every LLM-dependent feature produces a versioned "prepared package" (prompt + context + output schema) that a separate harness agent consumes asynchronously. This keeps generation auditable and the app's data boundary explicit.
- **Embeddings**: `intfloat/multilingual-e5-large` (1024-d), chosen after a benchmark spike (`ml/spikes/embedding_benchmark_report.md`). Requires the E5 `query:`/`passage:` prefix convention on all inputs.
- **One `rulings` table serves two RAGs**: the "reverted rulings" corpus used for risk explanation is a filtered view (`outcome = 'reverted'`) of the same table used for similar-ruling retrieval.
- **Package bundle assembly is split**: Python (`ml/packager/bundle.py`) is a pure, stateless renderer; Go owns the stateful package lifecycle (`draft → ready → submitted → completed/failed/cancelled`) and validates every result against the bundle's stored JSON Schema before advancing status.

See [`CLAUDE.md`](CLAUDE.md) for the full list of implementation gotchas (parameter typing, Docker path quirks, JSON field-naming conventions, etc.).

## Project status

The reference corpus (111 real rulings) is loaded with embeddings but currently **untagged** — `case_type`/`outcome`/`revert_reason` labels are pending a grading pass. This is a known, tracked blocker for genuine UC2/UC3/UC5 signal, not a bug. See [`plan.md` §9](plan.md#9-open-items) for the full list of open items.

## Contributing

This is currently a closed, single-team project. `CLAUDE.md` documents the conventions AI coding agents (and human contributors) should follow when working in this repository; `plan.md` is the source of truth for architecture and roadmap decisions — read and update it alongside non-trivial changes.

## License

[PolyForm Noncommercial License 1.0.0](LICENSE) — free to use, modify, and distribute for any noncommercial purpose. Commercial use requires a separate license from the copyright holder.
