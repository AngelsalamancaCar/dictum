"""UC3 hybrid retrieval: pgvector kNN + Postgres full-text search, combined
by reciprocal rank fusion (RRF).

The fusion math (`reciprocal_rank_fusion`) is a pure function over ranked id
lists so it can be unit tested without a live database; only
`vector_search`/`fulltext_search`/`hybrid_search` touch Postgres.
"""
from __future__ import annotations

from dataclasses import dataclass
from typing import Any

import numpy as np

from rag.db import get_pool


@dataclass
class Filters:
    case_type: str | None = None
    court: str | None = None
    date_from: str | None = None
    date_to: str | None = None


def _filter_clause_and_params(filters: Filters | None) -> tuple[str, dict[str, Any]]:
    filters = filters or Filters()
    return (
        """
        AND (%(case_type)s::text IS NULL OR case_type = %(case_type)s::text)
        AND (%(court)s::text IS NULL OR court = %(court)s::text)
        AND (%(date_from)s::date IS NULL OR date >= %(date_from)s::date)
        AND (%(date_to)s::date IS NULL OR date <= %(date_to)s::date)
        """,
        {
            "case_type": filters.case_type,
            "court": filters.court,
            "date_from": filters.date_from,
            "date_to": filters.date_to,
        },
    )


def vector_search(query_embedding: np.ndarray, k: int, filters: Filters | None = None) -> list[dict]:
    """Nearest rulings by cosine distance. Returns ranked (rank 1 = closest)."""
    clause, params = _filter_clause_and_params(filters)
    params.update({"q": query_embedding, "k": k})

    sql = f"""
        SELECT id, external_id, case_type, outcome, revert_reason, court, date,
               1 - (embedding <=> %(q)s) AS similarity
        FROM rulings
        WHERE embedding IS NOT NULL
        {clause}
        ORDER BY embedding <=> %(q)s
        LIMIT %(k)s
    """
    with get_pool().connection() as conn, conn.cursor() as cur:
        cur.execute(sql, params)
        cols = [d.name for d in cur.description]
        return [dict(zip(cols, row)) for row in cur.fetchall()]


def fulltext_search(query_text: str, k: int, filters: Filters | None = None) -> list[dict]:
    """Nearest rulings by Spanish full-text rank. Returns ranked (rank 1 = best)."""
    clause, params = _filter_clause_and_params(filters)
    params.update({"q": query_text, "k": k})

    sql = f"""
        SELECT id, external_id, case_type, outcome, court, date,
               ts_rank(to_tsvector('spanish', full_text), plainto_tsquery('spanish', %(q)s)) AS rank
        FROM rulings
        WHERE to_tsvector('spanish', full_text) @@ plainto_tsquery('spanish', %(q)s)
        {clause}
        ORDER BY rank DESC
        LIMIT %(k)s
    """
    with get_pool().connection() as conn, conn.cursor() as cur:
        cur.execute(sql, params)
        cols = [d.name for d in cur.description]
        return [dict(zip(cols, row)) for row in cur.fetchall()]


def reciprocal_rank_fusion(ranked_id_lists: list[list[str]], k: int = 60) -> list[tuple[str, float]]:
    """Standard RRF: score(d) = sum over lists containing d of 1/(k + rank),
    rank 1-indexed. Returns ids sorted by fused score, descending."""
    scores: dict[str, float] = {}
    for ranked_ids in ranked_id_lists:
        for rank, doc_id in enumerate(ranked_ids, start=1):
            scores[doc_id] = scores.get(doc_id, 0.0) + 1.0 / (k + rank)
    return sorted(scores.items(), key=lambda kv: kv[1], reverse=True)


def hybrid_search(
    query_text: str,
    query_embedding: np.ndarray,
    k: int = 10,
    filters: Filters | None = None,
    candidate_pool: int = 50,
) -> list[dict]:
    """Combines vector_search and fulltext_search via RRF, returning the top
    k results with full metadata attached."""
    vec_results = vector_search(query_embedding, candidate_pool, filters)
    ft_results = fulltext_search(query_text, candidate_pool, filters)

    by_id = {r["id"]: r for r in vec_results}
    for r in ft_results:
        by_id.setdefault(r["id"], r)

    fused = reciprocal_rank_fusion(
        [[r["id"] for r in vec_results], [r["id"] for r in ft_results]]
    )

    results = []
    for doc_id, score in fused[:k]:
        row = dict(by_id[doc_id])
        row["fused_score"] = score
        results.append(row)
    return results
