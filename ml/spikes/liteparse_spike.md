# LiteParse spike

## Scope and gap

`corpus_archive/texts/*.txt` is pre-extracted text (Labelbox export), not raw
PDFs — there are no real case-file PDFs available yet, so this spike could
not validate LiteParse against actual scanned court filings. It instead
validates the library against a synthetic PDF built from real sentencia text,
and documents what remains open until the first real folder ingest (UC1).

## What was validated

- `pip install liteparse` resolves to `run-llama/liteparse` (confirmed via
  `pip show`: homepage `github.com/run-llama/liteparse`, Apache-2.0) — matches
  plan.md's pick, not a namesquat.
- Built a text-layer PDF from a real archived sentencia (`30-202.txt`) using
  a Unicode TTF font (Arial), then parsed it with `LiteParse(output_format="text")`.
  Output round-trips Spanish diacritics correctly (Año, Innovación,
  Modernización, QUINTÍN) — verified at the byte level, zero U+FFFD
  replacement characters. (An initial attempt with reportlab's built-in
  Helvetica font mangled accents; that was a synthetic-PDF artifact from
  WinAnsi encoding, not a LiteParse bug — switching to a TTF font fixed it.)
- `output_format="json"` with `emit_word_boxes=True` returns, per page:
  `text_items` (line-level: text, x, y, width, height, font_name, font_size,
  confidence, rotation) each with nested `words` (word-level `WordBox`: text,
  x, y, width, height). This is what plan.md §3's "LiteParse output (text +
  bounding boxes) in object storage" needs — no gap.
- Multi-page PDFs paginate correctly (`result.pages`, `page.page_num`).

## What remains open (blocks Phase 1 exit item #4)

- **OCR fallback**: `ocr_enabled=True` is the default and uses Tesseract
  unless `ocr_server_url` is set. **No `tesseract` binary is installed on
  this dev machine** — OCR path is unverified end-to-end here. The `ml/Dockerfile`
  must install `tesseract-ocr` + a Spanish language pack (`tesseract-ocr-spa`)
  for container parity; local dev needs it too if OCR is exercised outside
  Docker. Not yet added to `ml/Dockerfile` — do before UC1 ingestion work starts.
- **Office docs (LibreOffice) and images (ImageMagick) paths** — not
  exercised at all in this spike; plan.md says LiteParse shells out to these
  for non-PDF inputs. Needs its own smoke test once those binaries are
  available in the target environment.
- **Real scanned-image PDFs** — synthetic text-layer PDF doesn't exercise the
  image-rendering + OCR pipeline, page-complexity heuristics
  (`PageComplexityStats`), or `ocr_hedge_delays_ms` retry behavior. Needs a
  real or realistic scanned sample once one is available.

## Recommendation

Proceed with LiteParse — PDF-direct text and bounding-box extraction is
solid for Spanish legal text. Before UC1 ingestion is built: add Tesseract +
`spa` language data to `ml/Dockerfile`, and get at least one real scanned
case PDF to close the OCR/image gap above.
