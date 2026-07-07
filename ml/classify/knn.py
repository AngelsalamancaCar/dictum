"""UC2 local classification signal: similarity-weighted vote of a case's
nearest tagged rulings' case_type. Instant, no LLM call — the LLM signal
(prepared package) is a separate, later addition per plan.md §4 UC2.

`weighted_vote` is a pure function over already-fetched neighbors so it can
be unit tested without a live database; only `classify_by_knn` touches
Postgres.
"""
from __future__ import annotations

import numpy as np

from rag.retrieval import Filters, vector_search


def weighted_vote(neighbors: list[dict]) -> dict:
    """neighbors: dicts with at least 'case_type' and 'similarity'. Rulings
    with no case_type (untagged) don't get a vote — they just don't count as
    evidence either way."""
    weights: dict[str, float] = {}
    evidence: dict[str, list[dict]] = {}

    for n in neighbors:
        case_type = n.get("case_type")
        if not case_type:
            continue
        weights[case_type] = weights.get(case_type, 0.0) + n["similarity"]
        evidence.setdefault(case_type, []).append(n)

    if not weights:
        return {"case_type": None, "confidence": 0.0, "evidence": []}

    total = sum(weights.values())
    winner = max(weights, key=weights.get)
    return {
        "case_type": winner,
        "confidence": weights[winner] / total,
        "evidence": evidence[winner],
    }


def classify_by_knn(query_embedding: np.ndarray, k: int = 10, candidate_pool: int = 100) -> dict:
    """Fetches a larger candidate pool than k and votes over the first k
    *tagged* neighbors within it — if the corpus is mostly untagged (true of
    the archive today, see plan.md §9 item 2), restricting the initial fetch
    to exactly k would starve the vote of tagged neighbors even when some
    exist further down the ranking."""
    neighbors = vector_search(query_embedding, candidate_pool, filters=Filters())
    tagged = [n for n in neighbors if n.get("case_type")][:k]
    return weighted_vote(tagged)
