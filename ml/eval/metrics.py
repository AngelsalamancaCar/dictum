"""Pure metric functions for the eval harness (plan.md §8 phase 5: "golden
set with known type/outcome; measure classification accuracy and retrieval
precision each release").

Everything here is a pure function over already-computed predictions so it
can be unit tested without a database or model — the same split
rag/retrieval.py and risk/score.py use. DB-touching orchestration lives in
eval/run_eval.py.
"""
from __future__ import annotations

import statistics


def classification_report(pairs: list[tuple[str, str | None]]) -> dict:
    """pairs: (expected, predicted) per evaluated ruling; predicted is None
    when the classifier abstained (no tagged neighbors to vote). Abstentions
    count against `accuracy` — an abstaining classifier isn't useful — but
    `accuracy_decided` is also reported so tag scarcity (plan.md §9 item 2)
    is distinguishable from genuinely wrong votes."""
    total = len(pairs)
    if total == 0:
        return {"n": 0, "accuracy": None, "accuracy_decided": None, "abstained": 0, "per_class": {}}

    correct = 0
    abstained = 0
    per_class: dict[str, dict[str, int]] = {}
    for expected, predicted in pairs:
        stats = per_class.setdefault(expected, {"n": 0, "correct": 0})
        stats["n"] += 1
        if predicted is None:
            abstained += 1
        elif predicted == expected:
            correct += 1
            stats["correct"] += 1

    decided = total - abstained
    return {
        "n": total,
        "accuracy": correct / total,
        "accuracy_decided": correct / decided if decided else None,
        "abstained": abstained,
        "per_class": per_class,
    }


def precision_at_k(retrieved_types: list[str | None], expected: str, k: int) -> float | None:
    """Fraction of the top-k retrieved labels matching the expected type.
    None (an unlabeled neighbor) counts as a miss — pass a pre-filtered
    labeled-only list to measure precision among labeled corpus instead.
    Returns None when nothing was retrieved."""
    top = retrieved_types[:k]
    if not top:
        return None
    return sum(1 for t in top if t == expected) / len(top)


def suggest_buckets(scores: list[float]) -> tuple[float, float] | None:
    """Empirical tertile cut points over observed risk scores — the
    calibration replacement for risk/score.py's fixed 1/3-2/3 thresholds
    once the corpus carries enough outcome tags (plan.md §4 UC5: "calibrate
    buckets once volume allows"). Returns None below 3 scores."""
    if len(scores) < 3:
        return None
    t1, t2 = statistics.quantiles(scores, n=3)
    return (t1, t2)


def outcome_separation(scored: list[tuple[str, float]]) -> dict:
    """scored: (actual_outcome, risk_score) per evaluated ruling. Mean risk
    per actual outcome — a useful calibration signal: reverted rulings
    should score visibly higher than upheld ones, or the score carries no
    revert signal at all."""
    by_outcome: dict[str, list[float]] = {}
    for outcome, score in scored:
        by_outcome.setdefault(outcome, []).append(score)
    return {
        outcome: {"n": len(values), "mean_risk": sum(values) / len(values)}
        for outcome, values in by_outcome.items()
    }
