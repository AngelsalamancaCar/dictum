"""UC5 revert-risk score: local, instant, no LLM call. Similarity-weighted
reverted ratio over a case/draft's nearest rulings — `risk = Σ(simᵢ·revertedᵢ)
/ Σ(simᵢ)` (plan.md §4 UC5) — bucketed into low/medium/high.

`compute_risk`/`risk_bucket`/`_filter_reverted` are pure functions over
already-fetched neighbors so they can be unit tested without a live
database; only `score_by_knn`/`nearest_reverted` touch Postgres.
`nearest_reverted` backs the explanation layer (plan.md §4 UC5: nearest
reverted rulings' `revert_reason` fed to a `risk_explain` harness package).
"""
from __future__ import annotations

import numpy as np

from rag.retrieval import Filters, vector_search

# Below this many outcome-tagged neighbors, the risk grade carries a
# sample-size caveat (plan.md §4 UC5: "111 rulings is thin; calibrate
# buckets once volume allows").
MIN_SAMPLE_SIZE = 20


def risk_bucket(risk: float) -> str:
    if risk < 1 / 3:
        return "low"
    if risk < 2 / 3:
        return "medium"
    return "high"


def compute_risk(neighbors: list[dict]) -> dict:
    """neighbors: dicts with at least 'outcome' and 'similarity'. Neighbors
    with outcome 'pending' (untagged — true of most of the archive today,
    see plan.md §9 item 2) carry no revert signal and are excluded from
    both the numerator and the denominator, same untagged-neighbors-don't-
    vote treatment as classify/knn.py's weighted_vote."""
    scored = [n for n in neighbors if n.get("outcome") in ("upheld", "reverted")]
    if not scored:
        return {"risk": None, "bucket": None, "sample_size": 0, "caveat": "no outcome-tagged neighbors found", "neighbors": []}

    numerator = sum(n["similarity"] * (1.0 if n["outcome"] == "reverted" else 0.0) for n in scored)
    denominator = sum(n["similarity"] for n in scored)
    risk = numerator / denominator

    caveat = None
    if len(scored) < MIN_SAMPLE_SIZE:
        caveat = f"only {len(scored)} outcome-tagged neighbors found; grade is low-confidence until the corpus grows"

    return {
        "risk": risk,
        "bucket": risk_bucket(risk),
        "sample_size": len(scored),
        "caveat": caveat,
        "neighbors": scored,
    }


def score_by_knn(query_embedding: np.ndarray, k: int = 10, candidate_pool: int = 100) -> dict:
    """Fetches a wider candidate pool than k and scores over the first k
    outcome-tagged neighbors within it — mirroring classify_by_knn's
    treatment of the same mostly-untagged corpus (plan.md §9 item 2): a
    naive top-k fetch would starve the score of tagged neighbors even when
    some exist further down the ranking."""
    neighbors = vector_search(query_embedding, candidate_pool, filters=Filters())
    tagged = [n for n in neighbors if n.get("outcome") in ("upheld", "reverted")][:k]
    return compute_risk(tagged)


def _filter_reverted(neighbors: list[dict], k: int) -> list[dict]:
    return [n for n in neighbors if n.get("outcome") == "reverted"][:k]


def nearest_reverted(query_embedding: np.ndarray, k: int = 5, candidate_pool: int = 100) -> list[dict]:
    """Fetches a wide candidate pool and returns only the neighbors tagged
    `reverted` — narrower than score_by_knn's outcome-tagged (upheld +
    reverted) filter, since the risk_explain package only ever explains a
    draft against rulings that were actually overturned."""
    neighbors = vector_search(query_embedding, candidate_pool, filters=Filters())
    return _filter_reverted(neighbors, k)
