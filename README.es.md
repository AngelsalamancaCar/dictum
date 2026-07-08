# Dictum

**Dictum** es una plataforma de asistencia para jueces y personal jurídico que trabaja en juicios del *procedimiento especial laboral* (tribunales laborales mexicanos). Ingiere carpetas de expedientes, clasifica el tipo de caso (*litis*), recupera sentencias similares, redacta proyectos de resolución y evalúa el riesgo de revocación comparando contra sentencias revocadas en apelación.

Todos los documentos del caso y el texto de la interfaz están en español; este documento está disponible en [English](README.md) y [Español](README.es.md).

> **Estado**: en desarrollo activo. El pipeline central, la recuperación, la clasificación, el ciclo de vida de paquetes, la redacción de borradores y una interfaz de administración de paquetes ya están construidos y verificados contra un stack en vivo. Consulta [`plan.md`](plan.md) para el documento de diseño completo, el estado por fase y los pendientes.

---

## Tabla de contenido

- [Descripción general](#descripción-general)
- [Arquitectura](#arquitectura)
- [Stack tecnológico](#stack-tecnológico)
- [Estructura del repositorio](#estructura-del-repositorio)
- [Primeros pasos](#primeros-pasos)
  - [Stack completo con Docker](#stack-completo-con-docker)
  - [API en Go (local)](#api-en-go-local)
  - [Worker de ML en Python (local)](#worker-de-ml-en-python-local)
  - [SPA web (local)](#spa-web-local)
- [Autenticación](#autenticación)
- [Importación del corpus](#importación-del-corpus)
- [Pruebas](#pruebas)
- [Decisiones de diseño relevantes](#decisiones-de-diseño-relevantes)
- [Estado del proyecto](#estado-del-proyecto)
- [Contribuir](#contribuir)
- [Licencia](#licencia)

---

## Descripción general

Dictum asiste — nunca sustituye — al juez humano. Cada artefacto generado (clasificación, notas de similitud, texto de borrador, explicación de riesgo) se produce mediante un **paquete preparado** versionado y auditable: un conjunto de prompt + contexto + esquema JSON de salida que se entrega a un agente externo (harness) de LLM y se recibe de forma asíncrona. **La aplicación nunca invoca un SDK de LLM directamente** — esto mantiene explícita la frontera de datos del sistema y hace que cada generación sea rastreable a una versión de prompt, un payload de contexto y un actor.

Casos de uso principales:

| # | Caso de uso | Señal |
|---|---|---|
| UC1 | Ingesta y parseo de carpetas | Local (LiteParse, deduplicación sha256, chunking por sección) |
| UC2 | Clasificación de tipología del caso (*litis*) | kNN local + paquete LLM |
| UC3 | Recuperación de sentencias similares (RAG) | Búsqueda híbrida local (pgvector + texto completo) + paquete LLM opcional |
| UC4 | Generación de proyectos de resolución | Paquete LLM, fundamentado en sentencias recuperadas |
| UC5 | Evaluación de riesgo de revocación | Puntaje local (instantáneo) + paquete LLM (explicación) |
| UC6 | Importación del corpus (única vez) | Herramienta CLI, no un flujo web |

## Arquitectura

```
Navegador (SPA en React, incrustada en el binario de Go)
      │ HTTPS/JSON + SSE (progreso de trabajos)
┌─────▼──────────────────────────────┐
│ SERVIDOR API EN GO (dictum-api)    │  autenticación, CRUD de casos,
│  - API REST + frontend estático    │  ingesta de carpetas, orquestación
│  - cola de trabajos (goroutines)   │  de trabajos, ciclo de vida de
└─────┬──────────────────────────────┘  paquetes, bitácora de auditoría
      │ HTTP interno
┌─────▼──────────────────────────────┐
│ WORKER DE ML EN PYTHON (dictum-ml) │  FastAPI: /parse /chunk /embed
│  - parseo con LiteParse            │  /similar /classify-knn
│  - embeddings (sentence-transf.)   │  /risk-score /package-build
│  - recuperación híbrida (pgvector+ │
│    búsqueda de texto completo)     │
│  - constructor de paquetes         │
└─────┬──────────────────────────────┘
      │
┌─────▼─────────────────────┐      ┌───────────────────────────┐
│ POSTGRES (+ pgvector)     │      │ AGENTE HARNESS (externo,  │
│ metadatos, chunks,        │ ◄──► │ servicio privado) —       │
│ vectores, paquetes,       │      │ ejecuta paquetes LLM      │
│ resultados                │      │ preparados                │
└────────────────────────────┘      └───────────────────────────┘
```

Go es responsable de todo lo orientado al usuario y con estado (API REST, orquestación de trabajos, acceso a Postgres, autenticación, bitácora de auditoría). Python es responsable de todo lo relacionado con ML (parseo, embeddings, recuperación, clasificación, ensamblado de paquetes) y se conecta de forma independiente a la **misma** base de datos Postgres — Go y Python no se comunican entre sí para el acceso a datos, solo para orquestación. Ambos lados son reemplazables de forma independiente.

## Stack tecnológico

| Capa | Tecnología |
|---|---|
| Servidor API | Go 1.26, `pgx/v5`, `pgvector-go`, `jsonschema/v5` |
| Worker de ML | Python 3.11+, FastAPI, sentence-transformers (`multilingual-e5-large`), psycopg 3, pgvector |
| Parseo | LiteParse (PDF directo; LibreOffice para documentos de Office; ImageMagick/Tesseract para imágenes y OCR) |
| Frontend | React 19 + TypeScript + Vite, incrustado en el binario de Go vía `embed.FS` |
| Base de datos | PostgreSQL + extensión pgvector |
| Contenedores | Docker Compose (postgres, api, ml) |

## Estructura del repositorio

```
dictum/
├── api/                     Módulo de Go (dictum/api)
│   ├── cmd/
│   │   ├── dictum-api/      punto de entrada del servidor
│   │   └── dictum-import/   CLI de importación del corpus (UC6)
│   └── internal/            http/router, jobs, store, packages, importer, mlclient
├── ml/                      Paquete de Python (dictum-ml), app de FastAPI
│   ├── parsing/  rag/  classify/  risk/  packager/
│   ├── prompts/             plantillas de prompt versionadas
│   └── eval/                arnés de evaluación offline
├── web/                     SPA en React (interfaz de administración de paquetes)
├── migrations/              migraciones del esquema SQL
├── corpus_archive/          corpus de sentencias de referencia (ignorado por git, no se distribuye)
├── docker-compose.yml       postgres + api + ml
├── plan.md                  documento de diseño autoritativo y bitácora de estado
└── CLAUDE.md                guía para colaboradores/agentes de IA en este repositorio
```

## Primeros pasos

### Stack completo con Docker

```bash
docker compose up -d --build
```

Esto levanta Postgres (con pgvector), el worker de ML y la API en Go. La migración de la base de datos se aplica automáticamente al iniciar Postgres por primera vez.

- API: `http://localhost:8080`
- Worker de ML: `http://localhost:8000`
- Postgres: `localhost:5432` (usuario/contraseña/db: `dictum`)

Comandos útiles adicionales:

```bash
docker compose logs ml --tail=50
docker compose restart ml         # aplica cambios de código en ml/ (montado por bind), sin reinstalar
docker compose up -d --build ml   # reconstruye en su lugar, si cambiaron las dependencias de pyproject.toml
```

### API en Go (local)

```bash
cd api
go build ./...
go vet ./...
go test ./...
```

### Worker de ML en Python (local)

```bash
cd ml
.venv/Scripts/python -m pip install -e ".[dev]"
.venv/Scripts/python -m uvicorn app:app --host 127.0.0.1 --port 8000
```

### SPA web (local)

```bash
cd web
npm install
npm run dev        # servidor de desarrollo, redirige /api a localhost:8080
npm run build       # build de producción → dist/, incrustado en el binario de Go
```

## Autenticación

Define `DICTUM_API_TOKENS="actor:token,actor:token"` antes de iniciar la API para exigir un token tipo bearer en cada ruta `/api/...`. El nombre del actor asociado a un token es lo que queda registrado en `audit_log` y en `packages.created_by`. Si no se define, la autenticación queda deshabilitada y toda acción se atribuye a `anonymous` (valor por defecto en desarrollo). Los clientes sin encabezados (SSE, enlaces de descarga) pueden usar `?access_token=` en su lugar.

## Importación del corpus

```bash
cd api
go run ./cmd/dictum-import -adapter=labelbox -manifest="../corpus_archive/manifest.json" -corpus-dir="../corpus_archive" -dry-run
go run ./cmd/dictum-import -adapter=foldercsv -folder=<carpeta de .txt> -csv=<etiquetas.csv> -db=<dsn> -ml-url=<url>
```

Siempre ejecuta primero en modo `-dry-run` — valida y reporta la cobertura de etiquetas sin escribir nada.

## Pruebas

```bash
# Go
cd api && go test ./...

# Python
cd ml && .venv/Scripts/python -m pytest

# Evaluación offline (UC2/UC3/UC5, contra una base de datos en vivo)
cd ml && .venv/Scripts/python -m eval.run_eval [--golden labels.ndjson]
```

## Decisiones de diseño relevantes

- **Aislamiento del LLM**: la aplicación nunca invoca un SDK de LLM directamente. Cada funcionalidad que depende de un LLM produce un "paquete preparado" versionado (prompt + contexto + esquema de salida) que un agente harness independiente consume de forma asíncrona. Esto mantiene la generación auditable y explícita la frontera de datos de la aplicación.
- **Embeddings**: `intfloat/multilingual-e5-large` (1024 dimensiones), elegido tras un spike de benchmarking (`ml/spikes/embedding_benchmark_report.md`). Requiere la convención de prefijos `query:`/`passage:` de E5 en todas las entradas.
- **Una sola tabla `rulings` sirve a dos RAG**: el corpus de "sentencias revocadas" usado para la explicación de riesgo es una vista filtrada (`outcome = 'reverted'`) de la misma tabla usada para la recuperación de sentencias similares.
- **El ensamblado de paquetes está dividido**: Python (`ml/packager/bundle.py`) es un renderizador puro y sin estado; Go es responsable del ciclo de vida con estado del paquete (`draft → ready → submitted → completed/failed/cancelled`) y valida cada resultado contra el JSON Schema almacenado en el paquete antes de avanzar su estado.

Consulta [`CLAUDE.md`](CLAUDE.md) para la lista completa de particularidades de implementación (tipado de parámetros, peculiaridades de rutas en Docker, convenciones de nomenclatura de campos JSON, etc.).

## Estado del proyecto

El corpus de referencia (111 sentencias reales) está cargado con embeddings pero actualmente **sin etiquetar** — las etiquetas `case_type`/`outcome`/`revert_reason` están pendientes de una pasada de calificación. Este es un bloqueante conocido y registrado para obtener señal genuina en UC2/UC3/UC5, no un error. Consulta [`plan.md` §9](plan.md#9-open-items) para la lista completa de pendientes.

## Contribuir

Actualmente este es un proyecto cerrado, de un solo equipo. `CLAUDE.md` documenta las convenciones que los agentes de codificación con IA (y colaboradores humanos) deben seguir al trabajar en este repositorio; `plan.md` es la fuente de verdad para decisiones de arquitectura y hoja de ruta — léelo y actualízalo junto con cualquier cambio no trivial.

## Licencia

[PolyForm Noncommercial License 1.0.0](LICENSE) — libre de usar, modificar y distribuir para cualquier fin no comercial. El uso comercial requiere una licencia distinta otorgada por el titular de los derechos.
