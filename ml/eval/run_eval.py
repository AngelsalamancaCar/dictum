"""Eval harness runner (plan.md §8 phase 5): leave-one-out evaluation of the
local UC2/UC3/UC5 signals against the labeled part of the rulings corpus.

For every labeled ruling, its stored embedding queries the rest of the
corpus (itself excluded — it would trivially rank first at similarity 1.0):

- classification accuracy: classify/knn.py's weighted_vote over the first k
  case_type-tagged neighbors vs the ruling's own case_type
- retrieval precision@k: fraction of the first k *labeled* neighbors sharing
  the ruling's case_type (unlabeled neighbors are skipped, not counted as
  misses — the corpus is still mostly untagged, plan.md §9 item 2, and this
  measures retrieval quality, not tag coverage)
- risk calibration: risk/score.py's compute_risk over the first k
  outcome-tagged neighbors, reported as mean score per actual outcome plus
  empirical tertile cut points to calibrate risk_bucket's fixed thresholds

Labels come from the rulings table, optionally overridden/augmented by a
golden NDJSON file (--golden): one {"external_id", "case_type", "outcome"}
per line, same fields as the canonical corpus format (plan.md §4 UC6). The
golden file lets the eval run before the grading pass lands in the DB —
and afterward serves as the held-out golden set the phase-5 spec asks for.

Usage:
    cd ml
    .venv/Scripts/python -m eval.run_eval [--k 10] [--pool 100] [--golden golden.ndjson]

Needs DATABASE_URL (defaults to the local compose Postgres).
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from classify.knn import weighted_vote
from eval.metrics import classification_report, outcome_separation, precision_at_k, suggest_buckets
from rag.db import get_pool
from rag.retrieval import vector_search
from risk.score import compute_risk

VALID_OUTCOMES = ("upheld", "reverted")


def load_golden(path: str) -> dict[str, dict]:
    overrides: dict[str, dict] = {}
    for line_no, line in enumerate(Path(path).read_text(encoding="utf-8").splitlines(), start=1):
        line = line.strip()
        if not line:
            continue
        row = json.loads(line)
        external_id = row.get("external_id")
        if not external_id:
            raise ValueError(f"{path}:{line_no}: missing external_id")
        overrides[external_id] = row
    return overrides


def fetch_rulings() -> list[dict]:
    with get_pool().connection() as conn, conn.cursor() as cur:
        cur.execute("""
            SELECT id, external_id, case_type, outcome, embedding
            FROM rulings
            WHERE embedding IS NOT NULL
        """)
        cols = [d.name for d in cur.description]
        return [dict(zip(cols, row)) for row in cur.fetchall()]


def apply_overrides(rows: list[dict], overrides: dict[str, dict]) -> None:
    """Golden labels win over DB tags; missing golden fields leave the DB
    value in place."""
    for row in rows:
        override = overrides.get(row["external_id"])
        if not override:
            continue
        if override.get("case_type"):
            row["case_type"] = override["case_type"]
        if override.get("outcome"):
            row["outcome"] = override["outcome"]


def neighbors_excluding_self(ruling: dict, overrides: dict[str, dict], pool: int) -> list[dict]:
    # pool + 1 because the evaluated ruling itself comes back as (usually)
    # the top hit and gets dropped.
    neighbors = vector_search(ruling["embedding"], pool + 1)
    neighbors = [n for n in neighbors if n["id"] != ruling["id"]][:pool]
    apply_overrides(neighbors, overrides)
    return neighbors


def evaluate(rulings: list[dict], overrides: dict[str, dict], k: int, pool: int) -> dict:
    type_labeled = [r for r in rulings if r.get("case_type")]
    outcome_labeled = [r for r in rulings if r.get("outcome") in VALID_OUTCOMES]

    pairs: list[tuple[str, str | None]] = []
    precisions: list[float] = []
    for ruling in type_labeled:
        neighbors = neighbors_excluding_self(ruling, overrides, pool)
        tagged = [n for n in neighbors if n.get("case_type")][:k]
        vote = weighted_vote(tagged)
        pairs.append((ruling["case_type"], vote["case_type"]))
        precision = precision_at_k([n["case_type"] for n in tagged], ruling["case_type"], k)
        if precision is not None:
            precisions.append(precision)

    scored: list[tuple[str, float]] = []
    for ruling in outcome_labeled:
        neighbors = neighbors_excluding_self(ruling, overrides, pool)
        tagged = [n for n in neighbors if n.get("outcome") in VALID_OUTCOMES][:k]
        result = compute_risk(tagged)
        if result["risk"] is not None:
            scored.append((ruling["outcome"], result["risk"]))

    return {
        "corpus_size": len(rulings),
        "type_labeled": len(type_labeled),
        "outcome_labeled": len(outcome_labeled),
        "classification": classification_report(pairs),
        "mean_precision_at_k": sum(precisions) / len(precisions) if precisions else None,
        "risk": {
            "n_scored": len(scored),
            "separation": outcome_separation(scored),
            "suggested_buckets": suggest_buckets([s for _, s in scored]),
        },
    }


def print_report(report: dict, k: int) -> None:
    def fmt(value: float | None) -> str:
        return f"{value:.3f}" if value is not None else "n/a"

    print(f"corpus: {report['corpus_size']} rulings with embeddings")
    print(f"  case_type-labeled: {report['type_labeled']}, outcome-labeled: {report['outcome_labeled']}")

    # ASCII only in report text: a Windows console defaulting to cp1252
    # raises UnicodeEncodeError on characters like U+2192 (found live).
    if report["type_labeled"] == 0 and report["outcome_labeled"] == 0:
        print()
        print("nothing to evaluate: no labeled rulings (plan.md section 9 item 2 --")
        print("the archive is untagged until the grading pass lands). Supply --golden")
        print("with external_id -> case_type/outcome rows to evaluate anyway.")
        return

    cls = report["classification"]
    print()
    print(f"classification (leave-one-out, k={k}):")
    print(f"  n={cls['n']}  accuracy={fmt(cls['accuracy'])}  "
          f"accuracy_decided={fmt(cls['accuracy_decided'])}  abstained={cls['abstained']}")
    for case_type, stats in sorted(cls["per_class"].items()):
        print(f"    {case_type}: {stats['correct']}/{stats['n']}")
    print(f"  retrieval precision@{k} (labeled neighbors): {fmt(report['mean_precision_at_k'])}")

    risk = report["risk"]
    print()
    print(f"risk calibration (leave-one-out, k={k}): n_scored={risk['n_scored']}")
    for outcome, stats in sorted(risk["separation"].items()):
        print(f"    {outcome}: n={stats['n']}  mean_risk={stats['mean_risk']:.3f}")
    if risk["suggested_buckets"] is not None:
        t1, t2 = risk["suggested_buckets"]
        print(f"  suggested bucket thresholds (empirical tertiles): {t1:.3f} / {t2:.3f}")
        print("    (risk/score.py's risk_bucket currently uses fixed 1/3 and 2/3)")
    else:
        print("  too few scored rulings to suggest bucket thresholds")


def main() -> int:
    parser = argparse.ArgumentParser(description="Leave-one-out eval of local UC2/UC3/UC5 signals")
    parser.add_argument("--k", type=int, default=10, help="neighbors per vote/score (default 10)")
    parser.add_argument("--pool", type=int, default=100,
                        help="candidate pool fetched before label filtering (default 100)")
    parser.add_argument("--golden", help="NDJSON of {external_id, case_type, outcome} label overrides")
    args = parser.parse_args()

    overrides = load_golden(args.golden) if args.golden else {}
    rulings = fetch_rulings()
    apply_overrides(rulings, overrides)

    unmatched = set(overrides) - {r["external_id"] for r in rulings}
    if unmatched:
        print(f"warning: {len(unmatched)} golden external_ids not in the corpus: "
              f"{sorted(unmatched)[:5]}{'...' if len(unmatched) > 5 else ''}", file=sys.stderr)

    report = evaluate(rulings, overrides, k=args.k, pool=args.pool)
    print_report(report, k=args.k)
    get_pool().close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
