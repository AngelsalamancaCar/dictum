# Embedding benchmark spike

Corpus: 111 sentencias, 2627 chunks (~400 words, 60 overlap).

No case_type/outcome labels exist yet, so this is not a supervised retrieval eval — see plan.md §9 item 2. It measures throughput, index-size trade-off, and eyeballed neighbor coherence.

| Model | Dim | Embed time (111 docs) | Throughput | Est. index size |
|---|---|---|---|---|
| `intfloat/multilingual-e5-large` | 1024 | 1284.1s | 2.0 chunks/s | 10.3 MB |
| `paraphrase-multilingual-mpnet-base-v2` | 768 | 82.5s | 31.8 chunks/s | 7.7 MB |

## Neighbor coherence sanity checks

### intfloat/multilingual-e5-large

- Query (`30-202.txt`): _2024, “Año de la Innovación y Modernización Judicial” SENTENCIA. SAN QUINTÍN, BAJA CALIFORNIA, A VEINTIOCHO DE AGOSTO DE..._
  → Neighbor (`67-202.txt`, sim=0.957): _2024, “Año de la Innovación y Modernización Judicial” SENTENCIA. SAN QUINTÍN, BAJA CALIFORNIA, A OCHO DE NOVIEMBRE DE DO..._
- Query (`SENTENCIA 252-202.txt`): _cfdi, con registros de asistencia electrónica semanal por el periodo del 28 de febrero de 2022 al 09 de octubre de 2022,..._
  → Neighbor (`SENTENCIA 1298-202.txt`, sim=0.953): _de pago, por los periodos comprendidos del 01 de enero al 30 de diciembre de 2022, no se presentaron en formato de Compr..._
- Query (`sentencia_98_2023.txt`): _EXPEDIENTE NÚMERO 98/2023 SENTENCIA CONDENATORIA CLAVE 8800 En los autos del Juicio Ordinario 98/2023 promovido por ****..._
  → Neighbor (`SENTENCIA 98-202.txt`, sim=0.932): _EXPEDIENTE NÚMERO 98/2023 SENTENCIA CONDENATORIA CLAVE 8800 En los autos del Juicio Ordinario 98/2023 promovido por ****..._

### paraphrase-multilingual-mpnet-base-v2

- Query (`30-202.txt`): _2024, “Año de la Innovación y Modernización Judicial” SENTENCIA. SAN QUINTÍN, BAJA CALIFORNIA, A VEINTIOCHO DE AGOSTO DE..._
  → Neighbor (`67-202.txt`, sim=0.938): _2024, “Año de la Innovación y Modernización Judicial” SENTENCIA. SAN QUINTÍN, BAJA CALIFORNIA, A OCHO DE NOVIEMBRE DE DO..._
- Query (`SENTENCIA 252-202.txt`): _cfdi, con registros de asistencia electrónica semanal por el periodo del 28 de febrero de 2022 al 09 de octubre de 2022,..._
  → Neighbor (`Sentencia 854-24 Lic L Resc actor.txt`, sim=0.694): _salario diario integrado de $813.30 pesos, lo anterior, salvo prueba en contrario. 3.- Del análisis de la prueba de insp..._
- Query (`sentencia_98_2023.txt`): _EXPEDIENTE NÚMERO 98/2023 SENTENCIA CONDENATORIA CLAVE 8800 En los autos del Juicio Ordinario 98/2023 promovido por ****..._
  → Neighbor (`SENTENCIA 98-202.txt`, sim=1.000): _EXPEDIENTE NÚMERO 98/2023 SENTENCIA CONDENATORIA CLAVE 8800 En los autos del Juicio Ordinario 98/2023 promovido por ****..._

## Recommendation

**Pick `intfloat/multilingual-e5-large` (1024-d).**

- Index-size difference (10.3 MB vs 7.7 MB at 111 docs) is negligible either way.
- Throughput difference is large (2.0 vs 31.8 chunks/s) but corpus embedding is
  a one-time batch job (`dictum-import` CLI, UC6) — ~21 min for the full
  archive is acceptable since it's not user-facing. Single-query embed latency
  (the case-summary embed in UC3/UC5) is well under a second either way.
- Neighbor coherence is where the models diverge: on the CFDI/attendance-records
  query, e5-large found a genuinely on-topic neighbor (same payroll-receipt
  dispute, sim 0.953) while mpnet's top neighbor was a different fact pattern
  entirely (salary/inspection dispute, sim 0.694) — a materially weaker match.
  Both models correctly found the near-duplicate pair (`sentencia_98_2023.txt`
  / `SENTENCIA 98-202.txt`, near-identical text, sim ≥0.93) and the same
  boilerplate-header pair. On the one query where they disagreed, e5-large's
  retrieval was more semantically precise — that matters more than throughput
  for UC3 (similar-ruling RAG) and UC5 (revert-risk grading), where retrieval
  quality is user- and judge-facing.

**Implementation note:** E5 models are trained for asymmetric retrieval and
expect inputs prefixed with `"query: "` (for the case summary / draft being
searched) or `"passage: "` (for corpus chunks being indexed). Skipping this
prefix convention degrades retrieval quality — the embedding module must
apply it at both index-time and query-time.

**Locks:** `vector(1024)` in the schema (`chunks.embedding`, `rulings.embedding`).
