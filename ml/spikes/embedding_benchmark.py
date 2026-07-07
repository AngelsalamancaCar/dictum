"""Phase-1 spike: multilingual-e5-large vs paraphrase-multilingual-mpnet-base-v2
on the 111 archived sentencias.

No case_type/outcome tags exist yet for this corpus (open gap in plan.md §4
UC6), so this can't be a supervised retrieval eval. Instead it measures what's
decidable now: embedding throughput, vector dimension/index-size trade-off,
and neighbor coherence sanity-checked by eye on a few query chunks.

Usage: ml/.venv/Scripts/python spikes/embedding_benchmark.py
"""
from __future__ import annotations

import json
import re
import time
from pathlib import Path

from sentence_transformers import SentenceTransformer

CORPUS_DIR = Path(__file__).parent.parent.parent / "corpus_archive" / "texts"
REPORT_PATH = Path(__file__).parent / "embedding_benchmark_report.md"

MODELS = [
    "intfloat/multilingual-e5-large",
    "paraphrase-multilingual-mpnet-base-v2",
]

CHUNK_WORDS = 400
CHUNK_OVERLAP = 60


def load_corpus() -> list[tuple[str, str]]:
    docs = []
    for path in sorted(CORPUS_DIR.glob("*.txt")):
        if path.name.startswith("prompt_"):
            continue
        docs.append((path.name, path.read_text(encoding="utf-8", errors="replace")))
    return docs


def chunk(text: str) -> list[str]:
    words = re.split(r"\s+", text.strip())
    if not words:
        return []
    chunks = []
    step = CHUNK_WORDS - CHUNK_OVERLAP
    for start in range(0, len(words), step):
        piece = words[start : start + CHUNK_WORDS]
        if piece:
            chunks.append(" ".join(piece))
        if start + CHUNK_WORDS >= len(words):
            break
    return chunks


def cosine_sim_matrix(vectors):
    import numpy as np

    normed = vectors / np.linalg.norm(vectors, axis=1, keepdims=True)
    return normed @ normed.T


def run():
    import numpy as np

    docs = load_corpus()
    print(f"Loaded {len(docs)} sentencias")

    all_chunks: list[str] = []
    chunk_doc: list[str] = []
    for name, text in docs:
        for c in chunk(text):
            all_chunks.append(c)
            chunk_doc.append(name)
    print(f"Total chunks: {len(all_chunks)} (avg {len(all_chunks)/len(docs):.1f}/doc)")

    results = {}
    for model_name in MODELS:
        print(f"\n=== {model_name} ===")
        model = SentenceTransformer(model_name)
        dim = model.get_sentence_embedding_dimension()

        t0 = time.perf_counter()
        embeddings = model.encode(
            all_chunks, batch_size=16, show_progress_bar=True, convert_to_numpy=True
        )
        elapsed = time.perf_counter() - t0

        # neighbor coherence sanity check: pick 3 query chunks from different
        # docs, report their nearest neighbor's source doc + similarity
        sims = cosine_sim_matrix(embeddings)
        np.fill_diagonal(sims, -1)
        sample_idxs = [0, len(all_chunks) // 2, len(all_chunks) - 1]
        neighbor_checks = []
        for i in sample_idxs:
            j = int(np.argmax(sims[i]))
            neighbor_checks.append(
                {
                    "query_doc": chunk_doc[i],
                    "query_excerpt": all_chunks[i][:120],
                    "neighbor_doc": chunk_doc[j],
                    "neighbor_excerpt": all_chunks[j][:120],
                    "similarity": float(sims[i][j]),
                }
            )

        results[model_name] = {
            "dimension": dim,
            "num_chunks": len(all_chunks),
            "embed_seconds": elapsed,
            "chunks_per_second": len(all_chunks) / elapsed,
            "index_bytes_estimate": dim * 4 * len(all_chunks),
            "neighbor_checks": neighbor_checks,
        }
        print(f"dim={dim} elapsed={elapsed:.1f}s ({len(all_chunks)/elapsed:.1f} chunks/s)")

    write_report(docs, all_chunks, results)
    print(f"\nReport written to {REPORT_PATH}")


def write_report(docs, all_chunks, results):
    lines = [
        "# Embedding benchmark spike\n",
        f"Corpus: {len(docs)} sentencias, {len(all_chunks)} chunks "
        f"(~{CHUNK_WORDS} words, {CHUNK_OVERLAP} overlap).\n",
        "No case_type/outcome labels exist yet, so this is not a supervised "
        "retrieval eval — see plan.md §9 item 2. It measures throughput, "
        "index-size trade-off, and eyeballed neighbor coherence.\n",
        "| Model | Dim | Embed time (111 docs) | Throughput | Est. index size |",
        "|---|---|---|---|---|",
    ]
    for name, r in results.items():
        mb = r["index_bytes_estimate"] / (1024 * 1024)
        lines.append(
            f"| `{name}` | {r['dimension']} | {r['embed_seconds']:.1f}s | "
            f"{r['chunks_per_second']:.1f} chunks/s | {mb:.1f} MB |"
        )

    lines.append("\n## Neighbor coherence sanity checks\n")
    for name, r in results.items():
        lines.append(f"### {name}\n")
        for chk in r["neighbor_checks"]:
            lines.append(
                f"- Query (`{chk['query_doc']}`): _{chk['query_excerpt']}..._\n"
                f"  → Neighbor (`{chk['neighbor_doc']}`, sim={chk['similarity']:.3f}): "
                f"_{chk['neighbor_excerpt']}..._"
            )
        lines.append("")

    lines.append("## Recommendation\n")
    lines.append("_Fill in after reviewing neighbor coherence and index-size numbers above._\n")

    REPORT_PATH.write_text("\n".join(lines), encoding="utf-8")
    (REPORT_PATH.parent / "embedding_benchmark_raw.json").write_text(
        json.dumps(results, ensure_ascii=False, indent=2), encoding="utf-8"
    )


if __name__ == "__main__":
    run()
